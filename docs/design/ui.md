# The UI: what each screen shows, and where it comes from

The October 2026 redesign started from a Claude Design canvas (a skeleton: layout, hierarchy, where actions go) and the UX audit of the old UI. The canvas didn't know what modalfs can actually tell, so every number on it was checked against the daemon and Modal's API. This page records the result: what each screen answers, where each value comes from, and what was added, replaced or dropped.

## Rules

- **Only the volumes modalfs manages are listed.** A workspace can hold hundreds of volumes from other projects; one is added on demand from a search dialog.
- **What a screen must answer is visible at a glance**, as a number, a chip, a bar or an icon, not a paragraph. Secondary detail is in a tooltip or a "⋯" menu.
- **Actions are buttons; preferences are switches.** Mounting is a button, never a switch.
- **Destructive actions are apart from the rest, named by what they do, and never the default.** A dismiss button never repeats a word of the action ("Keep", not "Cancel").
- **The remote downloads page is "Remote downloads" (遠程下載, 远程下载)**, so it can't be mistaken for downloading a file from a volume.

## What Modal's API can tell

Modal's public API (the Go SDK v0.11.0 and the Python client 1.5.5) has **no volume size, quota or per-volume billing**. `VolumeList` gives a volume's name, ID, creation time and creator, and its version. `VolumeListFiles2` with `recursive` lists every file with its size and modification time. Modal's web dashboard shows sizes, presumably from an internal endpoint the clients don't have.

Billing has this month's spend (before and after credits) and the live rates, but not the credit left or the spend limit (`internal/daemon/billing.go`).

## Values, screen by screen

| On the canvas | Where it comes from | Decision |
| --- | --- | --- |
| A volume's state (mounted, uploading, conflict, not mounted, error) | `state` and `status` of each mount in `/api/state` | kept |
| Mount path | the mount's `mountpoint` | kept |
| Bytes waiting to upload | `status.pending_bytes` | kept |
| Volume size, and the bar comparing volumes | one recursive listing, run when a volume's page opens (or on Refresh), one at a time, cancellable, with a file count while it runs; the result is kept in `usage.json` with its time | **added** (`/api/mounts/{id}/usage`). The home list shows only a size measured before and never starts a listing; a volume never measured shows none |
| "Total 412 GB" chip on the home page | sum of the measured sizes | shown only when every listed volume has been measured |
| Account chip | the mount's account | kept |
| "Last used 3 days ago" | nothing recorded it | **added**: a mount records when it was last unmounted (`last_used`) |
| Volume ID, version, creation date and creator | `VolumeList` | kept, in a tooltip |
| "Cache on this Mac" per volume | the block cache is shared by every volume and keyed by content, so a block can't be attributed to one volume | **replaced** by what this mount uploaded and read since it was mounted (`bytes_up`, `bytes_down`); the cache's total stays in Settings |
| Upload/read curves for the last hour | the daemon only had running totals | **added**: a sample of each mount's totals every 10 s for an hour, in memory (`/api/mounts/{id}/traffic`) |
| Space by folder | the same listing as the size | **added**, top folders only |
| Running downloads, total speed | the downloads with their live status | kept |
| Cost per hour now, this month | the workspace's live rates × running downloads; this month's download spend from the billing report | kept, the month in a tooltip |
| Time a moving download takes ("about 30 s") | not known in advance | **dropped**: the row says it's moving, with the bytes it kept |
| Cost of one download so far | its run time (from its events) × the rate for its size | **added** on the download's page, marked as an estimate |
| A green tick for a valid token | a token is only known to work when it's checked | **replaced** by a Check action in the account's menu, with the result shown |

## Added from the project (the canvas didn't have them)

- **Mounts:** the read-only and litter-filter options, the NFS/FUSE choice (under Advanced), the starting steps, the auto-mount retry countdown, upload progress while unmounting, the apps that keep a volume from unmounting with Try again and Force, and "Use the new volume" after a volume was recreated under the same name.
- **Conflicts:** a list per volume with both versions shown in Finder, and Mark as handled (`/api/mounts/{id}/conflicts`).
- **Pending uploads:** the files still waiting, with their sizes (`/api/mounts/{id}/pending`).
- **Accounts:** check the token, replace it (same workspace only, so mounts can't drift), rename, and the default environment; also in the CLI (`modalfs account check|token|rename|env`).
- **Remote downloads:** the events timeline, the log, the aria2 fallback, the 24-hour hand-over, stall stops, Modal's billing refusals and silent containers.
- **The app:** the daemon not running or reconnecting, the quit flow, the error page, the theme (System, Light, Dark; the rail no longer has a toggle that drops System), the language, the cache size and Clear, read-ahead, the connection protocol, start at login, the default mount location.
