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

### A copy larger than the cache size pauses in long stretches on macOS

When changes fill the cache size, writes wait for uploads. modalfs paces them once the room is three quarters full (each write takes its turn at the upload's speed) and holds a write for a block or two's upload at most (up to about 10 s on real Modal). The macOS NFS client still holds the copy's writes back by itself at times: a 1 GiB copy under a 256 MB cache on real Modal paused for 22-28 s at a time (46 s before pacing) and took 67-80 s, where the same copy within the cache size ran at the speed of the upload with no pause. The client's delayed writes are flushed by a thread that wakes every 30 s; steady small delays don't trigger this, bursts of blocked writes do, and room is still freed in bursts because a part only commits the start of the file. **Fix:** commit each uploaded block at once to a hidden staging file on Modal, so its room is freed right away whatever the order; then the final commit names the same blocks. Until then, keep the cache size larger than your largest copy. `internal/vfs/room.go`, `internal/vfs/ahead.go`.

### Read-ahead isn't cancelled when a reader stops

Read-ahead is bounded (it starts after three blocks read in a row and shares a limited number of slots), but a block already being prefetched finishes downloading even if the reader has gone. **Fix:** a context per reader, cancelled when it's been idle for a few seconds. `internal/vfs/vfs.go`.

## Remote downloads

### BitTorrent uploads while it downloads

gopeed and aria2 send pieces to other peers while they download a torrent, even with seeding off (the container exits as soon as the data is complete). Modal's terms (§1.5) forbid peer-to-peer file sharing, so a torrent download risks the account, and its peers see the container's address. The Cloudflare WARP option doesn't help: neither tool sends its peer traffic through a proxy, so torrents can't use it and connect directly (the app and CLI say so). **Fix:** a download-only client that proxies its peers (anacrolix/torrent with `NoUpload`, MPL-2.0, or libtorrent), run in the WARP namespace, with UDP through the proxy. `docs/design/remote-downloads.md`.

### gopeed gives up on some servers

When a server answers a range request with the whole file (HTTP 200 instead of 206), gopeed fails the task instead of retrying that connection. The agent continues the task, which usually works, and after 5 errors in a row with nothing gained it switches to aria2, which starts the file over. On datapacket's CDN this happens again and again, so a big file there can lose what gopeed had downloaded. **Fix:** report it upstream; meanwhile, pick aria2 for such servers. `internal/downloads/remote/app.py`.

### YouTube blocks most datacenter addresses

yt-dlp in a Modal container is often refused by YouTube ("Sign in to confirm you're not a bot"): every direct try in the spike was, and 3 of 5 later ones. So YouTube links go through Cloudflare WARP unless you choose Direct, which got past the check in 10 of 12 live runs that connected (direct: 2 of 5): YouTube can flag WARP's addresses too, depending on where a run leaves Cloudflare. A cookies file from a signed-in browser also helps. **Fix:** none certain on modalfs's side short of a residential proxy.

### Cloudflare WARP is free, unofficial and slower

The WARP option uses Cloudflare's free consumer WARP, registered with wgcf, an unofficial client, from a cloud server. Its terms license it for a device you own or control, and Cloudflare can limit or block use at its discretion, including new registrations from one network (modalfs registers once per volume and keeps the identity). It's also slower than direct: about 5–25 MB/s against up to 300 MB/s, depending on where a run leaves Cloudflare. A download through WARP never goes direct when WARP fails; it stops and offers to retry without it. **Fix:** Cloudflare's paid products (WARP Connector, Zero Trust) need the official client, which can't run in a Function. `internal/downloads/remote/warp/`.

### A spend limit isn't recognised for certain

Modal's API reports what a workspace spent, not its credit or its spend limit, and a workspace over its limit refused the deploy with only "Image build … terminated due to external shut-down". So such a failure is shown as "Modal stopped this without saying why", with the spend limit as one cause to check, and a running download Modal stops at the limit looks the same. Only a message that names the limit or credit is shown as one. **Fix:** once Modal's wording for a refused or stopped container at the limit has been seen, recognise it; or ask Modal for an API that reports the limit. `internal/downloads/refusal.go`.

## Operations

### Auto-mount retries on a timer, not when the network returns

A failed auto-mount retries with backoff for 30 minutes. A Mac that wakes on a network that comes up later still mounts, but up to two minutes after it could have. **Fix:** also retry when the network changes (`SCNetworkReachability` on macOS, NetworkManager's D-Bus signal on Linux). `internal/daemon/daemon.go`.

### Logging out can wait up to 30 minutes on Linux

The systemd unit gives the daemon 30 minutes to finish uploading when it's stopped, so a logout with a large upload pending waits for it. Anything cut short stays journaled and is uploaded at the next mount, so a shorter limit would be safe too; it's a trade-off between a slow logout and an upload that finishes later. `internal/daemon/service.go`.

### The end-to-end tests run only on macOS

`internal/e2e` mounts over NFS with the macOS client. The Linux FUSE frontend is covered by unit tests and by building in CI, not by mounting. **Fix:** a FUSE variant of the suite for Linux CI (it needs `/dev/fuse` in the runner).

### go-nfs is vendored with changes

`third_party/go-nfs` is go-nfs v0.0.4 with modalfs's changes (concurrent requests, write stability, per-request authorization; search for "modalfs:"). Upstream fixes have to be merged by hand. **Fix:** offer the changes upstream.
