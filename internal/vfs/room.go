package vfs

import (
	"errors"
	"fmt"
	"syscall"
	"time"
)

// Room for writes. Changes wait on this computer until they're on Modal,
// and they share one limit with the block cache (see blockCache). A write
// waits only when that limit (or the disk) is full: then for uploads to free
// room, so a copy far larger than the limit runs at the speed of the upload
// instead of filling the disk. Large files are uploaded as they're written
// and committed in parts (ahead.go), so the wait is for a block or two to
// upload, not for a whole commit. While it waits, modalfs keeps every byte
// already written.

var errClosing = errors.New("the volume is being unmounted")

// waitRoom waits, before a write of n bytes at off, until the blocks it
// would add fit in the shared limit and on the disk. It returns ENOSPC at
// once, and says so, when nothing waiting to upload could free room.
func (st *fileState) waitRoom(off int64, n int) error {
	fs := st.fs
	rounds := 0
	paced := false
	waiting := false
	defer func() {
		if waiting {
			fs.waiting.Add(-1)
		}
	}()
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
		st.mu.Unlock()
		if add == 0 {
			return nil
		}
		if !paced {
			paced = true
			if d := fs.pace(add, int64(n)); d > 0 {
				// nearly full: writes take turns at the upload's speed, each
				// a little later, rather than all stop once it's full (the
				// macOS NFS client turns that into pauses of half a minute)
				if !waiting {
					waiting = true
					fs.waiting.Add(1)
				}
				select {
				case <-time.After(d):
				case <-fs.ctx.Done():
					return errClosing
				}
				continue
			}
		}
		ok, freed, stuck, left := fs.cache.room(add)
		if ok {
			return nil
		}
		if stuck {
			if now := time.Now().Unix(); now-fs.noRoom.Swap(now) >= 60 {
				why := fmt.Sprintf("writes refused: less than %d GiB is free on the disk modalfs keeps its data on, and nothing waiting to upload could free it. Free some space.", minFree>>30)
				if left {
					why = "writes refused: changes of volumes that aren't mounted fill the cache size, and nothing waiting to upload could free it. Mount those volumes with write access so their changes upload, or raise the cache size in Settings."
				}
				fs.event("error", "", why)
			}
			return syscall.ENOSPC
		}
		if !waiting {
			waiting = true
			fs.waiting.Add(1)
		}
		// again every few seconds: a wake-up missed elsewhere then costs a
		// moment, not a write stuck for good
		if rounds%5 == 0 {
			fs.kickCommits()
		} else {
			fs.kickAhead()
		}
		rounds++
		select {
		case <-freed:
		case <-time.After(time.Second): // the disk can free up by itself
		case <-fs.ctx.Done():
			return errClosing
		}
	}
}

// paceFrom is the share of the room for changes past which writes are paced.
const paceFrom = 0.75

// pace is how long a write of n bytes, adding add bytes of changed blocks,
// waits for its turn once changes take more than paceFrom of their room:
// data then comes in at the speed uploads go out, one write after another.
// (It counts the bytes written: the writes filling one new block each find
// it not changed yet, so counting the block would charge it several times.)
func (fs *FS) pace(add, n int64) time.Duration {
	room := fs.cache.pendingMax()
	rate := fs.c.Client.UploadRate()
	if room <= 0 || rate <= 0 || float64(fs.cache.Pending()+add) <= paceFrom*float64(room) {
		return 0
	}
	fs.paceMu.Lock()
	defer fs.paceMu.Unlock()
	now := time.Now()
	start := now
	if fs.paceNext.After(now) {
		start = fs.paceNext
	}
	fs.paceNext = start.Add(time.Duration(float64(n) / rate * float64(time.Second)))
	return start.Sub(now)
}

// kickCommits frees room for waiting writes as soon as it can: a file being
// uploaded ahead commits the part that's on Modal (and keeps doing so as
// blocks arrive, see maybePart), any other file commits now rather than
// when it's idle. A file retrying a failed commit keeps its backoff.
func (fs *FS) kickCommits() {
	fs.mu.Lock()
	var all []*fileState
	for _, st := range fs.files {
		all = append(all, st)
	}
	fs.mu.Unlock()
	fs.kickAhead()
	for _, st := range all {
		st.mu.Lock()
		if !st.removed && st.gen != st.flushedGen && st.snap == nil && st.backoff == 0 {
			if _, _, ok, whole := st.partLocked(); ok || whole {
				go func() {
					if err := st.flushPart(); err != nil {
						fs.uploadFailed(err)
					}
				}()
			} else if len(st.dirty) < aheadMinBlocks {
				st.scheduleLocked(0) // a large file waits for its blocks, or its write-back
			}
		}
		st.mu.Unlock()
	}
}
