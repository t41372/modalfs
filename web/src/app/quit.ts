// Quitting from this window: the daemon unmounts everything once its uploads are done, and the window
// shows the progress until the daemon is gone. The desktop app then quits by itself.

import { useSyncExternalStore } from 'react'
import { api } from '@/api/client'
import { desktop } from '@/lib/platform'

export type QuitPhase = 'idle' | 'quitting' | 'quit'

let phase: QuitPhase = 'idle'
const listeners = new Set<() => void>()
const set = (p: QuitPhase) => {
  phase = p
  listeners.forEach((l) => l())
}

export const useQuitPhase = () =>
  useSyncExternalStore(
    (l) => (listeners.add(l), () => listeners.delete(l)),
    () => phase,
  )

/** Starts quitting. `now` doesn't wait for uploads; it also cuts short a quit that's already waiting. */
export async function quit(now = false) {
  if (phase === 'idle') {
    set('quitting')
    // First, so the desktop app won't start a new daemon when a poll finds this one gone, and quits once it is.
    await desktop?.quitting().catch(() => {})
  }
  await api('POST', now ? '/shutdown?now=1' : '/shutdown').catch(() => {})
}

/** The daemon stopped answering while quitting: it's done. */
export function quitDone() {
  if (phase === 'quitting') set('quit')
}
