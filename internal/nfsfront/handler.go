package nfsfront

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/go-git/go-billy/v5"
	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/willscott/go-nfs"
	"github.com/willscott/go-nfs-client/nfs/rpc"
)

// handler gives the NFS server its file handles and checks who's asking.
//
// Live raw paths retain their handle. Each allocation gets a monotonically
// increasing ID and an unguessable HMAC tag; invalidated handles can never
// be revived by a later lookup of the same path. Local renames relocate the
// bindings, including descendants, without changing their file IDs.
//
// Every request must carry AUTH_UNIX credentials of the user running
// modalfs (or root), the way the kernel's NFS client sends them for that
// user's processes. On a Mac shared by several people, that keeps the others
// from mounting this user's volumes. It isn't a defense against a local
// program that forges credentials in its own RPC packets.
type handler struct {
	fs  billy.Filesystem
	key [32]byte
	uid uint32
	log *slog.Logger

	namesMu sync.Mutex // namespace selection through identity publication
	mu      sync.RWMutex
	paths   map[[24]byte]*handleBinding
	byPath  map[string][24]byte
	nextID  uint64

	verifiers    *lru.Cache[uint64, verifier]
	nextVerifier atomic.Uint64
}

type verifier struct {
	path     string
	contents []fs.FileInfo
}

func newHandler(f billy.Filesystem) *handler {
	h := &handler{fs: f, uid: uint32(os.Getuid()), paths: map[[24]byte]*handleBinding{}, byPath: map[string][24]byte{}}
	_, _ = rand.Read(h.key[:])
	if b, ok := f.(*billyFS); ok {
		b.identity = h.fileID
	}
	h.verifiers, _ = lru.New[uint64, verifier](1024)
	return h
}

// Authorize implements the fork's per-request check (third_party/go-nfs/modalfs.go).
// Slow logs a request that kept the mount waiting (nfs.SlowLogger).
func (h *handler) Slow(msg string, args ...any) {
	if h.log != nil {
		h.log.Warn(msg, args...)
	}
}

func (h *handler) Authorize(hdr rpc.Header) bool {
	if hdr.Proc == 0 {
		return true // NULL: a ping, carries no request
	}
	uid, ok := unixUID(hdr.Cred)
	return ok && (uid == h.uid || uid == 0)
}

// unixUID reads the uid from AUTH_UNIX credentials (RFC 5531, section 9.2):
// stamp, machine name (an XDR string), uid, gid, gids.
func unixUID(a rpc.Auth) (uint32, bool) {
	const authUnix = 1
	b := a.Body
	if a.Flavor != authUnix || len(b) < 8 {
		return 0, false
	}
	n := binary.BigEndian.Uint32(b[4:8])
	off := 8 + int(n+3)&^3
	if n > 255 || len(b) < off+4 {
		return 0, false
	}
	return binary.BigEndian.Uint32(b[off : off+4]), true
}

func (h *handler) Mount(ctx context.Context, conn net.Conn, req nfs.MountRequest) (nfs.MountStatus, billy.Filesystem, []nfs.AuthFlavor) {
	if !h.Authorize(req.Header) {
		return nfs.MountStatusErrAcces, nil, nil
	}
	return nfs.MountStatusOk, h.fs, []nfs.AuthFlavor{nfs.AuthFlavorUnix}
}

func (h *handler) Change(f billy.Filesystem) billy.Change {
	if c, ok := f.(billy.Change); ok {
		return c
	}
	return nil
}

func (h *handler) FSStat(context.Context, billy.Filesystem, *nfs.FSStat) error { return nil }

func (h *handler) encodedHandle(counter uint64) [24]byte {
	var id [24]byte
	binary.BigEndian.PutUint64(id[:8], counter)
	mac := hmac.New(sha256.New, h.key[:])
	mac.Write(id[:8])
	copy(id[8:], mac.Sum(nil)[:16])
	return id
}

func (h *handler) ToHandle(_ billy.Filesystem, parts []string) []byte {
	p := strings.Join(parts, "/")
	h.mu.Lock()
	defer h.mu.Unlock()
	id, ok := h.byPath[p]
	if !ok {
		h.nextID++
		id = h.encodedHandle(h.nextID)
		h.byPath[p] = id
		h.paths[id] = &handleBinding{id: id, parts: append([]string(nil), parts...)}
	}
	return append([]byte(nil), id[:]...)
}

func (h *handler) fileID(p string) uint64 {
	var parts []string
	if p != "" {
		parts = strings.Split(p, "/")
	}
	handle := h.ToHandle(h.fs, parts)
	return binary.BigEndian.Uint64(handle[:8])
}

// A cached listing may contain an identity that has since been invalidated.
// Return its old tagged handle without registering it again or substituting
// a successor now occupying the same raw path.
func (h *handler) ToHandleInfo(f billy.Filesystem, path []string, info os.FileInfo) []byte {
	if entry, ok := info.(fileInfo); ok && entry.id != 0 {
		id := h.encodedHandle(entry.id)
		return append([]byte(nil), id[:]...)
	}
	return h.ToHandle(f, path)
}

func (h *handler) binding(handle []byte) (*handleBinding, error) {
	var id [24]byte
	if len(handle) != len(id) {
		return nil, &nfs.NFSStatusError{NFSStatus: nfs.NFSStatusBadHandle}
	}
	copy(id[:], handle)
	h.mu.RLock()
	binding := h.paths[id]
	h.mu.RUnlock()
	if binding == nil {
		return nil, &nfs.NFSStatusError{NFSStatus: nfs.NFSStatusStale}
	}
	return binding, nil
}

func (h *handler) bindingPath(binding *handleBinding) ([]string, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if binding.stale || h.paths[binding.id] != binding {
		return nil, &nfs.NFSStatusError{NFSStatus: nfs.NFSStatusStale}
	}
	return append([]string(nil), binding.parts...), nil
}

func (h *handler) FromHandle(handle []byte) (billy.Filesystem, []string, error) {
	binding, err := h.binding(handle)
	if err != nil {
		return nil, nil, err
	}
	binding.mu.RLock()
	defer binding.mu.RUnlock()
	parts, err := h.bindingPath(binding)
	return h.fs, parts, err
}

// Caller holds both binding.mu for writing and h.mu.
func (h *handler) invalidateBindingLocked(binding *handleBinding) {
	if h.paths[binding.id] != binding {
		return
	}
	p := strings.Join(binding.parts, "/")
	if h.byPath[p] == binding.id {
		delete(h.byPath, p)
	}
	delete(h.paths, binding.id)
	binding.stale = true
}

func (h *handler) InvalidateHandle(_ billy.Filesystem, handle []byte) error {
	binding, err := h.binding(handle)
	if err != nil {
		return nil
	}
	binding.mu.Lock()
	defer binding.mu.Unlock()
	h.mu.Lock()
	h.invalidateBindingLocked(binding)
	h.mu.Unlock()
	return nil
}

// These convenience methods are also used by focused identity tests.
func (h *handler) Removed(f billy.Filesystem, path []string) { h.GuardRemove(f, path)(nil) }
func (h *handler) Renamed(f billy.Filesystem, oldPath, newPath []string) {
	h.GuardRename(f, oldPath, newPath)(nil)
}

// HandleLimit only sizes READDIR batches (handles are never evicted).
func (h *handler) HandleLimit() int { return 1 << 16 }

// VerifierFor and DataForVerifier keep directory listings stable while a
// client pages through them with READDIR cookies.
func (h *handler) VerifierFor(path string, contents []fs.FileInfo) uint64 {
	// Every listing owns its snapshot. A digest of names would reuse the
	// verifier after replacement and silently substitute a successor's ID
	// or attributes for an older paginated READDIR request.
	id := h.nextVerifier.Add(1)
	h.verifiers.Add(id, verifier{path, contents})
	return id
}

func (h *handler) DataForVerifier(path string, id uint64) []fs.FileInfo {
	if v, ok := h.verifiers.Get(id); ok && v.path == path {
		return v.contents
	}
	return nil
}
