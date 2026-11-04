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
// Hash see them at once. Path/hash associations are scoped to this open:
// remounting relearns them from downloads or sidecars. This lets batched
// writes skip fsync without a crash resurrecting known-invalid mappings.
// Immutable block files and pending-write journals are kept separately.
type manifestDB struct {
	db        *bolt.DB
	chunkSize int64
	bucket    []byte

	mu      sync.Mutex
	lru     *list.List // of *manifestEnt, front = most recent
	byPath  map[string]*list.Element
	bytes   int64
	max     int64
	pending map[string]bool // paths whose cached manifest isn't in the DB yet
	// A disk error quarantines this open permanently. RAM remains a bounded
	// cache, but no old disk record may be loaded after an invalidation fails.
	diskErr  error
	onError  func(error) // called by the flusher, outside mu, once per open
	reported bool
	closed   bool
	kick     chan struct{}
	done     chan struct{}
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
	// NoSync is safe only because this open never reads an earlier open's
	// path/hash associations: acquire bbolt's lock, then clear the bucket.
	// Even a valid pre-invalidation DB image after power loss is discarded.
	// Timeout makes another process holding this file fail instead of hang.
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
		if err := tx.DeleteBucket(b); err != nil && !errors.Is(err, bolt.ErrBucketNotFound) {
			return err
		}
		_, err := tx.CreateBucket(b)
		return err
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	d := &manifestDB{db: db, chunkSize: blockSize, bucket: b, lru: list.New(), byPath: map[string]*list.Element{},
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
	if d.closed || d.diskErr != nil {
		return nil
	}
	var m *manifest
	err := d.db.View(func(tx *bolt.Tx) error {
		if v := tx.Bucket(d.bucket).Get([]byte(path)); v != nil {
			m, _ = decodeManifest(v)
		}
		return nil
	})
	if err != nil {
		d.quarantineLocked(err)
		return nil
	}
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
	if d.diskErr == nil && !d.closed {
		d.pending[path] = true
		d.flushLocked()
	}
}

// SetHash records one block hash for a version, replacing a record of
// another version. It's written out with the next batch.
func (d *manifestDB) SetHash(path string, size, mtime int64, idx int, h []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	m := d.lookupLocked(path)
	if m == nil || m.Size != size || m.Mtime != mtime {
		m = &manifest{Size: size, Mtime: mtime, Hashes: make([][]byte, (size+d.chunkSize-1)/d.chunkSize)}
		d.storeLocked(path, m)
	}
	if size == 0 && idx == 0 && len(m.Hashes) == 0 {
		m.Hashes = make([][]byte, 1)
	}
	if idx < 0 || idx >= len(m.Hashes) {
		return
	}
	m.Hashes[idx] = h
	if d.diskErr != nil || d.closed {
		return
	}
	d.pending[path] = true
	if len(d.pending) >= manifestFlushBatch {
		select {
		case d.kick <- struct{}{}:
		default:
		}
	}
}

// flushLocked writes every pending manifest in one transaction. A failure
// disables persistence: retrying could reload an obsolete path association.
func (d *manifestDB) flushLocked() {
	if len(d.pending) == 0 || d.closed || d.diskErr != nil {
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
	if err != nil {
		d.quarantineLocked(err)
	}
	clear(d.pending)
}

// quarantineLocked keeps immutable blocks usable, but never consults this
// disk DB again. Dropping pending writes also lets the RAM LRU stay bounded.
func (d *manifestDB) quarantineLocked(err error) {
	if d.diskErr != nil {
		return
	}
	d.diskErr = err
	clear(d.pending)
	select {
	case d.kick <- struct{}{}:
	default:
	}
}

func (d *manifestDB) setErrorHandler(f func(error)) {
	d.mu.Lock()
	d.onError = f
	d.mu.Unlock()
	select {
	case d.kick <- struct{}{}:
	default:
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
		d.flushLocked()
		closed := d.closed
		var report func(error)
		if d.diskErr != nil && !d.reported && d.onError != nil {
			d.reported = true
			report = d.onError
		}
		err := d.diskErr
		d.mu.Unlock()
		// Callbacks can acquire FS locks. Never invoke one under d.mu.
		if report != nil {
			report(err)
		}
		if closed {
			return
		}
	}
}

func (d *manifestDB) Delete(path string) error { return d.delete(path, false) }

func (d *manifestDB) DeleteTree(path string) error { return d.delete(path, true) }

// DeleteTree forgets p and, optionally, every descendant. In-memory removal
// always completes. The first disk failure is returned and permanently
// disables disk fallback, so a subsequent cache miss cannot revive a record.
func (d *manifestDB) delete(p string, tree bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	under := func(key string) bool { return key == p || (tree && strings.HasPrefix(key, p+"/")) }
	for key := range d.byPath {
		if under(key) {
			d.dropLocked(key)
			delete(d.pending, key)
		}
	}
	if d.closed || d.diskErr != nil {
		return nil
	}
	err := d.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(d.bucket)
		if err := b.Delete([]byte(p)); err != nil {
			return err
		}
		if !tree {
			return nil
		}
		c := b.Cursor()
		prefix := []byte(p + "/")
		for k, _ := c.Seek(prefix); k != nil && strings.HasPrefix(string(k), string(prefix)); k, _ = c.Next() {
			if err := c.Delete(); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		d.quarantineLocked(err)
	}
	return err
}

// Close flushes pending cache records and closes the DB. Later calls only
// touch memory. Disk failures are reported through onError; cached metadata
// does not determine whether the user's files were committed successfully.
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
