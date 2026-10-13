// Package volume presents plaintext files over Modal's raw block API.
// A Volume is immutable and scoped to one volume and one encryption key.
package volume

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/t41372/modalfs/internal/encryption"
	"github.com/t41372/modalfs/internal/modalapi"
)

// MetaDir is the root folder modalfs keeps its own files in; its name stays
// readable in an encrypted volume, and everything in it but the marker is
// encrypted.
const MetaDir = ".modalfs"

// MarkerPath is the optional plaintext marker of an encrypted volume.
const MarkerPath = MetaDir + "/encryption.json"

type Marker struct {
	Version     int    `json:"version"`
	Fingerprint string `json:"fingerprint"`
}

type Volume struct {
	Client *modalapi.Client
	ID     string
	Cipher *encryption.Cipher
	// Skipped, when set, is told about each entry of an encrypted volume
	// that doesn't decrypt with its key: put there by another tool, or
	// damaged. Listings then leave it out rather than fail, so one stray
	// name never hides the rest of the volume. Without it, listing fails.
	Skipped func(raw string, err error)
}

func (v *Volume) ChunkSize() int64 {
	if v.Cipher != nil {
		return encryption.ChunkSize
	}
	return modalapi.BlockSize
}

func (v *Volume) Path(p string) (string, error) {
	if v.Cipher == nil {
		return p, nil
	}
	return v.Cipher.EncodePath(p)
}

func (v *Volume) HashBlock(data []byte) []byte {
	if v.Cipher != nil {
		data = v.Cipher.SealBlock(data)
	}
	h := sha256.Sum256(data)
	return h[:]
}

func (v *Volume) HashBlocks(data []byte) [][]byte {
	var hashes [][]byte
	for i := 0; i < len(data); i += int(v.ChunkSize()) {
		hashes = append(hashes, v.HashBlock(data[i:min(i+int(v.ChunkSize()), len(data))]))
	}
	return hashes
}

func (v *Volume) decode(e modalapi.Entry) (modalapi.Entry, error) {
	if v.Cipher == nil {
		return e, nil
	}
	var err error
	e.Path, err = v.Cipher.DecodePath(e.Path)
	if err == nil && !e.IsDir {
		if path.Base(e.Path) == ".modalfs_keep" {
			e.Size = 0
		} else {
			e.Size, err = encryption.PlaintextSize(e.Size)
		}
	}
	return e, err
}

func (v *Volume) List(ctx context.Context, dir string, recursive bool) ([]modalapi.Entry, error) {
	p, err := v.Path(dir)
	if err != nil {
		return nil, err
	}
	es, err := v.Client.List(ctx, v.ID, p, recursive)
	if err != nil {
		return nil, err
	}
	return v.decodeAll(es)
}

// decodeAll decrypts a listing's names and sizes, leaving out the marker.
func (v *Volume) decodeAll(es []modalapi.Entry) ([]modalapi.Entry, error) {
	out := make([]modalapi.Entry, 0, len(es))
	for _, raw := range es {
		if v.Cipher != nil && raw.Path == MarkerPath {
			continue
		}
		e, err := v.decode(raw)
		if err != nil && v.Skipped != nil {
			v.Skipped(raw.Path, err)
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

func (v *Volume) Walk(ctx context.Context, dir string, fn func([]modalapi.Entry)) error {
	p, err := v.Path(dir)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var decodeErr error
	err = v.Client.Walk(ctx, v.ID, p, func(batch []modalapi.Entry) {
		if decodeErr != nil {
			return
		}
		out, err := v.decodeAll(batch)
		if err != nil {
			decodeErr = err
			cancel()
			return
		}
		fn(out)
	})
	if decodeErr != nil {
		return decodeErr
	}
	return err
}

func (v *Volume) BlockURLs(ctx context.Context, p string, first, n int64) ([]string, int64, error) {
	p, err := v.Path(p)
	if err != nil {
		return nil, 0, err
	}
	urls, size, err := v.Client.BlockURLs(ctx, v.ID, p, first, n)
	if err == nil && v.Cipher != nil {
		size, err = encryption.PlaintextSize(size)
	}
	return urls, size, err
}

func (v *Volume) Download(ctx context.Context, url string, plainLen int) ([]byte, error) {
	wireLen := plainLen
	if v.Cipher != nil {
		wireLen += encryption.Overhead
	}
	data, err := v.Client.Download(ctx, url, wireLen)
	if err == nil && v.Cipher != nil {
		data, err = v.Cipher.OpenBlock(data)
	}
	return data, err
}

func (v *Volume) ReadFile(ctx context.Context, p string, limit int64) ([]byte, error) {
	urls, size, err := v.BlockURLs(ctx, p, 0, 1)
	if err != nil {
		return nil, err
	}
	if size > limit {
		return nil, fmt.Errorf("%s is %d bytes, more than the %d expected", p, size, limit)
	}
	n := max(1, (size+v.ChunkSize()-1)/v.ChunkSize())
	if int64(len(urls)) < n && (size > 0 || v.Cipher != nil) {
		urls, size, err = v.BlockURLs(ctx, p, 0, n)
		if err != nil {
			return nil, err
		}
		if int64(len(urls)) != n || size > limit {
			return nil, errors.New("file changed or has an incomplete block list")
		}
	}
	out := make([]byte, 0, size)
	for i, url := range urls {
		want := min(v.ChunkSize(), size-int64(i)*v.ChunkSize())
		if want < 0 {
			return nil, errors.New("file has too many blocks")
		}
		b, err := v.Download(ctx, url, int(want))
		if err != nil {
			return nil, fmt.Errorf("%s block %d: %w", p, i, err)
		}
		out = append(out, b...)
	}
	if int64(len(out)) != size {
		return nil, errors.New("file has an incomplete block list")
	}
	return out, nil
}

func (v *Volume) Put(ctx context.Context, files []modalapi.PutFile) (int, error) {
	return v.PutChecked(ctx, files, nil)
}

func (v *Volume) PutChecked(ctx context.Context, files []modalapi.PutFile, check func() error) (int, error) {
	if v.Cipher == nil {
		return v.Client.PutChecked(ctx, v.ID, files, check)
	}
	wire := make([]modalapi.PutFile, len(files))
	for i, f := range files {
		p, err := v.Path(f.Path)
		if err != nil {
			return 0, err
		}
		wire[i] = f
		wire[i].Path = p
		wire[i].Size = encryption.CiphertextSize(f.Size)
		wire[i].Block = func(i int) ([]byte, error) {
			var plain []byte
			var err error
			if f.Size != 0 {
				plain, err = f.Block(i)
			}
			if err != nil {
				return nil, err
			}
			return v.Cipher.SealBlock(plain), nil
		}
		if f.Size == 0 {
			wire[i].Hashes = [][]byte{v.HashBlock(nil)}
		}
	}
	return v.Client.PutChecked(ctx, v.ID, wire, check)
}

func (v *Volume) WriteFile(ctx context.Context, p string, data []byte) error {
	_, err := v.Put(ctx, []modalapi.PutFile{{Path: p, Size: int64(len(data)), Hashes: v.HashBlocks(data),
		Block: func(i int) ([]byte, error) {
			start := i * int(v.ChunkSize())
			return data[start:min(start+int(v.ChunkSize()), len(data))], nil
		},
	}})
	return err
}

func (v *Volume) Remove(ctx context.Context, p string, recursive bool) error {
	p, err := v.Path(p)
	if err != nil {
		return err
	}
	return v.Client.Remove(ctx, v.ID, p, recursive)
}

// Copy keeps manifests on Modal. Every destination path is checked first,
// and the caller removes sources only after all copies have succeeded.
func (v *Volume) Copy(ctx context.Context, sources []string, dest string, recursive bool) error {
	if v.Cipher == nil {
		return v.Client.Copy(ctx, v.ID, sources, dest, recursive)
	}
	type pair struct{ source, dest string }
	var copies []pair
	for _, source := range sources {
		entries, err := v.List(ctx, source, recursive)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if e.IsDir {
				continue
			}
			target := dest
			if recursive {
				target = path.Join(dest, path.Base(source), strings.TrimPrefix(strings.TrimPrefix(e.Path, source), "/"))
			}
			from, err := v.Path(e.Path)
			if err != nil {
				return err
			}
			to, err := v.Path(target)
			if err != nil {
				return err
			}
			copies = append(copies, pair{from, to})
		}
	}
	for _, p := range copies {
		if err := v.Client.Copy(ctx, v.ID, []string{p.source}, p.dest, false); err != nil {
			return err
		}
	}
	return nil
}

// Detect doesn't require a key or local state. A malformed marker is an
// error; encrypted root names still identify a volume without its marker.
func Detect(ctx context.Context, c *modalapi.Client, id string) (bool, *Marker, error) {
	data, err := c.ReadFile(ctx, id, MarkerPath, 4096)
	if err == nil {
		var marker Marker
		if json.Unmarshal(data, &marker) != nil || marker.Version != 1 || len(marker.Fingerprint) != 32 {
			return true, nil, errors.New("damaged or unsupported encryption marker; restore it, or remove it and unlock using the key")
		}
		return true, &marker, nil
	}
	if !modalapi.IsNotFound(err) {
		return false, nil, err
	}
	es, err := c.List(ctx, id, "", false)
	if err != nil {
		return false, nil, err
	}
	for _, e := range es {
		if e.IsDir && e.Path == MetaDir {
			metadata, err := c.List(ctx, id, MetaDir, false)
			if err != nil {
				return false, nil, err
			}
			for _, child := range metadata {
				if strings.HasPrefix(path.Base(child.Path), encryption.NamePrefix) {
					return true, nil, nil
				}
			}
		}
		if strings.HasPrefix(e.Path, encryption.NamePrefix) {
			return true, nil, nil
		}
	}
	return false, nil, nil
}

func (v *Volume) WriteMarker(ctx context.Context) error {
	if v.Cipher == nil {
		return errors.New("cannot write encryption marker without a key")
	}
	data, _ := json.Marshal(Marker{Version: 1, Fingerprint: v.Cipher.Key().Fingerprint()})
	raw := &Volume{Client: v.Client, ID: v.ID}
	return raw.WriteFile(ctx, MarkerPath, data)
}

// VerifyKey authenticates root names, which prove possession of the volume key
// without making a disposable marker, label or sidecar's body mandatory. One
// name that authenticates is proof; names that don't (put there by another
// tool, or damaged) are reported when listed, and don't lock the volume.
func (v *Volume) VerifyKey(ctx context.Context, marker *Marker) error {
	if marker != nil && marker.Fingerprint != v.Cipher.Key().Fingerprint() {
		return errors.New("this isn't the right key for this volume")
	}
	entries, err := v.Client.List(ctx, v.ID, "", false)
	if err != nil {
		return err
	}
	if v.anyDecrypts(entries) {
		return nil
	}
	metadata, err := v.Client.List(ctx, v.ID, MetaDir, false)
	if err != nil && !modalapi.IsNotFound(err) {
		return err
	}
	if v.anyDecrypts(metadata) || marker != nil {
		return nil
	}
	for _, e := range append(entries, metadata...) {
		if strings.HasPrefix(path.Base(e.Path), encryption.NamePrefix) {
			return errors.New("this isn't the right key for this volume")
		}
	}
	return errors.New("no encrypted names remain to verify this key")
}

// anyDecrypts reports whether an encrypted name in the listing authenticates.
func (v *Volume) anyDecrypts(entries []modalapi.Entry) bool {
	for _, e := range entries {
		if e.Path == MetaDir || !strings.HasPrefix(path.Base(e.Path), encryption.NamePrefix) {
			continue
		}
		if _, err := v.Cipher.DecodePath(e.Path); err == nil {
			return true
		}
	}
	return false
}

// RecoveryEntries lists every user file and folder, leaving out disposable
// metadata before interpreting sizes, so even a truncated sidecar or keep
// marker can't prevent recovery. Entries that don't decrypt go to Skipped.
func (v *Volume) RecoveryEntries(ctx context.Context) ([]modalapi.Entry, error) {
	entries, err := v.Client.List(ctx, v.ID, "", true)
	if err != nil {
		return nil, err
	}
	user := make([]modalapi.Entry, 0, len(entries))
	for _, e := range entries {
		if e.Path != MetaDir && !strings.HasPrefix(e.Path, MetaDir+"/") {
			user = append(user, e)
		}
	}
	return v.decodeAll(user)
}
