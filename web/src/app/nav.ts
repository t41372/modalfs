// Where the window is: a section of the rail, and maybe one thing in it (a
// volume, a download). It lives in the address (?page=…&id=…), so Back and
// Forward work, and a click on the rail always goes to the section itself.

import { useSyncExternalStore } from 'react'

export const SECTIONS = ['volumes', 'downloads', 'activity', 'settings'] as const
export type Section = (typeof SECTIONS)[number]
export interface Route {
  section: Section
  /** A volume's mount ID, or a download's ID. */
  id?: string
}

const KEY = 'modalfs-page'
// Old addresses (and the screenshots of old READMEs) named the pages differently.
const ALIASES: Record<string, Section> = { mounts: 'volumes', accounts: 'settings' }

function parse(search: string): Route | null {
  const q = new URLSearchParams(search)
  const raw = q.get('page') ?? ''
  const section = (SECTIONS as readonly string[]).includes(raw) ? (raw as Section) : ALIASES[raw]
  if (!section) return null
  const id = q.get('id') || undefined
  return { section, id: section === 'volumes' || section === 'downloads' ? id : undefined }
}

function saved(): Route {
  try {
    const s = localStorage.getItem(KEY)
    if (s && (SECTIONS as readonly string[]).includes(s)) return { section: s as Section }
  } catch {
    // not remembered; that's fine
  }
  return { section: 'volumes' }
}

let route: Route = parse(location.search) ?? saved()
const listeners = new Set<() => void>()
const set = (r: Route) => {
  route = r
  try {
    localStorage.setItem(KEY, r.section)
  } catch {
    // not remembered; that's fine
  }
  listeners.forEach((l) => l())
}

function href(r: Route) {
  const q = new URLSearchParams(location.search)
  q.set('page', r.section)
  if (r.id) q.set('id', r.id)
  else q.delete('id')
  return `${location.pathname}?${q}${location.hash}`
}

addEventListener('popstate', () => set(parse(location.search) ?? saved()))

/** Goes to a section, or to one thing in it. */
export function navigate(section: Section, id?: string) {
  const next = { section, id }
  if (next.section === route.section && next.id === route.id) return
  history.pushState(null, '', href(next))
  set(next)
}

export const useRoute = () =>
  useSyncExternalStore(
    (l) => (listeners.add(l), () => listeners.delete(l)),
    () => route,
  )
