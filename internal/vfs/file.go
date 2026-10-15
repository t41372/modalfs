package vfs

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/t41372/modalfs/internal/encryption"
	"github.com/t41372/modalfs/internal/fsync"
	"github.com/t41372/modalfs/internal/modalapi"
)

// snapshot is the version of a file a commit is uploading. Writes made while
// it uploads go on as usual: a dirty block the snapshot still needs is first
// copied aside (to "<idx>.snap"), so the commit sends one consistent version.
type snapshot struct {
	gen, size                       int64
	baseExists                      bool
	baseSize, baseMtime, truncFloor int64
	dirty                           map[int64]bool
	saved                           map[int64]bool // snapshot content moved to <idx>.snap
	floor                           int64          // lowest size the live file reached since
	part                            bool           // the start of the file only (see ahead.go)
	sig                             [2]uint64      // which part
}

func (sn *snapshot) cleanIsRemote(idx, chunkSize int64) bool {
	return sn.baseExists && idx < (sn.baseSize+chunkSize-1)/chunkSize && idx*chunkSize < sn.truncFloor
}

// fileState holds local, not-yet-committed modifications of one file.
//
// Invariants:
//   - dirty[i] => dir/i holds the full current content of block i, padded
//     with zeros up to the plaintext chunk size (sparse file).
//   - a clean block i reads from the remote base version iff
//     the chunk starts below truncFloor (truncFloor = lowest size reached since base);
//     otherwise it reads as zeros.
//   - the base version's partial last block, and any block a truncate cut
//     through, is always dirty, so clean blocks keep their base length.
type fileState struct {
	fs   *FS
	path string
	dir  string

	// mu guards everything below. It's only ever held for local work (a few
	// milliseconds at most), never across a network call, because every
	// Stat, read and write of the file waits for it.
	mu         sync.Mutex
	baseExists bool
	baseSize   int64
	baseMtime  int64
	truncFloor int64
	size       int64
	mtime      time.Time
	dirty      map[int64]bool
	gen        int64
	flushedGen int64
	timer      *time.Timer
	backoff    time.Duration // retry delay after a failed commit
	removed    bool          // detached from fs.files (flushed or discarded)
	deleted    bool
	recovered  bool
	dirtySince time.Time // when the oldest uncommitted change was made

	// sure holds the base's block hashes known to be its own: known before
	// this edit began, or committed by it. Modal gives a block's URL with
	// the file's size but no version, so a block learned since could belong
	// to a replacement of the same size; unsure marks the dirty blocks
	// built from such a block. (nil entries: not sure)
	sure   [][]byte
	unsure map[int64]bool

	// hashes caches each dirty block's SHA-256 from when it was last hashed;
	// a change to the block drops it. A file committed several times while
	// it grows (a long copy) then hashes each block once.
	hashes map[int64][]byte

	// unsynced lists dirty blocks written since the last Sync; synced data
	// survives a power loss (see FS.Sync).
	unsynced        map[int64]bool
	journalUnsynced bool
	dirSynced       bool // its folder's entry in the dirty folder is durable
	// syncMu runs one sync at a time: a sync answers only once everything
	// written before it is durable, including what a sync in flight took.
	syncMu sync.Mutex

	// commitMu serializes commits of this file. Reads and writes never take
	// it, so they carry on while a commit uploads.
	commitMu   sync.Mutex
	snap       *snapshot // the commit in flight, if any
	prehashing bool      // prehashBase started for this state
	lastCommit time.Time // when a commit of it last ended
	lastWrite  time.Time // when it was last changed in this run (zero if recovered)

	// Uploading ahead (ahead.go): each changed block's version (bumped by
	// every change), when it last changed, and its upload ahead.
	bver     map[int64]uint64
	touched  map[int64]time.Time
	ahead    map[int64]aheadState
	lastPart [2]uint64 // the last part committed (see partSigLocked)
	// uploads ahead in flight, and a channel closed when they reach zero:
	// a commit waits for them rather than upload the same blocks again
	aheadBusy int
	aheadIdle chan struct{}

	// The conflict copy this file's earlier commits went to, as they left
	// it on Modal. A long write committed in parts keeps saving to it, so
	// the copy ends up whole instead of one partial copy per part.
	// Guarded by commitMu.
	conflictCopy        string
	copySize, copyMtime int64

	// held is the disk this file's pending changes take, as counted in the
	// shared cache's pending bytes (see accountLocked). leftover: the mount
	// closed, and they're counted as its volume's now.
	held     int64
	leftover bool

	// last journal written (crash recovery)
	jSize, jFloor int64
	jAt           time.Time
}

func newFileState(fs *FS, p, id string, exists bool, e modalapi.Entry) *fileState {
	st := &fileState{fs: fs, path: p, dir: filepath.Join(fs.opt.CacheDir, fs.volID, "dirty", id),
		baseExists: exists, dirty: map[int64]bool{}, hashes: map[int64][]byte{}, unsynced: map[int64]bool{}, mtime: time.Now()}
	st.initAhead()
	if exists {
		st.baseSize, st.baseMtime, st.size = e.Size, e.Mtime, e.Size
		st.truncFloor = e.Size
		st.mtime = time.Unix(e.Mtime, 0)
	} else {
		st.gen = 1 // a new (empty) file must be committed
	}
	return st
}

func (st *fileState) attr() Attr {
	st.mu.Lock()
	defer st.mu.Unlock()
	return Attr{Name: path.Base(st.path), Size: st.size, Mtime: st.mtime, Mode: 0o644}
}

func (st *fileState) blockFile(idx int64) string {
	return filepath.Join(st.dir, strconv.FormatInt(idx, 10))
}

func (st *fileState) snapFile(idx int64) string { return st.blockFile(idx) + ".snap" }

// preserveLocked keeps the in-flight commit's copy of block idx before the
// live file changes or drops it. move is for a block about to be deleted.
func (st *fileState) preserveLocked(idx int64, move bool) error {
	delete(st.hashes, idx)
	sn := st.snap
	if sn == nil || !sn.dirty[idx] || sn.saved[idx] {
		return nil
	}
	var err error
	if move {
		err = os.Rename(st.blockFile(idx), st.snapFile(idx))
	} else {
		err = copyFile(st.blockFile(idx), st.snapFile(idx))
	}
	if err != nil {
		return err
	}
	sn.saved[idx] = true
	return nil
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// accountLocked brings the shared count of pending bytes up to date with
// the block files this file keeps: its dirty blocks and the copies set
// aside for the commit in flight. Every change to them ends with it. What a
// closed mount leaves is counted as its volume's (see Close).
func (st *fileState) accountLocked() {
	var n int64
	if !st.removed && !st.leftover {
		n = int64(len(st.dirty))
		if st.snap != nil {
			n += int64(len(st.snap.saved))
		}
		n *= st.fs.chunkSize()
	}
	if d := n - st.held; d != 0 {
		st.held = n
		st.fs.cache.addPending(d)
	}
}

func (st *fileState) baseBlocks() int64 { return int64(st.fs.numBlocks(st.baseSize)) }

func (st *fileState) cleanIsRemote(idx int64) bool {
	return st.baseExists && idx < st.baseBlocks() && idx*st.fs.chunkSize() < st.truncFloor
}

// ensureDirtyLocked materialises block idx into a local dirty block file.
// If fullCover is true the caller will overwrite all meaningful bytes, so the
// remote content need not be fetched.
func (st *fileState) ensureDirtyLocked(idx int64, fullCover bool) error {
	return st.ensureDirtyFromLocked(idx, fullCover, nil)
}

// ensureDirtyFromLocked is ensureDirtyLocked with the base block's content
// already fetched (nil to fetch it here).
func (st *fileState) ensureDirtyFromLocked(idx int64, fullCover bool, base []byte) error {
	if st.dirty[idx] {
		return nil
	}
	if err := os.MkdirAll(st.dir, 0o700); err != nil {
		return err
	}
	if len(st.dirty) == 0 {
		st.writeJournalLocked(true)
		if st.baseExists && !st.prehashing {
			st.prehashing = true
			go st.prehashBase(st.baseSize, st.baseMtime)
		}
	}
	// Build the block under a temp name, so a failure never leaves a
	// partial block file that recovery would take for data.
	tmp := st.blockFile(idx) + ".tmp"
	f, err := os.OpenFile(tmp, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	fromBase := !fullCover && st.cleanIsRemote(idx)
	if fromBase {
		data := base
		if data == nil {
			data, _, err = st.fs.remoteBlock(st.path, st.baseSize, st.baseMtime, idx, true)
		}
		if err == nil {
			_, err = f.WriteAt(data, 0)
		}
		// The block now holds the file's unchanged bytes, which the user
		// never wrote and no NFS COMMIT covers: make them durable now, or a
		// power loss could leave a hole that recovery commits as zeros.
		if err == nil {
			err = fsync.File(f)
		}
	}
	if err == nil {
		err = f.Truncate(st.fs.chunkSize())
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, st.blockFile(idx))
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	st.dirty[idx] = true
	delete(st.unsure, idx)
	if fromBase && !st.sureLocked(idx, st.fs.mdb.Hash(st.path, st.baseSize, st.baseMtime, idx)) {
		if st.unsure == nil {
			st.unsure = map[int64]bool{}
		}
		st.unsure[idx] = true
	}
	delete(st.hashes, idx)
	st.unsynced[idx] = true
	st.changedBlockLocked(idx)
	return nil
}

func (st *fileState) sureLocked(idx int64, h []byte) bool {
	return h != nil && idx < int64(len(st.sure)) && bytes.Equal(st.sure[idx], h)
}

// mayMix reports whether the snapshot may hold blocks of a same-size
// replacement of its base, learned since the edit began.
func (st *fileState) mayMix(sn *snapshot, hashes [][]byte) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	for i := range hashes {
		idx := int64(i)
		switch {
		case sn.dirty[idx]:
			if st.unsure[idx] {
				return true
			}
		case sn.cleanIsRemote(idx, st.fs.chunkSize()):
			if !st.sureLocked(idx, hashes[i]) {
				return true
			}
		}
	}
	return false
}

// dropIfUnchangedLocked detaches a state that holds no completed change (an
// existing file whose first write or truncate failed), so it doesn't keep
// serving a stale view of the remote file. Any dirty blocks are leftovers of
// the failed call, which already reported the error.
func (st *fileState) dropIfUnchangedLocked() {
	if st.gen != st.flushedGen {
		return
	}
	st.removed = true
	_ = os.RemoveAll(st.dir)
	st.accountLocked()
	st.fs.mu.Lock()
	if st.fs.files[st.path] == st {
		delete(st.fs.files, st.path)
	}
	st.fs.mu.Unlock()
}

func (st *fileState) readBlockLocked(idx int64) ([]byte, error) {
	n := st.fs.blockLen(st.size, idx)
	if st.dirty[idx] {
		buf := make([]byte, n)
		f, err := os.Open(st.blockFile(idx))
		if err != nil {
			return nil, err
		}
		defer f.Close()
		if _, err := f.ReadAt(buf, 0); err != nil && n > 0 {
			return nil, err
		}
		return buf, nil
	}
	if st.cleanIsRemote(idx) {
		data, _, err := st.fs.remoteBlock(st.path, st.baseSize, st.baseMtime, idx, true)
		if err != nil {
			return nil, err
		}
		return data[:min(n, len(data))], nil
	}
	return make([]byte, n), nil
}

// fetchBase downloads (or reads from the cache) the base blocks a read or
// write of [off, end) will need, without holding the lock. It returns the
// base version they belong to, so the caller can check it's still current.
func (st *fileState) fetchBase(off, end int64, skipFull bool) (map[int64][]byte, [2]int64) {
	st.mu.Lock()
	ver := [2]int64{st.baseSize, st.baseMtime}
	var need []int64
	for idx := off / st.fs.chunkSize(); idx*st.fs.chunkSize() < end; idx++ {
		if st.dirty[idx] || !st.cleanIsRemote(idx) {
			continue
		}
		if skipFull {
			// a write that covers the block's whole base content needs nothing
			inBlk := max(off-idx*st.fs.chunkSize(), 0)
			want := min(end, (idx+1)*st.fs.chunkSize()) - (idx*st.fs.chunkSize() + inBlk)
			if inBlk == 0 && want >= int64(st.fs.blockLen(min(st.baseSize, st.truncFloor), idx)) {
				continue
			}
		}
		need = append(need, idx)
	}
	p, size, mtime := st.path, st.baseSize, st.baseMtime
	st.mu.Unlock()
	got := map[int64][]byte{}
	for _, idx := range need {
		if data, _, err := st.fs.remoteBlock(p, size, mtime, idx, true); err == nil {
			got[idx] = data
		}
	}
	return got, ver
}

func (st *fileState) readAt(buf []byte, off int64) (int, error) {
	// Parts that come from the base version on Modal are read first, without
	// the lock and as range reads; the edited parts are then filled in under
	// the lock. If a commit changed the base meanwhile, read it all again.
	for attempt := 0; ; attempt++ {
		st.mu.Lock()
		if off >= st.size {
			st.mu.Unlock()
			return 0, nil
		}
		end := min(off+int64(len(buf)), st.size)
		ver := [3]int64{st.baseSize, st.baseMtime, st.truncFloor}
		type part struct{ idx, inBlk, at, n int64 }
		var remote []part
		for pos := off; pos < end; {
			idx := pos / st.fs.chunkSize()
			inBlk := pos - idx*st.fs.chunkSize()
			want := min(end-pos, st.fs.chunkSize()-inBlk)
			if !st.dirty[idx] && st.cleanIsRemote(idx) {
				remote = append(remote, part{idx, inBlk, pos - off, want})
			}
			pos += want
		}
		p, size, mtime := st.path, st.baseSize, st.baseMtime
		st.mu.Unlock()

		for _, r := range remote {
			if err := st.fs.readRemoteBlock(p, size, mtime, r.idx, buf[r.at:r.at+r.n], r.inBlk); err != nil {
				return 0, err
			}
		}

		st.mu.Lock()
		if ver != [3]int64{st.baseSize, st.baseMtime, st.truncFloor} && attempt < 3 {
			st.mu.Unlock()
			continue
		}
		n, err := st.fillLocalLocked(buf, off)
		st.mu.Unlock()
		return n, err
	}
}

// fillLocalLocked fills the parts of buf (read at off) that are dirty or
// zero; parts from the base version are already there (see readAt). A part
// that was base when planned but became dirty since is read from its block.
func (st *fileState) fillLocalLocked(buf []byte, off int64) (int, error) {
	if off >= st.size {
		return 0, nil
	}
	end := min(off+int64(len(buf)), st.size)
	n := 0
	for pos := off; pos < end; {
		idx := pos / st.fs.chunkSize()
		inBlk := pos - idx*st.fs.chunkSize()
		want := min(end-pos, st.fs.chunkSize()-inBlk)
		switch {
		case st.dirty[idx]:
			f, err := os.Open(st.blockFile(idx))
			if err != nil {
				return n, err
			}
			_, err = f.ReadAt(buf[n:n+int(want)], inBlk)
			f.Close()
			if err != nil {
				return n, err
			}
		case st.cleanIsRemote(idx):
			// read above, without the lock
		default:
			clear(buf[n : n+int(want)])
		}
		n += int(want)
		pos += want
	}
	return n, nil
}

// growLocked extends the logical size; the old partial last block changes
// length and therefore must become dirty.
func (st *fileState) growLocked(newSize int64) error {
	if newSize <= st.size {
		return nil
	}
	if st.size%st.fs.chunkSize() != 0 {
		last := st.size / st.fs.chunkSize()
		if err := st.ensureDirtyLocked(last, false); err != nil {
			return err
		}
		st.changedBlockLocked(last) // it gets longer
	}
	st.size = newSize
	return nil
}

func (st *fileState) writeAt(data []byte, off int64) (n int, err error) {
	// A partial write to an existing block needs its old content: fetch that
	// before taking the lock, so a slow download doesn't stall the file.
	pre, ver := st.fetchBase(off, off+int64(len(data)), true)
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.removed {
		return 0, errDetached
	}
	defer st.accountLocked()
	if ver != [2]int64{st.baseSize, st.baseMtime} {
		pre = nil
	}
	modified := false
	oldSize := st.size
	defer func() {
		if err == nil {
			return
		}
		if modified {
			// Part of the write landed (the caller still gets the error);
			// commit what's there rather than leaving it pending unseen.
			st.changedLocked()
			st.writeJournalLocked(true)
		} else {
			st.size = oldSize // nothing landed: don't grow the file with zeros
			st.dropIfUnchangedLocked()
		}
	}()
	end := off + int64(len(data))
	if err = st.growLocked(end); err != nil {
		return 0, err
	}
	for pos := off; pos < end; {
		idx := pos / st.fs.chunkSize()
		inBlk := pos - idx*st.fs.chunkSize()
		want := min(end-pos, st.fs.chunkSize()-inBlk)
		baseLen := int64(0)
		if st.cleanIsRemote(idx) {
			baseLen = int64(st.fs.blockLen(min(st.baseSize, st.truncFloor), idx))
		}
		full := inBlk == 0 && want >= baseLen
		if err := st.ensureDirtyFromLocked(idx, full, pre[idx]); err != nil {
			return n, err
		}
		if err := st.preserveLocked(idx, false); err != nil {
			return n, err
		}
		f, err := os.OpenFile(st.blockFile(idx), os.O_RDWR, 0o600)
		if err != nil {
			return n, err
		}
		_, err = f.WriteAt(data[n:n+int(want)], inBlk)
		f.Close()
		modified = true
		if full {
			delete(st.unsure, idx) // every byte of the base it held is ours now
		}
		st.unsynced[idx] = true
		st.changedBlockLocked(idx)
		if err != nil {
			return n, err
		}
		n += int(want)
		pos += want
	}
	st.changedLocked()
	st.writeJournalLocked(false)
	if st.aheadManagedLocked() {
		st.fs.kickAhead()
	}
	return n, nil
}

// changedLocked records a completed change and schedules its commit: once
// the file has been idle for WriteBack, but no later than MaxPending after
// the oldest uncommitted change, so a long copy is uploaded as it goes
// instead of all at the end.
func (st *fileState) changedLocked() {
	st.gen++
	now := time.Now()
	st.mtime, st.lastWrite = now, now
	if st.dirtySince.IsZero() {
		st.dirtySince = now
	}
	d := st.fs.opt.WriteBack
	if limit := st.fs.opt.MaxPending; limit > 0 {
		d = min(d, max(limit-now.Sub(st.dirtySince), 0))
	}
	st.scheduleLocked(d)
}

func (st *fileState) truncate(newSize int64) (err error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.removed {
		return errDetached
	}
	defer st.accountLocked()
	defer func() {
		if err != nil {
			st.dropIfUnchangedLocked()
		}
	}()
	if newSize > st.size {
		if err := st.growLocked(newSize); err != nil {
			return err
		}
	} else if newSize < st.size {
		cut := newSize%st.fs.chunkSize() != 0
		if cut {
			// the block it cuts through first, from Modal maybe: if that
			// fails, nothing has changed yet
			if err := st.ensureDirtyLocked(newSize/st.fs.chunkSize(), false); err != nil {
				return err
			}
		}
		for idx := range st.dirty {
			if idx*st.fs.chunkSize() >= newSize {
				if err := st.preserveLocked(idx, true); err != nil {
					return err
				}
				_ = os.Remove(st.blockFile(idx))
				delete(st.dirty, idx)
				delete(st.unsure, idx)
				st.forgetBlockLocked(idx)
			}
		}
		if st.snap != nil {
			st.snap.floor = min(st.snap.floor, newSize)
		}
		if cut {
			idx := newSize / st.fs.chunkSize()
			if err := st.preserveLocked(idx, false); err != nil {
				return err
			}
			// zero the tail so a later extension reads zeros; in place, so
			// a crash never leaves a short block file
			f, err := os.OpenFile(st.blockFile(idx), os.O_WRONLY, 0)
			if err != nil {
				return err
			}
			_, err = f.WriteAt(make([]byte, st.fs.chunkSize()-newSize%st.fs.chunkSize()), newSize%st.fs.chunkSize())
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			st.unsynced[idx] = true
			st.changedBlockLocked(idx)
			if err != nil {
				return err
			}
		}
		st.truncFloor = min(st.truncFloor, newSize)
		st.size = newSize
	}
	st.changedLocked()
	st.writeJournalLocked(true)
	return nil
}

// touch records a new, empty file. Its journal is what keeps it through a
// crash: it has no blocks yet.
func (st *fileState) touch() {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.changedLocked()
	st.writeJournalLocked(true)
}

func (st *fileState) schedule(d time.Duration) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.gen != st.flushedGen {
		st.scheduleLocked(d)
	}
}

func (st *fileState) scheduleLocked(d time.Duration) {
	if st.fs.closed.Load() {
		return
	}
	if st.timer != nil {
		st.timer.Reset(d)
		return
	}
	st.timer = time.AfterFunc(d, func() {
		if st.fs.closed.Load() {
			return
		}
		commit, part := st.flush, false
		st.mu.Lock()
		writing := st.fs.opt.WriteBack - time.Since(st.lastWrite)
		if st.fs.waiting.Load() > 0 {
			writing = st.fs.opt.WriteBack // writes wait for room: it isn't idle
		}
		if st.aheadManagedLocked() && writing > 0 {
			// -max-pending while it's still being written: its blocks are
			// on their way already, so commit the start that has arrived,
			// and the whole file once it's idle
			commit, part = st.flushPart, true
			st.scheduleLocked(writing)
		}
		st.mu.Unlock()
		if part {
			if err := commit(); err != nil {
				st.fs.uploadFailed(err) // retried by the next part, or the whole commit
			}
			return
		}
		if err := commit(); err != nil {
			// back off: 10 s, doubling up to 5 min
			st.mu.Lock()
			st.backoff = min(max(2*st.backoff, 10*time.Second), 5*time.Minute)
			retry := st.backoff
			st.mu.Unlock()
			st.fs.log.Error("commit failed; will retry", "path", st.path, "err", err, "retry_in", retry)
			st.fs.uploadFailed(err)
			st.fs.event("error", st.path, err.Error())
			st.schedule(retry)
		}
	})
}

// discard drops local changes. Returns whether a remote copy may exist: the
// base version, or a commit that finished while we waited for the lock.
func (st *fileState) discard() bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.timer != nil {
		st.timer.Stop()
	}
	st.removed, st.deleted = true, true
	_ = os.RemoveAll(st.dir)
	st.accountLocked()
	return st.baseExists || st.flushedGen != 0 || st.snap != nil
}

// maxName is the longest file name Modal takes, in bytes.
const maxName = 255

// conflictName names a conflict copy of p, shortening the name to fit if
// it has to.
func (fs *FS) conflictName(p string) string {
	suffix, limit := ".conflict-"+time.Now().Format("20060102-150405"), maxName
	if fs.c.Cipher != nil {
		suffix, limit = suffix+"-"+rand.Text(), encryption.MaxNameBytes
	}
	ext := path.Ext(p)
	if len(ext) > limit-len(suffix)-1 {
		ext = ""
	}
	stem := strings.TrimSuffix(path.Base(p), ext)
	budget := limit - len(suffix) - len(ext)
	stem = stem[:min(len(stem), budget)]
	for !utf8.ValidString(stem) && len(stem) > 0 {
		stem = stem[:len(stem)-1]
	}
	return path.Join(path.Dir(p), stem+suffix+ext)
}

// copyTarget names the conflict copy a commit that conflicts goes to: the
// one this file's earlier parts went to, if it's still as they left it, or
// a new one. Called with commitMu held.
func (st *fileState) copyTarget(path string) string {
	if st.conflictCopy != "" {
		e, err := st.fs.remoteStat(st.conflictCopy, true)
		if err == nil && e.Size == st.copySize && e.Mtime == st.copyMtime {
			return st.conflictCopy
		}
	}
	return st.fs.conflictName(path)
}

// flush commits the file. It takes a snapshot under the lock, then hashes
// and uploads without it, so the file (and the mount) stay usable while a
// large commit runs; changes made meanwhile go into the next commit.
func (st *fileState) flush() error { return st.commit(false) }

// flushPart commits the start of the file that's on Modal already (see
// ahead.go), if there's anything new in it.
func (st *fileState) flushPart() error { return st.commit(true) }

func (st *fileState) commit(part bool) error {
	st.commitMu.Lock()
	defer st.commitMu.Unlock()

	st.mu.Lock()
	if st.removed || st.gen == st.flushedGen {
		st.mu.Unlock()
		return nil
	}
	sn := &snapshot{gen: st.gen, size: st.size, baseExists: st.baseExists, baseSize: st.baseSize,
		baseMtime: st.baseMtime, truncFloor: st.truncFloor, dirty: map[int64]bool{}, saved: map[int64]bool{}, floor: st.size}
	if part {
		_, _, ok, whole := st.partLocked()
		if !ok && !whole {
			st.mu.Unlock()
			return nil
		}
		part = !whole // everything is on Modal: commit it all, just as quickly
	}
	if part {
		cut, blocks, _, _ := st.partLocked()
		sn.size, sn.floor, sn.part = cut, cut, true
		for _, i := range blocks {
			sn.dirty[i] = true
		}
		sn.sig = st.partSigLocked(cut, blocks)
	} else {
		for i := range st.dirty {
			sn.dirty[i] = true
		}
	}
	st.snap = sn // no more uploads ahead start for this file (see aheadJobs)
	path := st.path
	st.mu.Unlock()

	committed := false
	defer func() {
		st.mu.Lock()
		if !committed {
			st.dropSnapshotLocked()
		}
		st.mu.Unlock()
		st.fs.kickAhead() // its blocks were left alone while it committed
	}()

	fs := st.fs
	t0 := time.Now()
	target := path
	if err := st.waitAhead(); err != nil {
		return err
	}

	// Optimistic concurrency: has someone else changed the file since we
	// based our edits on it? (Modal has no CAS, so this is best-effort.)
	// If we can't tell, don't commit: the retry will check again.
	cur, err := fs.remoteStat(path, true)
	looked := time.Now()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("check for remote changes: %w", err)
	}
	if err == nil {
		changed := !sn.baseExists || cur.Size != sn.baseSize || cur.Mtime != sn.baseMtime
		if changed && fs.opt.OnConflict != "overwrite" {
			target = st.copyTarget(path)
		}
	}

	found := err == nil
	hashes, err := st.snapshotHashes(sn, cur, found)
	// Replaced by a version of the same size: the blocks learned since the
	// edit began may be the replacement's, which a conflict copy would mix
	// with ours. Set the changes aside instead.
	sameSize := func(e modalapi.Entry) bool {
		return sn.baseExists && e.Size == sn.baseSize && e.Mtime != sn.baseMtime && st.mayMix(sn, hashes)
	}
	if err == nil && found && sameSize(cur) {
		err = errBaseLost
	}
	setAside := func(why error) error {
		if err := st.park(sn, why); err != nil {
			return err // tried again, with the error shown
		}
		committed = true // nothing left to retry; park kept the data
		return nil
	}
	if err == nil && target != path && target != st.conflictCopy {
		fs.log.Warn("remote file changed while editing; saving local version as conflict copy", "path", path, "copy", target)
		fs.event("conflict", path, target)
	}
	if errors.Is(err, errBaseLost) {
		return setAside(err)
	}
	if err != nil {
		if st.isDeleted() {
			return nil
		}
		return err
	}
	// Uploading can take minutes: just before committing, look once more for
	// a change made meanwhile, and save to a conflict copy if there was one.
	recheck := func() error {
		if target != path || fs.opt.OnConflict == "overwrite" {
			return nil
		}
		now, err := fs.remoteStat(path, true)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("check for remote changes: %w", err)
		}
		if !sn.baseExists || now.Size != sn.baseSize || now.Mtime != sn.baseMtime {
			return errLateConflict
		}
		return nil
	}
	put := func(to string, check func() error) (int, error) {
		return fs.c.PutChecked(fs.ctx, []modalapi.PutFile{{
			Path: to, Size: sn.size, Hashes: hashes,
			Block: func(i int) ([]byte, error) { return st.readSnapshotBlock(sn, int64(i)) },
		}}, check)
	}
	// The first request commits at once if Modal has every block already
	// (see PutChecked): if working out the blocks took a while (reading the
	// unchanged ones), look again before it too. A look under a second old
	// is as good as a new one, since there's no compare-and-swap anyway.
	var uploaded int
	if time.Since(looked) > time.Second {
		err = recheck()
	}
	if err == nil {
		uploaded, err = put(target, recheck)
	}
	if errors.Is(err, errLateConflict) {
		if now, serr := fs.remoteStat(path, false); serr == nil && sameSize(now) {
			return setAside(errBaseLost)
		}
		target = st.copyTarget(path)
		if target != st.conflictCopy {
			fs.log.Warn("remote file changed while committing; saving local version as conflict copy", "path", path, "copy", target)
			fs.event("conflict", path, target)
		}
		var more int
		more, err = put(target, nil) // only this file's commits write its copy
		uploaded += more
	}
	if err != nil {
		if st.isDeleted() {
			return nil // deleted while it uploaded: nothing left to commit
		}
		return err
	}
	fs.invalidate(parentOf(target))
	e, err := fs.remoteStat(target, true)
	if err != nil {
		return fmt.Errorf("stat after commit: %w", err)
	}
	m := &manifest{Size: sn.size, Mtime: e.Mtime, Hashes: hashes}
	fs.mdb.Put(target, m)
	if target != path {
		st.conflictCopy, st.copySize, st.copyMtime = target, sn.size, e.Mtime
	}

	st.mu.Lock()
	committed = true
	deleted := st.deleted
	st.finishCommitLocked(sn, target, e.Mtime, hashes)
	finished := st.removed // nothing written since the snapshot
	st.mu.Unlock()

	if deleted {
		// removed while it was uploading: don't leave the upload behind
		if err := fs.c.Remove(fs.ctx, target, false); err != nil && !modalapi.IsNotFound(err) {
			fs.log.Warn("could not remove a file deleted during its commit", "path", target, "err", err)
		}
		fs.invalidate(parentOf(target))
		return nil
	}
	// A sidecar costs an upload and a commit of its own: write it once the
	// file is done, not for each part of a long write.
	if fs.opt.SidecarMin > 0 && sn.size >= fs.opt.SidecarMin && finished {
		if err := fs.writeSidecar(target, m); err != nil {
			fs.log.Warn("sidecar write failed", "path", target, "err", err)
		}
	}
	n := len(hashes)
	fs.ctr.uploadedBlocks.Add(int64(uploaded))
	if sn.part {
		// a long copy has many: Activity shows the file once it's done
		fs.committedPart()
		fs.log.Debug("committed part", "path", target, "size", sn.size, "blocks", n, "dirty", len(sn.dirty), "uploaded", uploaded, "took", time.Since(t0).Round(time.Millisecond))
	} else {
		fs.event("commit", target, fmt.Sprintf("%d of %d blocks uploaded", uploaded, n))
		fs.log.Info("committed", "path", target, "size", sn.size, "blocks", n, "dirty", len(sn.dirty), "uploaded", uploaded, "took", time.Since(t0).Round(time.Millisecond))
	}
	if !finished {
		st.maybePart() // more may have arrived meanwhile
	}
	return nil
}

var (
	errLateConflict = errors.New("the remote file changed during the upload")
	// errBaseLost: the edit keeps parts of the version it was made on that
	// modalfs never read, and that version is gone from Modal.
	errBaseLost = errors.New("the version this edit was based on changed on Modal before modalfs read the unchanged parts")
)

// snapshotHashes works out the snapshot's block list, reading and hashing
// only what isn't known yet. It holds the lock only to read one block at a
// time. cur is the remote file as it is now (if curOK): unknown base blocks
// can only be read while it's still the version the edit was made on.
func (st *fileState) snapshotHashes(sn *snapshot, cur modalapi.Entry, curOK bool) ([][]byte, error) {
	fs := st.fs
	n := int64(st.fs.numBlocks(sn.size))
	hashes := make([][]byte, n)
	var unknown []int64
	var base *manifest
	if sn.baseExists {
		base = fs.mdb.Get(st.path, sn.baseSize, sn.baseMtime)
	}
	zeroHashes := map[int][]byte{}
	for i := int64(0); i < n; i++ {
		switch {
		case sn.dirty[i]:
			st.mu.Lock()
			h := st.hashes[i]
			if sn.saved[i] {
				h = nil // the live block changed; hash the snapshot's copy
			}
			st.mu.Unlock()
			if h == nil {
				data, err := st.readSnapshotBlock(sn, i)
				if err != nil {
					return nil, err
				}
				h = fs.c.HashBlock(data)
				st.mu.Lock()
				if !sn.saved[i] && st.dirty[i] { // still the content just hashed
					st.hashes[i] = h
				}
				st.mu.Unlock()
			}
			hashes[i] = h
		case sn.cleanIsRemote(i, fs.chunkSize()):
			if base != nil && int(i) < len(base.Hashes) && base.Hashes[i] != nil {
				hashes[i] = base.Hashes[i]
			} else {
				unknown = append(unknown, i)
			}
		default:
			l := st.fs.blockLen(sn.size, i)
			if zeroHashes[l] == nil {
				zeroHashes[l] = fs.c.HashBlock(make([]byte, l))
			}
			hashes[i] = zeroHashes[l]
		}
	}
	if len(unknown) > 0 && fs.opt.SidecarMin > 0 && sn.baseSize >= fs.opt.SidecarMin {
		if sc := fs.loadSidecar(st.path, sn.baseSize, sn.baseMtime); sc != nil {
			var still []int64
			for _, i := range unknown {
				if int(i) < len(sc.Hashes) && sc.Hashes[i] != nil {
					hashes[i] = sc.Hashes[i]
				} else {
					still = append(still, i)
				}
			}
			unknown = still
		}
	}
	if len(unknown) > 0 && (!curOK || cur.Size != sn.baseSize || cur.Mtime != sn.baseMtime) {
		return nil, errBaseLost
	}
	if len(unknown) > 0 {
		// Modal never reveals block hashes, so unchanged blocks we have
		// never seen must be streamed once and hashed locally.
		fs.log.Info("hashing unseen blocks to commit partial write", "path", st.path, "blocks", len(unknown), "MiB", len(unknown)*8)
		cacheThem := len(unknown) <= 32
		errc := make(chan error, len(unknown))
		sem := make(chan struct{}, 8)
		var wg sync.WaitGroup
		for _, i := range unknown {
			wg.Add(1)
			sem <- struct{}{}
			go func(i int64) {
				defer wg.Done()
				defer func() { <-sem }()
				_, h, err := fs.remoteBlock(st.path, sn.baseSize, sn.baseMtime, i, cacheThem)
				if errors.Is(err, syscall.ESTALE) || modalapi.IsNotFound(err) {
					err = errBaseLost
				}
				if err != nil {
					errc <- err
					return
				}
				hashes[i] = h
			}(i)
		}
		wg.Wait()
		close(errc)
		if err := <-errc; err != nil {
			return nil, err
		}
	}
	return hashes, nil
}

func (st *fileState) isDeleted() bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.deleted
}

// readSnapshotBlock returns block idx as the snapshot has it.
func (st *fileState) readSnapshotBlock(sn *snapshot, idx int64) ([]byte, error) {
	n := st.fs.blockLen(sn.size, idx)
	if sn.dirty[idx] {
		st.mu.Lock()
		defer st.mu.Unlock()
		if st.deleted {
			return nil, errDetached
		}
		p := st.blockFile(idx)
		if sn.saved[idx] {
			p = st.snapFile(idx)
		}
		buf := make([]byte, n)
		f, err := os.Open(p)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		if _, err := f.ReadAt(buf, 0); err != nil && n > 0 {
			return nil, err
		}
		return buf, nil
	}
	if sn.cleanIsRemote(idx, st.fs.chunkSize()) {
		data, _, err := st.fs.remoteBlock(st.path, sn.baseSize, sn.baseMtime, idx, true)
		if err != nil {
			return nil, err
		}
		return data[:min(n, len(data))], nil
	}
	return make([]byte, n), nil
}

// dropSnapshotLocked ends a commit that didn't happen; the live state still
// holds every change, and the copies set aside for the snapshot are stale.
func (st *fileState) dropSnapshotLocked() {
	if sn := st.snap; sn != nil {
		for i := range sn.saved {
			_ = os.Remove(st.snapFile(i))
		}
	}
	st.snap = nil
	st.accountLocked()
}

// finishCommitLocked records a finished commit. If nothing changed during
// it, the state is done and goes away. Otherwise the committed version
// becomes the new base and the newer changes stay pending.
func (st *fileState) finishCommitLocked(sn *snapshot, target string, mtime int64, hashes [][]byte) {
	fs := st.fs
	st.snap = nil
	st.lastCommit = time.Now()
	if sn.part {
		st.lastPart = sn.sig
	} else {
		st.flushedGen = sn.gen
	}
	st.backoff = 0
	defer st.accountLocked()
	if st.deleted {
		return
	}
	if st.gen == sn.gen && !sn.part {
		for i := range sn.dirty {
			if int(i) < len(hashes) {
				fs.cache.Adopt(hashes[i], st.blockFile(i), int64(st.fs.blockLen(sn.size, i)))
			}
		}
		_ = os.RemoveAll(st.dir)
		st.dirty = map[int64]bool{}
		st.hashes = map[int64][]byte{}
		st.initAhead()
		st.dirtySince = time.Time{}
		st.removed = true
		if st.timer != nil {
			st.timer.Stop()
		}
		fs.mu.Lock()
		if fs.files[st.path] == st {
			delete(fs.files, st.path)
		}
		fs.mu.Unlock()
		return
	}
	// Written to while it uploaded. The snapshot's own copies are committed
	// data now; the live blocks hold the newer changes.
	for i := range sn.saved {
		if int(i) < len(hashes) && fs.cache.Adopt(hashes[i], st.snapFile(i), int64(st.fs.blockLen(sn.size, i))) {
			continue
		}
		_ = os.Remove(st.snapFile(i))
	}
	if target == st.path {
		// The committed version is the new base. Snapshot blocks the live
		// file didn't touch are identical to it, so they may stay dirty.
		st.baseExists, st.baseSize, st.baseMtime = true, sn.size, mtime
		st.sure, st.unsure = append([][]byte(nil), hashes...), nil
		st.truncFloor = min(sn.size, sn.floor)
		st.releaseCommittedLocked(sn, hashes)
	}
	st.dirtySince = time.Now()
}

// releaseCommittedLocked lets go of the blocks a commit made while the file
// was still being written put on Modal: those the live file hasn't changed
// since are part of the new base now, so they become clean (moved into the
// cache) and stop taking pending room. A long copy then keeps only what
// isn't uploaded yet, not the whole file.
//
// The journal must name the new base on disk first: recovery fills a clean
// block from the base it finds there, so a released block with the old base
// in the journal would come back as the old content.
func (st *fileState) releaseCommittedLocked(sn *snapshot, hashes [][]byte) {
	fs := st.fs
	var done []int64
	for i := range sn.dirty {
		// whole blocks inside the new base only: its partial last block, and
		// any block a truncate cut through since, must stay dirty
		if !sn.saved[i] && st.dirty[i] && (i+1)*fs.chunkSize() <= st.truncFloor && int(i) < len(hashes) {
			done = append(done, i)
		}
	}
	if err := st.syncJournalLocked(); err != nil {
		fs.log.Warn("couldn't save the journal; committed blocks stay on disk until the file is done", "path", st.path, "err", err)
		return
	}
	for _, i := range done {
		if !fs.cache.Adopt(hashes[i], st.blockFile(i), int64(fs.blockLen(sn.size, i))) {
			_ = os.Remove(st.blockFile(i))
		}
		delete(st.dirty, i)
		delete(st.hashes, i)
		delete(st.unsynced, i)
		st.forgetBlockLocked(i)
	}
}

// syncJournalLocked writes the journal and makes it durable.
func (st *fileState) syncJournalLocked() error {
	if err := st.writeJournalLocked(true); err != nil {
		return err
	}
	st.journalUnsynced = false
	for _, p := range []string{filepath.Join(st.dir, journalName), st.dir} {
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		err = fsync.File(f)
		f.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// sync makes the file's written data durable: the blocks written since the
// last sync, the journal, and the directory holding them. The files are
// synced without the lock; data written after the snapshot of the list goes
// with the next sync, and what a failed sync took goes back on the list.
func (st *fileState) sync() (err error) {
	st.syncMu.Lock()
	defer st.syncMu.Unlock()
	st.mu.Lock()
	if st.removed {
		st.mu.Unlock()
		return nil
	}
	// The journal saves a growing file's size at most once a second, and
	// recovery can't tell trailing zeros from padding: save the size now
	// (and the journal itself, if writing it failed).
	if st.jAt.IsZero() || st.jSize != st.size || st.jFloor != st.truncFloor {
		if err := st.writeJournalLocked(true); err != nil {
			st.mu.Unlock()
			return fmt.Errorf("sync %s: %w", st.path, err)
		}
	}
	var blocks []string
	for idx := range st.unsynced {
		blocks = append(blocks, st.blockFile(idx))
	}
	taken := st.unsynced
	st.unsynced = map[int64]bool{}
	journal := st.journalUnsynced
	st.journalUnsynced = false
	dir := st.dir
	dirs := []string{dir}
	if !st.dirSynced {
		dirs = append(dirs, filepath.Dir(dir)) // the folder's own entry
	}
	st.mu.Unlock()
	defer func() {
		if err != nil {
			st.mu.Lock()
			for idx := range taken {
				st.unsynced[idx] = true
			}
			st.journalUnsynced = st.journalUnsynced || journal
			st.mu.Unlock()
		}
	}()
	if journal {
		blocks = append(blocks, filepath.Join(dir, journalName))
	}
	for _, p := range append(blocks, dirs...) {
		f, err := os.Open(p)
		if errors.Is(err, os.ErrNotExist) {
			continue // committed or truncated away meanwhile
		}
		if err != nil {
			return err
		}
		err = fsync.File(f)
		f.Close()
		if err != nil {
			return fmt.Errorf("sync %s: %w", st.path, err)
		}
	}
	st.mu.Lock()
	st.dirSynced = true
	st.mu.Unlock()
	return nil
}

// prehashBase learns the hashes of the base version's blocks in the
// background, once a file is first edited. A commit needs the hash of every
// block it doesn't change, and Modal never reveals them, so they're streamed
// and hashed here (the same work the commit would otherwise do) while the
// version the edit is based on is surely still there.
func (st *fileState) prehashBase(size, mtime int64) {
	fs := st.fs
	n := int64(st.fs.numBlocks(size))
	m := fs.mdb.Get(st.path, size, mtime)
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i := int64(0); i < n; i++ {
		if m != nil && int(i) < len(m.Hashes) && m.Hashes[i] != nil {
			continue
		}
		st.mu.Lock()
		skip := st.removed || st.baseSize != size || st.baseMtime != mtime || (st.dirty[i] && i*st.fs.chunkSize() >= st.truncFloor)
		st.mu.Unlock()
		if skip || fs.closed.Load() {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int64) {
			defer wg.Done()
			defer func() { <-sem }()
			_, _, _ = fs.remoteBlock(st.path, size, mtime, i, false) // records the hash
		}(i)
	}
	wg.Wait()
}

// park sets aside changes that can't be committed (errBaseLost): they're
// moved out of the way, with a note saying what they are, and reported.
// Retrying would fail forever and keep the volume from unmounting.
func (st *fileState) park(sn *snapshot, why error) error {
	fs := st.fs
	st.mu.Lock()
	defer st.mu.Unlock()
	st.dropSnapshotLocked()
	dest := filepath.Join(fs.opt.CacheDir, fs.volID, "unsaved", time.Now().Format("20060102-150405")+"-"+strings.ReplaceAll(path.Base(st.path), "/", "_"))
	_ = os.MkdirAll(filepath.Dir(dest), 0o700)
	if err := os.Rename(st.dir, dest); err != nil {
		return fmt.Errorf("%v, and the changes couldn't be set aside: %w", why, err)
	}
	var blocks []string
	for i := range st.dirty {
		blocks = append(blocks, fmt.Sprintf("  %d: bytes %d to %d", i, i*st.fs.chunkSize(), min((i+1)*st.fs.chunkSize(), st.size)))
	}
	sort.Strings(blocks)
	note := fmt.Sprintf(`modalfs couldn't save changes to %q.

%v.
Your edit changed only part of the file, and to save it modalfs needed the
parts you didn't change, which it never read. Saving your blocks on top of
the new version would have mixed two versions, so nothing was saved.

Each numbered file here is one %d-byte chunk of the file as you left it,
starting at its number multiplied by %d. The edited blocks are:
%s

meta.json describes the file. The file was %d bytes long.
`, st.path, why, fs.chunkSize(), fs.chunkSize(), strings.Join(blocks, "\n"), st.size)
	_ = os.WriteFile(filepath.Join(dest, "README.txt"), []byte(note), 0o600)
	st.removed = true
	st.accountLocked()
	if st.timer != nil {
		st.timer.Stop()
	}
	fs.mu.Lock()
	if fs.files[st.path] == st {
		delete(fs.files, st.path)
	}
	fs.mu.Unlock()
	fs.log.Error("unsaved changes set aside", "path", st.path, "kept_in", dest, "why", why)
	fs.event("error", st.path, fmt.Sprintf("couldn't save: %v; your changes are kept in %s", why, dest))
	return nil
}
