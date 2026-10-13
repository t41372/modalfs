package daemon

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/t41372/modalfs/internal/encryption"
	"github.com/t41372/modalfs/internal/fsync"
	"github.com/t41372/modalfs/internal/vfs"
	"github.com/t41372/modalfs/internal/volume"
)

// recoveryFailure is a file or name recovery couldn't decrypt.
type recoveryFailure struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

// decryptVolume copies an encrypted volume's files to a local folder. It
// intentionally doesn't consult the marker, key store or cache. A file that
// fails doesn't stop the rest: every one that can be decrypted is, and the
// ones that can't are listed.
func (d *Daemon) decryptVolume(w http.ResponseWriter, r *http.Request) {
	var in struct{ Key, To, Environment string }
	if err := readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	key, err := encryption.ParseKey(in.Key)
	if err != nil {
		writeErr(w, err)
		return
	}
	if in.To == "" || !filepath.IsAbs(in.To) {
		writeErr(w, errors.New("choose an absolute destination folder"))
		return
	}
	dest, err := d.recoveryPath(in.To)
	if err != nil {
		writeErr(w, err)
		return
	}
	c, _, err := d.clientFor(r.PathValue("id"), in.Environment)
	if err != nil {
		writeErr(w, err)
		return
	}
	var failed []recoveryFailure
	v := &volume.Volume{Client: c, ID: r.PathValue("vol"), Cipher: encryption.New(key), Skipped: func(raw string, err error) {
		failed = append(failed, recoveryFailure{raw, err.Error()})
	}}
	if err := v.VerifyKey(r.Context(), nil); err != nil {
		writeErr(w, err)
		return
	}
	es, err := v.RecoveryEntries(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := os.MkdirAll(dest, 0o700); err != nil {
		writeErr(w, err)
		return
	}
	existing, err := os.ReadDir(dest)
	if err != nil || len(existing) != 0 {
		writeErr(w, errors.New("destination must be empty; recovery never overwrites local files"))
		return
	}
	files := 0
	var total int64
	for _, e := range es {
		if e.Path == vfs.MetaDir || strings.HasPrefix(e.Path, vfs.MetaDir+"/") {
			continue
		}
		if path.Base(e.Path) == vfs.KeepName {
			if err := os.MkdirAll(filepath.Join(dest, filepath.FromSlash(path.Dir(e.Path))), 0o700); err != nil {
				writeErr(w, err)
				return
			}
			continue
		}
		p := filepath.Join(dest, filepath.FromSlash(e.Path))
		if e.IsDir {
			if err := os.MkdirAll(p, 0o700); err != nil {
				writeErr(w, err)
				return
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			writeErr(w, err)
			return
		}
		if err := recoverFile(r, v, e.Path, p, e.Size); err != nil {
			failed = append(failed, recoveryFailure{e.Path, err.Error()})
			continue
		}
		files++
		total += e.Size
	}
	writeJSON(w, 200, map[string]any{"files": files, "bytes": total, "to": dest, "failed": failed})
}

func recoverFile(r *http.Request, v *volume.Volume, source, dest string, size int64) error {
	f, err := os.CreateTemp(filepath.Dir(dest), ".modalfs-partial-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	n := max(1, (size+v.ChunkSize()-1)/v.ChunkSize())
	urls, currentSize, err := v.BlockURLs(r.Context(), source, 0, n)
	if err != nil {
		return fmt.Errorf("read file manifest: %w", err)
	}
	if currentSize != size || int64(len(urls)) != n {
		return errors.New("file changed during recovery or has an incomplete block list")
	}
	for i, url := range urls {
		b, err := v.Download(r.Context(), url, int(min(v.ChunkSize(), size-int64(i)*v.ChunkSize())))
		if err != nil {
			return fmt.Errorf("read block %d: %w", i, err)
		}
		if _, err := f.Write(b); err != nil {
			return err
		}
	}
	if err := fsync.File(f); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := publishRecovered(f.Name(), dest); err != nil {
		return fmt.Errorf("cannot publish recovered file without replacing an existing name: %w; choose an empty folder on a filesystem supporting exclusive rename", err)
	}
	return nil
}
