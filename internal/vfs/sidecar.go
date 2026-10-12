package vfs

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"

	"github.com/t41372/modalfs/internal/modalapi"
)

var errDetached = errors.New("file state detached")

// Sidecar manifests let other modalfs clients (or this one after losing its
// cache) commit partial writes to large files without first re-downloading
// every unchanged block just to learn its hash. They are only trusted when
// size and mtime still match the data file, and every block hash this
// client already knows for that version agrees. If it knows none, size and
// mtime are all there is to go on.
type sidecarJSON struct {
	V         int      `json:"v"`
	Path      string   `json:"path"`
	Size      int64    `json:"size"`
	Mtime     int64    `json:"mtime"`
	BlockSize int      `json:"block_size"`
	SHA256    []string `json:"sha256"`
}

func sidecarPath(p string) string {
	return path.Join(MetaDir, "manifests", p+".json")
}

func (fs *FS) writeSidecar(p string, m *manifest) error {
	sc := sidecarJSON{V: 1, Path: p, Size: m.Size, Mtime: m.Mtime, BlockSize: blockSize}
	for _, h := range m.Hashes {
		sc.SHA256 = append(sc.SHA256, hex.EncodeToString(h))
	}
	data, _ := json.Marshal(sc)
	_, err := fs.c.Put(fs.ctx, fs.volID, []modalapi.PutFile{{
		Path: sidecarPath(p), Size: int64(len(data)), Hashes: modalapi.HashBlocks(data),
		Block: func(i int) ([]byte, error) {
			return data[i*blockSize : min((i+1)*blockSize, len(data))], nil
		},
	}})
	return err
}

func (fs *FS) loadSidecar(p string, size, mtime int64) *manifest {
	sp := sidecarPath(p)
	urls, sz, err := fs.c.BlockURLs(fs.ctx, fs.volID, sp, 0, 1<<20)
	if err != nil || sz > 64<<20 {
		return nil
	}
	var data []byte
	for i, u := range urls {
		b, err := fs.c.Download(fs.ctx, u, blockLen(sz, int64(i)))
		if err != nil {
			return nil
		}
		data = append(data, b...)
	}
	var sc sidecarJSON
	if json.Unmarshal(data, &sc) != nil || sc.Size != size || sc.Mtime != mtime || sc.BlockSize != blockSize {
		return nil
	}
	if len(sc.SHA256) != numBlocks(size) {
		return nil
	}
	m := &manifest{Size: size, Mtime: mtime}
	for _, h := range sc.SHA256 {
		b, err := hex.DecodeString(h)
		if err != nil || len(b) != 32 {
			return nil
		}
		m.Hashes = append(m.Hashes, b)
	}
	checked := 0
	if known := fs.mdb.Get(p, size, mtime); known != nil {
		for i, h := range known.Hashes {
			if h == nil || i >= len(m.Hashes) {
				continue
			}
			if !bytes.Equal(h, m.Hashes[i]) {
				fs.log.Warn("ignoring a sidecar that disagrees with a block hash already known", "path", p, "block", i)
				return nil
			}
			checked++
		}
	}
	if checked == 0 {
		fs.log.Debug("sidecar accepted on size and mtime alone: no block hash known to check it against", "path", p)
	}
	fs.mdb.Put(p, m)
	return m
}
