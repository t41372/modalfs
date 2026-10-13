import { cn } from 'cn'
import { Liveline, type HoverPoint, type LivelinePoint } from 'liveline'
import { TrendingDown, TrendingUp } from 'lucide-react'
import { useEffect, useMemo, useRef, useState, useSyncExternalStore } from 'react'
import { useTranslation } from 'react-i18next'
import { intlTag } from '@/i18n'
import { chartRange, downsample, type Happening, slopeAt, speed, wholeRun } from '@/lib/downloads'
import { bytes } from '@/lib/format'

// The speed over a download's whole run, drawn by Liveline (one canvas, live
// interpolation and scrubbing), with an SVG layer on top for what Liveline
// doesn't draw: the times no run was going, and a tangent at the cursor with
// the acceleration there.
//
// The layer works out Liveline's layout the way liveline 0.0.7 does
// (src/Liveline.tsx, src/math/range.ts): the padding is set here, the time
// window is ours, the right edge is "now" plus a small buffer, and the value
// range is the visible values and zero (the reference line) with a 12%
// margin. Times are shifted for
// Liveline, which puts "now" at the right: by the container's clock skew
// while a download runs, and so that the last sample is "now" when it has
// ended (the chart is then paused, which stops it scrolling).

const PAD = { top: 18, bottom: 28, left: 12 }
/** Liveline's room on the right of the window, as a share of it: 5% with the badge, 1.5% without. */
const BUFFER = { badge: 0.05, none: 0.015 }
/** How far Liveline's clock runs on as it pauses: its lerp at 0.12 a frame adds up to about 0.14 s. */
const PAUSE_DRIFT = 0.14
/** Half the tangent's length, in pixels. */
const REACH = 46

export type Range = 'minute' | 'tenMinutes' | 'all'
const RANGE_SECS: Record<Exclude<Range, 'all'>, number> = { minute: 60, tenMinutes: 600 }

interface Props {
  /** The whole run, in the container's time: from `series()`. */
  points: { t: number; v: number }[]
  /** The download's run is going: the chart scrolls and new samples arrive. */
  running: boolean
  /** The latest speed. */
  value: number
  range: Range
  happenings: Happening[]
  dark: boolean
}

/** What the layers need to place things where Liveline draws them. */
interface Geometry {
  data: LivelinePoint[]
  window: number
  offset: number
  running: boolean
  value: number
  right: number
  buffer: number
  w: number
  h: number
}

export function SpeedChart(props: Props) {
  // A paused Liveline keeps the data it had when it paused, so a chart of a
  // finished download is drawn anew when the range changes.
  return <Chart key={props.running ? 'live' : `still-${props.range}`} {...props} />
}

function Chart({ points, running, value, range, happenings, dark }: Props) {
  const { t, i18n } = useTranslation()
  const box = useRef<HTMLDivElement>(null)
  const size = useSize(box)
  const color = useMemo(() => tokenColor('--brand'), []) // the same in both themes
  const [hover] = useState(hoverStore)

  // The display clock. A finished download's last sample is put at the moment
  // the chart was drawn; a running one's samples keep their time, unless the
  // container's clock is off by more than the relay's latency.
  const [drawnAt] = useState(() => Date.now() / 1000)
  const skew = useRef(0)
  const last = points.length ? points[points.length - 1].t : drawnAt
  let offset: number
  if (running) {
    const seen = Date.now() / 1000 - last
    if (Math.abs(seen) > 3 && (skew.current === 0 || seen < skew.current)) skew.current = seen
    offset = skew.current
  } else {
    offset = drawnAt + PAUSE_DRIFT - last
  }
  const now = running ? Date.now() / 1000 : drawnAt + PAUSE_DRIFT
  const first = points.length ? points[0].t + offset : now
  const window = range === 'all' ? wholeRun(Math.max(now - first, 30)) : RANGE_SECS[range]

  const data = useMemo<LivelinePoint[]>(() => {
    const from = Date.now() / 1000 - window * 1.1 - offset
    return downsample(
      points.filter((p) => p.t >= from),
      window,
    ).map((p) => ({ time: p.t + offset, value: p.v }))
  }, [points, window, offset])

  const badge = running
  const g: Geometry = {
    data,
    window,
    offset,
    running,
    value: running ? value : data.length ? data[data.length - 1].value : 0,
    right: 84,
    buffer: badge ? BUFFER.badge : BUFFER.none,
    w: size.w,
    h: size.h,
  }
  const fmtTime = useMemo(() => timeFormat(window, offset, i18n.language), [window, offset, i18n.language])

  return (
    <div>
      <div ref={box} className="relative h-56 max-[520px]:h-44">
        <Liveline
          data={data}
          value={g.value}
          theme={dark ? 'dark' : 'light'}
          color={color}
          window={window}
          grid
          badge={badge}
          momentum={false}
          pulse={running}
          paused={!running}
          loading={running && data.length < 2}
          emptyText={t('downloads.chart.empty')}
          formatValue={speed}
          formatTime={fmtTime}
          padding={{ ...PAD, right: g.right }}
          // a speed reads against zero: Liveline's range then starts there
          referenceLine={{ value: 0 }}
          onHover={hover.set}
          style={{ height: '100%' }}
        />
        {size.w > 0 && <Layer g={g} happenings={happenings} hover={hover} drawnAt={drawnAt} />}
      </div>
      <Readout g={g} hover={hover} fmtTime={fmtTime} points={points} />
    </div>
  )
}

/** Where Liveline's window ends and how it maps time and speed to pixels, from the hover when there is one (exact) or the clock. */
function layout(g: Geometry, p: HoverPoint | null, drawnAt: number) {
  const chartW = Math.max(1, g.w - PAD.left - g.right)
  const chartH = Math.max(1, g.h - PAD.top - PAD.bottom)
  const now = g.running ? Date.now() / 1000 : drawnAt + PAUSE_DRIFT
  const right = p ? p.time - ((p.x - PAD.left) / chartW) * g.window + g.window : now + g.window * g.buffer
  const left = right - g.window
  return { chartW, chartH, left, right, x: (ts: number) => PAD.left + ((ts + g.offset - left) / g.window) * chartW }
}

/** How far either side of the cursor the trend is measured: a few seconds, or more on a long window, where a pixel is several seconds. */
const half = (g: Geometry) => Math.max(2, g.window / 60)

/** The trend at the cursor, in bytes per second per second: a least-squares fit over `half` either side. */
const acceleration = (g: Geometry, p: HoverPoint) => slopeAt(g.data, p.time, half(g))

function Layer({ g, happenings, hover, drawnAt }: { g: Geometry; happenings: Happening[]; hover: HoverStore; drawnAt: number }) {
  const p = useSyncExternalStore(hover.sub, hover.get)
  // A running chart scrolls, so its marks follow it every frame.
  const [, tick] = useState(0)
  const marks = happenings.some((h) => h.gap || h.label)
  useEffect(() => {
    if (!g.running || !marks) return
    let id = requestAnimationFrame(function frame() {
      tick((n) => n + 1)
      id = requestAnimationFrame(frame)
    })
    return () => cancelAnimationFrame(id)
  }, [g.running, marks])

  const { chartW, chartH, left, right, x } = layout(g, p, drawnAt)
  const top = PAD.top
  const bottom = g.h - PAD.bottom
  const chartRight = g.w - g.right

  let tangent = null
  if (p) {
    const values = g.data.filter((d) => d.time >= left - 2 && d.time <= right).map((d) => d.value)
    const r = chartRange([...values, g.value, 0])
    const slope = (-acceleration(g, p) * (chartH / (r.max - r.min))) / (chartW / g.window)
    const dx = REACH / Math.sqrt(1 + slope * slope)
    tangent = <line x1={p.x - dx} y1={p.y - slope * dx} x2={p.x + dx} y2={p.y + slope * dx} className="stroke-foreground/70" strokeWidth={1.5} strokeLinecap="round" />
  }

  return (
    <svg className="pointer-events-none absolute inset-0" width={g.w} height={g.h} aria-hidden>
      <defs>
        <clipPath id="speed-chart-area">
          <rect x={PAD.left} y={0} width={chartW} height={g.h} />
        </clipPath>
      </defs>
      <g clipPath="url(#speed-chart-area)">
        {happenings.map((h, i) => {
          if (h.gap) {
            const x1 = x(h.gap[0])
            const x2 = h.gap[1] !== undefined ? x(h.gap[1]) : chartRight
            if (x2 < PAD.left || x1 > chartRight) return null
            const w = Math.max(2, x2 - x1)
            const warn = h.tone === 'warn'
            return (
              <g key={i}>
                <rect x={x1} y={top} width={w} height={bottom - top} className={warn ? 'fill-warn/12' : 'fill-foreground/6'} />
                <line x1={x1} x2={x1} y1={top} y2={bottom} className={warn ? 'stroke-warn/60' : 'stroke-foreground/25'} strokeDasharray="3 3" />
                {h.label && (
                  // inside the band when it's wide enough, else just after it (or before it, near the right edge)
                  <text
                    x={w > 48 ? x1 + 5 : x1 + w + 70 > chartRight ? x1 - 4 : x1 + w + 4}
                    textAnchor={w <= 48 && x1 + w + 70 > chartRight ? 'end' : 'start'}
                    y={top + 11} className={cn('text-[10.5px] font-medium', warn ? 'fill-warn-ink' : 'fill-muted-foreground')}>
                    {h.label}
                  </text>
                )}
              </g>
            )
          }
          if (!h.label) return null
          const at = x(h.t)
          if (at < PAD.left || at > chartRight) return null
          const bad = h.tone === 'bad'
          return (
            <g key={i}>
              <line x1={at} x2={at} y1={top} y2={bottom} className={bad ? 'stroke-destructive/60' : 'stroke-warn/70'} strokeDasharray="2 3" />
              <circle cx={at} cy={top + 3} r={3} className={bad ? 'fill-destructive' : 'fill-warn'} />
              {/* near the right edge, the label goes on the left of its line */}
              <text x={at > chartRight - 80 ? at - 6 : at + 6} y={top + 7} textAnchor={at > chartRight - 80 ? 'end' : 'start'} className={cn('text-[10.5px] font-medium', bad ? 'fill-destructive' : 'fill-warn-ink')}>
                {h.label}
              </text>
            </g>
          )
        })}
        {tangent}
      </g>
    </svg>
  )
}

/** Under the chart: the cursor's time, speed and acceleration, or the run's figures when there's no cursor. */
function Readout({ g, hover, fmtTime, points }: { g: Geometry; hover: HoverStore; fmtTime: (t: number) => string; points: { t: number; v: number }[] }) {
  const { t } = useTranslation()
  const p = useSyncExternalStore(hover.sub, hover.get)
  const peak = useMemo(() => points.reduce((m, x) => Math.max(m, x.v), 0), [points])
  if (p) {
    const accel = acceleration(g, p)
    // Steady: the trend changes the speed by under 3% across the span it's measured over.
    const steady = Math.abs(accel) * 2 * half(g) < Math.max(p.value * 0.03, 1024)
    const Icon = accel >= 0 ? TrendingUp : TrendingDown
    return (
      <div className="flex min-h-9 flex-wrap items-center gap-x-4 gap-y-1 border-t border-card-border px-4 py-2 text-[12px] tabular">
        <span className="text-muted-foreground">{fmtTime(p.time)}</span>
        <span className="font-medium">{speed(p.value)}</span>
        <span className={cn('inline-flex items-center gap-1', steady ? 'text-muted-foreground' : accel > 0 ? 'text-ok-ink' : 'text-warn-ink')}>
          {!steady && <Icon className="size-3.5" />}
          {steady ? t('downloads.chart.steady') : t(accel > 0 ? 'downloads.chart.faster' : 'downloads.chart.slower', { rate: `${bytes(Math.abs(accel))}/s²` })}
        </span>
      </div>
    )
  }
  return (
    <div className="flex min-h-9 flex-wrap items-center gap-x-4 gap-y-1 border-t border-card-border px-4 py-2 text-[12px] text-muted-foreground tabular">
      {g.running && <span className="font-medium text-foreground">{t('downloads.chart.now', { speed: speed(g.value) })}</span>}
      {peak > 0 && <span>{t('downloads.chart.peak', { speed: speed(peak) })}</span>}
      <span className="ml-auto hidden sm:inline">{t('downloads.chart.hint')}</span>
    </div>
  )
}

/** Time labels: with seconds for short windows, with the date for long ones. Liveline's times are shifted by `offset`. */
export function timeFormat(window: number, offset: number, lang: string) {
  const o: Intl.DateTimeFormatOptions = window <= 600 ? { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false } : { hour: '2-digit', minute: '2-digit', hour12: false }
  if (window > 86400) Object.assign(o, { month: 'short', day: 'numeric' })
  const f = new Intl.DateTimeFormat(intlTag(lang), o)
  return (ts: number) => f.format(new Date((ts - offset) * 1000))
}

type HoverStore = ReturnType<typeof hoverStore>

/** The cursor's point on the line. Liveline reports it every frame; the layers re-render only when it moves. */
function hoverStore() {
  let current: HoverPoint | null = null
  const listeners = new Set<() => void>()
  return {
    get: () => current,
    sub: (l: () => void) => (listeners.add(l), () => void listeners.delete(l)),
    set: (p: HoverPoint | null) => {
      if (p === current || (p && current && p.x === current.x && p.y === current.y && p.time === current.time)) return
      current = p
      listeners.forEach((l) => l())
    },
  }
}

function useSize(ref: React.RefObject<HTMLElement | null>) {
  const [size, setSize] = useState({ w: 0, h: 0 })
  useEffect(() => {
    const el = ref.current
    if (!el) return
    const ro = new ResizeObserver(() => setSize({ w: el.clientWidth, h: el.clientHeight }))
    ro.observe(el)
    return () => ro.disconnect()
  }, [ref])
  return size
}

/** A color token as rgb(): Liveline reads only hex and rgb, and the tokens are oklch. A translucent one is mixed onto the `under` tokens, painted in order. */
export function tokenColor(name: string, ...under: string[]) {
  const style = getComputedStyle(document.documentElement)
  const css = style.getPropertyValue(name).trim()
  const ctx = document.createElement('canvas').getContext('2d', { willReadFrequently: true })
  if (!ctx || !css) return '#3b82f6'
  for (const u of under) {
    ctx.fillStyle = style.getPropertyValue(u).trim()
    ctx.fillRect(0, 0, 1, 1)
  }
  ctx.fillStyle = css
  ctx.fillRect(0, 0, 1, 1)
  const [r, g, b] = ctx.getImageData(0, 0, 1, 1).data
  return `rgb(${r}, ${g}, ${b})`
}
