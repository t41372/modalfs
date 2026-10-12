# Remote downloads: design

Status: **direction agreed, not built yet.** Feasibility was tested on real Modal on 2026-10-12; the numbers below come from that spike unless a source is named.

## The idea

Paste a link in modalfs. A Modal Function downloads it straight into one of your volumes. You can close the laptop; the download carries on, and when modalfs is open again you see where it got to, including the full speed history and any interruptions. It supports gopeed (latest v2 beta, the default for plain links), aria2 and yt-dlp, and more downloaders can be added later.

## Decisions (2026-10-12)

| Question | Decision |
| --- | --- |
| Compute | Modal **Functions**, not Sandboxes. Sandboxes cost 3× and the downloaders can resume, so preemption is acceptable. |
| Principle 2 | Changes to "compute only when asked" (wording below). |
| Default for plain links | gopeed (aria2 hasn't had a release in years). aria2 and yt-dlp can be picked per download. |
| yt-dlp version | The latest release, installed at the start of every run. |
| Where finished files go | The folder the user picked. |
| BitTorrent | Supported as the tools do it. Upload while downloading isn't blocked for now; a proxy or tunnel (e.g. Cloudflare WARP) around the container may come later to reduce the risk. |
| Testing | Real Modal is allowed for development and tests (profile `example-workspace`). |

## What changes for modalfs

### Principle 2

> **2. Compute only when asked.** Mounting, browsing and syncing only call Modal's control-plane API and signed block URLs, and never start compute. The one exception is a remote download the user starts: it shows what it costs before it starts, runs in a Function that stops when the download ends, and is listed where the user can see and stop it.

Remote downloads are off until turned on in Settings. Turning them on explains the cost once.

### Modal's terms

§1.5 of Modal's terms (effective May 2026) forbids "peer-to-peer file sharing", and §1.4 forbids content that infringes copyright. A BitTorrent client uploads pieces while it downloads, even with seeding off. The user accepts that risk (account suspension, §1.6) for now. modalfs still turns seeding off, so the container exits as soon as the data is complete. A download-only client (anacrolix/torrent with `NoUpload`, which never sends file data) remains an option if the risk needs to go away.

## What the spike found

All on real Modal (`example-workspace`), in a scratch volume deleted afterwards. The whole spike, about fifteen downloads of 1–10 GB, cost **$0.0115** in compute.

### Running and controlling a Function from Go

- **The Go SDK can't deploy a Function, only call one** (`Functions.FromName`, `Spawn`, `FunctionCall.FromID/Get/Cancel`).
- **Deploying from a short-lived Sandbox works, so users need no local Python.** The Sandbox runs the official client (`pip install modal==<pinned>` in its image), the app file is copied in through the Sandbox filesystem API, and the account's token goes in as an ephemeral Secret. `modal deploy` then runs inside it.
  - The Sandbox is ready in 14 s the first time (its image is built then) and deploying takes 1.4 s.
  - Each deploy costs about $0.0001. An unchanged app is skipped ("no changes detected").
- **One deployed Function serves every volume.** The volume, CPU, memory and timeout are set per call with `WithOptions`.
  - **Pitfall:** the options only apply after `Instance(ctx)`. `Spawn` on the result of `WithOptions` silently ignores them; the first run had no volume attached and wrote to the container's own disk.
  - Inside the container `/vol` is a symlink to `/__modal/volumes/<volume id>`. The function checks that and refuses to run otherwise, which also pins the job to the volume ID (principle 3).
- **Spawning returns in about 0.1 s,** and the container starts in about 1 s once the image is cached.

### Preemption, simulated with `modal container stop`

That's what Modal does on preemption: SIGINT, then the input is rescheduled.

- **The signal arrives about 4 s after the stop.**
- **The grace period is 30 s.** The container was killed 30 s after SIGINT, which is plenty to pause the downloader and save its state (that took under 1 s).
- **The same input reruns on a new container:** the second attempt started 34 s after the signal. The function counts attempts itself, from its own event log on the volume.
- **aria2:** after the interruption at 196 MiB of 953 MiB, the second attempt continued from there; it took 142 s at a 6 MB/s cap, where starting over would have taken over 173 s. The file, read back through the volume API, has the same SHA-256 as an uninterrupted download.
- **gopeed:** its database (bbolt) lived on the volume. After the interruption the second attempt found the task paused at 819 MB, continued it, and kept going (1,093 MB 20 s later).
- **Losing gopeed's database is the case to avoid.** Tested locally after `kill -9`:

  | gopeed's database | Result |
  | --- | --- |
  | Intact | Resumed; 5 MiB downloaded twice |
  | Restored from a copy 1 s older | Resumed; 16 MiB downloaded twice; file correct |
  | Lost | Started over, and left a 286 MB file of mostly zeros next to the new one |

  So the database goes on the volume.
- **Cancelling a call** (`FunctionCall.Cancel`) stops the function's code at once without running its signal handler, and the container was gone 18 s later. So the user's "Pause" can't be a cancel: the function has to be asked to pause (see [Control](#control)), and cancel is only the fallback.

### Volumes as a status channel

- **Writes from the container are visible through the volume API within about 1–2 s** when written once.
- **A file rewritten often isn't readable reliably unless the writer runs `sync`:**

  | How `status.json` is written | Bad reads (zeros or torn) | Delay |
  | --- | --- | --- |
  | Every 0.5 s, write a temp file and rename it | 100% | — |
  | Every 0.5 s, overwrite in place | 43% | 6.3 s median, 14.8 s max |
  | Every 5 s, write a temp file and rename it | 62% | 5.5 s median |
  | Every 5 s, overwrite in place | 21% | 7.5 s median |
  | **Every 5 s, overwrite in place, then `sync /vol`** | **0%** | 4.5 s median, 7.6 s max |
  | **Every 5 s, a new file, then `sync /vol`** | **0%** | 4.9 s median |

  `sync` takes about 1.2 s.
- **So the volume is the durable record, updated every 5 s with `sync`,** and live data comes through a tunnel.
- **A Modal tunnel (`modal.forward`) from inside the Function works:**
  - A request without the job's token gets 403.
  - With the token, a server-sent event stream delivered 4 updates a second; the first byte arrived after about 0.5 s.

### The downloaders on Modal

| URL | gopeed v2.0.0-beta.3 | aria2 1.37 |
| --- | --- | --- |
| datapacket NYC, 1,000 MB | **Failed after 8 s:** one connection got `200` to a range request, and gopeed failed the whole task (`retries=0`) | 14 s (about 68 MiB/s) |
| OVH, 1 GiB | 57 MB/s, then stalled at 494 MB | Stalled at 61 MB |
| datapacket NYC, 10 GB, 4 connections | 32–37 MB/s, survived the interruption | not run |

- **The stalls are the server's doing.** OVH stops sending to Modal's IPs after a while, whichever tool is used. gopeed did keep retrying (15 s read timeout, retries every few seconds), with no data coming. The agent needs its own watchdog: report "stalled", try a pause and continue, and stop paying after a limit.
- **gopeed's hard failure on a `200` is a gopeed weakness.**
  - The agent continues the task after an error, up to a limit. Whether that recovers wasn't seen in the spike: the run that hit it didn't have the retry yet. **To verify.**
  - Worth reporting upstream.
  - If the retry doesn't recover, the agent can fall back to aria2 for that link and say so in the log.
- **gopeed's web build wants a username and password** whenever an API token is set (it panics otherwise). The agent passes random ones; nothing listens outside `127.0.0.1`.
- **There's no download speed limit in gopeed.** aria2 has one; it isn't needed for the product.

### Cost

From `modal billing rates` for the workspace:

| Size | Function (preemptible) | Per 24 h |
| --- | --- | --- |
| 0.125 core, 512 MiB (default) | $0.0099 / h | $0.24 |
| 0.25 core, 1 GiB | $0.020 / h | $0.48 |
| 1 core, 2 GiB (yt-dlp re-encoding) | $0.063 / h | $1.52 |

- **Billing is on the request or actual use, whichever is higher.** A fast download using a whole core is billed for it while it runs.
- **Ingress is free.** Storage ($0.09/GiB-month after 1 TiB) and egress when reading through the mount ($0.04/GiB after 1 TiB a month) are what modalfs already costs.
- **`modal billing report` breaks cost down by app and hour.** It showed the spike app at $0.0115 and the deploy Sandbox at $0.0001. The UI can show each download's estimate (runtime × size × rates) and the app's billed cost per hour.

### 24 hours

- **Functions and Sandboxes both run for at most 24 h** per attempt. Modal's timeouts doc: 1 s to 24 h, and each retry gets a new timeout.
- **Inside a Function the Modal client is already authenticated,** so the function can spawn its own successor before its deadline, with no token passed in and no laptop needed. **To verify.** Otherwise a download over 24 h pauses cleanly and resumes when modalfs is next running.

## Design

```
 laptop                                     Modal (the user's workspace)
┌───────────────────────────┐   deploy once  ┌────────────────────────────────────────────┐
│ UI / CLI                  │  (a Sandbox    │ app "modalfs-downloads"                     │
│   │ JSON API              │   runs modal   │   Function download(job)  ← Spawn(job)     │
│ daemon                    │   deploy)      │     modalfs agent (Python)                  │
│   internal/downloads      │──────────────► │       └ gopeed / aria2 / yt-dlp, loopback  │
│   - jobs (local store)    │  spawn, cancel │     live:  tunnel  https://…modal.host     │──┐
│   - deployer              │                │     durable: /vol → the user's volume       │  │
│   - status reader ◄───────┼── volume API ──┤       .modalfs/downloads/<id>/…             │  │
│   - live reader   ◄───────┼── tunnel (SSE, token) ──────────────────────────────────────┼──┘
└───────────────────────────┘                └────────────────────────────────────────────┘
```

### Deploying the Function

- **The app file is embedded in modalfs** (`internal/downloads/remote/app.py`). It defines the image and the function.
- **The deployed version is the hash of that file.** When modalfs starts a download and the deployed app's version differs, it deploys first through the Sandbox described above. The user sees "Preparing the downloader (first time only)".
- **Each account and environment gets its own app,** named `modalfs-downloads`.
- **The image:**
  - Debian slim with Python;
  - aria2 from Debian;
  - gopeed-web at a pinned version and SHA-256 (updated with modalfs; v2.0.0-beta.3 now);
  - static ffmpeg and Deno for yt-dlp.

  yt-dlp isn't in the image: each run that needs it installs the latest release first (`pip install -U "yt-dlp[default]"`, a few seconds).
- **Function settings:**
  - `scaledown_window=2`, so the container exits 2 s after the job;
  - one job per container (`max_inputs=1`; in the spike, warm containers took several jobs in a row);
  - `retries` for crashes, `timeout` 24 h;
  - CPU and memory from the job's size.

### The agent (in the Function)

One Python file. It needs only the standard library and the Modal client, which is there already. For each job it:

1. **Checks the setup.** `/vol` must be the volume with the expected ID. It appends `start` to `events.jsonl`, with the attempt number and container ID. If the last `status.json` still says "running", it adds `interrupted (no warning)` with the time the last status was written.
2. **Opens the live channel:** an HTTP server behind `modal.forward`, which needs the job's token. Its URL goes in `status.json`.
3. **Runs the downloader through its adapter,** with the downloader's state on the volume:
   - gopeed's database in `state/`;
   - aria2's `.aria2` control files next to the data;
   - yt-dlp's `.part` files.
4. **Records progress:**
   - every second in memory (the live stream sends it 4× a second);
   - `status.json` every 5 s, then `sync /vol`;
   - the speed history as new chunk files every 30 s (`speed/NNNNNN.jsonl`, one `[t, bytes, speed]` line per second), never rewritten.
5. **On SIGINT (preemption):** pauses the downloader, saves its state, writes `status.json` as "interrupted", records the event, runs `sync`, and returns. The 30 s grace period is enough. Modal reruns the input, and the next attempt continues.
6. **On a pause request** (see [Control](#control)): does the same as on SIGINT, then returns normally with "paused".
7. **Stall watchdog:**
   - After 60 s at zero speed: pause and continue the downloader, which opens new connections.
   - After N minutes (10 by default, a setting): stop with "stalled", so a dead server isn't billed for.
8. **When finished:** moves the files from `work/` into the destination folder, never overwriting (`name (1).ext`), and checks that every path stays inside it. Then it writes the final status with sizes and SHA-256s, runs `sync`, and returns the summary.

   The return value is a second durable record: the daemon gets it with `FunctionCall.Get`.
9. **Before the 24 h deadline:** pauses, then spawns its successor (if verified) or returns "time limit".

### Files on the volume

```
.modalfs/downloads/<id>/
  job.json        what was asked (links stripped of credentials), size, tool, destination
  status.json     the latest state, every 5 s
  events.jsonl    start, interrupted, signal, revived, stalled, paused, finished: one line each, with times
  speed/          the speed history in 30 s chunks
  state/          the downloader's own state (gopeed's database, aria2's session)
  work/           files being downloaded
```

The destination folder only ever receives finished files. The local mount sees them at its next listing refresh. If the user wrote a file of the same name meanwhile, the mount's existing conflict check keeps both.

### Control

- **Live view:** while the UI shows a download, the daemon connects to the tunnel and passes the stream on to the UI over the existing event stream. It's fast (4 updates a second), and nothing reaches the volume.
- **Pause:** `POST /pause` on the tunnel, with the token. The agent pauses, saves and returns "paused", so nothing is billed while paused. If the tunnel doesn't answer within 30 s, the daemon cancels the call.
- **Resume:** spawn a new call for the same job. The downloader continues from its state on the volume.
- **Cancel:** pause, then delete `work/` and `state/`, unless the user keeps the partial files.
- **Finding jobs after a restart:** the daemon keeps a job store (`downloads.json` in modalfs's home) with each job's call IDs. It reads `status.json` for the state, and `FunctionCall.Get` with no wait for calls that have ended. Since everything is in the volume, another computer could list the same downloads later.

### Showing interruptions

- **Every attempt is in `events.jsonl`.** The job's detail view shows them as a timeline: "Interrupted by Modal at 14:02 (preempted), resumed 34 s later on a new machine, 196 MiB kept".
- **On the speed chart,** an interruption is a shaded gap with a marker.
- **Two kinds of interruption are told apart:**
  - a signal was received (preemption or a stop), so the state was saved;
  - no warning (the next attempt found "running" with an old time).

### Speed history and the chart

- **The whole run's history is kept:** one point per second, about 1–2 MB for 24 h. The UI downsamples it for drawing (largest-triangle-three-buckets) and appends live points from the tunnel.
- **Chart: [Liveline](https://benji.org/liveline)** (`liveline` 0.0.7, MIT, React, one canvas, no dependencies).
  - It's built for exactly this: a live line with smooth interpolation, scrubbing, and light and dark themes.
  - `onHover` reports the point and its x/y, so an SVG layer on top can draw the **tangent line** and show the **acceleration** at the cursor.
  - The acceleration is the derivative of a smoothed speed (a centred difference over a few seconds, so it isn't noise), in MB/s².
  - The x/y scale for the tangent comes from pairs of hover points. If that proves unreliable, the fallback is a Recharts chart through the shadcn chart component we already have the setup for, where the scales are known exactly.
- **Liveline is pre-1.0 and has one author.** It's a small, isolated component; if it goes stale, swapping it out touches one file.

### API, CLI, UI

- **API:**
  - `POST /api/downloads` takes `{account, volume, folder, urls[], tool?, size?, options?}`.
  - `GET /api/downloads` lists jobs, and `GET /api/downloads/{id}` adds events and history.
  - `POST /api/downloads/{id}/pause|resume|cancel`.
  - `DELETE /api/downloads/{id}` forgets a finished job.
  - Live updates go out on the existing event stream.
- **CLI:** `modalfs download add <volume>[:/folder] <url>… [--tool gopeed|aria2|yt-dlp] [--size small|medium|large] [--cookies file]`, plus `download ls`, `download show <id>` (events and a text sparkline) and `download pause|resume|cancel <id>`.
- **UI:** a Downloads page in the rail.
  - **New download:** links, destination (volume and folder), tool (chosen automatically, can be changed) and size. The estimated cost is shown first.
  - **Each row:** progress, live speed, time left, cost so far and a status chip.
  - **The detail view:** the speed chart, the timeline of attempts and interruptions, the files and the log tail.
- **Settings:** the switch (off by default), the default size, the stall limit and auto-resume.

**Automatic tool choice:** yt-dlp for known video sites, gopeed for everything else (HTTP, FTP, magnet, torrent, m3u8). A YouTube link shows a note: YouTube blocks most datacenter IPs, and a cookies file may help.

### Security

- **The Modal token is used only by the daemon and the deploy Sandbox** (as an ephemeral Secret, gone with the Sandbox). The download Function never gets it; it uses the container's own credentials.
- **Links with credentials stay in the daemon's job store** and the call's arguments; `job.json` on the volume has them stripped.
- **The tunnel is public but needs the job's random token.** Downloader APIs listen on `127.0.0.1` with random secrets.
- **A yt-dlp cookies file travels in the call's arguments** and is written to the container's `/tmp`, never to the volume.

## Testing

- **The agent:** Python unit tests for each adapter against a local throttled HTTP server with range support (the spike's `fileserver`), run with the real gopeed and aria2 binaries. They cover progress, pause and resume from saved state, kill -9 and resume, the stall watchdog, the gopeed retry after a `200`, and "never overwrite".
- **The daemon:** a `Runner` interface. The test runner runs the agent as a local process over a temporary folder standing in for the volume. Covered: job lifecycle, daemon restarts, pause, resume, interruption events and history. No Modal needed; runs in CI.
- **Live** (`-tags live`, `MODALFS_E2E_PROFILE`): deploy through the Sandbox, one download per tool, a `modal container stop` mid-download with a check that it resumes and the hash matches, pause and resume, then the app stopped and the scratch volume deleted.

## Plan

1. **Agent and adapters** (gopeed first), with local tests, including kill -9 and resume.
2. **The app file, the Sandbox deployer, spawning and the job store,** tested live.
3. **API and CLI.**
4. **UI:** the Downloads page and its detail view (Liveline chart, tangent and acceleration, interruption timeline), plus settings, in all three languages.
5. **aria2 and yt-dlp adapters;** self-respawn past 24 h if it holds up.
6. **Docs:** README in both languages, known issues, and AGENTS.md principle 2.

## Still to verify while building

- Whether gopeed recovers when continued after the `200`-to-range failure (otherwise fall back to aria2 for that link).
- Self-respawn from inside the Function before 24 h.
- `sync /vol` cost while a large file is being written.
- How long `FunctionCall.Get` keeps results.
- Whether tunnels cost anything.
