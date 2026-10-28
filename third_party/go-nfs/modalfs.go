package nfs

import "github.com/willscott/go-nfs-client/nfs/rpc"

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
