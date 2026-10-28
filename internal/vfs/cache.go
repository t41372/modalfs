package vfs

import (
	"bytes"
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
)

// blockCache is an on-disk, content-addressed (sha256) cache of 8 MiB blocks
// with LRU eviction. Because Modal blocks are content addressed too, a block
// cached for one file is reused for every file (or version) sharing it.
//
// Its limit is all the disk modalfs uses, for every mount sharing it: the
// cached blocks and the changes not on Modal yet (pending), those a volume
// that isn't mounted left on disk too. Pending changes come first: cached
// blocks are evicted to make room for them, and writes wait for uploads
// once pending takes all but a share kept for reading.
type blockCache struct {
	dir     string
	max     int64
	mu      sync.Mutex
	size    int64
	lru     *list.List // front = most recent; values are hex keys
	entries map[string]*cacheEnt

	pending int64            // bytes of changes not on Modal yet, kept on disk by every mount
	left    map[string]int64 // of pending: what a volume not mounted left, by volume ID
	freed   chan struct{}    // closed, and replaced, when pending shrinks

	prefetching int64 // read-ahead downloads in flight, for every mount
}

// readRoom is the share of the limit pending changes leave to cached blocks,
// so reads keep working while writes wait.
func readRoom(max int64) int64 { return min(max/4, 1<<30) }

// minFree is the free space writes leave on the disk modalfs keeps its data on.
const minFree = 1 << 30

// maxPrefetch caps the read-ahead downloads in flight across all mounts
// sharing the cache (each holds an 8 MiB buffer). A small cache gets fewer,
// so read-ahead can't evict what is being read.
const maxPrefetch = 16

type cacheEnt struct {
	key  string
	size int64
	el   *list.Element
}

func newBlockCache(dir string, max int64) (*blockCache, error) {
	c := &blockCache{dir: dir, max: max, lru: list.New(), entries: map[string]*cacheEnt{}, left: map[string]int64{}, freed: make(chan struct{})}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	// Changes left by an earlier run, in <volume>/dirty/<file>/<block>
	// next to the blocks: counted until their volume is mounted.
	files, _ := filepath.Glob(filepath.Join(filepath.Dir(dir), "*", "dirty", "*", "*"))
	for _, p := range files {
		if _, err := strconv.ParseInt(filepath.Base(p), 10, 64); err != nil {
			continue // not a block
		}
		if info, err := os.Stat(p); err == nil {
			vol := filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(p))))
			c.left[vol] += info.Size()
			c.pending += info.Size()
		}
	}
	type fi struct {
		key  string
		size int64
		mt   int64
	}
	var all []fi
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if filepath.Ext(p) == ".tmp" {
			_ = os.Remove(p)
			return nil
		}
		info, err := d.Info()
		if err == nil {
			all = append(all, fi{d.Name(), info.Size(), info.ModTime().UnixNano()})
		}
		return nil
	})
	// oldest first so that the newest end up at the front
	sort.Slice(all, func(i, j int) bool { return all[i].mt < all[j].mt })
	for _, f := range all {
		e := &cacheEnt{key: f.key, size: f.size}
		e.el = c.lru.PushFront(e)
		c.entries[f.key] = e
		c.size += f.size
	}
	c.evictLocked()
	return c, nil
}

func (c *blockCache) path(key string) string {
	return filepath.Join(c.dir, key[:2], key)
}

// Get returns the whole cached block, or nil. A cached file that isn't
// size bytes long, or whose content doesn't match its hash (a damaged disk),
// is dropped. Writes and commits build on what Get returns, so it checks.
func (c *blockCache) Get(hash []byte, size int) []byte {
	return c.get(hash, size, func(b []byte) []byte {
		h := sha256.Sum256(b)
		return h[:]
	})
}

func (c *blockCache) get(hash []byte, size int, digest func([]byte) []byte) []byte {
	key, ok := c.touch(hash)
	if !ok {
		return nil
	}
	b, err := os.ReadFile(c.path(key))
	if err == nil && len(b) == size {
		if bytes.Equal(digest(b), hash) {
			return b
		}
	}
	c.drop(key, err == nil)
	return nil
}

// ReadAt fills buf from the cached block at offset off, reading only that
// range, and reports whether it could. The block's length is checked (a
// file of the wrong size is dropped) but its content isn't hashed: plain
// reads skip that cost, and Get checks before anything is written back.
func (c *blockCache) ReadAt(hash []byte, size int, buf []byte, off int64) bool {
	if off < 0 || off+int64(len(buf)) > int64(size) {
		return false
	}
	key, ok := c.touch(hash)
	if !ok {
		return false
	}
	f, err := os.Open(c.path(key))
	if err != nil {
		c.drop(key, false)
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() != int64(size) {
		c.drop(key, err == nil)
		return false
	}
	if n, err := f.ReadAt(buf, off); n != len(buf) {
		c.drop(key, err != nil)
		return false
	}
	return true
}

// touch marks a cached block as just used, and returns its key.
func (c *blockCache) touch(hash []byte) (string, bool) {
	if hash == nil {
		return "", false
	}
	key := hex.EncodeToString(hash)
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if ok {
		c.lru.MoveToFront(e.el)
	}
	return key, ok
}

// drop forgets a damaged or missing block; remove deletes its file too.
func (c *blockCache) drop(key string, remove bool) {
	if remove {
		_ = os.Remove(c.path(key))
	}
	c.mu.Lock()
	c.removeLocked(key)
	c.mu.Unlock()
}

// startPrefetch takes one of the read-ahead slots, if one is free; the
// caller gives it back with endPrefetch.
func (c *blockCache) startPrefetch() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.prefetching >= min(int64(maxPrefetch), c.max/(4*blockSize)) {
		return false
	}
	c.prefetching++
	return true
}

func (c *blockCache) endPrefetch() {
	c.mu.Lock()
	c.prefetching--
	c.mu.Unlock()
}

func (c *blockCache) Has(hash []byte) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.entries[hex.EncodeToString(hash)]
	return ok
}

// Put stores data under hash.
func (c *blockCache) Put(hash []byte, data []byte) {
	key := hex.EncodeToString(hash)
	c.mu.Lock()
	_, exists := c.entries[key]
	max := c.max
	c.mu.Unlock()
	if exists || max <= 0 {
		return
	}
	p := c.path(key)
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	// a temp file of our own: another Put of the same block may be running
	f, err := os.CreateTemp(filepath.Dir(p), key+".*.tmp")
	if err != nil {
		return
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), p)
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return
	}
	c.adopt(key, int64(len(data)))
}

// Adopt moves an existing file (e.g. a flushed dirty block) into the cache.
func (c *blockCache) Adopt(hash []byte, src string, size int64) bool {
	key := hex.EncodeToString(hash)
	c.mu.Lock()
	_, exists := c.entries[key]
	max := c.max
	c.mu.Unlock()
	if exists || max <= 0 {
		return false
	}
	p := c.path(key)
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	// dirty block files are full-size (sparse) files; trim to the real length
	// before the file appears under its cache name
	if err := os.Truncate(src, size); err != nil {
		return false
	}
	if err := os.Rename(src, p); err != nil {
		return false
	}
	c.adopt(key, size)
	return true
}

func (c *blockCache) adopt(key string, size int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.entries[key]; ok {
		return
	}
	e := &cacheEnt{key: key, size: size}
	e.el = c.lru.PushFront(e)
	c.entries[key] = e
	c.size += size
	c.evictLocked()
}

func (c *blockCache) removeLocked(key string) {
	if e, ok := c.entries[key]; ok {
		c.lru.Remove(e.el)
		delete(c.entries, key)
		c.size -= e.size
	}
}

func (c *blockCache) evictLocked() {
	for c.size+c.pending > c.max && c.lru.Len() > 0 {
		e := c.lru.Back().Value.(*cacheEnt)
		_ = os.Remove(c.path(e.key))
		c.removeLocked(e.key)
	}
}

// addPending changes the bytes pending changes keep on disk. Growth evicts
// cached blocks to make room; shrinking wakes the writes waiting for room.
func (c *blockCache) addPending(delta int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pending += delta
	if delta < 0 {
		close(c.freed)
		c.freed = make(chan struct{})
	} else {
		c.evictLocked()
	}
}

// leave keeps n bytes of a closing mount's changes counted, as left by its
// volume, until the volume is mounted again.
func (c *blockCache) leave(vol string, n int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.left[vol] += n
}

// takeOver stops counting what vol left: its mount counts the changes it
// recovers itself.
func (c *blockCache) takeOver(vol string) {
	c.addPending(-c.Left(vol))
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.left, vol)
}

// Left returns the bytes of changes vol left on disk while not mounted.
func (c *blockCache) Left(vol string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.left[vol]
}

// room reports whether need more bytes of pending changes fit. If not,
// freed is closed when some are uploaded, stuck says nothing a mount could
// upload is pending, so waiting can't help, and left says changes of
// volumes that aren't mounted fill the limit.
func (c *blockCache) room(need int64) (ok bool, freed <-chan struct{}, stuck, left bool) {
	free := diskFree(c.dir)
	c.mu.Lock()
	defer c.mu.Unlock()
	var kept int64
	for _, n := range c.left {
		kept += n
	}
	fits := c.max <= 0 || c.pending+need <= c.max-readRoom(c.max)
	ok = fits && (free < 0 || free-need >= minFree)
	return ok, c.freed, c.pending == kept, !fits && kept > 0
}

// pendingMax is how much pending changes may take, or 0 for no limit.
func (c *blockCache) pendingMax() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.max <= 0 {
		return 0
	}
	return c.max - readRoom(c.max)
}

// Pending returns the bytes pending changes keep on disk, for every mount.
func (c *blockCache) Pending() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pending
}

func (c *blockCache) Stats() (int, int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries), c.size
}

// BlockCache is shared by every mount in one process: blocks are content
// addressed, so identical data cached for one volume serves all of them.
type BlockCache = blockCache

func NewBlockCache(dir string, max int64) (*BlockCache, error) { return newBlockCache(dir, max) }

// SetMax changes the size limit, evicting immediately if needed.
func (c *blockCache) SetMax(max int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.max = max
	c.evictLocked()
}

// Clear drops every cached block.
func (c *blockCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key := range c.entries {
		_ = os.Remove(c.path(key))
	}
	c.entries = map[string]*cacheEnt{}
	c.lru.Init()
	c.size = 0
}

func (c *blockCache) Max() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.max
}
