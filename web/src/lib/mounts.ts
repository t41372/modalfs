// What a mount's state means to the person using it: one word and a tone for
// the icon, and the summary of every mount the rail shows.

import type { Mount } from '@/api/types'

export type Tone = 'ok' | 'up' | 'busy' | 'bad' | 'off'

/** What a volume is called here: its label, or else its name on Modal. */
export const nameOf = (m: Pick<Mount, 'label' | 'volume_name'>) => m.label || m.volume_name

export const live = (m: Mount) => m.state === 'mounted' || m.state === 'starting' || m.state === 'stopping'

/** The one thing to know about a mount, in order of what matters most. */
export type Look =
  | 'error' // couldn't mount
  | 'conflict' // mounted, with conflicts to look at
  | 'stalled' // mounted, with changes Modal isn't taking now (offline, a rejected token)
  | 'busy' // mounted, but an unmount failed because apps use it
  | 'starting'
  | 'stopping'
  | 'uploading' // mounted, with changes not on Modal yet
  | 'synced' // mounted, everything on Modal
  | 'locked' // encrypted, and this computer doesn't have the key
  | 'off' // not mounted

export function look(m: Mount): Look {
  if (m.state === 'starting') return 'starting'
  if (m.state === 'stopping') return 'stopping'
  if (m.locked && m.state !== 'mounted') return 'locked'
  if (m.state === 'error') return 'error'
  if (m.state !== 'mounted') return 'off'
  if ((m.open_conflicts ?? 0) > 0) return 'conflict'
  if (m.status?.upload_error && (m.status.pending_files ?? 0) > 0) return 'stalled'
  if (m.unmount_error) return 'busy'
  if ((m.status?.pending_files ?? 0) > 0) return 'uploading'
  return 'synced'
}

export const toneOf: Record<Look, Tone> = {
  error: 'bad',
  conflict: 'bad',
  stalled: 'bad',
  busy: 'up',
  starting: 'busy',
  stopping: 'up',
  uploading: 'up',
  synced: 'ok',
  locked: 'off',
  off: 'off',
}

/** Everything mounted, for the rail: is it all on Modal? */
export function summary(mounts: Mount[]) {
  const on = mounts.filter(live)
  const pending = on.reduce((n, m) => n + (m.status?.pending_bytes ?? 0), 0)
  const files = on.reduce((n, m) => n + (m.status?.pending_files ?? 0), 0)
  const problems = mounts.filter((m) => ['error', 'conflict', 'stalled'].includes(look(m))).length
  const moving = on.some((m) => m.state === 'starting' || m.state === 'stopping')
  const tone: Tone = problems ? 'bad' : files > 0 || moving ? 'up' : on.length ? 'ok' : 'off'
  // What's moving between this computer and Modal: now, and since each was mounted.
  const sum = (f: (m: Mount) => number | undefined) => on.reduce((n, m) => n + (f(m) ?? 0), 0)
  const traffic = { up: sum((m) => m.up_speed), down: sum((m) => m.down_speed), sent: sum((m) => m.status?.bytes_up), read: sum((m) => m.status?.bytes_down) }
  return { on: on.length, pending, files, problems, tone, traffic }
}
