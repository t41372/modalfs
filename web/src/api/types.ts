// The daemon's JSON API (internal/daemon/api.go). Field names are the Go json tags.

export type MountState = 'stopped' | 'starting' | 'mounted' | 'stopping' | 'error'
export type MountMode = 'auto' | 'nfs' | 'fuse'
export type ConflictPolicy = 'keep-both' | 'overwrite'
export type Language = 'system' | 'zh-Hant' | 'zh-Hans' | 'en'
export type Appearance = 'system' | 'light' | 'dark'

export interface Account {
  id: string
  label: string
  token_id: string
  server_url?: string
  environment?: string
  workspace?: string
  username?: string
  imported_from?: string
  secret_store: 'keychain' | 'file' | 'env'
  added_at: number
}

export interface MountStatus {
  pending_files: number
  pending_bytes: number
  bytes_down: number
  bytes_up: number
  commits: number
  conflicts: number
  errors: number
  last_error?: string
  last_error_at?: number
  last_commit_at?: number
  local_only_files: number
}

export interface Mount {
  id: string
  account_id: string
  environment?: string
  volume_name: string
  volume_id?: string
  /** The volume's label, kept in the volume itself; shown instead of its name. */
  label?: string
  mountpoint: string
  mode?: MountMode
  read_only?: boolean
  auto_mount?: boolean
  filter_junk: boolean
  write_back_ms?: number
  on_conflict?: ConflictPolicy
  // from the daemon, not saved
  account_label: string
  workspace: string
  state: MountState
  error?: string
  active_mode?: string
  started_at?: number
  /** While mounted or stopping. */
  status?: MountStatus
  /** Starting: what it's doing now. */
  step?: 'connect' | 'open' | 'mount'
  /** Error: an auto-mount tries again at this time (Unix seconds). */
  retry_at?: number
  /** Stopping: when it began, and the upload counter and pending bytes then. */
  stopping_since?: number
  stop_bytes_up?: number
  stop_pending_bytes?: number
  /** Stopping: the folder is unmounted already; the uploads finish in the background. */
  unmounted?: boolean
  /** Mounted: why the last unmount failed, and the apps that had files open in it. */
  unmount_error?: string
  in_use_by?: string[]
  /** When it was last unmounted (Unix seconds). */
  last_used?: number
  /** Conflicts not marked handled yet. */
  open_conflicts?: number
  /** The volume's size from its last listing, and when that was. */
  size_bytes?: number
  size_at?: number
  /** End-to-end encrypted (docs/design/encryption.md): Modal sees only ciphertext. */
  encrypted?: boolean
  /** Encrypted, and this computer doesn't have its key: it can't mount until unlocked. */
  locked?: boolean
  /** Unlocked, and the key is kept on this computer (keychain or a private file) rather than until modalfs quits. */
  key_remembered?: boolean
}

/** A file that changed on Modal while it was edited here; the local version was saved as `copy`. */
export interface Conflict {
  path: string
  copy: string
  at: number
}

/** A file with changes that aren't on Modal yet. */
export interface PendingFile {
  path: string
  /** Changed data, in whole 8 MiB blocks. */
  bytes: number
  size: number
  since?: number
}

/** A mount's running totals at a moment: [Unix seconds, bytes up, bytes down], every 10 s for an hour. */
export type TrafficSample = [number, number, number]

/** A volume's size, from listing every file in it (Modal's API doesn't tell it). */
export interface Usage {
  total: number
  files: number
  /** The largest top-level folders, biggest first. */
  folders: { name: string; bytes: number; files: number }[]
  at: number
}

export interface UsageView {
  last?: Usage
  running: boolean
  counted?: number
  counted_files?: number
  started?: number
  error?: string
}

export interface Settings {
  cache_dir?: string
  cache_size_gb?: number
  read_ahead?: number
  transport?: string
  language?: Language
  appearance?: Appearance
  mount_root?: string
  /** Remote downloads start Modal Functions, billed to the account: off until turned on. */
  remote_downloads?: boolean
  download_size?: DownloadSize
  /** Stop a download that has received nothing for this long (10 when unset). */
  download_stall_minutes?: number
}

export interface State {
  version: string
  /** The commit the daemon was built from, "-dirty" when the tree had changes. */
  build?: string
  platform: string
  accounts: Account[]
  mounts: Mount[]
  settings: Settings
  cache: { blocks: number; bytes: number; max: number }
  service: {
    supported: boolean
    installed: boolean
    path?: string
    /** The program start at login runs, and whether it's gone. */
    binary?: string
    binary_missing?: boolean
  }
  last_seq: number
  /** Random per daemon process: a new one means the daemon restarted and event numbers began again. */
  boot_id?: string
  config_dir: string
  /** modalfs is quitting: unmounting everything. */
  quitting?: boolean
  downloads?: Download[]
}

export type EventKind = 'mounted' | 'unmounted' | 'commit' | 'conflict' | 'error' | 'recovered' | 'info'

export interface DaemonEvent {
  seq: number
  at: number
  kind: EventKind
  mount_id?: string
  volume?: string
  message: string
  code?: string
  args?: string[]
}

export interface Environment {
  name: string
  default: boolean
}

export interface Volume {
  id: string
  name: string
  created_at?: number
  created_by?: string
  format: string // v1 | v2
  mounted: boolean
  mount_id?: string
  /** Only when modalfs already knows it: labels aren't read for every volume. */
  label?: string
  /** Only when modalfs already knows it, like the label. */
  encrypted?: boolean
}

export interface Profile {
  name: string
  token_id: string
  active: boolean
  imported: boolean
}

export interface Identity {
  token_id: string
  workspace_id: string
  workspace_name: string
  username?: string
}

// Remote downloads (internal/daemon/downloads.go, internal/downloads).

export type DownloadTool = 'gopeed' | 'aria2' | 'yt-dlp'
export type DownloadSize = 'small' | 'medium' | 'large'

/**
 * The daemon's state for a download. While a run is going, what its agent last reported:
 * starting, running, retrying, pausing, finishing, interrupted (Modal stopped its container;
 * it continues on a new one). It ends done, paused, stalled, failed, time limit or cancelled.
 */
export type DownloadState =
  | 'preparing'
  | 'starting'
  | 'running'
  | 'retrying'
  | 'pausing'
  | 'finishing'
  | 'interrupted'
  | 'cancelling'
  | 'done'
  | 'paused'
  | 'stalled'
  | 'failed'
  | 'time limit'
  | 'cancelled'

export interface DownloadStatus {
  state: string
  tool: string
  url: string
  name?: string
  note?: string
  error?: string
  done: number
  /** -1 when unknown. */
  total: number
  /** Bytes per second, over the last 2 s. */
  speed: number
  eta?: number
  attempt?: number
  files?: { path: string; size: number; sha256?: string }[]
  log_tail?: string[]
  /** Where it leaves Cloudflare's network, when it goes through Cloudflare WARP: the data center and the address sites see. */
  warp?: { colo?: string; ip?: string; loc?: string }
  /** When the agent wrote it (Unix seconds, the container's clock). */
  t: number
}

export interface Download {
  id: string
  account_id: string
  account_label: string
  environment?: string
  volume_id: string
  volume_name: string
  folder: string
  /** The link without credentials or query string. */
  url: string
  name: string
  tool: DownloadTool
  size: DownloadSize
  created: number
  state: DownloadState
  error?: string
  /** Why it failed, when it's not the link: Modal said the workspace is out of money ('billing'), Modal stopped the work without a reason ('unexplained'), or Cloudflare WARP couldn't be used ('warp'; nothing was downloaded directly instead). */
  error_kind?: 'billing' | 'unexplained' | 'warp'
  /** Seconds a download that should be running has gone without a word from its container. */
  silent?: number
  cookies?: boolean
  /** Through Cloudflare WARP: the downloader can reach nothing else. */
  warp?: boolean
  status?: DownloadStatus
}

/** An account's spending this month. Modal's API doesn't tell the credit left or the spend limit. */
export interface Billing {
  period_start: number
  period_end: number
  /** This month's cost before credits and free allowances, in US dollars. */
  metered: number
  /** What Modal charges after them. */
  billed: number
  adjustments?: Record<string, number>
  /** What remote downloads cost this month in the environment (Modal's report lags an hour or so). */
  downloads?: number
  downloads_error?: string
  /** What a download costs an hour while it runs, per size, at the workspace's rates. */
  hourly?: Partial<Record<DownloadSize, number>>
  /** Modal's Usage & Billing page, where the spend limit and the credit are. */
  usage_url: string
  at: number
}

/** A line of the download's events.jsonl: what happened to it, and when. */
export interface DownloadEvent {
  ev: 'start' | 'interrupted' | 'resumed' | 'signal' | 'error' | 'fallback' | 'stalled' | 'paused' | 'continued' | 'failed' | 'finished' | 'warp'
  t: number
  [field: string]: unknown
}

/** One point of the speed history: time (Unix seconds), bytes done, bytes per second. */
export type Sample = [number, number, number]

export interface DownloadDetail {
  download: Download
  events: DownloadEvent[]
  history: Sample[]
  records_error?: string
}

export interface LiveView {
  /** Connected to the download's container. */
  live: boolean
  status?: DownloadStatus
  samples: Sample[]
  error?: string
}
