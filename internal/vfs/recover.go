package vfs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Crash safety: every file with uncommitted local changes keeps a small
// journal (meta.json) next to its dirty block files. If modalfs is killed,
// the next start re-attaches those changes and commits them, using the
// normal conflict rules (a copy is made if the file changed remotely).

type journal struct {
	Fingerprint string `json:"encryption,omitempty"`
	ChunkSize   int64  `json:"chunk_size,omitempty"`
	Path        string `json:"path"`
	BaseExists  bool   `json:"base_exists"`
	BaseSize    int64  `json:"base_size"`
	BaseMtime   int64  `json:"base_mtime"`
	TruncFloor  int64  `json:"trunc_floor"`
	Size        int64  `json:"size"`
}

const journalName = "meta.json"

// writeJournalLocked persists the file's shape. force=false throttles to
// one write per second unless the shape changed block count. Most callers
// ignore its error: the journal is written again with the next change.
func (st *fileState) writeJournalLocked(force bool) error {
	if !force {
		if st.jSize == st.size && st.jFloor == st.truncFloor {
			return nil
		}
		if st.fs.numBlocks(st.jSize) == st.fs.numBlocks(st.size) && time.Since(st.jAt) < time.Second {
			return nil
		}
	}
	if err := os.MkdirAll(st.dir, 0o700); err != nil {
		return err
	}
	raw, _ := json.Marshal(journal{Fingerprint: st.fs.fingerprint(), ChunkSize: st.fs.chunkSize(), Path: st.path, BaseExists: st.baseExists, BaseSize: st.baseSize,
		BaseMtime: st.baseMtime, TruncFloor: st.truncFloor, Size: st.size})
	tmp := filepath.Join(st.dir, journalName+".tmp")
	err := os.WriteFile(tmp, raw, 0o600)
	if err == nil {
		err = os.Rename(tmp, filepath.Join(st.dir, journalName))
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	st.jSize, st.jFloor, st.jAt = st.size, st.truncFloor, time.Now()
	st.journalUnsynced = true
	return nil
}

// recover re-attaches journals left by a previous run.
func (fs *FS) recover(dirtyRoot string) {
	ents, _ := os.ReadDir(dirtyRoot)
	sort.Slice(ents, func(i, j int) bool { return ents[i].Name() < ents[j].Name() }) // oldest first
	orphan := filepath.Join(filepath.Dir(dirtyRoot), fmt.Sprintf("dirty-orphan-%d", time.Now().Unix()))
	for _, e := range ents {
		dir := filepath.Join(dirtyRoot, e.Name())
		raw, err := os.ReadFile(filepath.Join(dir, journalName))
		var j journal
		if err != nil || json.Unmarshal(raw, &j) != nil || j.Path == "" {
			_ = os.MkdirAll(orphan, 0o700)
			_ = os.Rename(dir, filepath.Join(orphan, e.Name()))
			fs.log.Warn("unrecoverable leftover data moved aside", "dir", filepath.Join(orphan, e.Name()))
			continue
		}
		st := &fileState{fs: fs, path: j.Path, dir: dir, baseExists: j.BaseExists, baseSize: j.BaseSize,
			baseMtime: j.BaseMtime, truncFloor: j.TruncFloor, size: j.Size, dirty: map[int64]bool{},
			hashes: map[int64][]byte{}, unsynced: map[int64]bool{}, mtime: time.Now(), gen: 1, recovered: true}
		st.initAhead()
		blocks, _ := os.ReadDir(dir)
		for _, b := range blocks {
			// not a block: the journal, or a *.tmp block still being built,
			// or a commit's *.snap copy (the live block is newer)
			idx, err := strconv.ParseInt(b.Name(), 10, 64)
			if err != nil {
				if strings.HasSuffix(b.Name(), ".tmp") || strings.HasSuffix(b.Name(), ".snap") {
					_ = os.Remove(filepath.Join(dir, b.Name()))
				}
				continue
			}
			// A complete dirty block is exactly fs.chunkSize(); any other size is
			// left over from a failed write (older versions) and holds no
			// user data.
			if info, err := b.Info(); err != nil || info.Size() != fs.chunkSize() {
				fs.log.Warn("discarding incomplete dirty block", "path", j.Path, "block", idx)
				_ = os.Remove(filepath.Join(dir, b.Name()))
				continue
			}
			st.dirty[idx] = true
			// The journal's size is throttled, so it can lag the last
			// second of appends. Dirty blocks at or past it reveal the
			// real end (trailing zero bytes are indistinguishable from
			// padding and are dropped).
			if (idx+1)*fs.chunkSize() > j.Size {
				if end := dataEnd(filepath.Join(dir, b.Name())); end > 0 && idx*fs.chunkSize()+end > st.size {
					st.size = idx*fs.chunkSize() + end
				}
			}
		}
		st.jSize, st.jFloor = st.size, st.truncFloor
		st.accountLocked() // not shared yet: no lock needed
		fs.mu.Lock()
		if old := fs.files[j.Path]; old != nil {
			// a newer journal for the same path wins; park the older one
			old.removed = true
			old.accountLocked()
			_ = os.MkdirAll(orphan, 0o700)
			_ = os.Rename(old.dir, filepath.Join(orphan, filepath.Base(old.dir)))
		}
		fs.files[j.Path] = st
		fs.mu.Unlock()
		fs.log.Info("recovered uncommitted changes from previous run", "path", j.Path, "size", st.size, "dirty_blocks", len(st.dirty))
		fs.event("recovered", j.Path, fmt.Sprintf("%d dirty blocks", len(st.dirty)))
	}
	fs.mu.Lock()
	for _, st := range fs.files {
		if st.recovered {
			st.scheduleLocked(2 * time.Second)
		}
	}
	fs.mu.Unlock()
}

// dataEnd returns the offset just past the last non-zero byte of a block file.
func dataEnd(p string) int64 {
	b, err := os.ReadFile(p)
	if err != nil {
		return 0
	}
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] != 0 {
			return int64(i + 1)
		}
	}
	return 0
}

func (fs *FS) fingerprint() string {
	if fs.c != nil && fs.c.Cipher != nil {
		return fs.c.Cipher.Key().Fingerprint()
	}
	return ""
}

// Refuse before recovery can move or discard any dirty block.
func (fs *FS) checkJournals(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(root, e.Name(), journalName))
		if err != nil {
			continue
		}
		var j journal
		if json.Unmarshal(raw, &j) != nil {
			continue
		}
		chunk := j.ChunkSize
		if chunk == 0 {
			chunk = blockSize
		}
		if chunk != fs.chunkSize() || j.Fingerprint != fs.fingerprint() {
			return fmt.Errorf("pending writes use a different encryption key or chunk size; unlock with the original key; journal kept at %s", filepath.Join(root, e.Name()))
		}
	}
	return nil
}
