package fusefront

import (
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"weak"

	"github.com/t41372/modalfs/internal/vfs"
)

// bindings separates raw Volume paths from kernel-visible spellings. Nodes
// resolved through equivalent names share one identity. The weak index does
// not keep forgotten go-fuse inodes (or discarded lookup candidates) alive.
type bindings struct {
	names   sync.Mutex // namespace callback plus binding publication/relocation
	mu      sync.Mutex
	byPath  map[string]weak.Pointer[binding]
	nextID  uint64
	sweepAt int
	swept   time.Time
}

type binding struct {
	mu    sync.RWMutex // raw path and stale; held during raw data I/O
	raw   string
	stale bool
	ino   uint64
	kind  uint32
}

func newBindings() *bindings {
	return &bindings{byPath: make(map[string]weak.Pointer[binding]), nextID: 2, sweepAt: 256}
}

// get is called with names held. Ino is unique within the mount, rather than
// a hash that could merge two distinct raw paths.
func (b *bindings) get(raw string, kind uint32) *binding {
	b.mu.Lock()
	if len(b.byPath) >= b.sweepAt || len(b.byPath) >= 256 && time.Since(b.swept) >= time.Second {
		for p, entry := range b.byPath {
			if entry.Value() == nil {
				delete(b.byPath, p)
			}
		}
		b.sweepAt = max(256, 2*len(b.byPath))
		b.swept = time.Now()
	}
	old := b.byPath[raw].Value()
	b.mu.Unlock()
	if old != nil {
		old.mu.RLock()
		if !old.stale && old.kind == kind {
			old.mu.RUnlock()
			return old
		}
		old.mu.RUnlock()
		old.mu.Lock()
		old.stale = true
		old.mu.Unlock()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	id := &binding{raw: raw, ino: b.nextID, kind: kind}
	b.nextID++
	b.byPath[raw] = weak.Make(id)
	return id
}

func (b *binding) acquire() (string, func(), syscall.Errno) {
	b.mu.RLock()
	if b.stale {
		b.mu.RUnlock()
		return "", nil, syscall.ESTALE
	}
	return b.raw, b.mu.RUnlock, 0
}

func (b *binding) current() (string, syscall.Errno) {
	p, release, status := b.acquire()
	if status != 0 {
		return "", status
	}
	release()
	return p, 0
}

func inside(p, dir string) bool { return p == dir || strings.HasPrefix(p, dir+"/") }

// lockAffected is called with names held. An in-flight lookup has published
// its binding before this can run, even if go-fuse has not registered its
// returned inode yet. Unrelated files keep serving reads, writes and syncs.
func (b *bindings) lockAffected(paths ...string) []*binding {
	b.mu.Lock()
	var affected []*binding
	for raw, entry := range b.byPath {
		id := entry.Value()
		if id == nil {
			delete(b.byPath, raw)
			continue
		}
		for _, p := range paths {
			if inside(raw, p) {
				affected = append(affected, id)
				break
			}
		}
	}
	b.mu.Unlock()
	sort.Slice(affected, func(i, j int) bool { return affected[i].ino < affected[j].ino })
	for _, id := range affected {
		id.mu.Lock()
	}
	return affected
}

func unlockBindings(ids []*binding) {
	for i := len(ids) - 1; i >= 0; i-- {
		ids[i].mu.Unlock()
	}
}

func (b *bindings) createGuard(raw string, existed bool) func(error) {
	if existed {
		return nil
	}
	return b.removeGuard(raw)
}

func (b *bindings) removeGuard(raw string) func(error) {
	ids := b.lockAffected(raw)
	return func(err error) {
		if err == nil || vfs.RemoveMayHaveChangedEntry(err) {
			b.mu.Lock()
			for _, id := range ids {
				id.stale = true
				if b.byPath[id.raw].Value() == id {
					delete(b.byPath, id.raw)
				}
			}
			b.mu.Unlock()
		}
		unlockBindings(ids)
	}
}

func (b *bindings) renameGuard(oldRaw, newRaw string) func(error) {
	if oldRaw == newRaw {
		return func(error) {}
	}
	ids := b.lockAffected(oldRaw, newRaw)
	return func(err error) {
		if err == nil || vfs.RenameMayHaveChangedDestination(err) {
			b.mu.Lock()
			// Overwritten destination handles must not start targeting the source.
			for _, id := range ids {
				if inside(id.raw, newRaw) {
					id.stale = true
					if b.byPath[id.raw].Value() == id {
						delete(b.byPath, id.raw)
					}
				}
			}
			if err == nil {
				for _, id := range ids {
					if !id.stale && inside(id.raw, oldRaw) {
						if b.byPath[id.raw].Value() == id {
							delete(b.byPath, id.raw)
						}
						id.raw = newRaw + strings.TrimPrefix(id.raw, oldRaw)
						b.byPath[id.raw] = weak.Make(id)
					}
				}
			}
			b.mu.Unlock()
		}
		unlockBindings(ids)
	}
}
