# Large-write failure investigation

Branch: `feat/stabilize-large-file-writes`. Baseline: `cc99faf` (installed application reports `c3c01d3`; the relevant VFS, NFS and daemon mount code has no diff). Investigation started on November 4, 2026.

## Incident evidence

The user copied several large files from another partition of a local macOS disk using Finder. Finder reported error 100060, and listing the destination reported OS error 60. The user reports no VPN, proxy or network switch and no sleep/wake around the failure. The app continued to show both volumes mounted. These observations alone do not identify a cause.

Read-only inspection of the running application found:

- The cache limit is 2 GiB. At inspection there are no pending journals or pending bytes. The affected volume reports 1,319,119,346 uploaded bytes, four commits and no errors. Its last recorded nonempty commit was 1,319,108,608 bytes at 03:03:55 (America/Phoenix). Two other destination files had only zero-length commits in the log.
- Both the lock-free Unix `/api/ping` and Unix `/api/state` answer successfully. Short process samples show sleeping threads, not a CPU loop. A sample of a stripped Go executable cannot establish goroutine lock ownership.
- Both NFS listening descriptors and the browser HTTP listening descriptor are still open in the daemon, but `lsof` reports all three TCP sockets as CLOSED. They are absent from `netstat`, and independent TCP connects to all three return ECONNREFUSED. `nfsstat -m` marks both mounts not responding. This is stronger evidence than a slow request or a cache backlog.
- No matching kernel/network-extension log was found in the narrow incident interval. Absence of logs is not evidence that no kernel event occurred.

No user mount, pending write, process or setting was changed to obtain this evidence. Raw local diagnostics are kept outside the repository; account identifiers, credentials and user filenames are not needed in this report.

## Questions and experiment design

The immediate outage is unavailable TCP listeners while the Unix API remains alive. Why those listeners stopped listening is still unproven. Do not equate a discovered code weakness with the cause of this incident.

The existing write e2e tests mostly use the foreground `modalfs run`, one file, and Go writes. Add an experiment through the actual background daemon, with native copy processes, multiple files, incompressible multi-GiB data and the incident's 2 GiB shared cache. Monitor listing errors as well as latency, Unix API availability and browser TCP reachability. Check complete remote content through the control-plane/block API, independently of local caches. Run against the fake first for bounded network conditions, then real Modal using only a newly created scratch volume. The live path must not start Modal compute.

Investigate transport, local cache backpressure, write/commit interleaving and native-copy behavior separately. Record a failing baseline and an explanation of every wait/resource transition before changing production code. Passing tests constrain the tested conditions; they cannot prove arbitrary hardware, OS or remote-service behavior.

## Adjacent findings (not yet fixes)

- Daemon NFS and HTTP serve errors are discarded. Mount-table presence is treated as mounted even if the server is unavailable. These explain the misleading status but do not explain the sockets becoming CLOSED.
- The vendored NFS record reader rejects fragmented RPC records, and some connection error exits do not close the connection. There is no evidence yet that this incident involved fragmented records; it would also not by itself explain all listening sockets becoming CLOSED.

## Reproduction and resource evidence

`TestDaemonLargeNativeCopies` creates a fresh daemon and volume, then concurrently copies 3 GiB, 512 MiB and 64 MiB + 123 bytes from ordinary local files using `/bin/cp`. The test uses a 2 GiB cache and checks the Unix state API, browser TCP listener and native directory access. The sources use different ChaCha8 streams, so zero compression and repeated-block deduplication cannot disguise the transfer size. Remote verification streams every block into SHA-256, independently of the mount cache.

- Fake Modal at 20 MiB/s: all copies returned in 113.402 seconds. The first probe incorrectly applied the plain-directory 2-second threshold to native `ls`, which also stats changing files; it recorded 6.932 seconds. Corrected the probe to the existing file-stat threshold (45 seconds locally), retained error checks, and recorded this as an experiment correction, not a product fix.
- Direct real Modal, profile `topsoil-drakes`, scratch volume created at 03:33:51: the 3 GiB copy failed in 58.828 seconds with `fcopyfile failed: Operation timed out`, followed by chmod/xattr failures. The last committed prefix was 1,266,679,808 bytes. The browser TCP probe got ECONNREFUSED while Unix API latency remained below 62 ms. Test failed in 82.30 seconds and deleted its scratch volume. This reproduces the incident without using the user's volumes or starting compute.
- In that run, `kern.ipc.mbstat.m_forcedefunct` rose from 30 to 33; allocation failures rose from 3,048 to 37,584. The daemon's HTTP and NFS listening descriptors acquired state `0xa130` and error 9 (EBADF). `0x8000` is `SS_DEFUNCT`. An outbound connection sampled during the event held 3,707,116 unsent payload bytes and 7,557,120 bytes of kernel send-buffer memory; roughly 32 block-upload TCP connections were open.
- This host's TCP initial send buffer is 128 KiB, auto-growth ceiling 4 MiB, and maximum socket buffer 8 MiB. The upload transport never limits its send buffers. The adaptive upload controller measures bytes read from request bodies, before TCP has delivered them, and can permit 32 simultaneous uploads. Buffered bytes therefore look like upload progress and consume a large kernel allocation pool while the slow path drains them.
- Apple XNU's [`mbuf_watchdog_defunct`](https://github.com/apple-oss-distributions/xnu/blob/f6217f891ac0bb64f3d375211650a4c1ff8ca1ea/bsd/kern/uipc_mbuf.c) selects the process holding the most socket memory and defuncts its sockets. [`sodefunct`](https://github.com/apple-oss-distributions/xnu/blob/f6217f891ac0bb64f3d375211650a4c1ff8ca1ea/bsd/kern/uipc_socket.c) sets the exact state/error observed. No matching retained kernel log was available, so attribution uses the counter transition, descriptor states and isolated reproduction together.
- The fake already limits both accepted socket buffers to 256 KiB (`internal/modalfake/listener.go`, introduced in #16) specifically to avoid this watchdog. This protects the test server and applies receiver backpressure absent from real Modal. Existing live e2e also routes traffic through a local throttling proxy. Neither substitutes for a direct-WAN production-client test.

## Proposed fix and rejected alternatives

Bound the production block transport's TCP send buffer before TLS starts, using the standard library's `TCPConn.SetWriteBuffer`, and fail the dial if that requirement cannot be applied. Start with 256 KiB, the budget already used by the fake. At 32 connections this bounds configured send payload capacity to 8 MiB per client on macOS (kernel accounting includes overhead). This leaves transfer concurrency available for Modal's per-block latency and makes the application's writes experience backpressure earlier. Verify throughput, exact remote contents and the watchdog counter on the same direct live test.

Do not merely raise NFS timeouts, enlarge the cache, restart a broken listener, or tune machine-wide sysctls: none removes the unbounded producer-side socket growth. Reducing upload concurrency alone sacrifices parallelism and still permits each remaining socket to grow with platform defaults. Changing the adaptive rate algorithm alone does not bound kernel allocation when the observed rate legitimately selects 32 uploads. A process-wide transfer scheduler would be larger and is unnecessary unless bounded sockets prove insufficient with multiple mounts.

Before the production edit, add a local test that drives the real upload HTTP transport against a stalled receiver and observes the actual TCP send-buffer bound after it fills. Keep the direct multi-GiB mounted e2e as the failure regression, plus crash/recovery and existing write/commit tests. Linux FUSE validation will use the user's disposable VM.
