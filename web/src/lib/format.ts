import type { TFunction } from 'i18next'
import type { DaemonEvent } from '@/api/types'
import { intlTag } from '@/i18n'

export function bytes(n: number | undefined) {
  if (!n) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024
    i++
  }
  return `${n >= 100 || i === 0 ? n.toFixed(0) : n.toFixed(1).replace(/\.0$/, '')} ${units[i]}`
}

/** "2 minutes ago", "yesterday", in the UI language; `justNow` for the last 45 seconds. */
export function ago(ts: number | undefined, lang: string, justNow: string) {
  if (!ts) return ''
  const s = Date.now() / 1000 - ts
  const rtf = new Intl.RelativeTimeFormat(intlTag(lang), { numeric: 'auto' })
  if (s < 45) return justNow
  if (s < 3600) return rtf.format(-Math.round(s / 60), 'minute')
  if (s < 86400) return rtf.format(-Math.round(s / 3600), 'hour')
  return rtf.format(-Math.round(s / 86400), 'day')
}

export const date = (ts: number | undefined, lang: string) =>
  ts ? new Date(ts * 1000).toLocaleDateString(intlTag(lang), { year: 'numeric', month: 'short', day: 'numeric' }) : ''

export const clock = (ts: number, lang: string) =>
  new Date(ts * 1000).toLocaleTimeString(intlTag(lang), { hour: '2-digit', minute: '2-digit', hour12: false })

/** The home folder, read off the mount root (which defaults to ~/ModalVolumes). */
export const homeOf = (mountRoot: string | undefined) => (mountRoot || '').replace(/\/ModalVolumes\/?$/, '')

/** A path with the home folder shown as ~. */
export function tilde(path: string | undefined, home: string) {
  if (!path) return ''
  return home && (path === home || path.startsWith(home + '/')) ? '~' + path.slice(home.length) : path
}

/** ~/x → /Users/me/x, for paths typed by the user. */
export const untilde = (path: string, home: string) => (path.startsWith('~/') && home ? home + path.slice(1) : path)

/** An event in the UI language: its code's string with its arguments, or the daemon's English message for codes we don't know. */
export function eventText(e: DaemonEvent, t: TFunction, home: string) {
  if (!e.code) return e.message
  const key = `ev.${e.code}` as const
  const args = Object.fromEntries((e.args ?? []).map((a, i) => [`a${i}`, tilde(a, home)]))
  const s = t(key as 'ev.commit', args)
  return s === key ? e.message : s
}
