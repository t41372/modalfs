package vfs

import (
	"bytes"
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// blockCache is an on-disk, content-addressed (sha256) cache of 8 MiB blocks
// with LRU eviction. Because Modal blocks are content addressed too, a block
// cached for one file is reused for every file (or version) sharing it.
type blockCache struct {
	dir     string
	max     int64
	mu      sync.Mutex
	size    int64
	lru     *list.List // front = most recent; values are hex keys
	entries map[string]*cacheEnt

	prefetching int64 // read-ahead downloads in flight, for every mount
}

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
	c := &blockCache{dir: dir, max: max, lru: list.New(), entries: map[string]*cacheEnt{}}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
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
	for c.size > c.max && c.lru.Len() > 0 {
		e := c.lru.Back().Value.(*cacheEnt)
		_ = os.Remove(c.path(e.key))
		c.removeLocked(e.key)
	}
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
