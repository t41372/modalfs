package vfs

import (
	"sync"
	"sync/atomic"
	"time"
)

type counters struct {
	commits, conflicts, errors, uploadedBlocks atomic.Int64
	mu                                         sync.Mutex
	lastErr                                    string
	lastErrAt                                  time.Time
	lastCommit                                 time.Time
}

// Status is a point-in-time summary of a mounted volume.
type Status struct {
	PendingFiles   int    `json:"pending_files"`
	PendingBytes   int64  `json:"pending_bytes"` // changed data not yet on Modal (whole changed blocks)
	BytesDown      int64  `json:"bytes_down"`
	BytesUp        int64  `json:"bytes_up"`
	Commits        int64  `json:"commits"`
	Conflicts      int64  `json:"conflicts"`
	Errors         int64  `json:"errors"`
	LastError      string `json:"last_error,omitempty"`
	LastErrorAt    int64  `json:"last_error_at,omitempty"`
	LastCommitAt   int64  `json:"last_commit_at,omitempty"`
	LocalOnlyFiles int    `json:"local_only_files"`
}

func (fs *FS) Status() Status {
	fs.mu.Lock()
	var files []*fileState
	for _, st := range fs.files {
		files = append(files, st)
	}
	nEph := len(fs.eph)
	fs.mu.Unlock()
	s := Status{LocalOnlyFiles: nEph}
	for _, st := range files {
		st.mu.Lock()
		if st.gen != st.flushedGen && !st.removed {
			s.PendingFiles++
			for idx := range st.dirty {
				s.PendingBytes += int64(blockLen(st.size, idx))
			}
		}
		st.mu.Unlock()
	}
	s.BytesDown = fs.c.BytesDown.Load()
	s.BytesUp = fs.c.BytesUp.Load()
	s.Commits = fs.ctr.commits.Load()
	s.Conflicts = fs.ctr.conflicts.Load()
	s.Errors = fs.ctr.errors.Load()
	fs.ctr.mu.Lock()
	s.LastError = fs.ctr.lastErr
	if !fs.ctr.lastErrAt.IsZero() {
		s.LastErrorAt = fs.ctr.lastErrAt.Unix()
	}
	if !fs.ctr.lastCommit.IsZero() {
		s.LastCommitAt = fs.ctr.lastCommit.Unix()
	}
	fs.ctr.mu.Unlock()
	return s
}

func (fs *FS) event(kind, p, detail string) {
	switch kind {
	case "commit":
		fs.ctr.commits.Add(1)
		fs.ctr.mu.Lock()
		fs.ctr.lastCommit = time.Now()
		fs.ctr.mu.Unlock()
	case "conflict":
		fs.ctr.conflicts.Add(1)
	case "error":
		fs.ctr.errors.Add(1)
		fs.ctr.mu.Lock()
		fs.ctr.lastErr, fs.ctr.lastErrAt = p+": "+detail, time.Now()
		fs.ctr.mu.Unlock()
	}
	if fs.opt.OnEvent != nil {
		fs.opt.OnEvent(kind, p, detail)
	}
}

// Cache exposes the block cache (for clearing / stats).
func (fs *FS) Cache() *BlockCache { return fs.cache }
