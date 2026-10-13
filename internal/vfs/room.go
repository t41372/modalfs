package vfs

import (
	"errors"
	"fmt"
	"syscall"
	"time"
)

// Room for writes. Changes wait on this computer until they're on Modal,
// and they share one limit with the block cache (see blockCache). A write
// that would go over it waits for uploads to free room, so a copy far
// larger than the limit runs at the speed of the upload instead of filling
// the disk. While it waits, modalfs keeps every byte already written.

// commitTarget is how long one batch of a long write should take to upload.
// It bounds how long a write waits for room: about one batch.
const commitTarget = 20 * time.Second

// commitBatch is how much new data a file collects before its commit
// starts: commitTarget of uploading at the speed measured so far, and at
// most half the room for pending changes, so the next batch can be written
// while one uploads.
func (fs *FS) commitBatch() int64 {
	b := int64(32 << 20) // until a commit shows the speed: small, in case it's slow
	if rate := fs.upRate.Load(); rate > 0 {
		b = rate * int64(commitTarget/time.Second)
	}
	if room := fs.cache.pendingMax(); room > 0 {
		b = min(b, room/2)
	}
	return min(max(b, 32<<20), 1<<30)
}

// noteUpload records how fast a commit's upload ran.
func (fs *FS) noteUpload(blocks int, took time.Duration) {
	if blocks == 0 || took <= 0 {
		return // nothing uploaded: Modal already had every block
	}
	rate := int64(float64(int64(blocks)*fs.chunkSize()) / took.Seconds())
	if old := fs.upRate.Load(); old > 0 {
		rate = (old + rate) / 2
	}
	fs.upRate.Store(rate)
}

var errClosing = errors.New("the volume is being unmounted")

// waitRoom waits, before a write of n bytes at off, until the blocks it
// would add fit: until the file's commit in flight ends if the file already
// holds a full batch besides it, and until uploads free room if the shared
// limit or the disk is full. It returns ENOSPC at once, and says so, when
// nothing waiting to upload could free room.
func (st *fileState) waitRoom(off int64, n int) error {
	fs := st.fs
	kicked := false
	waiting := false
	defer func() {
		if waiting {
			fs.waiting.Add(-1)
		}
	}()
	wait := func(c <-chan struct{}) error {
		if !waiting {
			waiting = true
			fs.waiting.Add(1)
		}
		select {
		case <-c:
		case <-time.After(time.Second): // the disk can free up by itself
		case <-fs.ctx.Done():
			return errClosing
		}
		return nil
	}
	for {
		if fs.closed.Load() {
			return nil // unmounting: what's written now is committed by Close
		}
		st.mu.Lock()
		if st.removed {
			st.mu.Unlock()
			return nil // writeAt reports it
		}
		var add int64
		for idx := off / fs.chunkSize(); idx*fs.chunkSize() < off+int64(n); idx++ {
			if !st.dirty[idx] {
				add += fs.chunkSize()
			}
		}
		var snapDone <-chan struct{}
		if add > 0 && st.snap != nil && st.unsnappedLocked() >= fs.commitBatch() {
			snapDone = st.snapDone
		}
		st.mu.Unlock()
		if add == 0 {
			return nil
		}
		if snapDone != nil {
			if err := wait(snapDone); err != nil {
				return err
			}
			continue
		}
		ok, freed, stuck := fs.cache.room(add)
		if ok {
			return nil
		}
		if stuck {
			if now := time.Now().Unix(); now-fs.noRoom.Swap(now) >= 60 {
				fs.event("error", "", fmt.Sprintf("writes refused: less than %d GiB is free on the disk modalfs keeps its data on, and nothing waiting to upload could free it. Free some space.", minFree>>30))
			}
			return syscall.ENOSPC
		}
		if !kicked {
			kicked = true
			fs.kickCommits()
		}
		if err := wait(freed); err != nil {
			return err
		}
	}
}

// kickCommits starts the commits of this mount's files now, rather than
// when they're idle, so waiting writes get room sooner. A file retrying a
// failed commit keeps its backoff.
func (fs *FS) kickCommits() {
	fs.mu.Lock()
	var all []*fileState
	for _, st := range fs.files {
		all = append(all, st)
	}
	fs.mu.Unlock()
	for _, st := range all {
		st.mu.Lock()
		if !st.removed && st.gen != st.flushedGen && st.snap == nil && st.backoff == 0 {
			st.scheduleLocked(0)
		}
		st.mu.Unlock()
	}
}
