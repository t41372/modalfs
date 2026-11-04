package nfs

import (
	"errors"
	"github.com/go-git/go-billy/v5"
	"github.com/willscott/go-nfs-client/nfs/rpc"
	"os"
	"strings"
	"syscall"
)

// modalfs: a filesystem that implements Syncer gets real NFS write
// stability. WRITE replies UNSTABLE for unstable writes (the client keeps the
// data until a COMMIT succeeds, and resends it if the server restarted in
// between: the write verifier, Server.ID, is random per server), and COMMIT
// (or a stable WRITE) calls SyncPath to make the file's data durable.
// Upstream always claims FILE_SYNC without syncing, so a power loss could
// lose data the client had already dropped.

// Authorizer, if the Handler implements it, is asked about every request
// before it's handled; a request it refuses gets an AUTH_ERROR reply.
type Authorizer interface {
	Authorize(rpc.Header) bool
}

// Syncer makes the written data of one file durable.
type Syncer interface {
	SyncPath(path string) error
}

// SlowLogger, if the Handler implements it, hears about requests that kept
// the client waiting: one other than a write that took longer than two
// seconds to answer, or reading that stopped because every write slot was
// taken.
type SlowLogger interface {
	Slow(msg string, args ...any)
}

// NamedOperations resolves client-provided child names separately from the
// raw paths already held by file handles. Implementations return stored
// paths, so a later READ/WRITE/COMMIT never reselects a different alias.
type NamedOperations interface {
	LookupNamed(parent, name string) (string, os.FileInfo, error)
	CreateNamed(parent, name string, exclusive bool, size *uint64, guard func(string, bool) func(error)) (string, os.FileInfo, error)
	MkdirNamed(parent, name string, guard func(string) func(error)) (string, os.FileInfo, error)
	RemoveNamed(parent, name string, guard func(string) func(error)) (string, error)
	RenameNamed(oldParent, oldName, newParent, newName string, guard func(string, string) func(error)) (string, string, error)
}

func splitFSPath(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

// Named operations preserve the backend's real failure, rather than making
// an unavailable namespace look like a missing file.
func statusFromNamedError(err error) NFSStatus {
	if errors.Is(err, os.ErrNotExist) {
		return NFSStatusNoEnt
	}
	if errors.Is(err, os.ErrExist) {
		return NFSStatusExist
	}
	if errors.Is(err, os.ErrPermission) {
		return NFSStatusAccess
	}
	for _, pair := range []struct {
		err    error
		status NFSStatus
	}{
		{syscall.ENOTDIR, NFSStatusNotDir}, {syscall.EISDIR, NFSStatusIsDir},
		{syscall.EROFS, NFSStatusROFS}, {syscall.ESTALE, NFSStatusStale},
		{syscall.EINVAL, NFSStatusInval}, {syscall.EFBIG, NFSStatusFBig},
		{syscall.ENOSPC, NFSStatusNoSPC}, {syscall.EDQUOT, NFSStatusDQuot},
	} {
		if errors.Is(err, pair.err) {
			return pair.status
		}
	}
	return statusFromNameError(err, NFSStatusIO)
}

// NamespaceGuards protect the selected raw identities for the complete
// mutation, including partial publication and callback/handle publication.
type NamespaceGuards interface {
	GuardCreate(fs billy.Filesystem, path []string, existed bool) func(error)
	GuardMkdir(fs billy.Filesystem, path []string) func(error)
	GuardRemove(fs billy.Filesystem, path []string) func(error)
	GuardRename(fs billy.Filesystem, oldPath, newPath []string) func(error)
}

// RequestScopes gives a handler request-lifetime guards without changing
// every operation. The returned wrapper may pin raw handle bindings while
// namespace requests use their own publication lock.
type RequestScopes interface {
	BeginRequest(header rpc.Header) (Handler, func())
}

// FileInfoHandles binds a listing snapshot's attributes to the same object
// identity, even when that raw name was replaced between READDIR pages.
type FileInfoHandles interface {
	ToHandleInfo(fs billy.Filesystem, path []string, info os.FileInfo) []byte
}

func handleForInfo(h Handler, fs billy.Filesystem, path []string, info os.FileInfo) []byte {
	if identities, ok := h.(FileInfoHandles); ok {
		return identities.ToHandleInfo(fs, path, info)
	}
	return h.ToHandle(fs, path)
}
