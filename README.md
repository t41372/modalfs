<p align="center">
  <img src="packaging/icon.png" width="112" alt="modalfs icon">
</p>

<h1 align="center">modalfs</h1>

<p align="center">
  <b>Mount Modal Volumes as folders on your Mac.</b><br>
  Files stream on demand, so terabytes open instantly without filling your disk.
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

modalfs puts a [Modal Volume](https://modal.com/docs/guide/volumes) in a folder on your Mac (or Linux machine). Finder, your editor, `ffmpeg` and your agents use it like any other folder. The data stays on Modal, so your Modal jobs read the same files.

> [!IMPORTANT]
> modalfs is an independent project, **not affiliated with or endorsed by Modal Labs**. It uses the same API as Modal's official SDKs, and parts of that API are undocumented and may change. Read [Limitations](#limitations) before you trust it with data you care about.

> [!WARNING]
> modalfs uses your own Modal account, so [Modal's Terms of Service](https://modal.com/legal/terms) cover everything you do with it, and Modal can suspend an account that breaks them. The terms (§1.4 and §1.5) forbid, among other things, content that infringes copyright, peer-to-peer file sharing, and hosting files or serving media for others. Downloading a torrent is peer-to-peer file sharing, because it uploads to other peers at the same time.

## Contents

- [Highlights](#highlights)
- [Install](#install)
- [Getting started](#getting-started)
- [The app](#the-app)
- [Remote downloads](#remote-downloads)
- [Encrypted volumes](#encrypted-volumes)
- [How it works](#how-it-works)
- [Performance](#performance)
- [Limitations](#limitations)
- [Security](#security)
- [Files and settings](#files-and-settings)
- [FAQ](#faq)
- [Command line](#command-line)
- [Development](#development)
- [License](#license)

## Highlights

- **Files open instantly.** modalfs reads only the parts of a file an app asks for, straight from Modal. Reading 4 KiB from the middle of a 100 GiB file fetches one 8 MiB block. Reading a file from start to end fetches ahead, several blocks at a time.
- **Saving uploads only what changed.** Change 4 KiB of a 100 GiB file and one 8 MiB block goes up. Renames and copies move no data. A big copy uploads in the background while you keep using the folder.
- **Works with the apps you already use.** It's a normal folder, so Finder, VS Code, Jupyter, `ffmpeg` and Blender need nothing extra. macOS mounts it with its built-in NFS client, so there's no kernel extension to install. Linux uses FUSE.
- **Your Modal jobs see the same files.** A Function that mounts the volume reads what you saved, and what it commits appears in your folder within seconds.
- **Download straight into a volume.** Paste a link and a Modal Function downloads it into a volume while your Mac sleeps, with a live speed chart. It's off until you turn it on, because it's the only feature that runs compute on Modal.
- **Writes survive crashes.** Every change is kept on disk until Modal has it, through crashes and power cuts. When a file changed on both sides, you get both versions.
- **Each mount stays on its volume.** A mount is pinned to its account and volume ID, so switching Modal CLI profiles or renaming a volume never points it somewhere else.
- **Labels.** Give a volume a name with spaces, emoji or any language. The label is stored in the volume, so everyone using it with modalfs sees it, and its folder takes the same name. Scripts keep using the Volume name on Modal.
- **End-to-end encryption, if you want it.** Modal can't read an encrypted volume's files, file names or label. Its key opens it on any computer.
- **A small native app** with a menu bar icon, and a [command line](#command-line) that can do everything the app does.

## Install

### macOS

modalfs needs macOS 13 or later. Download `modalfs-<version>-macos.zip` from [Releases](https://github.com/t41372/modalfs/releases), unzip it and move **modalfs.app** to Applications. The same app runs on Apple silicon and Intel.

The app is signed ad hoc, not notarized, so macOS may refuse to open it the first time. If it does, right-click the app in Finder and choose **Open**.

### Linux

Download `modalfs-gui-<version>-linux-amd64.tar.gz` from [Releases](https://github.com/t41372/modalfs/releases) and unpack it. The app needs WebKitGTK 4.1 (`libwebkit2gtk-4.1-0`) and FUSE (the `fuse3` package and `/dev/fuse`). On other machines, or without WebKitGTK, use the [command line](#command-line): `modalfs ui` opens the same app in your browser.

## Getting started

1. Open modalfs.
2. Connect your Modal account. You need an API token from [modal.com → Settings → API Tokens](https://modal.com/settings/tokens), or modalfs can copy the profiles you already have in the Modal CLI.
3. Add a volume, or create one. It appears in `~/ModalVolumes`, and you can open it in Finder.

## The app

| Page | What's there |
|---|---|
| **Volumes** | The volumes modalfs manages, with each one's state (synced, uploading, can't upload, conflict, not mounted), folder and size. Add another volume from your account by searching for it. A volume's page shows its speed now and over the last minute, ten minutes or hour, what's still waiting to upload, its conflicts, and its size by folder. Its settings are all in one place: label, Volume name, mounting at login, folder, read-only, what to do on a conflict, and the encryption key. |
| **Remote downloads** | Add links, pause, resume and delete. See what's running, how fast and what it costs, and each download's speed over its whole run. Finished downloads are folded away, each with how long it ran and what it cost. |
| **Activity** | Mounts, uploads, conflicts, errors and crash recoveries, grouped by day. Important ones also send a notification. |
| **Settings** | Your Modal accounts, start at login, the mount folder, light or dark mode, language (English, 繁體中文, 简体中文), remote downloads, and under Advanced the cache, read-ahead and connection mode. |

The window is only a remote control: the mounts live in a background process. **Closing the window never unmounts anything.** On macOS, modalfs stays in the menu bar, where its icon shows whether everything is on Modal. Its menu lists your volumes (click one to open it in Finder or to mount it) and reopens the window.

**Quitting** (⌘Q, or Quit in the menu bar menu or the window) is different. It uploads what's left, unmounts each volume and then quits. If an app is still using a volume, you can retry or force it, and **Quit now** stops waiting without losing anything: what wasn't uploaded yet goes up at the next mount.

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

Give modalfs a link, and a Modal Function in your workspace downloads it straight into a volume. Your computer can sleep, go offline or be turned off in the meantime. The files appear in the folder you picked once they're complete.

- **Off until you turn it on**, on the Remote downloads page or in Settings, because it's the only part of modalfs that runs compute. A Function starts only for a download you add or resume, and stops when the download finishes, pauses or stalls.
- **Cost.** The smallest size (0.125 CPU, 512 MiB) costs about $0.01 an hour while it downloads, plus Modal's network charges. Watching a download live adds about 2% to the bytes transferred. The first download, and the first after modalfs updates, deploys the Function from a short-lived Sandbox: about 10 seconds and $0.0001. You don't need Python.
- **Downloaders.** gopeed, aria2 and yt-dlp, always the latest version. modalfs picks one for each link, or you choose. If gopeed keeps failing on a server, aria2 takes over and starts that file again.
- **Modal's terms.** Download only what you have the right to. A torrent counts as peer-to-peer file sharing, which [Modal's terms](https://modal.com/legal/terms) (§1.5) forbid, so torrent downloads can get your account suspended. See [known issues](docs/known-issues.md#bittorrent-uploads-while-it-downloads).
- **Interruptions.** Modal sometimes stops a container to move work around. The download's progress is saved on the volume, so it continues on a new container where it left off, usually within a minute. A Function runs for at most 24 hours, and a longer download moves to a new one by itself. The download's page shows each of these.
- **Live speed**, four times a second, while you watch a download in the app. Its status, events and speed history are also saved on the volume, in `.modalfs/downloads/`.
- **Cloudflare WARP.** A download can go through Cloudflare WARP, for sites that refuse cloud servers. It's slower, and the downloader can reach nothing else: if WARP is down, the download stops rather than going direct, and you can retry it without WARP.
- **No overwriting.** If a finished file's name is taken, the new file gets ` (1)` added.
- **Spending.** The Remote downloads page shows what the workspace and its downloads spent this month. Modal's API doesn't report the credit left or the spend limit, so the page links to Modal's Usage & Billing page for those. When Modal refuses or stops a download, the download's page says why, or says Modal gave no reason.

[Known issues](docs/known-issues.md) lists what remote downloads don't handle well yet.

## Encrypted volumes

A new volume can be encrypted end to end. Its files, file names and label are encrypted on your computer before they leave it, so Modal, and anyone else who can read the workspace, sees only ciphertext. On your computer the folder works as usual.

Turn on **Encrypt end to end** when you create a volume. modalfs then shows the volume's key, a line starting with `mfs1-`. **Save it, for example in your password manager.** The key and the files on Modal are all you need to read them, on any computer, even if modalfs's own files are gone. Without the key nobody can read them, modalfs included. There's no passphrase and no recovery.

- This computer remembers the key (in the macOS keychain, or the secret store or a private file on Linux), so the volume mounts by itself. On another computer it shows as locked: click **Unlock** and paste the key.
- Encryption protects what's on Modal, not what's on this computer. The cache, changes not uploaded yet and conflict copies stay readable here, even after you forget the key. Clear the cache in Settings before you hand the computer to someone else.
- Modal Functions can't read an encrypted volume, and remote downloads can't save into one.
- Modal can still see how many files and folders there are, how they're nested, their sizes and times, and which 8 MiB blocks are identical.
- A file name can be up to 147 bytes (about 49 Chinese characters). Renaming a folder with many files takes one request per file. Only new volumes can be encrypted, and the key can't be changed.

[The format and its trade-offs](docs/design/encryption.md) are written down in full, enough to decrypt a volume without modalfs.

## How it works

<p align="center">
  <img src="docs/diagrams/how-it-works.en.svg" alt="Your computer's apps use an ordinary folder. modalfs serves it over NFS or FUSE and keeps a cache and a journal on disk. It lists and commits files through Modal's Volume API and moves 8 MiB blocks to and from Modal's block store, which your Modal Functions read too.">
</p>

On macOS, modalfs serves the folder to the system's NFS client from `127.0.0.1`; on Linux it uses FUSE. Behind both is one background process with a cache of blocks read from Modal, a journal of changes not on Modal yet, and the hashes of the blocks it has seen.

**A Volume v2 file is a list of 8 MiB blocks**, each named by its SHA-256 hash. To commit a file, modalfs sends Modal the list of hashes. Modal answers with the blocks it doesn't have yet, and modalfs uploads only those. The same block is never stored twice, even across files and volumes. To read, Modal returns a signed URL for each block, for any range of the file. Copying a file copies only its list: a 100 GiB copy took 2.9 s.

So changing part of a file means hashing the changed blocks, keeping the hashes of the rest, and uploading what Modal says is missing. But Modal's API never returns the hashes of blocks already there, so modalfs keeps them three ways:

1. **On this computer.** Every full block modalfs reads is hashed and recorded with the file's path, size and modification time.
2. **In the volume.** For files of 64 MiB or more that it writes, modalfs stores the block list in `/.modalfs/manifests/` inside the volume, so other computers and a fresh cache can edit them right away. A list is used only when the file's size and time still match.
3. **By reading.** For a big file nobody listed yet, such as one a Modal container just wrote, the first partial write reads the blocks it needs once to hash them, without keeping them on disk.

**Large writes upload as they go.** During a long copy, each block uploads once it has stopped changing for half a second, and the file is committed in parts: each part is the start of the file up to the first block not on Modal yet, so committing it needs no upload. A part is always the file as it was, cut at a block boundary, never a mix of old and new. Uploads run 4 to 32 at a time, as many as keep each block to about 5 s on your connection, because one connection moves only a few MiB/s.

**Some of this relies on behavior Modal doesn't document**, measured rather than promised. Uploading ahead depends on three things: a commit with a made-up first block hands out upload URLs but never commits; a block uploaded that way counts for a later commit, of any file in the volume, only with the receipt its upload returned (modalfs uses receipts for 10 minutes, and 15-minute-old ones were seen to work); and Modal refuses bytes that don't match their hash. Tests check these against real Modal every day. If Modal changes, modalfs stops uploading ahead and says so in Activity; files then upload when they're committed, as before, and nothing is lost. Blocks uploaded ahead but never committed (a file deleted first, or a crash) stay in Modal's block store, and Modal doesn't say whether it removes or bills them.

Some other details:

- **Folders.** Modal has no empty folders, so creating one writes a hidden `.modalfs_keep` file in it.
- **Renames** copy the file's block list and remove the old one, so editors that save by renaming work.
- **Conflicts.** Before each commit, modalfs checks the file on Modal again. If someone else changed it while you were editing, your version is saved next to it as `name.conflict-YYYYMMDD-HHMMSS.ext`. You can set a volume to overwrite instead.
- **macOS litter.** `.DS_Store` and `._*` files stay in memory and are never uploaded. You can turn this off for a volume.
- **Accounts.** modalfs keeps its own list of accounts, with the secrets in the keychain. It never follows the Modal CLI's active profile, and reads `~/.modal.toml` only when you import from it. Each mount remembers its account, environment, Volume name and volume ID. If the volume is renamed, modalfs finds it by ID. If it was deleted and a new volume took its name, modalfs refuses to mount it rather than mount a different volume in the same folder.

## Performance

Measured from a Linux arm64 sandbox through an HTTP/1.1 proxy with limited bandwidth. A normal connection does better.

| Operation | Result |
|---|---|
| Cold 4 KiB read at offset 77 GiB of a 100 GiB file | 1.45 s, then 0 ms from cache |
| Sequential read, 512 MiB | 40.9 MiB/s (the sandbox limited each block download to about 7 MB/s) |
| Change 4 KiB in a 100 GiB file | 8 MiB uploaded, committed in about 3.5 s |
| Change 4 KiB in a 50 MB file | 1 block uploaded, 2.0 s |
| Change 5 bytes in a 20 MB file never read before | 2 blocks read to hash, 1 uploaded, 3.7 s |
| Copy or rename a 100 GiB file | 2.9 s, no data transferred |
| `kill -9` during a write, then restart | changes recovered and committed, checksum identical |

## Limitations

- **Volume v2 only.** v1 volumes are listed but can't be mounted.
- **No locking.** Modal has no compare-and-swap, so when several writers change the same file, the last commit wins. modalfs detects this when it can and keeps a conflict copy. If the other version is the same size and modalfs read parts you didn't change while you edited, it can't tell which version those parts came from: rather than make a copy that may mix the two, it keeps the parts you changed aside, as 8 MiB pieces with a note on putting them back together, and Activity says where.
- **8 MiB blocks.** A small random write still uploads a whole 8 MiB block.
- **Metadata.** Modal sets a file's modification time when it's committed. modalfs accepts chmod, chown and utimes but ignores them. There are no symlinks, hard links or extended attributes.
- **Consistency.** Reads show the latest version modalfs knows of. Files and folders are refreshed every 10 s, so changes from other computers show up within that time, and a file changed elsewhere while you read it can switch to the new version midway. Modal containers need `volume.reload()` to see what you wrote.
- **Write-back delay.** NFS doesn't say when a file is closed, so a change reaches Modal about 3 s after the file stops changing, or every 30 s while it keeps changing. After a crash it's recovered. One exception: if the last second of an append was all zero bytes and the app didn't sync the file, those trailing zeros can be lost.
- **Unmounting waits for uploads**, with progress shown. **Unmount now** unmounts at once and finishes uploading in the background, or at the next mount. macOS won't unmount a folder an app has open, and modalfs says which app.
- **Large copies pause on a Mac with little memory.** There, the system's NFS client sends a long copy's writes in 30 s steps by itself (on a 7.5 GB Mac, about 100 MB per step), and the copy and Finder wait in between. See [known issues](docs/known-issues.md#large-copies-pause-on-a-mac-with-little-memory).
- **The background process must keep running while volumes are mounted**, because it serves them. If it dies, the next start cleans up what it left mounted.
- **The menu bar icon is macOS only.** On Linux, run `modalfs ui` to reopen the window.
- **Undocumented API.** modalfs uses the same calls as Modal's SDKs. If Modal changes them, modalfs may stop working until it's updated.
- **It's new.** Please report what breaks.

## Security

- **Token secrets** live in the macOS keychain or the Linux Secret Service. On a Linux machine without one, they go in `secrets.json` with mode `0600`. They're never written to logs.
- **The background process** listens on a Unix socket readable only by you, for the app and the command line, and on a random `127.0.0.1` port for the browser. The link `modalfs ui` opens carries a one-time token that's traded for a cookie. Every request also needs a loopback `Host` header and a custom header, which keeps out other web pages and DNS rebinding.
- **The NFS server** listens on `127.0.0.1` and answers only requests with your user's credentials, so other people on the same Mac can't mount your volumes. [Known issues](docs/known-issues.md) says what that doesn't cover.
- **Deleting** a volume or removing an account asks first. A mounted volume can't be deleted.
- Report vulnerabilities privately, as [SECURITY.md](SECURITY.md) describes, not in public issues.

## Files and settings

| | macOS | Linux |
|---|---|---|
| Settings, accounts, socket, log | `~/Library/Application Support/modalfs/` | `~/.config/modalfs/` |
| Block cache | `~/Library/Caches/modalfs/` | `~/.cache/modalfs/` |
| Mount folder | `~/ModalVolumes/` | `~/ModalVolumes/` |
| Start at login | `~/Library/LaunchAgents/dev.modalfs.daemon.plist` | `~/.config/systemd/user/modalfs.service` |

**Cache size** is all the disk modalfs uses: blocks read from Modal, shared by every volume, plus changes not on Modal yet. Changes come first, and cached blocks make way for them. When changes fill the cache (all of it except a quarter, at most 1 GiB, kept for reading), new writes wait for uploads to make room. A copy larger than the cache still works, at the speed of your upload, and never fills your disk. A long copy uploads as it's written, and each part leaves the disk once it's committed, so a write waits for a block or two, not for the whole file. Still, on macOS such a copy can pause for half a minute or more at a time once writes wait, because the system's NFS client holds them back too, so keep the cache larger than your largest copy. Writes also wait while less than 1 GiB is free on the disk, or fail at once, with an error in Activity, if nothing waiting to upload could free it.

If Modal can't be reached, apps writing to a volume wait. On macOS they give up after a few minutes, as with any network drive that went away. What they already wrote stays on this computer and uploads later, and the app shows that it can't upload and why.

The cache can be any whole number of GB from 1. The default is 20 GB, and 2 GB or more is recommended (about 2 × concurrent readers × (read-ahead + 1) × 8 MiB, plus 256 MiB).

## FAQ

**Does mounting cost Modal compute?**
No. Mounting only makes API calls. Modal bills storage and bandwidth as usual. Only [remote downloads](#remote-downloads), off until you turn them on, run a Function.

**Can a Modal Function use the volume at the same time?**
Yes. Call `volume.reload()` in the container to see changes made through modalfs, and don't have both sides write the same file at once.

**Why NFS on macOS instead of FUSE?**
macFUSE has to be installed and allowed in your Mac's security settings. The built-in NFS client needs nothing. If you already use macFUSE, set a volume's **Connection** to FUSE, under Advanced in its settings.

**Do I have to keep the app open?**
No. Only the background process has to run. Turn on **Start at login** and **Mount at login**, and your volumes are back after every restart.

**Where did my edit go after a crash?**
modalfs replays it at the next start and commits it. Activity shows a "recovered" entry, or a conflict copy if the file changed on Modal in the meantime.

## Command line

Most people won't need it, but the command line can do everything the app does: the app and the command line both talk to the same background process.

### Install

On macOS, the app includes it. To use it from a terminal:

```sh
sudo ln -sf /Applications/modalfs.app/Contents/Resources/bin/modalfs /usr/local/bin/modalfs
```

To get only the command line, download `modalfs-<version>-darwin-arm64.tar.gz` (or `-darwin-amd64`, `-linux-amd64`, `-linux-arm64`) from [Releases](https://github.com/t41372/modalfs/releases):

```sh
tar -xzf modalfs-*-linux-amd64.tar.gz
sudo install modalfs-*-linux-amd64/modalfs /usr/local/bin/modalfs
```

On Linux, FUSE must be available (the `fuse3` package and `/dev/fuse`). `go install github.com/t41372/modalfs/cmd/modalfs@latest` works too, but can't build the app's UI, so `modalfs ui` then shows a page saying so.

### Quick start

1. **Add an account.** Create a token at [modal.com → Settings → API Tokens](https://modal.com/settings/tokens), then:

   ```sh
   modalfs account add                    # asks for the Token ID and Secret
   modalfs account import                 # or copy your Modal CLI profiles
   ```

2. **Mount a volume**, creating it first if you need to:

   ```sh
   modalfs volume create my-data
   modalfs mount my-data                  # ~/ModalVolumes/my-data
   ```

3. **Use it.** Open the folder in Finder or your editor. A change is committed about 3 seconds after the file stops changing.

   ```console
   $ echo hi >> ~/ModalVolumes/my-data/notes.md
   $ modalfs status
   VOLUME   ACCOUNT  STATE    MOUNTPOINT                      PENDING  DOWN/UP
   my-data  work     mounted  /Users/me/ModalVolumes/my-data  0        0 B / 3 B
   ```

4. **Unmount** when you're done. Anything not uploaded yet goes up first.

   ```sh
   modalfs unmount my-data
   ```

modalfs remembers each mount, so `modalfs mount my-data` uses the same account, environment and folder next time. Options you give it then, like `--ro`, change what it remembers.

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
  modalfs account add [--label L] [--token-id ID] [--env E]
  modalfs account import [PROFILE...]
  modalfs account ls
  modalfs account check ACCOUNT               ask Modal whether the token still works
  modalfs account token ACCOUNT               replace an expired token (same workspace)
  modalfs account rename ACCOUNT NEWNAME
  modalfs account env ACCOUNT ENVIRONMENT     the environment new volumes are looked up in
  modalfs account rm ACCOUNT

Remote downloads (off until enabled; each runs in a Modal Function billed to your account)
  modalfs download enable|disable             turn remote downloads on or off
  modalfs download add VOLUME[:/FOLDER] URL...
        [--downloader gopeed|aria2|yt-dlp] [--size small|medium|large] [--cookies FILE]
        [--warp | --no-warp] [--account A] [--env E]
  modalfs download ls
  modalfs download show ID [--follow]         progress, attempts and interruptions, speed
  modalfs download pause ID
  modalfs download resume ID [--warp | --no-warp]
  modalfs download cancel ID [--keep]         stop it and delete the partial files
  modalfs download logs ID                    the downloader's log
  modalfs download rm ID                      forget it (finished files stay)
  modalfs download spend [--account A]        what the workspace and its downloads spent this month

Background process
  modalfs daemon                              run it in the foreground
  modalfs quit [--now]                        upload everything, unmount, stop it;
                                              --now stops at once (what's pending uploads at the next mount)
  modalfs service install|uninstall           start at login (LaunchAgent / systemd user unit)
  modalfs settings [NAME VALUE]               show the settings, or change one: cache-size, read-ahead,
                                              mount-root, transport, download-size, download-stall
  modalfs cache clear                         delete the block cache (changes not uploaded yet are kept)

Without the background process (scripts, debugging)
  modalfs run VOLUME MOUNTPOINT [flags]       mount in the foreground until Ctrl-C
  modalfs serve VOLUME [flags]                NFS server only; prints the mount command
```

`--account` takes a label, an account ID or a workspace name, and can be left out when there's only one account.

For example, a remote download:

```console
$ modalfs download enable
$ modalfs download add datasets:/raw https://example.org/train-2024.tar
$ modalfs download show dl-3f2a --follow
```

### Environment variables

| Variable | Effect |
|---|---|
| `MODALFS_HOME` | Keep settings and cache somewhere else (useful for testing) |
| `MODALFS_TRANSPORT=http1` | Send API calls over HTTP/1.1, for proxies that allow only that |
| `MODALFS_NO_KEYCHAIN=1` | Keep secrets in `secrets.json` instead of the keychain |
| `MODALFS_DEBUG=1` | Log more |

## Development

You need Go 1.26 or later, [Bun](https://bun.sh) for the UI and [just](https://github.com/casey/just). The macOS app also needs the Xcode Command Line Tools (`xcode-select --install`), and the Linux app needs `libgtk-3-dev` and `libwebkit2gtk-4.1-dev`. Run `just` to list every task.

```sh
git clone https://github.com/t41372/modalfs && cd modalfs
just app             # macOS app: dist/modalfs.app, for Apple silicon and Intel
just linux-gui       # Linux app: dist/modalfs-gui
just cli             # command line for macOS and Linux: dist/modalfs-{darwin,linux}-{arm64,amd64}
just build           # command line for this machine: dist/modalfs
just dev-mock        # the UI with hot reload on sample data: no background process, no Modal account
just dev             # the UI with hot reload against your running background process
just lint            # gofmt, go vet, the UI's types and translations
just test-e2e        # real NFS mounts against a fake Modal with a slow network (macOS)
just fake -up 20M    # a fake Modal, to try modalfs without an account
MODALFS_TEST_VOLUME=scratch just test-integration   # CREATES AND DELETES files in that volume
```

On macOS 12 you can build the app with `MIN_MACOS=12.0 just app`, but it isn't tested or supported.

The UI is React with Tailwind and shadcn/ui on Base UI, in the style of [Jezo](https://github.com/t41372/Jezo). `just ui` builds it into `internal/webui/dist`, which the background process and the app embed.

The README's pictures are generated: `cd web && bun run screenshots` takes the screenshots from the UI on the sample data in `web/src/api/mock.ts`, and `bun run diagrams` draws the "How it works" picture. The first screenshot adds a real Finder window, captured from a mount of the fake Modal by `web/scripts/capture-finder.sh`.

| Folder | What's in it |
|---|---|
| `cmd/modalfs` | the command line and the background process |
| `cmd/modalfs-gui` | the desktop app (Wails), which can also run the background process |
| `internal/modalapi` | Modal's API, block uploads and downloads |
| `internal/vfs` | the file system: caches, changes, journal, conflicts |
| `internal/nfsfront`, `internal/fusefront` | the NFS and FUSE servers |
| `internal/daemon` | mounts, the JSON API, start at login |
| `internal/config` | accounts, saved mounts, settings, secrets |
| `web` | the UI (React, Vite); its words are in `web/src/i18n` |
| `packaging` | the icon and the macOS app bundle |
| `docs` | design notes, known issues, the README's pictures |

Contributions are welcome, and testing on real Macs helps most. See [CONTRIBUTING.md](CONTRIBUTING.md), and run `just lint` and, where you can, the integration tests against a scratch volume before you open a pull request.

## License

[Apache License 2.0](LICENSE).

Modal is a trademark of Modal Labs, Inc. This project is not affiliated with Modal Labs.
