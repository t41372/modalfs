// Package vfs implements a POSIX-ish filesystem on top of a Modal Volume v2,
// entirely from the client side: reads stream 8 MiB blocks straight from
// Modal's block store; writes are buffered locally per block and committed
// with VolumePutFiles2, re-using the content hashes of unchanged blocks so
// only modified blocks are uploaded.
package vfs

import (
	"bytes"
	"container/list"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/t41372/modalfs/internal/encryption"
	"github.com/t41372/modalfs/internal/modalapi"
	"github.com/t41372/modalfs/internal/volume"
	"golang.org/x/sync/singleflight"
)

const blockSize = modalapi.BlockSize

// Marker file used to materialise empty directories (Modal has no mkdir).
const KeepName = ".modalfs_keep"

// Hidden metadata directory at the volume root (sidecar manifests).
const MetaDir = ".modalfs"

type Options struct {
	Cipher        *encryption.Cipher
	CacheDir      string
	CacheMaxBytes int64
	DirCacheTTL   time.Duration
	WriteBack     time.Duration // idle time before dirty files are committed
	// MaxPending bounds how long a file that keeps changing waits for its
	// commit (default 30 s; negative: only commit when idle). A long copy is
	// then uploaded in parts while it runs, not all at once at the end.
	MaxPending time.Duration
	ReadAhead  int    // blocks
	SidecarMin int64  // write sidecar manifests for files >= this size (0 = never)
	OnConflict string // "keep-both" (default) or "overwrite"
	ReadOnly   bool
	Logger     *slog.Logger
	// Cache, when set, is a block cache shared with other mounts.
	Cache *BlockCache
	// FilterJunk keeps macOS litter (.DS_Store, ._*) in memory only and
	// never uploads it.
	FilterJunk bool
	// OnEvent, when set, receives user-visible events
	// (kind: commit, conflict, error, unreadable, recovered).
	OnEvent func(kind, path, detail string)
}

type Attr struct {
	Name  string
	IsDir bool
	Size  int64
	Mtime time.Time
	Mode  os.FileMode
}

// dirListing is a directory's cached listing. Every invalidation gives it a
// new generation (unique across the FS, so a listing that is swept and made
// again never reuses one): listings in flight from an older generation
// are neither joined nor stored.
type dirListing struct {
	gen     uint64
	at      time.Time
	entries map[string]modalapi.Entry // by base name; nil: list again
}

// dirSweepMin is how many listings the cache holds before it sweeps out
// expired ones.
const dirSweepMin = 256

type FS struct {
	c     *volume.Volume
	volID string
	opt   Options
	log   *slog.Logger
	cache *blockCache
	mdb   *manifestDB

	mu         sync.Mutex
	dirs       map[string]*dirListing
	dirGen     uint64                // last listing generation handed out
	dirSwept   time.Time             // last sweep of dirs
	files      map[string]*fileState // open / dirty files, by path
	inflight   map[string]bool       // blocks being read ahead, by singleflight key
	eph        map[string]*ephFile   // FilterJunk: local-only files
	ephBytes   int64
	ephTrimmed time.Time       // last trim of eph
	stray      map[string]bool // encrypted volume: names reported as not decrypting

	ctr   counters
	reads readTracker // sequential reads, for read-ahead (own lock)

	sf            singleflight.Group
	ctx           context.Context
	cancel        context.CancelFunc
	closed        atomic.Bool // no more commit timers
	readlinkNoted atomic.Bool // Activity said once that links can't be read
	nextID        int64

	waiting atomic.Int32 // writes waiting for room (see waitRoom)

	noRoom atomic.Int64 // when a write last failed for lack of disk (unix seconds), to report it once a minute

	paceMu   sync.Mutex
	paceNext time.Time // when the next paced write may go on (see pace)

	aheadKick chan struct{} // wakes aheadLoop
	aheadOff  atomic.Bool   // Modal stopped behaving as uploading ahead needs
}

func New(c *modalapi.Client, vol modalapi.VolumeInfo, opt Options) (*FS, error) {
	encrypted, marker, err := volume.Detect(context.Background(), c, vol.ID)
	if err != nil {
		return nil, err
	}
	if encrypted && opt.Cipher == nil {
		return nil, errors.New("volume is encrypted and locked; unlock it before mounting")
	}
	remote := &volume.Volume{Client: c, ID: vol.ID, Cipher: opt.Cipher}
	if opt.Cipher != nil {
		if !encrypted {
			return nil, errors.New("volume is not encrypted; converting existing volumes is not supported")
		}
		if err := remote.VerifyKey(context.Background(), marker); err != nil {
			return nil, err
		}
	}

	if opt.Logger == nil {
		opt.Logger = slog.Default()
	}
	if opt.DirCacheTTL == 0 {
		opt.DirCacheTTL = 10 * time.Second
	}
	if opt.WriteBack == 0 {
		opt.WriteBack = 3 * time.Second
	}
	if opt.MaxPending == 0 {
		opt.MaxPending = 30 * time.Second
	}
	if opt.ReadAhead == 0 {
		opt.ReadAhead = 4
	}
	if opt.OnConflict == "" {
		opt.OnConflict = "keep-both"
	}
	base := filepath.Join(opt.CacheDir, vol.ID)
	if err := os.MkdirAll(filepath.Join(base, "dirty"), 0o700); err != nil {
		return nil, err
	}
	bc := opt.Cache
	if bc == nil {
		var err error
		bc, err = newBlockCache(filepath.Join(opt.CacheDir, "blocks"), opt.CacheMaxBytes)
		if err != nil {
			return nil, err
		}
	}
	mdb, err := openManifestDB(filepath.Join(base, "manifests.db"), vol.ID)
	if err != nil {
		return nil, fmt.Errorf("manifest db: %w (is another modalfs using this cache dir?)", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	mdb.chunkSize = remote.ChunkSize()
	fs := &FS{c: remote, volID: vol.ID, opt: opt, log: opt.Logger, cache: bc, mdb: mdb,
		dirs: map[string]*dirListing{}, files: map[string]*fileState{}, inflight: map[string]bool{},
		eph: map[string]*ephFile{}, stray: map[string]bool{}, ctx: ctx, cancel: cancel, aheadKick: make(chan struct{}, 1)}
	remote.Skipped = fs.reportStray
	if err := fs.checkJournals(filepath.Join(base, "dirty")); err != nil {
		mdb.Close()
		cancel()
		return nil, err
	}
	if !opt.ReadOnly {
		bc.takeOver(vol.ID) // recovery counts the changes it finds itself
		fs.recover(filepath.Join(base, "dirty"))
		go fs.aheadLoop()
	}
	return fs, nil
}

func clean(p string) string {
	p = path.Clean("/" + p)
	return strings.TrimPrefix(p, "/")
}

func parentOf(p string) string {
	d := path.Dir(p)
	if d == "." || d == "/" {
		return ""
	}
	return d
}

func hidden(dir, name string) bool {
	return name == KeepName || (dir == "" && name == MetaDir)
}

// ---------- metadata ----------

// listDir returns dir's entries, from the cache unless it's older than
// DirCacheTTL or fresh is set. Callers share a listing in flight only if no
// invalidation happened since it started, so a fresh listing always
// reflects every change made before the call.
// Logger is the mount's log.
func (fs *FS) Logger() *slog.Logger { return fs.log }

func (fs *FS) listDir(dir string, fresh bool) (map[string]modalapi.Entry, error) {
	fs.mu.Lock()
	l := fs.dirs[dir]
	if l != nil && l.entries != nil && !fresh && time.Since(l.at) < fs.opt.DirCacheTTL {
		entries := l.entries // read under the lock: a listing that lands replaces it
		fs.mu.Unlock()
		return entries, nil
	}
	if l == nil {
		fs.sweepDirsLocked()
		fs.dirGen++
		l = &dirListing{gen: fs.dirGen, at: time.Now()}
		fs.dirs[dir] = l
	}
	gen := l.gen
	fs.mu.Unlock()
	v, err, _ := fs.sf.Do(fmt.Sprintf("ls:%s#%d", dir, gen), func() (any, error) {
		start := time.Now()
		es, err := fs.c.List(fs.ctx, dir, false)
		if err != nil {
			return nil, err
		}
		m := map[string]modalapi.Entry{}
		for _, e := range es {
			if e.Path == dir { // listing a file path returns the file itself
				return nil, syscall.ENOTDIR
			}
			m[path.Base(e.Path)] = e
		}
		fs.mu.Lock()
		if l := fs.dirs[dir]; l != nil && l.gen == gen { // not invalidated meanwhile
			l.at, l.entries = start, m
		}
		fs.mu.Unlock()
		return m, nil
	})
	if err != nil {
		if modalapi.IsNotFound(err) {
			return nil, os.ErrNotExist
		}
		return nil, err
	}
	return v.(map[string]modalapi.Entry), nil
}

// sweepDirsLocked drops expired and invalidated listings, at most once per
// TTL and only once there are many.
func (fs *FS) sweepDirsLocked() {
	if len(fs.dirs) < dirSweepMin || time.Since(fs.dirSwept) < fs.opt.DirCacheTTL {
		return
	}
	fs.dirSwept = time.Now()
	for k, l := range fs.dirs {
		if time.Since(l.at) >= fs.opt.DirCacheTTL {
			delete(fs.dirs, k)
		}
	}
}

func (fs *FS) invalidateLocked(l *dirListing) {
	fs.dirGen++
	l.gen, l.at, l.entries = fs.dirGen, time.Now(), nil // at: not swept while relisted
}

func (fs *FS) invalidate(dir string) {
	fs.mu.Lock()
	if l := fs.dirs[dir]; l != nil {
		fs.invalidateLocked(l)
	}
	fs.mu.Unlock()
}

func (fs *FS) invalidateTree(dir string) {
	fs.mu.Lock()
	for k, l := range fs.dirs {
		if k == dir || strings.HasPrefix(k, dir+"/") || dir == "" {
			fs.invalidateLocked(l)
		}
	}
	fs.mu.Unlock()
}

// remoteStat looks a path up in its parent's (cached) listing.
func (fs *FS) remoteStat(p string, fresh bool) (modalapi.Entry, error) {
	if p == "" {
		return modalapi.Entry{IsDir: true}, nil
	}
	m, err := fs.listDir(parentOf(p), fresh)
	if err != nil {
		if errors.Is(err, syscall.ENOTDIR) {
			return modalapi.Entry{}, os.ErrNotExist
		}
		return modalapi.Entry{}, err
	}
	e, ok := m[path.Base(p)]
	if !ok {
		return modalapi.Entry{}, os.ErrNotExist
	}
	return e, nil
}

func entryAttr(e modalapi.Entry) Attr {
	return Attr{Name: path.Base(e.Path), IsDir: e.IsDir, Size: e.Size, Mtime: time.Unix(e.Mtime, 0), Mode: e.Mode()}
}

func (fs *FS) Stat(p string) (Attr, error) {
	p = clean(p)
	if a, ok := fs.ephStat(p); ok {
		return a, nil
	}
	fs.mu.Lock()
	st := fs.files[p]
	fs.mu.Unlock()
	if st != nil {
		return st.attr(), nil
	}
	if p == "" {
		return Attr{Name: "", IsDir: true, Mode: os.ModeDir | 0o755, Mtime: time.Now()}, nil
	}
	if hidden(parentOf(p), path.Base(p)) {
		return Attr{}, os.ErrNotExist
	}
	e, err := fs.remoteStat(p, false)
	if err != nil {
		return Attr{}, err
	}
	if fs.c.Cipher != nil && e.Mode().IsRegular() && e.Size == 0 {
		if _, _, err := fs.remoteBlock(p, 0, e.Mtime, 0, true); err != nil {
			return Attr{}, err
		}
	}
	return entryAttr(e), nil
}

func (fs *FS) ReadDir(p string) ([]Attr, error) {
	p = clean(p)
	m, err := fs.listDir(p, false)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []Attr
	for name, e := range m {
		if hidden(p, name) {
			continue
		}
		seen[name] = true
		out = append(out, entryAttr(e))
	}
	fs.mu.Lock()
	var local []*fileState
	for fp, st := range fs.files {
		if parentOf(fp) == p {
			local = append(local, st)
		}
	}
	fs.mu.Unlock()
	for _, st := range local {
		a := st.attr()
		if st.deleted {
			continue
		}
		if seen[a.Name] {
			for i := range out {
				if out[i].Name == a.Name {
					out[i] = a
				}
			}
		} else {
			out = append(out, a)
		}
	}
	out = fs.ephList(p, out)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ---------- reads ----------

func blockLen(size int64, idx int64) int {
	end := min((idx+1)*blockSize, size)
	if end <= idx*blockSize {
		return 0
	}
	return int(end - idx*blockSize)
}

func (fs *FS) blockLen(size int64, idx int64) int {
	end := min((idx+1)*fs.chunkSize(), size)
	if end <= idx*fs.chunkSize() {
		return 0
	}
	return int(end - idx*fs.chunkSize())
}

// refused tells the user why a folder wasn't moved or deleted, if it holds
// something that doesn't decrypt, and returns err.
func (fs *FS) refused(p, what string, err error) error {
	if errors.Is(err, volume.ErrUnreadable) {
		detail := fmt.Sprintf("%s wasn't %s: %v. modalfs doesn't leave behind or delete what it can't read; move or delete that entry with the Modal CLI, then try again", p, what, err)
		fs.log.Error(detail)
		fs.event("error", p, detail)
	}
	return err
}

// reportStray tells the user, once per name, about an entry of an encrypted
// volume that doesn't decrypt with its key. The mount leaves it out rather
// than refuse to list the folder it's in.
func (fs *FS) reportStray(raw string, err error) {
	fs.mu.Lock()
	seen := fs.stray[raw]
	fs.stray[raw] = true
	fs.mu.Unlock()
	if seen {
		return
	}
	detail := "left out of the mount: it doesn't decrypt with this volume's key (written by another tool, or damaged): " + err.Error()
	fs.log.Warn(detail, "path", raw)
	fs.event("unreadable", raw, detail)
}

func (fs *FS) chunkSize() int64 {
	if fs.c == nil {
		return blockSize
	}
	return fs.c.ChunkSize()
}

func (fs *FS) numBlocks(size int64) int {
	return int((size + fs.chunkSize() - 1) / fs.chunkSize())
}

// remoteBlock returns block idx of the remote version (size, mtime) of p.
func (fs *FS) remoteBlock(p string, size, mtime int64, idx int64, cache bool) ([]byte, []byte, error) {
	return fs.remoteBlockURL(p, size, mtime, idx, cache, "")
}

func (fs *FS) remoteBlockURL(p string, size, mtime int64, idx int64, cache bool, url string) ([]byte, []byte, error) {
	want := fs.blockLen(size, idx)
	if h := fs.mdb.Hash(p, size, mtime, idx); h != nil {
		if b := fs.cache.get(h, want, fs.c.HashBlock); b != nil {
			return b, h, nil
		}
	}
	key := fmt.Sprintf("blk:%s@%d.%d#%d", p, size, mtime, idx)
	type res struct{ data, hash []byte }
	v, err, _ := fs.sf.Do(key, func() (any, error) {
		urls, rsize := []string{url}, size
		if url == "" {
			var err error
			urls, rsize, err = fs.c.BlockURLs(fs.ctx, p, idx, 1)
			if err != nil {
				return nil, err
			}
		}
		if rsize != size {
			fs.invalidate(parentOf(p))
			return nil, fmt.Errorf("%s changed remotely (size %d -> %d): %w", p, size, rsize, syscall.ESTALE)
		}
		if len(urls) == 0 {
			if want > 0 {
				return nil, fmt.Errorf("%s: no download URL for block %d: %w", p, idx, syscall.EIO)
			}
			return res{data: []byte{}}, nil
		}
		data, err := fs.c.Download(fs.ctx, urls[0], want)
		if err != nil {
			return nil, err
		}
		h := fs.c.HashBlock(data)
		if known := fs.mdb.Hash(p, size, mtime, idx); known != nil && !bytes.Equal(known, h) {
			// the same size but other content: replaced since
			fs.invalidate(parentOf(p))
			return nil, fmt.Errorf("%s changed remotely (block %d differs): %w", p, idx, syscall.ESTALE)
		}
		fs.mdb.SetHash(p, size, mtime, int(idx), h)
		if cache {
			fs.cache.Put(h, data)
		}
		return res{data, h}, nil
	})
	if err != nil {
		return nil, nil, err
	}
	r := v.(res)
	return r.data, r.hash, nil
}

// prefetch starts downloading the blocks after from that aren't cached, as
// far as the read-ahead window and the free prefetch slots allow. It never
// waits: the downloads run on their own.
func (fs *FS) prefetch(p string, size, mtime int64, from int64) {
	to := min(from+int64(fs.opt.ReadAhead), int64(fs.numBlocks(size)))
	var want []int64
	for i := from; i < to; i++ {
		if h := fs.mdb.Hash(p, size, mtime, i); h == nil || !fs.cache.Has(h) {
			want = append(want, i)
		}
	}
	key := func(i int64) string { return fmt.Sprintf("blk:%s@%d.%d#%d", p, size, mtime, i) }
	var got []int64
	fs.mu.Lock()
	for _, i := range want {
		if fs.inflight[key(i)] {
			continue
		}
		if !fs.cache.startPrefetch() {
			break
		}
		fs.inflight[key(i)] = true
		got = append(got, i)
	}
	fs.mu.Unlock()
	if len(got) == 0 {
		return
	}
	done := func(i int64) {
		fs.cache.endPrefetch()
		fs.mu.Lock()
		delete(fs.inflight, key(i))
		fs.mu.Unlock()
	}
	go func() {
		// one control-plane call for the whole window, then parallel downloads
		first, last := got[0], got[len(got)-1]
		urls, rsize, err := fs.c.BlockURLs(fs.ctx, p, first, last-first+1)
		for _, i := range got {
			if err != nil || rsize != size || i-first >= int64(len(urls)) {
				done(i)
				continue
			}
			go func(i int64, url string) {
				defer done(i)
				_, _, _ = fs.remoteBlockURL(p, size, mtime, i, true, url)
			}(i, urls[i-first])
		}
	}()
}

func (fs *FS) ReadAt(p string, buf []byte, off int64) (int, error) {
	p = clean(p)
	if n, ok := fs.ephRead(p, buf, off); ok {
		return n, nil
	}
	fs.mu.Lock()
	st := fs.files[p]
	fs.mu.Unlock()
	if st != nil {
		return st.readAt(buf, off)
	}
	e, err := fs.remoteStat(p, false)
	if err != nil {
		return 0, err
	}
	if e.IsDir {
		return 0, syscall.EISDIR
	}
	if why := specialEntry(e); why != nil {
		fs.unsupported(p, "read", why)
		return 0, why
	}
	return fs.readRemote(p, e.Size, e.Mtime, buf, off)
}

// readRemoteBlock fills dst with block idx of the remote version (size,
// mtime) of p, from offset inBlk in the block. A cached block is read just
// for that range; otherwise the whole block is fetched (and cached).
func (fs *FS) readRemoteBlock(p string, size, mtime int64, idx int64, dst []byte, inBlk int64) error {
	n := fs.blockLen(size, idx)
	if inBlk < 0 || inBlk+int64(len(dst)) > int64(n) {
		return fmt.Errorf("%s: read of [%d, %d) is outside block %d (%d bytes): %w", p, inBlk, inBlk+int64(len(dst)), idx, n, syscall.EIO)
	}
	if h := fs.mdb.Hash(p, size, mtime, idx); h != nil && fs.cache.ReadAt(h, n, dst, inBlk) {
		return nil
	}
	data, _, err := fs.remoteBlock(p, size, mtime, idx, true)
	if err != nil {
		return err
	}
	if int64(len(data)) < inBlk+int64(len(dst)) {
		return fmt.Errorf("%s: block %d is %d bytes, short of %d: %w", p, idx, len(data), inBlk+int64(len(dst)), syscall.EIO)
	}
	copy(dst, data[inBlk:])
	return nil
}

func (fs *FS) readRemote(p string, size, mtime int64, buf []byte, off int64) (int, error) {
	if off >= size {
		return 0, nil
	}
	end := min(off+int64(len(buf)), size)
	n := 0
	for pos := off; pos < end; {
		idx := pos / fs.chunkSize()
		inBlk := pos - idx*fs.chunkSize()
		want := min(end-pos, fs.chunkSize()-inBlk)
		if err := fs.readRemoteBlock(p, size, mtime, idx, buf[n:n+int(want)], inBlk); err != nil {
			return n, err
		}
		n += int(want)
		pos += want
		if fs.reads.sequential(p, idx) {
			fs.prefetch(p, size, mtime, idx+1)
		}
	}
	return n, nil
}

// readTracker remembers where the readers of recently read files are, so
// read-ahead starts only for a reader that is going through a file: one
// that has read readAheadAfter blocks in a row. One step from a block to
// the next isn't enough: the macOS NFS client reads 16 MiB ahead on its
// own when a file is first read, so reading 64 KiB (a Finder preview)
// already touches blocks 0 and 1. A file can have a few readers at once (a
// stream each). The tracker holds at most readFiles files; the least
// recently read is forgotten first, as is the least recently used stream
// of a file.
type readTracker struct {
	mu    sync.Mutex
	lru   *list.List // of *readFile, front = most recent
	files map[string]*list.Element
	tick  uint64
}

const (
	readFiles      = 1024
	readStreams    = 4
	readAheadAfter = 3 // blocks read in a row, i.e. two steps forward
)

type readFile struct {
	path    string
	streams [readStreams]readStream
}

type readStream struct {
	last int64  // block read last
	run  int    // blocks read in a row, ending at last
	used uint64 // tick of the last read; 0: free
}

// sequential records a read of block idx of p, and reports whether it
// moved a reader onto the next block, after at least readAheadAfter blocks
// read in order.
func (t *readTracker) sequential(p string, idx int64) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.files == nil {
		t.files, t.lru = map[string]*list.Element{}, list.New()
	}
	t.tick++
	var f *readFile
	if el := t.files[p]; el != nil {
		t.lru.MoveToFront(el)
		f = el.Value.(*readFile)
	} else {
		if t.lru.Len() >= readFiles {
			back := t.lru.Back()
			t.lru.Remove(back)
			delete(t.files, back.Value.(*readFile).path)
		}
		f = &readFile{path: p}
		t.files[p] = t.lru.PushFront(f)
	}
	for i := range f.streams {
		if s := &f.streams[i]; s.used != 0 && s.last == idx { // more of the same block
			s.used = t.tick
			return false
		}
	}
	oldest := 0
	for i := range f.streams {
		s := &f.streams[i]
		if s.used != 0 && s.last == idx-1 {
			s.last, s.run, s.used = idx, s.run+1, t.tick
			return s.run >= readAheadAfter
		}
		if s.used < f.streams[oldest].used {
			oldest = i
		}
	}
	f.streams[oldest] = readStream{last: idx, run: 1, used: t.tick}
	return false
}

// ---------- writes ----------

func (fs *FS) checkPath(p string) error {
	_, err := fs.c.Path(p)
	if err != nil {
		fs.log.Error("cannot store path", "path", p, "err", err)
		fs.event("error", p, err.Error())
	}
	return err
}

func (fs *FS) getState(p string, create bool) (*fileState, error) {
	if err := fs.checkPath(p); err != nil {
		return nil, err
	}
	fs.mu.Lock()
	st := fs.files[p]
	fs.mu.Unlock()
	if st != nil {
		return st, nil
	}
	if fs.opt.ReadOnly {
		return nil, syscall.EROFS
	}
	e, err := fs.remoteStat(p, true)
	exists := true
	if errors.Is(err, os.ErrNotExist) {
		if !create {
			return nil, os.ErrNotExist
		}
		exists = false
		if _, perr := fs.dirStat(parentOf(p)); perr != nil {
			return nil, perr
		}
	} else if err != nil {
		return nil, err
	} else if e.IsDir {
		return nil, syscall.EISDIR
	} else if why := specialEntry(e); why != nil {
		fs.unsupported(p, "change", why) // before anything is kept that couldn't be committed
		return nil, why
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if st := fs.files[p]; st != nil {
		return st, nil
	}
	fs.nextID++
	st = newFileState(fs, p, fmt.Sprintf("%d-%d", time.Now().UnixNano(), fs.nextID), exists, e)
	if m := fs.mdb.Get(p, e.Size, e.Mtime); exists && m != nil {
		st.sure = m.Hashes // known before the edit began: the base's own
	}
	fs.files[p] = st
	return st, nil
}

func (fs *FS) dirStat(p string) (Attr, error) {
	a, err := fs.Stat(p)
	if err != nil {
		return a, err
	}
	if !a.IsDir {
		return a, syscall.ENOTDIR
	}
	return a, nil
}

func (fs *FS) Create(p string, exclusive bool) error {
	p = clean(p)
	if fs.opt.ReadOnly {
		return syscall.EROFS
	}
	if fs.isJunk(p) {
		return fs.ephCreate(p, exclusive)
	}
	if _, err := fs.Stat(p); err == nil {
		if exclusive {
			return os.ErrExist
		}
		return fs.Truncate(p, 0)
	}
	st, err := fs.getState(p, true)
	if err != nil {
		return err
	}
	st.touch()
	return nil
}

func (fs *FS) WriteAt(p string, data []byte, off int64) (int, error) {
	p = clean(p)
	if fs.isJunk(p) && !fs.opt.ReadOnly {
		return fs.ephWrite(p, data, off), nil
	}
	for {
		st, err := fs.getState(p, true)
		if err != nil {
			return 0, err
		}
		if err := st.waitRoom(off, len(data)); err != nil {
			return 0, err
		}
		n, err := st.writeAt(data, off)
		if err != errDetached {
			return n, err
		}
		if st.isDeleted() {
			// removed while this write waited: don't bring the file back
			return 0, syscall.ESTALE
		}
	}
}

func (fs *FS) Truncate(p string, size int64) error {
	p = clean(p)
	if fs.ephTruncate(p, size) {
		return nil
	}
	for {
		st, err := fs.getState(p, false)
		if err != nil {
			return err
		}
		if err := st.truncate(size); err != errDetached {
			return err
		}
	}
}

// Flush synchronously commits p if it has pending changes.
func (fs *FS) Flush(p string) error {
	p = clean(p)
	fs.mu.Lock()
	st := fs.files[p]
	fs.mu.Unlock()
	if st == nil {
		return nil
	}
	return st.flush()
}

// Sync makes the data written to p durable on local disk (NFS COMMIT, fsync).
// Committing it to Modal still happens on its own schedule.
func (fs *FS) Sync(p string) error {
	p = clean(p)
	fs.mu.Lock()
	st := fs.files[p]
	fs.mu.Unlock()
	if st == nil {
		return nil
	}
	return st.sync()
}

// Release is called when an application closes a file (FUSE). It schedules
// a commit soon instead of blocking close().
func (fs *FS) Release(p string) {
	p = clean(p)
	fs.mu.Lock()
	st := fs.files[p]
	fs.mu.Unlock()
	if st != nil {
		st.schedule(200 * time.Millisecond)
	}
}

// FlushAll commits everything; used on shutdown.
func (fs *FS) FlushAll() error {
	fs.mu.Lock()
	var all []*fileState
	for _, st := range fs.files {
		all = append(all, st)
	}
	fs.mu.Unlock()
	var first error
	for _, st := range all {
		if err := st.flush(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (fs *FS) Close() error {
	fs.closed.Store(true)
	err := fs.FlushAll()
	fs.mu.Lock()
	var all []*fileState
	for _, st := range fs.files {
		all = append(all, st)
	}
	fs.mu.Unlock()
	for _, st := range all { // left with uncommitted changes; journaled for the next start
		st.mu.Lock()
		if st.timer != nil {
			st.timer.Stop()
		}
		fs.cache.leave(fs.volID, st.held) // still on disk: counted until the volume is mounted again
		st.held, st.leftover = 0, true
		st.mu.Unlock()
	}
	fs.cancel()
	_ = fs.mdb.Close()
	return err
}

// ---------- namespace ops ----------

func (fs *FS) Mkdir(p string) error {
	p = clean(p)
	if err := fs.checkPath(p); err != nil {
		return err
	}
	if fs.opt.ReadOnly {
		return syscall.EROFS
	}
	if _, err := fs.Stat(p); err == nil {
		return os.ErrExist
	}
	if _, err := fs.dirStat(parentOf(p)); err != nil {
		return err
	}
	_, err := fs.c.Put(fs.ctx, []modalapi.PutFile{{Path: path.Join(p, KeepName), Size: 0}})
	fs.invalidate(parentOf(p))
	fs.invalidate(p)
	return err
}

func (fs *FS) Remove(p string) error {
	p = clean(p)
	if fs.opt.ReadOnly {
		return syscall.EROFS
	}
	if fs.ephRemove(p) {
		return nil
	}
	a, err := fs.Stat(p)
	if err != nil {
		return err
	}
	if a.IsDir {
		var m map[string]modalapi.Entry
		if m, err = fs.listDir(p, true); err != nil {
			return err
		}
		for name := range m {
			if name != KeepName {
				return syscall.ENOTEMPTY
			}
		}
		fs.mu.Lock()
		for fp := range fs.files {
			if parentOf(fp) == p {
				fs.mu.Unlock()
				return syscall.ENOTEMPTY
			}
		}
		fs.mu.Unlock()
		err = fs.refused(p, "deleted", fs.c.Remove(fs.ctx, p, true))
		fs.ephDropTree(p)
	} else {
		fs.mu.Lock()
		st := fs.files[p]
		delete(fs.files, p)
		fs.mu.Unlock()
		remoteExists := true
		if st != nil {
			remoteExists = st.discard()
		}
		if remoteExists {
			err = fs.c.Remove(fs.ctx, p, false)
			if modalapi.IsNotFound(err) {
				err = nil
			} else if err != nil {
				err = fs.explain(p, err)
				fs.unsupported(p, "delete", err)
			}
		}
		fs.mdb.Delete(p)
		go func() { _ = fs.c.Remove(fs.ctx, fs.sidecarPath(p), false) }()
	}
	fs.invalidate(parentOf(p))
	fs.invalidateTree(p)
	return err
}

// dropEmptyCopy takes away the folder a move made before Modal refused to
// copy into it, if nothing but its keep file is there.
func (fs *FS) dropEmptyCopy(dir string) {
	m, err := fs.listDir(dir, true)
	if err == nil {
		for name := range m {
			if name != KeepName {
				fs.log.Warn("a refused move left a folder with files in it", "path", dir, "file", name)
				return
			}
		}
		err = fs.c.Remove(fs.ctx, dir, true)
	}
	if err != nil {
		fs.log.Warn("couldn't remove the folder a refused move made", "path", dir, "err", err)
	}
	fs.invalidate(parentOf(dir))
	fs.invalidateTree(dir)
}

func (fs *FS) Rename(oldp, newp string) (err error) {
	oldp, newp = clean(oldp), clean(newp)
	defer func() {
		fs.refused(oldp, "moved", err)
		fs.unsupported(oldp, "move", err)
	}()
	if err := fs.checkPath(newp); err != nil {
		return err
	}
	if fs.opt.ReadOnly {
		return syscall.EROFS
	}
	if oldp == newp {
		return nil
	}
	if strings.HasPrefix(newp+"/", oldp+"/") {
		return syscall.EINVAL
	}
	if handled, err := fs.ephRename(oldp, newp); handled {
		return err
	}
	if err := fs.Flush(oldp); err != nil {
		return err
	}
	// a directory is copied server-side, so its pending writes must be there
	fs.mu.Lock()
	var pending []*fileState
	for fp, st := range fs.files {
		if strings.HasPrefix(fp, oldp+"/") {
			pending = append(pending, st)
		}
	}
	fs.mu.Unlock()
	for _, st := range pending {
		if err := st.flush(); err != nil {
			return err
		}
	}
	a, err := fs.Stat(oldp)
	if err != nil {
		return err
	}
	if _, err := fs.dirStat(parentOf(newp)); err != nil {
		return err
	}
	if dst, err := fs.Stat(newp); err == nil {
		if dst.IsDir != a.IsDir {
			if dst.IsDir {
				return syscall.EISDIR
			}
			return syscall.ENOTDIR
		}
		if dst.IsDir {
			if err := fs.Remove(newp); err != nil { // must be empty
				return err
			}
		}
	}
	if !a.IsDir {
		// The file this replaces takes its pending changes with it, once the
		// copy has replaced it. No commit of them may run until then: one
		// landing after the copy would bring them back, or delete the copy.
		fs.mu.Lock()
		old := fs.files[newp]
		fs.mu.Unlock()
		if old != nil {
			old.commitMu.Lock()
			defer old.commitMu.Unlock()
		}
		// VolumeCopyFiles2 is a manifest-level copy (O(#blocks), no data
		// transfer) and atomically replaces an existing destination file.
		if err := fs.c.Copy(fs.ctx, []string{oldp}, newp, false); err != nil {
			if why := fs.explain(oldp, err); why != err {
				return why
			}
			return fs.explain(newp, err)
		}
		if old != nil {
			fs.mu.Lock()
			if fs.files[newp] == old {
				delete(fs.files, newp)
			}
			fs.mu.Unlock()
			old.discard()
		}
		if err := fs.c.Remove(fs.ctx, oldp, false); err != nil && !modalapi.IsNotFound(err) {
			return err
		}
		go func() {
			if err := fs.c.Copy(fs.ctx, []string{fs.sidecarPath(oldp)}, fs.sidecarPath(newp), false); err == nil {
				_ = fs.c.Remove(fs.ctx, fs.sidecarPath(oldp), false)
			}
		}()
	} else {
		// Directory copy semantics are `cp -r src... dstdir/`; so create the
		// destination dir and copy the children into it. All of them: one
		// that doesn't decrypt stops the move before anything changes.
		children, err := fs.c.ListAll(fs.ctx, oldp, false)
		if err != nil {
			return err
		}
		if _, err := fs.c.Put(fs.ctx, []modalapi.PutFile{{Path: path.Join(newp, KeepName), Size: 0}}); err != nil {
			return err
		}
		var srcs []string
		for _, e := range children {
			if name := path.Base(e.Path); name != KeepName {
				srcs = append(srcs, path.Join(oldp, name))
			}
		}
		if len(srcs) > 0 {
			if err := fs.c.Copy(fs.ctx, srcs, newp, true); err != nil {
				var why *unsupportedEntry
				if err = fs.explainTree(oldp, err); errors.As(err, &why) {
					fs.dropEmptyCopy(newp)
				}
				return err
			}
		}
		if err := fs.c.Remove(fs.ctx, oldp, true); err != nil && !modalapi.IsNotFound(err) {
			return err
		}
	}
	fs.mdb.Rename(oldp, newp)
	fs.ephMoveTree(oldp, newp)
	fs.invalidate(parentOf(oldp))
	fs.invalidate(parentOf(newp))
	fs.invalidateTree(oldp)
	fs.invalidateTree(newp)
	return nil
}

// Stats for diagnostics.
func (fs *FS) Stats() map[string]any {
	n, sz := fs.cache.Stats()
	fs.mu.Lock()
	open := len(fs.files)
	fs.mu.Unlock()
	return map[string]any{"cached_blocks": n, "cache_bytes": sz, "open_files": open}
}
