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

	"github.com/go-git/go-billy/v5"
	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/willscott/go-nfs"
	"github.com/willscott/go-nfs-client/nfs/rpc"
)

// handler gives the NFS server its file handles and checks who's asking.
//
// File handles are an HMAC of the path with a key made at startup: the same
// path always gets the same handle (no cache to evict, so a long `find`
// can't make a shell's working directory go stale, and no table to scan),
// and nobody without the key can make one up.
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

	mu    sync.RWMutex
	paths map[[16]byte][]string

	verifiers *lru.Cache[uint64, verifier]
}

type verifier struct {
	path     string
	contents []fs.FileInfo
}

func newHandler(f billy.Filesystem) *handler {
	h := &handler{fs: f, uid: uint32(os.Getuid()), paths: map[[16]byte][]string{}}
	_, _ = rand.Read(h.key[:])
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

func (h *handler) handleFor(p string) [16]byte {
	m := hmac.New(sha256.New, h.key[:])
	m.Write([]byte(p))
	var id [16]byte
	copy(id[:], m.Sum(nil))
	return id
}

func (h *handler) ToHandle(f billy.Filesystem, path []string) []byte {
	id := h.handleFor(strings.Join(path, "/"))
	h.mu.RLock()
	_, known := h.paths[id]
	h.mu.RUnlock()
	if !known {
		h.mu.Lock()
		h.paths[id] = append([]string(nil), path...)
		h.mu.Unlock()
	}
	return id[:]
}

func (h *handler) FromHandle(fh []byte) (billy.Filesystem, []string, error) {
	var id [16]byte
	if len(fh) != len(id) {
		return nil, nil, &nfs.NFSStatusError{NFSStatus: nfs.NFSStatusBadHandle}
	}
	copy(id[:], fh)
	h.mu.RLock()
	p, ok := h.paths[id]
	h.mu.RUnlock()
	if !ok {
		return nil, nil, &nfs.NFSStatusError{NFSStatus: nfs.NFSStatusStale}
	}
	return h.fs, append([]string(nil), p...), nil
}

// InvalidateHandle forgets a removed or renamed path; if it comes back, it
// gets the same handle again.
func (h *handler) InvalidateHandle(_ billy.Filesystem, fh []byte) error {
	var id [16]byte
	if len(fh) == len(id) {
		copy(id[:], fh)
		h.mu.Lock()
		delete(h.paths, id)
		h.mu.Unlock()
	}
	return nil
}

// HandleLimit only sizes READDIR batches (handles are never evicted).
func (h *handler) HandleLimit() int { return 1 << 16 }

// VerifierFor and DataForVerifier keep directory listings stable while a
// client pages through them with READDIR cookies.
func (h *handler) VerifierFor(path string, contents []fs.FileInfo) uint64 {
	s := sha256.New()
	s.Write([]byte(path))
	for _, c := range contents {
		s.Write([]byte{0})
		s.Write([]byte(c.Name()))
	}
	id := binary.BigEndian.Uint64(s.Sum(nil)[:8])
	h.verifiers.Add(id, verifier{path, contents})
	return id
}

func (h *handler) DataForVerifier(path string, id uint64) []fs.FileInfo {
	if v, ok := h.verifiers.Get(id); ok && v.path == path {
		return v.contents
	}
	return nil
}
