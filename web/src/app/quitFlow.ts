// The quit checklist (⌘Q, Quit in the menu bar menu, or Settings → Unmount all and quit): each
// mounted volume uploads what's left, then unmounts, one at a time; then modalfs quits. One at a
// time, so "Keep running" can stop between volumes and so a busy volume can wait for the person
// while the others go on. Uploading first (a sync) and only then unmounting means stopping during
// an upload leaves the volume mounted, as if nothing happened.

import { useSyncExternalStore } from 'react'
import { api } from '@/api/client'
import type { Mount, State } from '@/api/types'
import { RUNNING } from '@/lib/downloads'
import { desktop } from '@/lib/platform'
import { live, nameOf } from '@/lib/mounts'
import { quit as quitWithoutList } from './quit'

export type RowPhase =
  | 'waiting'
  | 'uploading' // committing what isn't on Modal yet
  | 'unmounting'
  | 'done'
  | 'blocked' // apps have files open in it
  | 'failed'
  | 'skipped' // "Quit now": unmounted without waiting; the rest uploads next time
  | 'kept' // stopped by "Keep running": still mounted

export interface Row {
  id: string
  name: string
  phase: RowPhase
  /** What it had to upload when its upload began, and its upload counter then, for the progress ring. */
  total?: number
  up0?: number
  apps?: string[]
  error?: string
}

export type Stage =
  | 'closed'
  | 'running'
  | 'waiting' // nothing left to do but the rows that need the person
  | 'stopping' // "Keep running" while a volume unmounts: stop after it
  | 'stopped' // stopped part way; says what's mounted and what isn't
  | 'quitting' // every volume is unmounted; the daemon is stopping
  | 'quit'

export interface Flow {
  stage: Stage
  rows: Row[]
  /** Remote downloads running on Modal: quitting doesn't stop them. */
  downloads: number
  /** "Quit now" was chosen. */
  now: boolean
}

let flow: Flow = { stage: 'closed', rows: [], downloads: 0, now: false }
let run = 0 // a new checklist (or closing it) makes an older loop stop
const listeners = new Set<() => void>()

function update(f: Partial<Flow>) {
  flow = { ...flow, ...f }
  listeners.forEach((l) => l())
}
function setRow(id: string, r: Partial<Row>) {
  update({ rows: flow.rows.map((x) => (x.id === id ? { ...x, ...r } : x)) })
}

export const useQuitFlow = () =>
  useSyncExternalStore(
    (l) => (listeners.add(l), () => listeners.delete(l)),
    () => flow,
  )

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms))
const ack = () => desktop?.quitAck().catch(() => {})
const allowQuit = () => desktop?.quitting().catch(() => {})

async function mount(id: string): Promise<Mount | undefined> {
  const st = await api<State>('GET', '/state')
  return st.mounts.find((m) => m.id === id)
}

/** Waits until a mount is unmounted (or never was); the folder can be gone while its last uploads finish. */
async function unmounted(id: string, token: number) {
  for (;;) {
    const m = await mount(id).catch(() => undefined)
    if (!m || m.state === 'stopped' || m.state === 'error' || (m.state === 'stopping' && m.unmounted)) return
    if (m.state === 'mounted') throw new Error(m.unmount_error || 'still mounted')
    if (token !== run) return
    await sleep(400)
  }
}

/** Opens the checklist and starts it. Asked again while it's open, it only says so. */
export async function startQuit() {
  void ack()
  if (flow.stage !== 'closed') return
  let st: State
  try {
    st = await api<State>('GET', '/state')
  } catch {
    // No daemon to stop: the window is all there is.
    void allowQuit()
    desktop?.quit()
    return
  }
  const on = st.mounts.filter(live)
  const downloads = (st.downloads ?? []).filter((d) => RUNNING.has(d.state)).length
  if (!on.length) {
    // Nothing to unmount: no list to show.
    void quitWithoutList()
    return
  }
  const pending = (m: Mount) => (m.state === 'stopping' ? -1 : (m.status?.pending_bytes ?? 0))
  const rows = [...on].sort((a, b) => pending(a) - pending(b)).map((m): Row => ({ id: m.id, name: nameOf(m), phase: 'waiting' }))
  const token = ++run
  update({ stage: 'running', rows, downloads, now: false })
  await loop(token)
}

async function loop(token: number) {
  for (const r of flow.rows) {
    if (token !== run || flow.stage === 'stopping' || flow.now) break
    if (r.phase === 'waiting') await unmountOne(r.id, token)
  }
  if (token === run) settle()
}

async function unmountOne(id: string, token: number) {
  try {
    let m = await mount(id)
    // Still mounting: let it finish, so it can be unmounted.
    while (m?.state === 'starting' && token === run) {
      await sleep(400)
      m = await mount(id)
    }
    if (!m || m.state === 'stopped' || m.state === 'error') {
      setRow(id, { phase: 'done' })
      return
    }
    if (m.state === 'mounted') {
      const left = m.status?.pending_bytes ?? 0
      if (left > 0) {
        setRow(id, { phase: 'uploading', total: left, up0: m.status?.bytes_up ?? 0 })
        // A commit that fails here is tried again by the unmount, which reports it.
        await api('POST', `/mounts/${id}/sync`).catch(() => {})
        if (token !== run) return
        if (flow.stage === 'stopping' || flow.now) {
          if (!flow.now) setRow(id, { phase: 'kept' })
          return
        }
      }
      setRow(id, { phase: 'unmounting' })
      await api('POST', `/mounts/${id}/stop?patient=1`)
    } else {
      setRow(id, { phase: 'unmounting' }) // already unmounting: wait for it
    }
    await unmounted(id, token)
    if (token === run && !flow.now) setRow(id, { phase: 'done' })
  } catch (e) {
    if (token !== run || flow.now) return
    const m = await mount(id).catch(() => undefined)
    if (m?.in_use_by?.length) setRow(id, { phase: 'blocked', apps: m.in_use_by })
    else setRow(id, { phase: 'failed', error: m?.unmount_error || (e instanceof Error ? e.message : String(e)) })
  }
}

/** After a pass: quit when every volume is unmounted; otherwise wait for the person. */
function settle() {
  if (flow.stage === 'stopping') {
    update({ stage: 'stopped', rows: flow.rows.map((r) => (r.phase === 'done' || r.phase === 'unmounting' ? r : { ...r, phase: 'kept', apps: undefined, error: undefined })) })
    return
  }
  if (flow.stage !== 'running' && flow.stage !== 'waiting') return
  if (flow.rows.every((r) => r.phase === 'done')) void finish()
  else update({ stage: 'waiting' })
}

/** Every volume is unmounted (or "Quit now"): stop the daemon, then the app. */
async function finish(now = false) {
  const token = run
  update({ stage: 'quitting' })
  await allowQuit()
  await api('POST', now ? '/shutdown?now=1' : '/shutdown').catch(() => {})
  // The daemon is gone when it stops answering.
  for (let i = 0; i < 600 && token === run; i++) {
    try {
      await api('GET', '/ping')
    } catch {
      break
    }
    await sleep(250)
  }
  if (token !== run) return
  update({ stage: 'quit' })
  if (desktop) {
    await sleep(700)
    desktop?.quit()
  }
}

/** Tries a blocked or failed volume again. */
export async function retry(id: string) {
  const token = run
  setRow(id, { phase: 'waiting', apps: undefined, error: undefined })
  update({ stage: 'running' })
  await unmountOne(id, token)
  if (token === run) settle()
}

/** Unmounts a busy volume anyway: apps using it lose the folder (and any unsaved work in it). */
export async function force(id: string) {
  const token = run
  setRow(id, { phase: 'unmounting', apps: undefined, error: undefined })
  update({ stage: 'running' })
  try {
    await api('POST', `/mounts/${id}/stop?force=1`)
    await unmounted(id, token)
    if (token === run) setRow(id, { phase: 'done' })
  } catch (e) {
    if (token === run) setRow(id, { phase: 'failed', error: e instanceof Error ? e.message : String(e) })
  }
  if (token === run) settle()
}

/** Quits without waiting for uploads: what isn't on Modal yet stays saved here and uploads the next time each volume is mounted. */
export function quitNow() {
  update({ now: true, rows: flow.rows.map((r) => (r.phase === 'done' ? r : { ...r, phase: 'skipped', apps: undefined, error: undefined })) })
  void finish(true)
}

/**
 * "Keep running". Nothing unmounted yet: the checklist just closes. A volume unmounting: it
 * finishes, then the list stops and says what's still mounted.
 */
export function keepRunning() {
  if (flow.stage === 'stopped') {
    close()
    return
  }
  const unmounting = flow.rows.some((r) => r.phase === 'unmounting')
  const anyDone = flow.rows.some((r) => r.phase === 'done')
  if (unmounting) {
    update({ stage: 'stopping' })
    return
  }
  if (!anyDone) {
    close()
    return
  }
  run++
  update({ stage: 'stopping' })
  settle()
}

export function close() {
  run++
  update({ stage: 'closed', rows: [], downloads: 0, now: false })
}

export const flowActive = (f: Flow) => f.stage !== 'closed'

/** Whether the checklist is showing, outside React (its notifications would only repeat it). */
export const checklistOpen = () => flow.stage !== 'closed'
