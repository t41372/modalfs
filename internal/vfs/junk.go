package vfs

import (
	"os"
	"path"
	"sort"
	"strings"
	"syscall"
	"time"
)

// macOS writes AppleDouble (._*) and .DS_Store files on every network
// filesystem without xattrs. With FilterJunk they live in memory only, so
// Finder stays happy and the volume stays clean.

const maxEphSize = 4 << 20

// The local-only files of a mount are capped, so they don't grow for the
// mount's whole life. Over the cap, the least recently used ones go first,
// but never one used within ephKeep: an app may still be working with it.
// macOS rewrites these files whenever it needs them, so losing an old one
// only loses a folder's view settings or a file's Finder metadata.
const (
	ephMaxBytes = 64 << 20
	ephMaxFiles = 20000
	ephKeep     = 10 * time.Minute
)

type ephFile struct {
	data  []byte
	mtime time.Time
	used  time.Time // last created, read, written or looked up
}

// ephSetLocked changes a local-only file's content, keeping the byte count.
func (fs *FS) ephSetLocked(e *ephFile, data []byte) {
	fs.ephBytes += int64(len(data)) - int64(len(e.data))
	e.data = data
}

func (fs *FS) ephDeleteLocked(p string) {
	if e := fs.eph[p]; e != nil {
		fs.ephBytes -= int64(len(e.data))
		delete(fs.eph, p)
	}
}

// ephTrimLocked forgets the least recently used local-only files while
// there are too many, sparing recently used ones. The cap can be exceeded
// for up to a second, or for as long as the files are in use.
func (fs *FS) ephTrimLocked() {
	if fs.ephBytes <= ephMaxBytes && len(fs.eph) <= ephMaxFiles {
		return
	}
	// at most once a second: when everything is in use there's nothing to
	// drop, and scanning on every write would be quadratic
	if time.Since(fs.ephTrimmed) < time.Second {
		return
	}
	fs.ephTrimmed = time.Now()
	type old struct {
		p    string
		used time.Time
	}
	var cands []old
	for p, e := range fs.eph {
		if time.Since(e.used) >= ephKeep {
			cands = append(cands, old{p, e.used})
		}
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].used.Before(cands[j].used) })
	// down to 90%, so a full cache isn't scanned on every write
	for _, c := range cands {
		if fs.ephBytes <= ephMaxBytes*9/10 && len(fs.eph) <= ephMaxFiles*9/10 {
			break
		}
		fs.ephDeleteLocked(c.p)
	}
}

func IsJunkName(name string) bool {
	return name == ".DS_Store" || strings.HasPrefix(name, "._") || name == ".apdisk"
}

func (fs *FS) isJunk(p string) bool {
	return fs.opt.FilterJunk && p != "" && IsJunkName(path.Base(p))
}

func (fs *FS) ephStat(p string) (Attr, bool) {
	if !fs.opt.FilterJunk {
		return Attr{}, false
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	e := fs.eph[p]
	if e == nil {
		return Attr{}, false
	}
	e.used = time.Now()
	return Attr{Name: path.Base(p), Size: int64(len(e.data)), Mtime: e.mtime, Mode: 0o644}, true
}

func (fs *FS) ephList(dir string, out []Attr) []Attr {
	if !fs.opt.FilterJunk {
		return out
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	for p, e := range fs.eph {
		if parentOf(p) != dir {
			continue
		}
		a := Attr{Name: path.Base(p), Size: int64(len(e.data)), Mtime: e.mtime, Mode: 0o644}
		replaced := false
		for i := range out {
			if out[i].Name == a.Name {
				out[i], replaced = a, true
			}
		}
		if !replaced {
			out = append(out, a)
		}
	}
	return out
}

func (fs *FS) ephRead(p string, buf []byte, off int64) (int, bool) {
	if !fs.opt.FilterJunk {
		return 0, false
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	e := fs.eph[p]
	if e == nil {
		return 0, false
	}
	e.used = time.Now()
	if off >= int64(len(e.data)) {
		return 0, true
	}
	return copy(buf, e.data[off:]), true
}

func (fs *FS) ephCreate(p string, exclusive bool) error {
	if _, err := fs.dirStat(parentOf(p)); err != nil {
		return err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if e := fs.eph[p]; e != nil {
		if exclusive {
			return os.ErrExist
		}
		fs.ephSetLocked(e, nil)
		e.mtime, e.used = time.Now(), time.Now()
		return nil
	}
	fs.eph[p] = &ephFile{mtime: time.Now(), used: time.Now()}
	fs.ephTrimLocked()
	return nil
}

func (fs *FS) ephWrite(p string, data []byte, off int64) int {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	e := fs.eph[p]
	if e == nil {
		e = &ephFile{}
		fs.eph[p] = e
	}
	e.used = time.Now()
	defer fs.ephTrimLocked()
	end := off + int64(len(data))
	if end > maxEphSize {
		// absurdly large "junk": accept but keep only what fits
		end = maxEphSize
		if off >= end {
			return len(data)
		}
		data = data[:end-off]
	}
	if int64(len(e.data)) < end {
		fs.ephSetLocked(e, append(e.data, make([]byte, end-int64(len(e.data)))...))
	}
	copy(e.data[off:], data)
	e.mtime = time.Now()
	return len(data)
}

func (fs *FS) ephTruncate(p string, size int64) bool {
	if !fs.opt.FilterJunk {
		return false
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	e := fs.eph[p]
	if e == nil {
		return false
	}
	size = min(size, maxEphSize)
	if int64(len(e.data)) > size {
		fs.ephSetLocked(e, e.data[:size])
	} else {
		fs.ephSetLocked(e, append(e.data, make([]byte, size-int64(len(e.data)))...))
	}
	e.mtime, e.used = time.Now(), time.Now()
	return true
}

func (fs *FS) ephRemove(p string) bool {
	if !fs.opt.FilterJunk {
		return false
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.eph[p] == nil {
		return false
	}
	fs.ephDeleteLocked(p)
	return true
}

// ephRename handles renames whose source is a local-only file.
func (fs *FS) ephRename(oldp, newp string) (bool, error) {
	if !fs.opt.FilterJunk {
		return false, nil
	}
	fs.mu.Lock()
	e := fs.eph[oldp]
	if e == nil {
		fs.mu.Unlock()
		if fs.isJunk(newp) {
			// a real file renamed to a junk name: keep it remote (rare)
			return false, nil
		}
		return false, nil
	}
	if fs.isJunk(newp) {
		fs.ephDeleteLocked(newp)
		delete(fs.eph, oldp)
		fs.eph[newp] = e
		e.used = time.Now()
		fs.mu.Unlock()
		return true, nil
	}
	data := append([]byte(nil), e.data...)
	fs.ephDeleteLocked(oldp)
	fs.mu.Unlock()
	// promoted to a real name: materialise it remotely
	if _, err := fs.dirStat(parentOf(newp)); err != nil {
		return true, err
	}
	if err := fs.Create(newp, false); err != nil {
		return true, err
	}
	if len(data) > 0 {
		if _, err := fs.WriteAt(newp, data, 0); err != nil {
			return true, err
		}
	}
	return true, fs.Flush(newp)
}

func (fs *FS) ephMoveTree(oldp, newp string) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	for p, e := range fs.eph {
		if strings.HasPrefix(p, oldp+"/") {
			np := newp + strings.TrimPrefix(p, oldp)
			fs.ephDeleteLocked(np)
			delete(fs.eph, p)
			fs.eph[np] = e
		}
	}
}

func (fs *FS) ephDropTree(dir string) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	for p := range fs.eph {
		if strings.HasPrefix(p, dir+"/") {
			fs.ephDeleteLocked(p)
		}
	}
}

var _ = syscall.EROFS
