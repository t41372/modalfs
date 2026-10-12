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
}

export interface Settings {
  cache_dir?: string
  cache_size_gb?: number
  read_ahead?: number
  transport?: string
  language?: Language
  appearance?: Appearance
  mount_root?: string
}

export interface State {
  version: string
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
