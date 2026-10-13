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
- **What the API tells about money, and what it doesn't** (checked against the Go SDK's proto and the Python client 1.5.5):
  - `WorkspaceBillingSummary` gives the month's metered cost, the billed cost after adjustments, and the adjustments themselves (credits used, free storage, the egress allowance). `WorkspaceBillingRates` gives the live rates (`cpu_hour_cost`, `mem_gib_hour_cost`, …), `WorkspaceBillingReport` the cost per app and day (hourly reports can't span more than 7 days), and `WorkspaceDashboardUrlGet` the workspace's dashboard address.
  - **Nothing gives the credit left or the spend limit.** They're only on the dashboard's Usage & Billing page (`/settings/<workspace>/usage`). Modal's budgets guide says that at the spend limit Modal "stops workloads that would incur additional out-of-pocket charges", but not what a refused or stopped container reports.
  - **What a refusal looked like:** on a workspace over its spend limit, the deploy Sandbox's image build ended within two seconds with "Image build for im-… terminated due to external shut-down. Please try again." Nothing in it names the limit, and the cause wasn't proven. The Go SDK drops Modal's exception text for a terminated build, so the daemon asks for it (`ImageJoinStreaming`) and adds it when there is one.
  - **Not seen:** what a running download reports when the limit is hit mid-run, and Modal's wording when it does name the limit.

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
  - ffmpeg from Debian and Deno (pinned by SHA-256) for yt-dlp.

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
   - 4 times a second in memory, each sample sent on the live stream as it's taken. The speed is what arrived over the last 2 s, measured between the polls that saw the byte count change (gopeed updates it about every 350 ms, so measuring between any two polls made it jitter by ±10%), with a light exponential average on top (±3% on a steady 4 MiB/s, about 0.6 s of lag). The same measure for every downloader; the stall watchdog uses it too;
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

- **Live view:** while the UI shows a download, the daemon connects to the tunnel and the UI polls the daemon for what arrived (4 updates a second); nothing reaches the volume. The list keeps a live feed open for the newest 8 running downloads while it's on screen, so its numbers are current too.
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

- **The whole run's history is kept:** one point per second, about 1–2 MB for 24 h. The UI reads it once, then only what's new, and appends the live points (4 a second). For drawing it averages the points into about 600 slices of the window, fixed in time so the older part of the line doesn't shimmer as samples arrive. Where no run was going, the line drops to zero.
- **Chart: [Liveline](https://benji.org/liveline)** (`liveline` 0.0.7, MIT, React, one canvas, no dependencies).
  - It's built for exactly this: a live line with smooth interpolation, scrubbing, and light and dark themes.
  - `onHover` reports the point and its x/y, so an SVG layer on top draws the **tangent line** and the readout under the chart shows the **acceleration** at the cursor.
  - The acceleration is a least-squares slope over the drawn points within a few seconds of the cursor (a sixtieth of the window either side, at least 2 s), so it's the trend there rather than the step between two samples. "Steady" when it changes the speed by under 3% across that span.
  - The layer works out Liveline's layout the way liveline 0.0.7 does: the padding is set by us, the time window is ours, the window's right edge comes exactly from the hover point, and the value range is the visible values and zero (a reference line at zero, so speeds read against it) with Liveline's 12% margin. Checked by eye at several points on running and finished downloads: the tangent lies along the line.
  - Liveline puts "now" at the right edge. A running download's samples keep their time (shifted only if the container's clock is off by more than 3 s); a finished one's are shifted so its last sample is "now", and the chart is paused.
  - The times no run was going (Modal moving the container, a lost container, a pause, the 24 h hand-over) are shaded bands with a label, and a fallback to aria2 or a stall stop is a marked line.
- **Liveline is pre-1.0 and has one author.** It's a small, isolated component; if it goes stale, swapping it out touches one file.

### API, CLI, UI

- **API:**
  - `POST /api/downloads` takes `{account, volume, folder, urls[], tool?, size?, options?}`.
  - `GET /api/downloads` lists jobs, and `GET /api/downloads/{id}` adds events and history. `?live=1` on the list keeps live feeds open for the newest running downloads (the UI's list sends it while on screen).
  - `GET /api/accounts/{id}/volumes/{vol}/folders?env=&path=` lists the folders in a volume's folder, for picking the destination.
  - `POST /api/downloads/{id}/pause|resume|cancel` (cancel takes `{keep}` to keep the partial files).
  - `DELETE /api/downloads/{id}` forgets a job that isn't running and deletes its records on the volume; finished files stay.
  - `GET /api/downloads/{id}/live?after=T` returns what the container sent since `T`. While someone polls it, the daemon holds the tunnel connection (this run's history first, then the stream) and closes it 15 s after the last poll. The tunnel's address and token never leave the daemon: the agent answers no CORS preflight, `EventSource` can't send the token header, and the desktop app reaches the daemon only through its socket.
  - `GET /api/downloads/{id}/log` returns the downloader's log, saved when a run ends.
  - `GET /api/accounts/{id}/billing?env=` returns the month's spend (metered and billed), what remote downloads cost this month in the environment (the downloads app and its deploy app, stopped ones included, from the daily report), the hourly cost of each size at the workspace's rates, and the Usage & Billing page's address. It's cached for 5 minutes; `?fresh=1` skips that.
  - A failed download carries `error_kind`: `billing` when Modal's message names the spend limit, credit, a budget or billing, and `unexplained` when Modal stopped or refused the work without a reason (a build "terminated due to external shut-down", a call that ended without the agent's last word). The UI and CLI say which, and only claim the spend limit for `billing`. A download that should be running carries `silent`, the seconds since its container was last heard from; past 3 minutes (a move takes about one) the UI says Modal may not be starting it.
  - The list is also in `/api/state` (`downloads`), and state changes go on the existing event feed (`dl_*` codes).
- **CLI:** `modalfs download add <volume>[:/folder] <url>… [--downloader gopeed|aria2|yt-dlp] [--size small|medium|large] [--cookies file]`, plus `download ls`, `download show <id> [--follow]` (attempts, interruptions and a text sparkline), `download pause|resume|cancel|logs|rm <id>`, `download spend` (the month's spend and the Usage & Billing link) and `download enable|disable`.
- **UI:** a Downloads page in the rail: the list (live numbers, pause and resume), a page per download (stats, the speed chart over 1 min, 10 min or the whole run, what happened in plain sentences, details, the log), and the add dialog (links, volume, a folder picked in it, the downloader, the size and what it costs an hour). Settings has the switch, the size and the stall limit.
  - **New download:** links, destination (volume and folder), tool (chosen automatically, can be changed) and size. The estimated cost is shown first.
  - **Each row:** progress, live speed, time left, cost so far and a status chip.
  - **The detail view:** the speed chart, the timeline of attempts and interruptions, the files and the log tail.
- **Settings:** the switch (off by default), the default size, the stall limit and auto-resume.

**Automatic tool choice:** yt-dlp for known video sites, gopeed for everything else (HTTP, FTP, magnet, torrent, m3u8). A YouTube link shows a note: YouTube blocks most datacenter IPs, and a cookies file may help.

### Security

- **The Modal token is used only by the daemon and the deploy Sandbox** (as an ephemeral Secret, gone with the Sandbox). The download Function never gets it; it uses the container's own credentials.
- **Links with credentials stay in the daemon's job store** and the call's arguments; `job.json` on the volume has them stripped.
- **The tunnel is public but needs the job's random token.** Downloader APIs listen on `127.0.0.1` with random secrets.
- **A yt-dlp cookies file travels in the call's arguments** and is written to a folder in the container's `/tmp` that only the downloader user can read, never to the volume, and removed when yt-dlp stops.
- **The downloaders can't use the workspace's API.** Any process in a Modal container that can open `/run/modal.sock` has the whole workspace's API, with no token: every volume (read, write, delete), apps, Functions and Sandboxes, in every environment (verified; a separate environment isolates nothing). The agent needs it for the tunnel and the 24-hour hand-over; gopeed, aria2, yt-dlp (fetched fresh every run) and ffmpeg don't. So they run as a user of their own (`dl`, in the image), with an environment that has nothing of the agent's, owning only the job's `work/` and gopeed's `state/gopeed/data/`; the volume root, the records and the rest stay root's. The agent:
  - refuses to start a downloader if that user can open the socket (a check every run, in case Modal changes how containers authenticate);
  - reads the downloaders' log files only if they're plain files that user owns, so a planted link can't copy one of the agent's files into the record;
  - kills everything left running as that user before it moves the finished files, and moves only plain files with one link (os.walk without following links, `safe_join`, `unique_path`), then gives them to root.
  - The live test's self-test runs, as that user inside a real download Function and given the agent's `MODAL_*` variables: connect to the socket, list volumes, read, write and delete another volume, spawn a Function, create a Sandbox, read `/proc/1/environ`, write the volume root and the job's record. Every one fails (the API calls never get an answer), while the agent lists volumes in 0.3 s. No file it can read holds the job's token or cookies.
  - What remains: the downloaders can still use the network and write the job's `work/`; a kernel or gVisor escape to root would undo this. Modal's `restrict_modal_access=True` would also shut out root, but takes the tunnel and the hand-over with it.

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

## Found while building (steps 1–2)

Steps 1 and 2 of the plan are built: the agent and its Function (`internal/downloads/remote/app.py`), the Go side (`internal/downloads`), local tests (`just test-agent`) and a live test (`just test-downloads-live <profile>`). The live test passes on real Modal. It covers:

- deploying through a Sandbox;
- gopeed through a simulated preemption;
- pause and resume through the tunnel;
- continuing past the time limit;
- aria2;
- yt-dlp.

What the open questions turned out to be:

- **gopeed after a `200` to a range request: continuing it works,** locally (a test server that does it once) and on Modal. On datapacket gopeed hits it again and again, sometimes every few hundred MB, and keeps making progress after each continue.
  - The agent gives up on gopeed only after 5 errors in a row with less than 64 MiB gained in between. It then falls back to aria2, which starts over, because gopeed's preallocated file can't be resumed by another tool.
  - A fallback sticks: later runs of the job use aria2 (`state/tool`). Before that fix, a resumed run went back to gopeed and threw away aria2's progress.
  - **Worth raising upstream:** gopeed fails the whole task when one connection's range request gets a `200`.
- **Self-respawn before 24 h works.** With a 2-minute limit, the call paused 30 s before it, spawned its successor from inside the container (no token needed) and returned "continued". The successor picked up at 3.13 GB.
  - **Pitfall:** a call spawned from Python returns its result pickled, which the Go SDK can't read. The function sets `_experimental_restrict_output=True` so results are always CBOR. It's experimental in the Modal client, and the daemon relies on `status.json` anyway, so it can live without results if that flag goes away.
- **`sync /vol` during a 40–70 MB/s download takes 0.8–1.1 s on average, 2.4 s at most.** `status.json` read from the laptop was typically 2–5 s old, at most about 9 s.
- **`FunctionCall.Get` still returned results about 45 minutes later,** after the app had been stopped. Longer wasn't tested.
- **Tunnels have no line of their own in the billing report.** It lists CPU, memory and network egress per app.
  - **Egress isn't zero for downloads:** it came to about 2% of the bytes downloaded (presumably TCP acknowledgements and TLS), about $0.0008 per GB, roughly half of the compute cost of a fast download.
- **Other things found:**
  - gopeed logs to `<storage>/logs/core.log` when given a storage folder, not to stdout. The agent reads errors from there and copies all logs to `log.txt`.
  - The Go SDK decodes a Python dict as `map[any]any`; `Modal.Result` converts it.
  - Using the Modal SDK's Function and Sandbox APIs adds its own dependencies (CBOR, JWT, a pickle reader) to go.mod. It's the module modalfs already used for its protos.
- **Deploying through a Sandbox** took 50 s the first time (building the Sandbox's image) and 6–10 s after that. Checking that the agent is current takes one API call.
- **ffmpeg comes from Debian's package,** not a static build; yt-dlp only needs it to merge formats.
