package vfs

import (
	"container/list"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
)

// manifestDB remembers, per (volume, path), the block hashes of a specific
// remote file version (identified by size + mtime). Modal's API never returns
// block hashes, so this is what makes client-side partial writes cheap: to
// re-commit a file after modifying one block we must supply the sha256 of
// every unchanged block.
//
// Decoded manifests are kept in an in-memory LRU, so a read looks a hash up
// without decoding the file's whole record. Hashes learned from downloads
// (SetHash) are recorded in memory and written out in batches: every
// manifestFlushEvery, when many are waiting, and on Put and Close. Get and
// Hash see them at once. The DB is only a cache of what can be learned again
// (by downloading a block, or from a sidecar), so a crash that loses the
// last batch only costs a re-download.
type manifestDB struct {
	db     *bolt.DB
	bucket []byte

	mu      sync.Mutex
	lru     *list.List // of *manifestEnt, front = most recent
	byPath  map[string]*list.Element
	bytes   int64
	max     int64
	pending map[string]bool // paths whose cached manifest isn't in the DB yet
	closed  bool
	kick    chan struct{}
	done    chan struct{}
}

type manifestEnt struct {
	path string
	m    *manifest // owned by the cache: changed in place under mu, never handed out
	cost int64
}

type manifest struct {
	Size   int64
	Mtime  int64
	Hashes [][]byte // nil entry = unknown
}

const (
	manifestCacheBytes = 32 << 20 // a 100 GiB file's manifest is about 700 KiB in memory
	manifestFlushEvery = 2 * time.Second
	manifestFlushBatch = 64 // pending manifests that trigger an early write
)

func openManifestDB(path, volumeID string) (*manifestDB, error) {
	// NoSync: commits skip fsync. The DB only caches hashes that can be
	// learned again (by downloading the block, or from a sidecar), so after
	// a power loss it may lack recent hashes or, rarely, be unreadable; an
	// unreadable one is moved aside and started afresh below. Syncing would
	// cost an fsync per batch of downloaded blocks for nothing that matters.
	// Timeout: another process holding the file gets an error, not a hang.
	opts := &bolt.Options{Timeout: time.Second, NoSync: true}
	db, err := bolt.Open(path, 0o600, opts)
	if err != nil && !errors.Is(err, bolt.ErrTimeout) {
		if _, serr := os.Stat(path); serr == nil {
			aside := fmt.Sprintf("%s.broken-%d", path, time.Now().Unix())
			if os.Rename(path, aside) == nil {
				db, err = bolt.Open(path, 0o600, opts)
				if err != nil {
					err = fmt.Errorf("%w (the previous one was unreadable and is now %s)", err, aside)
				}
			}
		}
	}
	if err != nil {
		return nil, err
	}
	b := []byte(volumeID)
	err = db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(b)
		return err
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	d := &manifestDB{db: db, bucket: b, lru: list.New(), byPath: map[string]*list.Element{},
		max: manifestCacheBytes, pending: map[string]bool{}, kick: make(chan struct{}, 1), done: make(chan struct{})}
	go d.flusher()
	return d, nil
}

func encodeManifest(m *manifest) []byte {
	out := make([]byte, 20+32*len(m.Hashes))
	binary.BigEndian.PutUint64(out[0:], uint64(m.Size))
	binary.BigEndian.PutUint64(out[8:], uint64(m.Mtime))
	binary.BigEndian.PutUint32(out[16:], uint32(len(m.Hashes)))
	for i, h := range m.Hashes {
		if h != nil {
			copy(out[20+32*i:], h)
		}
	}
	return out
}

var zeroHash = make([]byte, 32)

func decodeManifest(b []byte) (*manifest, error) {
	if len(b) < 20 {
		return nil, errors.New("short manifest")
	}
	m := &manifest{Size: int64(binary.BigEndian.Uint64(b[0:])), Mtime: int64(binary.BigEndian.Uint64(b[8:]))}
	n := int(binary.BigEndian.Uint32(b[16:]))
	if len(b) < 20+32*n {
		return nil, errors.New("short manifest")
	}
	m.Hashes = make([][]byte, n)
	for i := 0; i < n; i++ {
		h := b[20+32*i : 20+32*(i+1)]
		if string(h) != string(zeroHash) {
			m.Hashes[i] = append([]byte(nil), h...)
		}
	}
	return m, nil
}

// clone copies the hash list; the hashes themselves are never changed in
// place, only replaced, so they can be shared.
func (m *manifest) clone() *manifest {
	return &manifest{Size: m.Size, Mtime: m.Mtime, Hashes: append([][]byte(nil), m.Hashes...)}
}

func manifestCost(path string, m *manifest) int64 {
	return int64(len(path)) + 96 + int64(len(m.Hashes))*(24+32)
}

// lookupLocked returns the cached manifest of path (any version), loading
// it from the DB on a miss. nil if there is none.
func (d *manifestDB) lookupLocked(path string) *manifest {
	if el := d.byPath[path]; el != nil {
		d.lru.MoveToFront(el)
		return el.Value.(*manifestEnt).m
	}
	if d.closed {
		return nil
	}
	var m *manifest
	_ = d.db.View(func(tx *bolt.Tx) error {
		if v := tx.Bucket(d.bucket).Get([]byte(path)); v != nil {
			m, _ = decodeManifest(v)
		}
		return nil
	})
	if m != nil {
		d.storeLocked(path, m)
	}
	return m
}

// storeLocked caches m (which the cache then owns) as path's manifest.
func (d *manifestDB) storeLocked(path string, m *manifest) {
	d.dropLocked(path)
	e := &manifestEnt{path: path, m: m, cost: manifestCost(path, m)}
	d.byPath[path] = d.lru.PushFront(e)
	d.bytes += e.cost
	for d.bytes > d.max && d.lru.Len() > 1 {
		back := d.lru.Back().Value.(*manifestEnt)
		if d.pending[back.path] {
			d.flushLocked() // write it out before forgetting it
			if d.pending[back.path] {
				break // the DB can't be written; keep it rather than lose it
			}
		}
		d.dropLocked(back.path)
	}
}

func (d *manifestDB) dropLocked(path string) {
	if el := d.byPath[path]; el != nil {
		d.bytes -= el.Value.(*manifestEnt).cost
		d.lru.Remove(el)
		delete(d.byPath, path)
	}
}

// Get returns the stored manifest if it matches the given version. The
// caller gets its own copy.
func (d *manifestDB) Get(path string, size, mtime int64) *manifest {
	d.mu.Lock()
	defer d.mu.Unlock()
	m := d.lookupLocked(path)
	if m == nil || m.Size != size || m.Mtime != mtime {
		return nil
	}
	return m.clone()
}

// Hash returns block idx's hash in the given version, or nil if unknown.
func (d *manifestDB) Hash(path string, size, mtime int64, idx int64) []byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	m := d.lookupLocked(path)
	if m == nil || m.Size != size || m.Mtime != mtime || idx < 0 || idx >= int64(len(m.Hashes)) {
		return nil
	}
	return m.Hashes[idx]
}

// Put stores a whole manifest, and writes it and every pending update out.
func (d *manifestDB) Put(path string, m *manifest) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.storeLocked(path, m.clone())
	d.pending[path] = true
	d.flushLocked()
}

// SetHash records one block hash for a version, replacing a record of
// another version. It's written out with the next batch.
func (d *manifestDB) SetHash(path string, size, mtime int64, idx int, h []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	m := d.lookupLocked(path)
	if m == nil || m.Size != size || m.Mtime != mtime {
		m = &manifest{Size: size, Mtime: mtime, Hashes: make([][]byte, numBlocks(size))}
		d.storeLocked(path, m)
	}
	if idx < 0 || idx >= len(m.Hashes) {
		return
	}
	m.Hashes[idx] = h
	d.pending[path] = true
	if len(d.pending) >= manifestFlushBatch {
		select {
		case d.kick <- struct{}{}:
		default:
		}
	}
}

// flushLocked writes every pending manifest in one transaction. On failure
// they stay pending (and cached) for the next try.
func (d *manifestDB) flushLocked() {
	if len(d.pending) == 0 || d.closed {
		return
	}
	err := d.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(d.bucket)
		for p := range d.pending {
			el := d.byPath[p]
			if el == nil {
				continue
			}
			if err := b.Put([]byte(p), encodeManifest(el.Value.(*manifestEnt).m)); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		clear(d.pending)
	}
}

func (d *manifestDB) flusher() {
	defer close(d.done)
	t := time.NewTicker(manifestFlushEvery)
	defer t.Stop()
	for {
		select {
		case <-t.C:
		case <-d.kick:
		}
		d.mu.Lock()
		if d.closed {
			d.mu.Unlock()
			return
		}
		d.flushLocked()
		d.mu.Unlock()
	}
}

func (d *manifestDB) Delete(path string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dropLocked(path)
	delete(d.pending, path)
	if d.closed {
		return
	}
	_ = d.db.Update(func(tx *bolt.Tx) error { return tx.Bucket(d.bucket).Delete([]byte(path)) })
}

// Rename moves path (and, for directories, everything below it).
func (d *manifestDB) Rename(oldp, newp string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.flushLocked() // the DB then holds everything to move
	under := func(p, dir string) bool { return p == dir || strings.HasPrefix(p, dir+"/") }
	for p := range d.byPath {
		if under(p, oldp) || under(p, newp) {
			d.dropLocked(p)
		}
	}
	if d.closed {
		return
	}
	_ = d.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(d.bucket)
		type kv struct{ k, v []byte }
		var moves []kv
		c := b.Cursor()
		if v := b.Get([]byte(oldp)); v != nil {
			moves = append(moves, kv{[]byte(oldp), append([]byte(nil), v...)})
		}
		pre := []byte(oldp + "/")
		for k, v := c.Seek(pre); k != nil && len(k) >= len(pre) && string(k[:len(pre)]) == string(pre); k, v = c.Next() {
			moves = append(moves, kv{append([]byte(nil), k...), append([]byte(nil), v...)})
		}
		for _, m := range moves {
			_ = b.Delete(m.k)
			_ = b.Put([]byte(newp+string(m.k[len(oldp):])), m.v)
		}
		return nil
	})
}

// Close writes out pending updates and closes the DB. Later calls only
// touch the in-memory cache.
func (d *manifestDB) Close() error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil
	}
	d.flushLocked()
	d.closed = true
	d.mu.Unlock()
	select {
	case d.kick <- struct{}{}:
	default:
	}
	<-d.done
	return d.db.Close()
}

func numBlocks(size int64) int {
	return int((size + blockSize - 1) / blockSize)
}
