// What the Downloads page works out from a download's records: its speed
// over the whole run, what interrupted it, and what it costs.

import type { TFunction } from 'i18next'
import type { Billing, Download, DownloadEvent, DownloadSize, Sample } from '@/api/types'
import { intlTag } from '@/i18n'
import { duration as span } from '@/components/StopProgress'
import { bytes } from './format'

/** Roughly what a download's Function costs an hour while it runs, in US dollars (Modal's rates for a Function's cores and memory). */
export const HOURLY: Record<DownloadSize, number> = { small: 0.0099, medium: 0.0198, large: 0.063 }

/** What a size costs an hour at the workspace's own rates when they're known, else Modal's list price. */
export const hourlyFor = (size: DownloadSize, b?: Billing) => b?.hourly?.[size] ?? HOURLY[size]

/** A download that should be running has gone this long (seconds) without a word: longer than Modal takes to move a container. */
export const SILENT_WARN = 180

/** Dollars, with cents for bigger sums and more places for the small ones a download costs. */
export const dollars = (n: number) => `$${n >= 1 || n === 0 ? n.toFixed(2) : n.toFixed(4)}`

/** States in which a run is going, so it can be paused. */
export const RUNNING = new Set(['starting', 'running', 'retrying', 'finishing', 'interrupted'])
/** States in which it's stopping, or about to start: nothing to do but wait. */
export const BUSY = new Set(['preparing', 'pausing', 'cancelling'])
/** States it can be resumed from. */
export const RESUMABLE = new Set(['paused', 'stalled', 'failed', 'time limit'])
/** States in which no run is going. */
export const ENDED = new Set(['done', 'paused', 'stalled', 'failed', 'time limit', 'cancelled'])

export const speed = (n: number) => `${bytes(n)}/s`

/** "1.2 GB of 4.7 GB", or just "1.2 GB" while the size is unknown. */
export const progressText = (d: Download, t: TFunction) => {
  const s = d.status
  if (!s) return ''
  return s.total > 0 ? t('downloads.of', { done: bytes(s.done), total: bytes(s.total) }) : bytes(s.done)
}

/** A time of day, with the date when it isn't today. */
export function when(ts: number, lang: string) {
  const d = new Date(ts * 1000)
  const today = new Date().toDateString() === d.toDateString()
  return d.toLocaleString(intlTag(lang), today ? { hour: '2-digit', minute: '2-digit', hour12: false } : { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', hour12: false })
}

// ─── The speed history ───

/** A gap between samples longer than this is time when nothing ran. */
const GAP = 5

/**
 * The whole run's samples: the durable history (one a second, from the volume)
 * up to where the live feed (4 a second, from the container) begins. Where no
 * run was going, the speed is drawn as zero.
 */
export function series(history: Sample[], live: Sample[]): { t: number; v: number }[] {
  const from = live.length ? live[0][0] : Infinity
  const rows = [...history.filter((s) => s[0] < from), ...live]
  const out: { t: number; v: number }[] = []
  let last = -Infinity
  for (const [t, , v] of rows) {
    if (t <= last) continue
    if (t - last > GAP && out.length) {
      out.push({ t: last + 0.5, v: 0 }, { t: t - 0.5, v: 0 })
    }
    out.push({ t, v })
    last = t
  }
  return out
}

/** Window lengths the whole-run view snaps to, so it changes rarely as a download goes on. */
const SPANS = [60, 90, 120, 180, 300, 450, 600, 900, 1200, 1800, 2700, 3600, 5400, 7200, 3 * 3600, 4 * 3600, 6 * 3600, 9 * 3600, 12 * 3600, 18 * 3600, 24 * 3600, 30 * 3600, 36 * 3600, 48 * 3600, 72 * 3600, 5 * 86400, 7 * 86400, 14 * 86400, 30 * 86400]

export const wholeRun = (span: number) => SPANS.find((s) => s >= span * 1.04) ?? span * 1.04

/**
 * At most about `buckets` points across a window: the average speed in each
 * slice of time. The slices are fixed in time, so new samples don't make the
 * older part of the line shimmer.
 */
export function downsample(points: { t: number; v: number }[], window: number, buckets = 600) {
  const size = window / buckets
  if (size <= 0.25) return points
  const out: { t: number; v: number }[] = []
  let key = NaN
  let sumT = 0
  let sumV = 0
  let n = 0
  const flush = () => n && out.push({ t: sumT / n, v: sumV / n })
  for (const p of points) {
    const k = Math.floor(p.t / size)
    if (k !== key) {
      flush()
      key = k
      sumT = sumV = n = 0
    }
    sumT += p.t
    sumV += p.v
    n++
  }
  flush()
  return out
}

/**
 * The slope of the line around time t, in bytes per second per second: a
 * least-squares fit to the points within `half` seconds either side, so it's
 * the trend at the cursor rather than the step between two samples.
 */
export function slopeAt(points: { time: number; value: number }[], t: number, half: number) {
  let n = 0
  let st = 0
  let sv = 0
  let stt = 0
  let stv = 0
  for (const p of points) {
    if (p.time < t - half || p.time > t + half) continue
    const x = p.time - t
    n++
    st += x
    sv += p.value
    stt += x * x
    stv += x * p.value
  }
  const d = n * stt - st * st
  return n >= 3 && d > 0 ? (n * stv - st * sv) / d : 0
}

/** The value range Liveline draws for these values (src/math/range.ts in liveline 0.0.7), so a tangent can be drawn to its scale. */
export function chartRange(values: number[]) {
  let min = Infinity
  let max = -Infinity
  for (const v of values) {
    if (v < min) min = v
    if (v > max) max = v
  }
  const raw = max - min
  const minRange = raw * 0.1 || 0.4
  if (raw < minRange) {
    const mid = (min + max) / 2
    return { min: mid - minRange / 2, max: mid + minRange / 2 }
  }
  return { min: min - raw * 0.12, max: max + raw * 0.12 }
}

// ─── What happened ───

export type Tone = 'neutral' | 'ok' | 'warn' | 'bad'

export interface Happening {
  t: number
  tone: Tone
  text: string
  /** A time when no run was going: [from, to], to absent while it still lasts. */
  gap?: [number, number?]
  /** A short label for the chart. */
  label?: string
}

const num = (v: unknown) => (typeof v === 'number' ? v : 0)
const str = (v: unknown) => (typeof v === 'string' ? v : '')

/**
 * The download's events as plain sentences: each attempt, what ended it
 * (Modal stopping the container, a lost container, a pause, the 24-hour
 * limit) and how long until the next one began.
 */
export function happenings(events: DownloadEvent[], t: TFunction, lang: string): Happening[] {
  const out: Happening[] = []
  const at = (ts: number) => when(ts, lang)
  // What ended the last run, waiting for the next start to say how long the gap was.
  let ending: { kind: 'signal' | 'paused' | 'continued'; t: number; done: number } | null = null
  for (let i = 0; i < events.length; i++) {
    const e = events[i]
    switch (e.ev) {
      case 'start': {
        const attempt = num(e.attempt) || 1
        const next = events[i + 1]
        if (next?.ev === 'interrupted') {
          // The run before ended without a word: its container was lost.
          const lost = num(next.last_status) || e.t
          out.push({
            t: lost,
            tone: 'warn',
            text: t('downloads.ev.lost', { time: at(lost), gap: span(e.t - lost, t), done: bytes(num(next.done)) }),
            gap: [lost, e.t],
            label: t('downloads.mark.lost'),
          })
          i++
        } else if (ending?.kind === 'signal') {
          out.push({
            t: ending.t,
            tone: 'warn',
            text: t('downloads.ev.preempted', { time: at(ending.t), gap: span(e.t - ending.t, t), done: bytes(ending.done) }),
            gap: [ending.t, e.t],
            label: t('downloads.mark.preempted'),
          })
        } else if (ending?.kind === 'paused') {
          out.push({
            t: ending.t,
            tone: 'neutral',
            text: t('downloads.ev.pausedResumed', { time: at(ending.t), done: bytes(ending.done), resumed: at(e.t) }),
            gap: [ending.t, e.t],
            label: t('downloads.mark.paused'),
          })
        } else if (ending?.kind === 'continued') {
          out.push({
            t: ending.t,
            tone: 'neutral',
            text: t('downloads.ev.handover', { time: at(ending.t), gap: span(e.t - ending.t, t) }),
            gap: [ending.t, e.t],
            label: t('downloads.mark.handover'),
          })
        } else if (attempt === 1) {
          out.push({ t: e.t, tone: 'neutral', text: t('downloads.ev.start', { time: at(e.t) }) })
        } else {
          out.push({ t: e.t, tone: 'neutral', text: t('downloads.ev.attempt', { time: at(e.t), n: attempt }) })
        }
        ending = null
        break
      }
      case 'signal':
        ending = { kind: 'signal', t: e.t, done: num(e.done) }
        break
      case 'paused':
        ending = { kind: 'paused', t: e.t, done: num(e.done) }
        break
      case 'continued':
        ending = { kind: 'continued', t: e.t, done: 0 }
        break
      case 'error': {
        // A server that keeps answering the same way makes a run of identical errors: one line.
        const message = str(e.message)
        let n = 1
        while (events[i + 1]?.ev === 'error' && str(events[i + 1].message) === message) {
          i++
          n++
        }
        out.push({ t: e.t, tone: 'warn', text: t('downloads.ev.error', { tool: str(e.tool) || t('downloads.theDownloader'), message, count: n }) })
        break
      }
      case 'fallback':
        out.push({ t: e.t, tone: 'warn', text: t('downloads.ev.fallback', { tool: str(e.tool) || 'aria2' }), label: t('downloads.mark.fallback') })
        break
      case 'stalled':
        out.push(
          e.action === 'stop'
            ? { t: e.t, tone: 'bad', text: t('downloads.ev.stallStop', { time: span(num(e.seconds), t) }), label: t('downloads.mark.stalled') }
            : { t: e.t, tone: 'warn', text: t('downloads.ev.stallKick', { time: span(num(e.seconds), t) }) },
        )
        break
      case 'failed':
        out.push({ t: e.t, tone: 'bad', text: t('downloads.ev.failed', { message: str(e.message) }) })
        break
      case 'finished': {
        const files = Array.isArray(e.files) ? (e.files as { path: string }[]) : []
        out.push({
          t: e.t,
          tone: 'ok',
          text: files.length === 1 ? t('downloads.ev.finishedOne', { path: files[0].path }) : t('downloads.ev.finished', { count: files.length }),
        })
        break
      }
    }
  }
  // Whatever ended the last run, with no run after it yet.
  if (ending?.kind === 'signal') {
    out.push({ t: ending.t, tone: 'warn', text: t('downloads.ev.preemptedNow', { time: at(ending.t), done: bytes(ending.done) }), gap: [ending.t], label: t('downloads.mark.preempted') })
  } else if (ending?.kind === 'paused') {
    out.push({ t: ending.t, tone: 'neutral', text: t('downloads.ev.paused', { time: at(ending.t), done: bytes(ending.done) }) })
  } else if (ending?.kind === 'continued') {
    out.push({ t: ending.t, tone: 'neutral', text: t('downloads.ev.handoverNow', { time: at(ending.t) }), gap: [ending.t], label: t('downloads.mark.handover') })
  }
  return out
}
