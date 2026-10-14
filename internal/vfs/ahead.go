package vfs

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"github.com/t41372/modalfs/internal/modalapi"
)

// Uploading ahead. A file with a lot of changes (a long copy) is uploaded as
// it's written: each changed block, once it has settled, goes to Modal on
// its own (see modalapi.Client.Ahead), with no commit waiting for it. The
// file is then committed in parts: a part commits the start of the file as
// it is now, up to the first block not on Modal yet, so it's one quick call
// with nothing left to upload. Its blocks stop taking room on this computer
// at once, and a write waiting for room waits for a block or two to upload,
// not for a whole commit. Once the file is idle, the usual commit finishes it.
//
// A part is never a version the file didn't have: it's always the file cut
// at a block boundary, and never shorter than what Modal holds unless the
// file was cut there locally too.

const (
	aheadMinBlocks = 4                      // fewer changed blocks: the file commits the usual way
	aheadSettle    = 500 * time.Millisecond // a block unchanged this long is uploaded
	aheadInFlight  = 32                     // blocks being uploaded ahead at most (each held in memory)
	partEvery      = 10 * time.Second       // parts are committed at most this often, unless writes wait for room
)

var errChanged = errors.New("the block changed while it was read")

// aheadState is a block's upload ahead: of which version, and whether it's
// done (on Modal, with a receipt for the commit).
type aheadState struct {
	ver  uint64
	done bool
}

func (st *fileState) initAhead() {
	st.bver, st.touched, st.ahead = map[int64]uint64{}, map[int64]time.Time{}, map[int64]aheadState{}
}

// changedBlockLocked records that block idx's content changed.
func (st *fileState) changedBlockLocked(idx int64) {
	st.bver[idx]++
	st.touched[idx] = time.Now()
	delete(st.ahead, idx)
}

// forgetBlockLocked drops what's known about block idx's upload ahead, once
// it's no longer a changed block.
func (st *fileState) forgetBlockLocked(idx int64) {
	delete(st.touched, idx)
	delete(st.ahead, idx)
}

// aheadManagedLocked: this file's changes are uploaded ahead and committed in parts.
func (st *fileState) aheadManagedLocked() bool {
	fs := st.fs
	return fs.opt.MaxPending >= 0 && !fs.aheadOff.Load() && (len(st.dirty) >= aheadMinBlocks || fs.waiting.Load() > 0)
}

// partLocked finds the part of the file that can be committed now: the cut
// (a block boundary) and the changed blocks before it, all on Modal. ok is
// false if no part would commit anything new. whole: every change is on
// Modal, so the whole file can be committed as quickly as a part.
func (st *fileState) partLocked() (cut int64, blocks []int64, ok, whole bool) {
	cs := st.fs.chunkSize()
	n := int64(st.fs.numBlocks(st.size))
	var idx int64
	for idx = 0; idx < n; idx++ {
		if !st.dirty[idx] {
			continue // the base's content, or a hole: as the file has it
		}
		if a, up := st.ahead[idx]; !up || !a.done || a.ver != st.bver[idx] || st.hashes[idx] == nil {
			break
		}
		blocks = append(blocks, idx)
	}
	if idx == n {
		return 0, nil, false, len(st.dirty) > 0
	}
	cut = idx * cs
	floor := int64(0)
	if st.baseExists {
		floor = min(st.baseSize, st.truncFloor)
	}
	if len(blocks) == 0 || cut < floor || st.partSigLocked(cut, blocks) == st.lastPart {
		return 0, nil, false, false // nothing new since the last part (one to a conflict copy keeps its blocks)
	}
	return cut, blocks, true, false
}

// partSigLocked tells parts apart: the cut and the versions of the blocks in it.
func (st *fileState) partSigLocked(cut int64, blocks []int64) [2]uint64 {
	var v uint64
	for _, i := range blocks {
		v = v*31 + uint64(i)<<20 + st.bver[i]
	}
	return [2]uint64{uint64(cut), v}
}

// kickAhead wakes the uploader.
func (fs *FS) kickAhead() {
	select {
	case fs.aheadKick <- struct{}{}:
	default:
	}
}

type aheadJob struct {
	st  *fileState
	idx int64
	ver uint64
}

// aheadLoop uploads settled changed blocks of the files that upload ahead.
func (fs *FS) aheadLoop() {
	sem := make(chan struct{}, aheadInFlight)
	var again <-chan time.Time
	inFlight := func() int { return len(sem) }
	for {
		select {
		case <-fs.ctx.Done():
			return
		case <-fs.aheadKick:
		case <-again:
		}
		again = nil
		if fs.closed.Load() || fs.aheadOff.Load() {
			continue
		}
		// no more than can upload at once: queued ones would each hold a
		// block in memory, and blocks finishing out of order hold back the part
		jobs, unsettled := fs.aheadJobs(min(cap(sem), fs.c.Client.UploadLimit()) - inFlight())
		for _, j := range jobs {
			sem <- struct{}{}
			go func() {
				defer func() { <-sem; fs.kickAhead() }()
				fs.aheadOne(j)
			}()
		}
		if unsettled {
			again = time.After(aheadSettle / 2)
		}
	}
}

// aheadJobs picks up to max blocks to upload, lowest first in each file, and
// marks them taken. unsettled: some block will be ready soon.
func (fs *FS) aheadJobs(max int) (jobs []aheadJob, unsettled bool) {
	fs.mu.Lock()
	var files []*fileState
	for _, st := range fs.files {
		files = append(files, st)
	}
	fs.mu.Unlock()
	now := time.Now()
	for _, st := range files {
		st.mu.Lock()
		if st.removed || st.deleted || st.snap != nil || !st.aheadManagedLocked() {
			st.mu.Unlock()
			continue
		}
		var idxs []int64
		for idx := range st.dirty {
			if a, ok := st.ahead[idx]; ok && a.ver == st.bver[idx] {
				continue // done, or on its way
			}
			if now.Sub(st.touched[idx]) < aheadSettle {
				unsettled = true
				continue
			}
			idxs = append(idxs, idx)
		}
		sort.Slice(idxs, func(i, j int) bool { return idxs[i] < idxs[j] })
		for _, idx := range idxs {
			if len(jobs) >= max {
				break
			}
			st.ahead[idx] = aheadState{ver: st.bver[idx]}
			if st.aheadBusy == 0 {
				st.aheadIdle = make(chan struct{})
			}
			st.aheadBusy++
			jobs = append(jobs, aheadJob{st, idx, st.bver[idx]})
		}
		st.mu.Unlock()
	}
	return jobs, unsettled
}

// aheadOne uploads one block, then commits a part if one is due.
func (fs *FS) aheadOne(j aheadJob) {
	st := j.st
	defer func() {
		st.mu.Lock()
		if st.aheadBusy--; st.aheadBusy == 0 {
			close(st.aheadIdle)
		}
		st.mu.Unlock()
	}()
	// Read the block without the lock, which every stat and write of the
	// file waits for; a change meanwhile bumps its version, checked below.
	st.mu.Lock()
	still := !st.removed && st.dirty[j.idx] && st.bver[j.idx] == j.ver
	n := st.fs.blockLen(st.size, j.idx)
	var f *os.File
	var err error
	if still {
		f, err = os.Open(st.blockFile(j.idx))
	}
	st.mu.Unlock()
	if !still {
		return
	}
	data := make([]byte, n)
	if err == nil {
		_, err = f.ReadAt(data, 0)
		if err == io.EOF && n == 0 {
			err = nil
		}
		f.Close()
	}
	st.mu.Lock()
	if st.bver[j.idx] != j.ver || n != st.fs.blockLen(st.size, j.idx) {
		err = errChanged
	}
	st.mu.Unlock()
	var hash []byte
	if err == nil {
		hash, _, err = fs.c.Ahead(fs.ctx, data)
	}
	st.mu.Lock()
	if err == nil && st.dirty[j.idx] && st.bver[j.idx] == j.ver {
		st.ahead[j.idx] = aheadState{ver: j.ver, done: true}
		st.hashes[j.idx] = hash
	} else if a := st.ahead[j.idx]; a.ver == j.ver && !a.done {
		delete(st.ahead, j.idx) // to be tried again
	}
	st.mu.Unlock()
	switch {
	case modalapi.IsAheadBroken(err):
		fs.aheadBroke(err)
	case errors.Is(err, errChanged):
		// written to while it was read: uploaded once it settles again
	case err != nil && fs.ctx.Err() == nil:
		// shown like a failed commit (offline, say); it's tried again, and
		// the commit uploads it if this keeps failing. Don't spin meanwhile.
		fs.uploadFailed(err)
		fs.log.Debug("uploading a block ahead failed", "path", st.path, "block", j.idx, "err", err)
		time.Sleep(time.Second)
	default:
		st.maybePart()
	}
}

// waitAhead waits for the file's uploads ahead in flight, so a commit uses
// them instead of uploading the same blocks again. None start meanwhile.
func (st *fileState) waitAhead() error {
	st.mu.Lock()
	busy, idle := st.aheadBusy, st.aheadIdle
	st.mu.Unlock()
	if busy == 0 {
		return nil
	}
	select {
	case <-idle:
		return nil
	case <-st.fs.ctx.Done():
		return errClosing
	}
}

// aheadBroke turns uploading ahead off for this mount: Modal no longer
// behaves as it relies on. Commits upload everything themselves, as before.
func (fs *FS) aheadBroke(err error) {
	if fs.aheadOff.Swap(true) {
		return
	}
	fs.log.Error("uploading ahead turned off", "err", err)
	fs.event("error", "", fmt.Sprintf("Modal changed how uploads work (%v). modalfs now uploads each file when it commits it, which is slower for large copies; nothing is lost. Please report this.", err))
	if err := fs.c.Remove(fs.ctx, MetaDir+"/upload-ahead", false); err != nil && !modalapi.IsNotFound(err) {
		fs.log.Warn("could not remove the file a request for upload URLs created", "err", err)
	}
}

// maybePart commits a part of the file if one is ready and due: writes are
// waiting for room, or the last commit was partEvery ago.
func (st *fileState) maybePart() {
	fs := st.fs
	st.mu.Lock()
	_, _, ok, whole := st.partLocked()
	ok = ok || whole && fs.waiting.Load() > 0 // the whole file, for room
	due := fs.waiting.Load() > 0 || time.Since(st.lastCommit) >= partEvery
	busy := st.snap != nil
	st.mu.Unlock()
	if !ok || !due || busy || fs.closed.Load() {
		return
	}
	go func() {
		if err := st.flushPart(); err != nil {
			fs.uploadFailed(err)
			fs.log.Debug("committing part of a file failed; the next part or the whole file retries", "path", st.path, "err", err)
		}
	}()
}
