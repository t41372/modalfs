package vfs

import (
	"sort"
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
	uploadErr                                  string    // why commits fail, until one succeeds
	uploadErrAt                                time.Time // since when
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
	// WaitingForRoom: writes are waiting for uploads to free room on this
	// computer (the cache size limit, or a full disk).
	WaitingForRoom bool `json:"waiting_for_room,omitempty"`
	// UploadError is why commits keep failing (they're retried), since
	// UploadErrorAt; empty once one succeeds.
	UploadError   string `json:"upload_error,omitempty"`
	UploadErrorAt int64  `json:"upload_error_at,omitempty"`
}

func (fs *FS) Status() Status {
	fs.mu.Lock()
	var files []*fileState
	for _, st := range fs.files {
		files = append(files, st)
	}
	nEph := len(fs.eph)
	fs.mu.Unlock()
	s := Status{LocalOnlyFiles: nEph, WaitingForRoom: fs.waiting.Load() > 0}
	for _, st := range files {
		st.mu.Lock()
		if st.gen != st.flushedGen && !st.removed {
			s.PendingFiles++
			for idx := range st.dirty {
				s.PendingBytes += int64(fs.blockLen(st.size, idx))
			}
		}
		st.mu.Unlock()
	}
	s.BytesDown = fs.c.Client.BytesDown.Load()
	s.BytesUp = fs.c.Client.BytesUp.Load()
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
	if fs.ctr.uploadErr != "" {
		s.UploadError, s.UploadErrorAt = fs.ctr.uploadErr, fs.ctr.uploadErrAt.Unix()
	}
	fs.ctr.mu.Unlock()
	return s
}

// PendingFile is a file with changes that aren't on Modal yet.
type PendingFile struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"` // changed data (whole changed blocks)
	Size  int64  `json:"size"`
	Since int64  `json:"since,omitempty"` // when its oldest unsaved change was made (Unix seconds)
}

// PendingFiles lists the files Status counts as pending, oldest change first.
func (fs *FS) PendingFiles() []PendingFile {
	fs.mu.Lock()
	var files []*fileState
	for _, st := range fs.files {
		files = append(files, st)
	}
	fs.mu.Unlock()
	out := []PendingFile{}
	for _, st := range files {
		st.mu.Lock()
		if st.gen != st.flushedGen && !st.removed {
			p := PendingFile{Path: st.path, Size: st.size}
			for idx := range st.dirty {
				p.Bytes += int64(fs.blockLen(st.size, idx))
			}
			if !st.dirtySince.IsZero() {
				p.Since = st.dirtySince.Unix()
			}
			out = append(out, p)
		}
		st.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Since != out[j].Since {
			return out[i].Since < out[j].Since
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// uploadFailed records why a commit failed; it's retried.
func (fs *FS) uploadFailed(err error) {
	fs.ctr.mu.Lock()
	defer fs.ctr.mu.Unlock()
	if fs.ctr.uploadErr == "" {
		fs.ctr.uploadErrAt = time.Now()
	}
	fs.ctr.uploadErr = err.Error()
}

func (fs *FS) event(kind, p, detail string) {
	switch kind {
	case "commit":
		fs.ctr.commits.Add(1)
		fs.ctr.mu.Lock()
		fs.ctr.lastCommit = time.Now()
		fs.ctr.uploadErr = ""
		fs.ctr.mu.Unlock()
	case "conflict":
		fs.ctr.conflicts.Add(1)
	case "error", "unreadable":
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
