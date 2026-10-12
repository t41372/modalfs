import { useSyncExternalStore } from 'react'

export const PAGES = ['mounts', 'volumes', 'downloads', 'accounts', 'activity', 'settings'] as const
export type PageId = (typeof PAGES)[number]

const KEY = 'modalfs-page'
const fromUrl = new URLSearchParams(location.search).get('page')
let page: PageId = (PAGES as readonly string[]).includes(fromUrl ?? '')
  ? (fromUrl as PageId)
  : (() => {
      try {
        const saved = localStorage.getItem(KEY)
        return (PAGES as readonly string[]).includes(saved ?? '') ? (saved as PageId) : 'mounts'
      } catch {
        return 'mounts'
      }
    })()

const listeners = new Set<() => void>()

export function navigate(next: PageId) {
  if (next === page) return
  page = next
  try {
    localStorage.setItem(KEY, next)
  } catch {
    // not remembered; that's fine
  }
  listeners.forEach((l) => l())
}

export const usePage = () =>
  useSyncExternalStore(
    (l) => (listeners.add(l), () => listeners.delete(l)),
    () => page,
  )
