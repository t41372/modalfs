<p align="center">
  <img src="packaging/icon.png" width="112" alt="modalfs icon">
</p>

<h1 align="center">modalfs</h1>

<p align="center">
  <b>Mount Modal Volumes as local folders on your Mac.</b><br>
  Files stream on demand, so multi-terabyte datasets open instantly without filling your disk.
</p>

<p align="center">
  <a href="https://github.com/t41372/modalfs/actions/workflows/ci.yml"><img src="https://github.com/t41372/modalfs/actions/workflows/ci.yml/badge.svg?branch=main" alt="CI"></a>
  <a href="https://github.com/t41372/modalfs/actions/workflows/modal-live.yml"><img src="https://github.com/t41372/modalfs/actions/workflows/modal-live.yml/badge.svg" alt="Tests against real Modal"></a>
  <a href="https://github.com/t41372/modalfs/actions/workflows/zizmor.yml"><img src="https://github.com/t41372/modalfs/actions/workflows/zizmor.yml/badge.svg?branch=main" alt="zizmor"></a>
  <a href="https://github.com/t41372/modalfs/releases"><img src="https://img.shields.io/github/v/release/t41372/modalfs?sort=semver" alt="Latest release"></a>
  <a href="#install"><img src="https://img.shields.io/badge/platform-macOS%2013%2B%20%7C%20Linux-informational" alt="Platforms: macOS 13+ and Linux"></a>
  <a href="go.mod"><img src="https://img.shields.io/github/go-mod/go-version/t41372/modalfs" alt="Go version"></a>
  <a href="LICENSE"><img src="https://img.shields.io/github/license/t41372/modalfs" alt="License"></a>
</p>

<p align="center">English · <a href="README.zh-CN.md">简体中文</a></p>

<p align="center">
  <img src="docs/screenshots/en/hero.jpg" alt="A Modal Volume named datasets open in Finder as an ordinary folder of parquet files, in front of the modalfs app listing the volumes it manages: three mounted, one uploading 1.2 GB">
</p>

---

**modalfs** mounts any [Modal Volume](https://modal.com/docs/guide/volumes) as a local folder on your Mac or Linux machine. Finder, your code editor, `ffmpeg`, and local agents can read and write to it like any ordinary directory. Because the underlying data lives on Modal, your cloud jobs and local tools always work off the same files.

> [!IMPORTANT]
> modalfs is an independent project and is **not affiliated with or endorsed by Modal Labs**. It communicates with the same API used by Modal's official SDKs, parts of which are undocumented and subject to change. Please read [Limitations](#limitations) before relying on it for critical data.

> [!WARNING]
> Because modalfs operates through your Modal account, everything you do with it is governed by [Modal's Terms of Service](https://modal.com/legal/terms), and violations can result in account suspension. Among other things, sections §1.4 and §1.5 prohibit copyright infringement, peer-to-peer file sharing, and hosting or serving media for third parties. Note that BitTorrent downloads count as peer-to-peer file sharing because clients upload to peers while downloading.

## Contents

- [Highlights](#highlights)
- [Install](#install)
- [Getting started](#getting-started)
- [FAQ](#faq)
- [The app](#the-app)
- [Remote downloads](#remote-downloads)
- [Encrypted volumes](#encrypted-volumes)
- [How it works](#how-it-works)
- [Performance](#performance)
- [Limitations](#limitations)
- [Security](#security)
- [Files and settings](#files-and-settings)
- [Command line](#command-line)
- [Development](#development)
- [License](#license)

## Highlights

- **One shared workspace across every machine.** Mount the exact same Volume on your laptop, desktop, Modal Sandboxes, and Modal Functions.
- **Instant file access.** modalfs fetches only the byte ranges an application requests directly from Modal. Reading 4 KiB from the middle of a 100 GiB file downloads a single 8 MiB block; sequential reads automatically prefetch multiple blocks ahead.
- **Delta block uploads.** Modifying 4 KiB inside a 100 GiB file uploads just one 8 MiB block. Renames transfer zero data (modalfs copies the file's block list on Modal's side), and large file writes stream to Modal in the background while the folder stays responsive.
- **Zero-config app compatibility.** Mounted volumes behave like standard local folders in Finder, VS Code, Jupyter, `ffmpeg`, and Blender. macOS uses the built-in system NFS client—no kernel extensions (kexts) or macFUSE required. Linux mounts via FUSE.
- **Live two-way visibility with Modal jobs.** New Functions read what you saved as soon as modalfs commits it (running containers call `volume.reload()`), and files committed by Modal containers show up in your local folder within about ten seconds.
- **Direct cloud-to-volume downloads.** Paste a URL and let a Modal Function download it straight into your volume—complete with a live speed chart—even while your Mac is asleep. Disabled by default, since it is the only feature that runs compute on Modal.
- **Crash-safe local write journal.** Every local write is journaled to disk until Modal confirms the commit, surviving app crashes and power loss. If a file is modified both locally and remotely, both versions are preserved.
- **Pinned mount identities.** Each mount is bound to its specific account and volume ID. Switching Modal CLI profiles or renaming a volume will never silently point a mount at the wrong data.
- **Human-friendly labels.** Give volumes display names with spaces, emoji, or non-ASCII characters. Labels are stored inside the volume so anyone opening it in modalfs sees the same folder name, while your Python scripts keep referencing the canonical Modal Volume name.
- **Optional end-to-end encryption.** Encrypt file contents, filenames, and volume labels locally before they ever leave your machine. Unlock your volume on any computer with its key.
- **Lightweight native app & full CLI.** Control your mounts from the macOS menu bar or automate everything with the [command-line interface](#command-line).

## Install

### macOS

Requires macOS 13 or later. Download `modalfs-<version>-macos.zip` from [Releases](https://github.com/t41372/modalfs/releases), extract it, and move **modalfs.app** to `/Applications`. The universal binary runs natively on both Apple Silicon and Intel Macs.

Because the app is ad-hoc signed rather than notarized by Apple, macOS Gatekeeper may block it on first launch. On macOS 15 and later, try to open it once, then click **Open Anyway** in **System Settings → Privacy & Security**. On macOS 13 and 14, right-click **modalfs.app** in Finder and select **Open**.

### Linux

Download `modalfs-gui-<version>-linux-amd64.tar.gz` from [Releases](https://github.com/t41372/modalfs/releases) and extract it. The desktop GUI is built on Ubuntu 24.04 and needs a distribution at least that new, plus WebKitGTK 4.1 (`libwebkit2gtk-4.1-0`) and FUSE 3 (`fuse3` and `/dev/fuse`). On older distributions, headless servers, ARM64 Linux, or systems without WebKitGTK, install the [CLI binary](#command-line) instead—`modalfs ui` serves the exact same interface on the machine's `127.0.0.1` (on a headless server, reach it through an SSH tunnel to that port; `modalfs ui --print` shows the link).

## Getting started

1. Launch **modalfs**.
2. Connect your Modal account using an API token from [modal.com → Settings → API Tokens](https://modal.com/settings/tokens), or import your existing profiles directly from the Modal CLI.
3. Click **Add volume** to pick a volume from your account (it's mounted right away), or create a new one. It will appear under `~/ModalVolumes`, ready to open in Finder.

## FAQ

**Does mounting a volume consume Modal compute?**
No. Mounting and file I/O only make standard API and storage requests—Modal bills purely for storage and network bandwidth as usual. Only [remote downloads](#remote-downloads) (which are disabled by default) spin up a Modal Function.

**Can a Modal Function read and write to a volume while it's mounted locally?**
Yes. Call `volume.reload()` inside your Modal container to pick up changes committed by modalfs, and avoid having both sides write to the exact same file simultaneously.

**Why does macOS use NFS by default instead of FUSE?**
macFUSE has to be installed and then allowed in macOS's security settings; with its kernel-extension backend, Apple Silicon Macs must also lower system security in Recovery mode. The built-in macOS NFS client works out of the box with zero system modifications. If you already have macFUSE installed and prefer it, you can switch any volume's **Connection** to FUSE under **Advanced** in its settings.

**Do I need to keep the modalfs window open?**
No. Only the background daemon needs to run. Enable **Start at login** and **Mount at login**, and your volumes will mount automatically in the background every time you log in.

**What happens to edits that haven't uploaded yet if my computer crashes?**
The next time the volume is mounted, modalfs replays uncommitted blocks from its on-disk journal and commits them to Modal. Activity shows "Recovered unsaved changes to …" (or a conflict copy, if the file was updated on Modal in the meantime).

## The app

| View                 | Overview                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                          |
| -------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Volumes**          | View all managed volumes alongside their state (_everything on Modal_, _uploading_, _can't upload_, _conflicts to look at_, or _not mounted_), mount path, and size, or search your workspace to add more. Clicking a volume shows live and historical throughput (1 min, 10 min, 1 hour), what's not on Modal yet, open conflicts, a per-folder storage breakdown, and all its settings: **Label**, **Volume name**, **Mount at login**, **Folder**, **Read-only**, **When both sides changed a file**, the key (for encrypted volumes), and under **Advanced**, **Keep macOS litter local** and **Connection**. |
| **Remote downloads** | Add links (each starts at once in its own container), pause, resume, or delete them. Monitor active downloads with live speed and cost per hour, with a throughput chart on each download's page; finished downloads fold into a **Done** list showing how long each ran and roughly what it cost.                                                                                                                                                                                                                                                                                                                |
| **Activity**         | A log of mounts, commits, conflicts, errors, and crash recoveries, newest first and grouped by day, with a **Conflicts and errors** filter. While the window is open, conflicts and errors also pop up as alerts.                                                                                                                                                                                                                                                                                                                                                                                                 |
| **Settings**         | Manage Modal accounts, **Start at login**, **Mount location**, appearance (System, Light, or Dark), language (System, English, 繁體中文, 简体中文), remote downloads (**Allow remote downloads**, **Container size**, and **Pause when nothing arrives for**), and under **Advanced** the local cache size, read-ahead, and the protocol used to reach Modal.                                                                                                                                                                                                                                                     |

The application window is purely a control surface—all mounts are served by an independent background daemon. **Closing the window never unmounts your volumes.** On macOS, modalfs stays in the menu bar, where its status icon reflects sync activity and gives you quick access to open mounted volumes in Finder or reopen the window. Linux has no menu bar icon, so the first time you close the window with volumes mounted, modalfs says they stay mounted and offers to quit instead.

**Quitting** (⌘Q, or selecting **Quit** from the menu bar or app window) performs a clean shutdown: it flushes pending uploads, unmounts every volume one by one, and stops the background daemon. If an open application is keeping a volume busy, you can **Try again** or **Force unmount**, and **Quit now** stops waiting at any point—any uncommitted data stays safely in the local journal and uploads the next time the volume is mounted. Remote downloads keep running (and billing) on Modal after modalfs quits.

<table>
  <tr>
    <td width="50%" valign="top">
      <a href="docs/screenshots/en/volume.jpg"><img src="docs/screenshots/en/volume.jpg" alt="A volume's page in the dark theme: where it's mounted, its size, its upload and read speed now and over the last minute"></a>
    </td>
    <td width="50%" valign="top">
      <a href="docs/screenshots/en/activity.jpg"><img src="docs/screenshots/en/activity.jpg" alt="Activity: uploads, mounts and a recovery, grouped by day"></a>
    </td>
  </tr>
  <tr>
    <td width="50%" valign="top">
      <a href="docs/screenshots/en/settings.jpg"><img src="docs/screenshots/en/settings.jpg" alt="Settings in the dark theme: Modal accounts, start at login, mount location, appearance, language and remote downloads"></a>
    </td>
    <td width="50%" valign="top">
      <a href="docs/screenshots/en/download.jpg"><img src="docs/screenshots/en/download.jpg" alt="A remote download: its progress, speed and cost so far, its speed over the whole run with the time Modal moved it to a new container, and what happened"></a>
    </td>
  </tr>
</table>

## Remote downloads

Give modalfs a URL, and a Modal Function running in your workspace will download the file directly into your target volume. Your computer can go to sleep, disconnect from Wi-Fi, or shut down completely while the transfer finishes in the cloud.

- **Off by default.** Remote downloads are disabled until you turn them on in **Remote downloads** or **Settings**, as this is the only feature that consumes Modal compute. Functions spin up only when a download starts or resumes, and terminate immediately when the job completes, pauses, or stalls.
- **Transparent cost.** The default `small` worker (0.125 CPU, 512 MiB RAM) costs from about $0.01/hour while active (Modal bills actual CPU use when it's above the request, so a fast download can cost more), plus standard Modal bandwidth rates; downloads also add Modal network egress of about 2% of the bytes downloaded. On first use (and after modalfs updates), a short-lived Sandbox deploys the worker Function into your workspace—about a minute the first time while its images build, then around 10 seconds and ~$0.0001—with no local Python installation required.
- **Built-in download engines.** Ships with `gopeed`, `aria2`, and `yt-dlp`, and updates `yt-dlp` to its latest release on every run, since sites change faster than modalfs does. modalfs automatically selects the best engine for each URL (or lets you pick manually). If `gopeed` keeps failing on a server, `aria2` takes over and downloads that file again from the start.
- **Modal Terms of Service.** Only download content you have the right to access. BitTorrent transfers involve peer-to-peer uploading, which violates [Modal's Terms of Service](https://modal.com/legal/terms) (§1.5) and may lead to account suspension. See [known issues](docs/known-issues.md#bittorrent-uploads-while-it-downloads).
- **Preemption recovery.** If Modal preempts a container mid-download, progress is preserved on the volume and a replacement container resumes from where it left off—typically within a minute. Jobs exceeding Modal's 24-hour Function limit automatically hand off to a fresh container. All container transitions are logged on the download's detail page.
- **Real-time telemetry.** Speed charts update four times per second while viewing an active job. Job state, event logs, and throughput history are persisted inside the volume under `.modalfs/downloads/`.
- **Cloudflare WARP routing.** For origin servers that block cloud datacenter IPs, downloads can be routed through Cloudflare WARP. Traffic is strictly bound to the WARP tunnel (at reduced throughput)—if WARP can't be reached, the download stops instead of falling back to a direct connection, and you can retry it without WARP. If the tunnel drops mid-download, the download stalls and pauses rather than going direct.
- **Safe file naming.** Completed downloads never overwrite existing files; collisions are automatically renamed with a ` (1)` suffix.
- **Spend tracking.** The Remote downloads view shows this month's spend for the whole workspace (storage, network, and compute) and for remote downloads. Because Modal's API does not expose remaining credit balances or spend limits, the UI links directly to Modal's Usage & Billing dashboard. If Modal terminates or rejects a container, the download's page shows the exact reason, or says Modal gave none.

See [known issues](docs/known-issues.md) for current edge cases with remote downloads.

## Encrypted volumes

A new volume can be created with end-to-end encryption. File contents, filenames, and the volume label are encrypted locally before leaving your machine—meaning Modal Labs and anyone else with access to your workspace sees only ciphertext, while your local mount behaves like a normal folder.

Enable **Encrypt end to end** when creating a volume. modalfs will generate and display a volume master key prefixed with `mfs1-`. **Store this key safely (e.g., in a password manager).** With this key and the raw ciphertext on Modal, you can decrypt your data on any machine—even if modalfs's own files on your computer are gone. If you lose the key, your data is permanently unrecoverable; there is no passphrase fallback or key recovery mechanism.

- **Keychain integration.** Your local machine stores the key in the macOS Keychain (or the Linux Secret Service / a `0600` local file) so the volume mounts automatically. On other machines, the volume appears locked until you click **Unlock** and paste the key.
- **Local vs. remote protection.** Encryption protects data at rest and in transit to Modal, not local files on your unlocked computer. Local block caches, uncommitted journal entries, and conflict files remain readable on disk even after locking or forgetting the key. Before handing the computer to someone else, wait for pending uploads to finish, choose **Forget the key on this computer** for each encrypted volume, then run **Clear cache** in Settings (it never deletes changes that haven't uploaded yet).
- **Cloud compute limitations.** Modal Functions cannot read encrypted volumes directly, and remote downloads cannot target them.
- **Metadata visibility.** Modal can still observe the volume's Modal name, directory structure, file counts, file sizes (and from them the exact lengths of files and names), timestamps, when files change, and which 8 MiB ciphertext blocks are identical.
- **Constraints.** Encrypted filenames are limited to 147 bytes (~49 UTF-8 CJK characters). Renaming a directory requires one metadata request per nested file. Only newly created volumes can be encrypted, and volume keys cannot be rotated.

Full cryptographic specifications and design trade-offs are documented in [docs/design/encryption.md](docs/design/encryption.md), including everything needed to decrypt a volume independently of modalfs.

## How it works

<p align="center">
  <img src="docs/diagrams/how-it-works.en.svg" alt="Your computer's apps use an ordinary folder. modalfs serves it over NFS or FUSE and keeps a cache and a journal on disk. It lists and commits files through Modal's Volume API and moves 8 MiB blocks to and from Modal's block store, which your Modal Functions read too.">
</p>

On macOS, modalfs runs a loopback NFS server on `127.0.0.1` mounted by the native macOS NFS client; on Linux, it mounts directly via FUSE. Both frontends are powered by a single background daemon that manages an on-disk read cache, a crash-safe write journal for uncommitted edits, and an index of known block hashes.

**A Modal Volume v2 file is an ordered list of 8 MiB blocks**, each addressed by its SHA-256 hash. To commit a file, modalfs sends the ordered list of block hashes to Modal. Modal replies with whichever hashes are missing from its content-addressed block store, and modalfs uploads only those missing blocks. Identical blocks are automatically deduplicated across all files and volumes. To read a file, Modal returns pre-signed URLs for the requested blocks. A server-side copy only duplicates the block manifest, so copying a 100 GiB file this way takes just 2.9 seconds; modalfs uses it for renames.

Updating a small slice of a large file therefore requires hashing the modified 8 MiB blocks, retaining the hashes of all untouched blocks, and uploading only the newly changed blocks. Because Modal's API does not return the block hashes of existing files, modalfs tracks them through three layers:

1. **Local hash index.** Every full 8 MiB block read from Modal is hashed and cached locally alongside the file's path, size, and modification timestamp.
2. **In-volume manifests.** Whenever modalfs writes a file of 64 MiB or larger, it saves the block hash list inside the volume under `/.modalfs/manifests/`. This allows other machines (or a cleared cache) to perform fast partial writes immediately, provided the file's size and timestamp still match.
3. **On-demand block hashing.** For large files written outside modalfs (such as outputs from a Modal container) that have no manifest yet, a commit still needs the hash of every block you didn't change. As soon as you start editing such a file, modalfs streams those blocks once in the background and hashes them, so the first partial write reads the rest of the file once.

**Streaming writes and progressive commits.** During large file copies, any 8 MiB block that remains unmodified for 500 ms is uploaded immediately in the background. Files are committed progressively in contiguous prefixes—from byte zero up to the first changed block not yet on Modal—so committing each prefix needs no further upload. Every intermediate commit represents a clean prefix of the file aligned on an 8 MiB boundary, never a torn mix of old and new data. Each block upload carries a fixed cost of about a second and a half on Modal's side, so a single stream moves only a few MiB/s; upload concurrency therefore scales dynamically between 4 and 32 parallel streams (targeting ~5 seconds per block) to saturate your connection.

**Reliance on undocumented API behavior.** Pre-uploading blocks before final commit relies on three empirically verified behaviors of Modal's API:

1. Submitting a commit request with a synthetic first block hash returns pre-signed upload URLs for the remaining blocks without committing the file.
2. Blocks uploaded this way can be referenced in subsequent commits for any file in the volume, but only with the upload receipt returned by Modal (modalfs uses a receipt for up to 10 minutes; receipts 15 minutes old have been seen to work).
3. Modal rejects uploaded payloads whose contents do not match their declared SHA-256 hash.

Automated CI tests verify these invariants against production Modal every day. If Modal stops returning upload URLs without committing, modalfs turns uploading ahead off, says so in Activity, and uploads each file when it commits it. If a receipt is refused, that commit simply uploads its blocks again. Either way nothing is lost, and the daily tests watch for the rest. Note that blocks pre-uploaded for a file that is deleted or aborted before commit remain in Modal's block store; Modal does not document its garbage-collection or billing policy for unreferenced blocks.

Additional implementation details:

- **Empty directories.** Modal Volumes do not natively store empty directories, so modalfs places a hidden `.modalfs_keep` placeholder inside newly created folders.
- **Renames.** Renaming a file copies its block manifest and deletes the old path, ensuring compatibility with editors that save files via temporary-file rename.
- **Conflict detection.** Before committing, modalfs verifies the remote file's state on Modal. If another writer modified the file while you were editing it, your local version is saved alongside it as `name.conflict-YYYYMMDD-HHMMSS.ext` (configurable to overwrite instead).
- **macOS metadata filtering.** Finder `.DS_Store` and AppleDouble `._*` files are kept purely in memory and never uploaded to Modal (configurable per volume).
- **Account & volume pinning.** modalfs maintains its own account store with credentials saved in your system keychain. The daemon never follows the active profile in `~/.modal.toml` (reading that file only when you explicitly run an import; the daemon-less `modalfs run` and `modalfs serve` debugging commands are the exception). Each mount records its account, environment, Volume name, and immutable volume ID: if a volume is renamed on Modal, modalfs tracks it by ID; if a volume is deleted and replaced by a new volume of the same name, modalfs refuses to mount rather than silently exposing different data at the same path.

## Performance

Benchmarked from a Linux arm64 sandbox over a bandwidth-capped HTTP/1.1 proxy (~7 MB/s per stream). Direct residential or office connections will see higher throughput.

| Operation                                              | Result                                                          |
| ------------------------------------------------------ | --------------------------------------------------------------- |
| Cold 4 KiB read at offset 77 GiB inside a 100 GiB file | **1.45 s** (subsequent reads: **0 ms** from cache)              |
| Sequential read (512 MiB)                              | **40.9 MiB/s** (sandbox capped single-block streams at ~7 MB/s) |
| In-place 4 KiB edit inside a 100 GiB file              | **~3.5 s** to commit (only one 8 MiB block uploaded)            |
| In-place 4 KiB edit inside a 50 MB file                | **2.0 s** (1 block uploaded)                                    |
| In-place 5-byte edit inside an unindexed 20 MB file    | **3.7 s** (2 blocks fetched to hash, 1 block uploaded)          |
| Server-side copy of a 100 GiB file (used for renames)  | **2.9 s** (metadata-only, zero bytes transferred)               |
| `kill -9` mid-write followed by daemon restart         | Journal replayed and committed cleanly; checksum verified       |

## Limitations

### Modal API constraints

modalfs operates through the Volume API that Modal's SDKs use, which exposes fewer capabilities than volumes mounted directly inside Modal containers. Daily CI tests monitor these constraints—if Modal adds support for any of the items below, please [open an issue](https://github.com/t41372/modalfs/issues).

- **No distributed file locks.** Modal does not support compare-and-swap writes. When concurrent writers update the same file, the last commit wins. modalfs detects remote modifications prior to commit whenever possible and saves a conflict copy.
- **No POSIX permission persistence.** Modal ignores file mode bits on upload, making all files saved via modalfs (or `modal volume put`) appear as `0666` inside Modal containers. Scripts edited locally will fail with `Permission denied` inside a Sandbox, and `git` will report file mode changes. Run `chmod +x` inside the Sandbox and set `git config core.fileMode false` for repositories stored on a volume. Locally, modalfs accepts `chmod`, `chown`, and `utimes` calls as no-ops so tools like `cp -p` and `rsync` succeed without errors.
- **No symlink support.** Symlinks created inside a Modal container appear as symlinks locally, but Modal's API cannot read their target paths or create, modify, move, or delete them. Opening a remote symlink returns `Operation not supported` (`Input/output error` on macOS), and Activity explains why the first time it happens on each mount. Manage symlinks from inside a Modal container. Running `git clone` through modalfs writes symlinks as plain text files and sets `core.symlinks=false` in the repo config—clone symlink-heavy repositories directly inside a Sandbox instead.
- **No hard links.** Hard links cannot be created locally, and editing one path of a container-created hard link splits it into an independent file.
- **Server-assigned timestamps.** File modification times (`mtime`) are set by Modal at commit time.
- **8 MiB minimum write granularity.** Any small random write uploads a full 8 MiB block.
- **Container reload required.** Running Modal containers must call `volume.reload()` to see newly committed changes from modalfs.
- **Undocumented API surface.** Because modalfs relies on parts of that API that Modal doesn't document, upstream changes by Modal may temporarily break compatibility until modalfs is updated.

### Current modalfs limitations

- **Modal Volume v2 only.** Legacy v1 volumes are listed in the UI/CLI but cannot be mounted.
- **Eventual directory consistency.** Directory listings refresh every 10 seconds. If a file is modified remotely while you are actively streaming it locally, subsequent block reads may switch to the newer version mid-read.
- **Files replaced on Modal mid-edit.** If a file you're editing is replaced or deleted on Modal before modalfs has read the parts you didn't change, or is replaced by a file of the same size after it read some of them, modalfs can't tell which version those parts belong to. Rather than make a conflict copy that might mix two versions, it quarantines your modified 8 MiB blocks on this computer alongside assembly instructions and logs the location in Activity.
- **No extended attributes (`xattr`).** Extended attributes are not persisted.
- **macOS-only menu bar item.** On Linux, nothing on screen shows that modalfs is still running after you close the window. Open the app again to get it back (or run `modalfs ui` for the same interface in a browser tab), and choose **Quit** there or run `modalfs quit` to stop it.
- **Early-stage software.** Please report bugs and edge cases via GitHub Issues.

### Operational notes

- **Write-back commit delay.** On an NFS mount (the macOS default), modalfs can't see when a file is closed, so it commits a changed file ~3 seconds after writes pause, and at least every 30 seconds during continuous writes (a large copy commits its finished start about every 10 seconds). On a FUSE mount, closing a file commits it almost immediately. Uncommitted writes are recovered from the journal after a crash—with one edge case: zero bytes at the very end of what an app appended in its last second, without syncing, may be dropped on recovery.
- **Graceful unmounting.** Unmounting flushes pending uploads with a progress indicator. Choosing **Unmount now** detaches the filesystem immediately and finishes uploading in the background (or upon next mount). If an application has files open in the folder, modalfs names it, and **Force unmount** detaches the folder anyway.
- **macOS NFS stalls on low-memory Macs.** On Macs with ~8 GB of RAM, the kernel NFS client sends sustained large file copies in steps about 30 seconds apart (~100 MB per step on a 7.5 GB system), pausing the copy and Finder in between. See [known issues](docs/known-issues.md#large-copies-pause-on-a-mac-with-little-memory).
- **Daemon lifecycle.** The background process must remain running while volumes are mounted. If the daemon is forcefully terminated, the next launch automatically cleans up stale mount points.

## Security

- **Credential storage.** API token secrets are stored in the macOS Keychain or Linux Secret Service (falling back to `~/.config/modalfs/secrets.json` with `0600` permissions on headless Linux). Secrets are never written to logs.
- **Local IPC & Web UI protection.** The daemon communicates with the CLI and desktop app over a `0600` Unix domain socket, and serves the browser UI on a random `127.0.0.1` port. Launching `modalfs ui` exchanges a single-use URL token for an authenticated session cookie, and every request must carry a loopback `Host` header, and every API call a custom request header too, which blocks CSRF and DNS-rebinding attacks.
- **Loopback NFS isolation.** The built-in NFS server binds strictly to `127.0.0.1` and accepts only RPC calls carrying your user ID (or root's), preventing other local user accounts on the same Mac from mounting your volumes. See [known issues](docs/known-issues.md) for local threat-model details.
- **Destructive action safeguards.** Deleting a volume requires typing its name, and a mounted volume can't be deleted. Removing an account asks first (`modalfs account rm` too, unless you pass `--yes`), and isn't allowed while one of its volumes is mounted or one of its remote downloads is running.
- **Vulnerability disclosure.** Please report security issues privately following [SECURITY.md](SECURITY.md) rather than opening a public issue.

## Files and settings

| Component                       | macOS                                             | Linux                                    |
| ------------------------------- | ------------------------------------------------- | ---------------------------------------- |
| Config, accounts, socket & logs | `~/Library/Application Support/modalfs/`          | `~/.config/modalfs/`                     |
| Block & journal cache           | `~/Library/Caches/modalfs/`                       | `~/.cache/modalfs/`                      |
| Default mount directory         | `~/ModalVolumes/`                                 | `~/ModalVolumes/`                        |
| Launch at login service         | `~/Library/LaunchAgents/dev.modalfs.daemon.plist` | `~/.config/systemd/user/modalfs.service` |

On Linux, a daemon started at login by systemd logs to the user journal (`journalctl --user -u modalfs`) instead of a log file.

**Cache sizing & backpressure.** The configured cache limit governs the total disk footprint used by modalfs across all volumes—combining cached read blocks and uncommitted write blocks. Uncommitted writes always take priority, evicting clean read blocks as needed. Up to 25% of the cache (capped at 1 GiB) is reserved for reads; once dirty blocks fill the remaining space, new writes block until background uploads free up capacity.

This means you can copy files much larger than your cache limit without filling your disk—writes simply pace themselves to your upload bandwidth. Because large files upload and commit progressively in segments, and each committed segment frees its disk space at once, blocked writes only wait for one or two 8 MiB blocks to flush rather than the entire file. However, on macOS, triggering write backpressure can cause the kernel NFS client to pause transfers for 20–30 seconds at a time; for smooth Finder copies, set your cache size larger than the largest copy you make at once. Writes also pause if free disk space drops below 1 GiB, and fail immediately with an Activity error when nothing waiting to upload could free room—for example when the disk is full, or the cache is full of changes from volumes that aren't mounted.

If Modal becomes unreachable, applications writing to a volume will block (timing out after a few minutes on macOS, just like a disconnected network share). Data already written to the local journal remains intact and uploads automatically once connectivity returns. Meanwhile the app shows that the volume can't upload, and why.

The cache limit can be set to any integer from `1` GB upward (default: **20 GB**; recommended minimum: **2 GB**, or roughly `2 × concurrent_readers × (read_ahead + 1) × 8 MiB + 256 MiB`).

## Command line

While the desktop app covers everyday use, the `modalfs` CLI exposes every feature of the daemon, the Activity feed included (`modalfs activity`), for terminal workflows, scripts, and headless Linux servers.

### Install

On macOS, the CLI binary is bundled inside the app. Symlink it into your `PATH`:

```sh
sudo ln -sf /Applications/modalfs.app/Contents/Resources/bin/modalfs /usr/local/bin/modalfs
```

For standalone CLI installations, download `modalfs-<version>-darwin-arm64.tar.gz` (or `-darwin-amd64`, `-linux-amd64`, `-linux-arm64`) from [Releases](https://github.com/t41372/modalfs/releases):

```sh
tar -xzf modalfs-*-linux-amd64.tar.gz
sudo install modalfs-*-linux-amd64/modalfs /usr/local/bin/modalfs
```

On Linux, ensure FUSE 3 is installed (`fuse3` package and `/dev/fuse`). To build from source, clone the repository and run `just build` (see [Development](#development)). A plain `go build ./cmd/modalfs` in a checkout leaves out the web UI, so `modalfs ui` then shows a placeholder page; `go install …@latest` doesn't work, because modalfs builds against a patched go-nfs in `third_party/`.

### Quick start

1. **Connect your Modal account.** Generate a token at [modal.com → Settings → API Tokens](https://modal.com/settings/tokens), or import your existing CLI profiles:

   ```sh
   modalfs account add                    # prompts for Token ID and Secret
   modalfs account import                 # or import profiles from ~/.modal.toml
   ```

2. **Create and mount a volume:**

   ```sh
   modalfs volume create my-data
   modalfs mount my-data                  # mounts at ~/ModalVolumes/my-data
   ```

3. **Read and write files.** Use Finder, your editor, or standard shell tools. Changes commit automatically ~3 seconds after writes stop:

   ```console
   $ echo hi >> ~/ModalVolumes/my-data/notes.md
   $ modalfs status
   VOLUME   LABEL  ACCOUNT  STATE    MOUNTPOINT                      PENDING  DOWN/UP
   my-data  -      work     mounted  /Users/me/ModalVolumes/my-data  0        0 B / 3 B

   on this computer: 0 B cached + 0 B not uploaded yet, of 20.0 GB
   ```

4. **Unmount when finished.** Pending uploads flush automatically before unmounting:

   ```sh
   modalfs unmount my-data
   ```

modalfs remembers mount configurations across sessions—running `modalfs mount my-data` again reuses the saved environment and mountpoint (and the account, if you have only one; otherwise pass `--account` again). Passing flags like `--ro` updates the saved configuration for that volume.

### Reference

```
Everyday
  modalfs ui [--print]                        open the app (starts the background process if needed);
                                              --print shows the browser URL instead
  modalfs status                              mounts and what they're uploading
  modalfs mount VOLUME [MOUNTPOINT]           mount and remember it
        [--account A] [--env E] [--ro] [--auto] [--mode auto|nfs|fuse] [--keep-junk]
        [--on-conflict keep-both|overwrite] [--write-back 3s]
  modalfs unmount VOLUME|PATH [--now]         upload what's pending, then unmount;
                                              --now unmounts at once and uploads the rest in the background
  modalfs sync VOLUME|PATH                    upload what's pending now
  modalfs forget VOLUME|PATH                  remove a saved mount (the volume is untouched)
  modalfs info VOLUME|PATH [--measure]        size, files waiting to upload, open conflicts;
                                              --measure lists the volume to measure its size
  modalfs conflicts VOLUME|PATH [--handled]   conflict copies; --handled marks them handled
  modalfs activity [--problems] [--follow]    the Activity feed: mounts, uploads, conflicts, errors,
                                              recoveries; --problems shows only conflicts and errors

Volumes
  modalfs volume ls      [--account A] [--env E]
  modalfs volume create  NAME [--label L] [--account A] [--env E] [--mount]
  modalfs volume rename  NAME NEWNAME [--account A] [--env E]
  modalfs volume label   NAME [LABEL | --clear]   a name shown instead of NAME, kept in the volume
  modalfs volume rm      NAME [--account A] [--env E] [--yes]

Encrypted volumes
  modalfs volume create  NAME --encrypt       shows the new volume's key; save it before going on
  modalfs unlock VOLUME [--remember=false]    unlock with the key (typed, or from stdin);
                                              remembered on this computer unless --remember=false
  modalfs lock VOLUME                         forget the key on this computer (unmount first)
  modalfs volume key     NAME                 show the key, if this computer remembers it
  modalfs volume decrypt NAME --to FOLDER     copy every file out, decrypted, with only the key

Accounts
  modalfs account add [--label L] [--token-id ID] [--env E] [--secret-from-env]
  modalfs account import [PROFILE...]
  modalfs account ls
  modalfs account check ACCOUNT               ask Modal whether the token still works
  modalfs account token ACCOUNT [--token-id ID]   replace an expired token (same workspace)
  modalfs account rename ACCOUNT NEWNAME
  modalfs account env ACCOUNT ENVIRONMENT     the environment new volumes are looked up in
  modalfs account rm ACCOUNT [--yes]          forget the token and its saved mounts (asks first)

Remote downloads (off until enabled; each runs in a Modal Function billed to your account)
  modalfs download enable|disable             turn remote downloads on or off
  modalfs download add VOLUME[:/FOLDER] URL...
        [--downloader gopeed|aria2|yt-dlp] [--size small|medium|large] [--cookies FILE]
        [--warp | --no-warp] [--account A] [--env E]
  modalfs download ls
  modalfs download show ID [--follow]         progress, attempts and interruptions, speed
  modalfs download pause ID
  modalfs download resume ID [--warp | --no-warp]
  modalfs download cancel ID [--keep]         stop it and delete the partial files (--keep keeps them)
  modalfs download logs ID                    the downloader's log
  modalfs download rm ID                      forget it (finished files stay)
  modalfs download spend [--account A] [--env E]
                                              what the workspace and its downloads spent this month

Background process
  modalfs daemon                              run it in the foreground
  modalfs quit [--now]                        upload everything, unmount, stop it;
                                              --now stops at once (what's pending uploads at the next mount)
  modalfs service install|uninstall           start at login (LaunchAgent / systemd user unit)
  modalfs settings [NAME VALUE]               show the settings, or change one: cache-size, read-ahead,
                                              mount-root, transport, download-size, download-stall
  modalfs cache clear                         delete the block cache (changes not uploaded yet are kept)
  modalfs version                             the version and platform

Without the background process (scripts, debugging)
  modalfs run [flags] VOLUME MOUNTPOINT       mount in the foreground until Ctrl-C (modalfs run -h lists the flags)
  modalfs serve [flags] VOLUME                NFS server only; prints the mount command
```

`run` and `serve` use your Modal CLI credentials (the active profile in `~/.modal.toml`, `--profile`, or `MODAL_TOKEN_ID`/`MODAL_TOKEN_SECRET`), not the accounts added to modalfs.

`--account` accepts an account label, account ID, or workspace name, and can be omitted if only one account is configured.

Example remote download workflow:

```console
$ modalfs download enable
$ modalfs download add datasets:/raw https://example.org/train-2024.tar
$ modalfs download show dl-3f2a --follow
```

### Environment variables

| Variable                  | Description                                                                                                                 |
| ------------------------- | --------------------------------------------------------------------------------------------------------------------------- |
| `MODALFS_HOME`            | Override the directory used for configuration and cache files (useful for testing)                                          |
| `MODALFS_TRANSPORT=http1` | Force HTTP/1.1 for Modal API calls (for environments behind HTTP/1.1-only proxies)                                          |
| `MODALFS_NO_KEYCHAIN=1`   | Store secrets in `secrets.json` (`0600`) instead of the system keychain                                                     |
| `MODALFS_DEBUG=1`         | Debug logging for a daemon started with `modalfs daemon` (quit the running one first)                                       |
| `MODAL_TOKEN_SECRET`      | Token secret for `modalfs account add` in scripts, or for accounts added with `--secret-from-env` (the daemon must have it) |
| `MODAL_CONFIG_PATH`       | Where `modalfs account import` reads Modal CLI profiles (default `~/.modal.toml`)                                           |

## Development

Prerequisites: Go 1.27.3+, [Bun](https://bun.sh) (for building the frontend), and [just](https://github.com/casey/just). Building the macOS desktop app requires Xcode Command Line Tools (`xcode-select --install`); building the Linux desktop app requires `libgtk-3-dev` and `libwebkit2gtk-4.1-dev`. Run `just` to list all available recipes:

```sh
git clone https://github.com/t41372/modalfs && cd modalfs
just app             # Build universal macOS app bundle: dist/modalfs.app (Apple Silicon & Intel)
just linux-gui       # Build Linux desktop app: dist/modalfs-gui
just cli             # Cross-compile CLI binaries: dist/modalfs-{darwin,linux}-{arm64,amd64}
just build           # Build CLI binary for the host machine: dist/modalfs
just dev-mock        # Run Web UI with hot reload against mock data (no daemon or Modal account needed)
just dev             # Run Web UI with hot reload against your local running daemon
just lint            # Run gofmt, go vet, TypeScript typechecks, and i18n checks
just test            # Run unit tests with the race detector (offline, no Modal account needed)
just test-e2e        # Run end-to-end mount tests (NFS on macOS, FUSE on Linux) against a simulated slow-network Modal server
just fake -up 20M    # Launch a local fake Modal server to test modalfs without an account
MODALFS_TEST_VOLUME=scratch just test-integration   # Run live integration tests (CREATES AND DELETES files in the target volume)
```

macOS 12 isn't supported. Go 1.27 and later require macOS 13 or later.

The frontend is built with React, Tailwind CSS, and shadcn/ui (on Base UI), following the visual language of [Jezo](https://github.com/t41372/Jezo). Running `just ui` compiles the frontend into `internal/webui/dist`, which is embedded into both the daemon and desktop binaries.

All README screenshots and diagrams are generated programmatically: `cd web && bun run screenshots` captures UI screenshots using mock state from `web/src/api/mock.ts`, and `bun run diagrams` renders the architecture SVG. The hero image composites a live macOS Finder window captured from a mount of the fake Modal server via `web/scripts/capture-finder.sh`.

| Directory                                 | Contents                                                                                   |
| ----------------------------------------- | ------------------------------------------------------------------------------------------ |
| `cmd/modalfs`                             | CLI entrypoint and background daemon                                                       |
| `cmd/modalfs-gui`                         | Native desktop application (Wails), capable of spawning the daemon                         |
| `internal/modalapi`                       | Modal Volume API client, block uploader, and block downloader                              |
| `internal/downloads`                      | Remote downloads: jobs, the Modal Function and its worker (`remote/app.py`)                |
| `internal/encryption`                     | End-to-end encryption of file contents, names and labels                                   |
| `internal/vfs`                            | Virtual filesystem core: block cache, dirty tracking, write journal, and conflict handling |
| `internal/nfsfront`, `internal/fusefront` | Loopback NFSv3 and FUSE filesystem servers                                                 |
| `internal/daemon`                         | Mount lifecycle manager, local JSON API server, and login service installer                |
| `internal/config`                         | Account management, saved mount state, preferences, and keychain integration               |
| `internal/modalfake`, `cmd/modalfs-fake`  | The fake Modal server used by tests and `just fake`                                        |
| `internal/e2e`                            | End-to-end tests that mount modalfs for real                                               |
| `third_party/go-nfs`                      | Patched copy of go-nfs that the NFS server builds on                                       |
| `web`                                     | React + Vite frontend (`web/src/i18n` contains localization strings)                       |
| `packaging`                               | App icons and macOS `.app` bundle packaging scripts                                        |
| `docs`                                    | Architecture design docs, known issues, and generated README assets                        |

Contributions are welcome—testing on real macOS setups is especially helpful. Please read [CONTRIBUTING.md](CONTRIBUTING.md), run `just lint` and `just test`, and (where possible) run the integration suite against a scratch volume before opening a pull request.

## License

[Apache License 2.0](LICENSE).

Modal is a trademark of Modal Labs, Inc. This project is not affiliated with or endorsed by Modal Labs.
