// Daemon events (Activity, notifications). The state carries the newest event's
// number and the daemon's boot ID; when either moves, the new events are fetched
// and appended here.

import { useEffect, useRef, useSyncExternalStore } from 'react'
import { api } from './client'
import type { DaemonEvent } from './types'

interface Feed {
  events: DaemonEvent[]
  lastSeq: number
  bootId: string
  /** Conflicts and errors that arrived while Activity wasn't open. */
  unseen: number
  loaded: boolean
  /** The last fetch failed; it's retried on the next poll. */
  failed: boolean
}

let feed: Feed = { events: [], lastSeq: 0, bootId: '', unseen: 0, loaded: false, failed: false }
const listeners = new Set<() => void>()
const set = (next: Partial<Feed>) => {
  feed = { ...feed, ...next }
  listeners.forEach((l) => l())
}
const subscribe = (l: () => void) => {
  listeners.add(l)
  return () => listeners.delete(l)
}

export const useFeed = () => useSyncExternalStore(subscribe, () => feed)
export const markSeen = () => feed.unseen && set({ unseen: 0 })

const isProblem = (e: DaemonEvent) => e.kind === 'conflict' || e.kind === 'error'

let fetching = false

/**
 * Keeps the feed in step with the daemon. `onNew` gets events that arrived
 * after the window opened, for notifications; after a daemon restart, only the
 * new daemon's problems (an auto-mount that failed, say) count as new.
 *
 * Called with every poll, so a fetch that failed or was skipped while another
 * ran is simply tried again on the next one.
 */
export function useEventSync(
  state: { last_seq: number; boot_id?: string } | undefined,
  polledAt: number,
  onNew: (e: DaemonEvent[]) => void,
  activityOpen: boolean,
) {
  const latest = useRef({ onNew, activityOpen })
  latest.current = { onNew, activityOpen }
  useEffect(() => {
    if (!state || fetching) return
    const bootId = state.boot_id ?? ''
    const restarted = feed.loaded && bootId !== feed.bootId
    if (!restarted && feed.loaded && !feed.failed && state.last_seq <= feed.lastSeq) return
    fetching = true
    const after = restarted || !feed.loaded ? 0 : feed.lastSeq
    api<DaemonEvent[]>('GET', `/events?after=${after}`)
      .then((ev) => {
        const fresh = !feed.loaded ? [] : restarted ? ev.filter(isProblem) : ev
        const { onNew, activityOpen } = latest.current
        const unseen = activityOpen ? 0 : fresh.filter(isProblem).length
        set({
          events: (restarted ? ev : feed.events.concat(ev)).slice(-500),
          lastSeq: Math.max(state.last_seq, ev.at(-1)?.seq ?? 0),
          bootId,
          unseen: feed.unseen + unseen,
          loaded: true,
          failed: false,
        })
        if (fresh.length) onNew(fresh)
      })
      .catch(() => set({ failed: true }))
      .finally(() => {
        fetching = false
      })
    // polledAt changes with every successful poll, even when the state itself didn't change.
  }, [state, polledAt])
}
