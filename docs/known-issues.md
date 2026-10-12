# Known issues

What's still open after the October 2026 review and fixes. Each entry says what goes wrong and how we'd fix it; they should become GitHub issues. Fixed items were removed from this list; `git log` has them.

## Safety

### Forged NFS credentials get past the owner check

The NFS server only accepts requests carrying the AUTH_UNIX credentials of the user running modalfs (or root), which keeps other users of a shared Mac from mounting your volumes with `mount_nfs`. A local program that builds its own RPC packets can still claim to be you, because AUTH_UNIX isn't authenticated. **Fix:** there's no strong fix while macOS mounts through its NFS client over TCP; FUSE (with macFUSE or FSKit) would let the kernel vouch for the caller. `internal/nfsfront/handler.go`.

### Sidecars are trusted when nothing can be checked against them

A sidecar (the block list modalfs stores next to a large file) is now compared with any hashes already known, and ignored on a mismatch. When none is known (a fresh cache editing a file another client wrote), it's still trusted on size and mtime alone. **Fix:** hash one block of the file before trusting the sidecar (8 MiB of download per first edit). `internal/vfs/sidecar.go`.

### Range reads from the cache aren't checked

A cached block used whole (as the base of a write or a commit) is checked against its SHA-256. A plain read of part of a block isn't, so a disk error in the cache can return wrong bytes to a reader, though it can't reach Modal through a commit. **Fix:** verify a block once when it's first read after startup, and remember that it was. `internal/vfs/cache.go`.

## Performance

### Read-ahead isn't cancelled when a reader stops

Read-ahead is bounded (it starts after three blocks read in a row and shares a limited number of slots), but a block already being prefetched finishes downloading even if the reader has gone. **Fix:** a context per reader, cancelled when it's been idle for a few seconds. `internal/vfs/vfs.go`.

## Operations

### Auto-mount retries on a timer, not when the network returns

A failed auto-mount retries with backoff for 30 minutes. A Mac that wakes on a network that comes up later still mounts, but up to two minutes after it could have. **Fix:** also retry when the network changes (`SCNetworkReachability` on macOS, NetworkManager's D-Bus signal on Linux). `internal/daemon/daemon.go`.

### Logging out can wait up to 30 minutes on Linux

The systemd unit gives the daemon 30 minutes to finish uploading when it's stopped, so a logout with a large upload pending waits for it. Anything cut short stays journaled and is uploaded at the next mount, so a shorter limit would be safe too; it's a trade-off between a slow logout and an upload that finishes later. `internal/daemon/service.go`.

### The end-to-end tests run only on macOS

`internal/e2e` mounts over NFS with the macOS client. The Linux FUSE frontend is covered by unit tests and by building in CI, not by mounting. **Fix:** a FUSE variant of the suite for Linux CI (it needs `/dev/fuse` in the runner).

### go-nfs is vendored with changes

`third_party/go-nfs` is go-nfs v0.0.4 with modalfs's changes (concurrent requests, write stability, per-request authorization; search for "modalfs:"). Upstream fixes have to be merged by hand. **Fix:** offer the changes upstream.
