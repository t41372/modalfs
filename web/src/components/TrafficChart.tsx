import { Liveline } from 'liveline'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import type { TrafficSample } from '@/api/types'
import { axisSpeed, timeFormat, tokenColor } from '@/components/SpeedChart'
import { downsample } from '@/lib/downloads'
import { speed } from '@/lib/format'

// A mount's upload and read speed, drawn like a download's (Liveline, scrolling
// live) with the two as separate lines. The daemon samples the totals each
// second; the speeds are the rates between samples.

export type TrafficRange = 'minute' | 'tenMinutes' | 'hour'
const RANGE_SECS: Record<TrafficRange, number> = { minute: 60, tenMinutes: 600, hour: 3600 }
const PAD = { top: 18, bottom: 28, left: 12, right: 84 }

type Point = { t: number; v: number }

export function TrafficChart({ samples, range, dark }: { samples: TrafficSample[]; range: TrafficRange; dark: boolean }) {
  const { t, i18n } = useTranslation()
  const window = RANGE_SECS[range]

  const { up, down } = useMemo(() => {
    const up: Point[] = []
    const down: Point[] = []
    for (let i = 1; i < samples.length; i++) {
      const [t0, u0, d0] = samples[i - 1]
      const [t1, u1, d1] = samples[i]
      const dt = Math.max(1, t1 - t0)
      up.push({ t: t1, v: Math.max(0, (u1 - u0) / dt) })
      down.push({ t: t1, v: Math.max(0, (d1 - d0) / dt) })
    }
    return { up, down }
  }, [samples])

  const brand = useMemo(() => tokenColor('--brand'), [])
  // eslint-disable-next-line react-hooks/exhaustive-deps -- the token changes with the theme
  const muted = useMemo(() => tokenColor('--muted-foreground', '--popover'), [dark])
  const series = useMemo(() => {
    const from = Date.now() / 1000 - window * 1.1
    const shown = (pts: Point[]) => downsample(pts.filter((p) => p.t >= from), window).map((p) => ({ time: p.t, value: p.v }))
    // Named by id, not label: Liveline writes a label at each line's end,
    // and two lines at the same speed (often both 0) put one over the other.
    return [
      { id: t('vol.uploads'), color: brand, data: shown(up), value: up.at(-1)?.v ?? 0 },
      { id: t('vol.reads'), color: muted, data: shown(down), value: down.at(-1)?.v ?? 0 },
    ]
  }, [up, down, window, brand, muted, t])
  const fmtTime = useMemo(() => timeFormat(window, 0, i18n.language), [window, i18n.language])

  return (
    <div>
      {/* Liveline puts its toggles for the two lines above the chart, which takes the rest. */}
      <div className="relative flex h-64 flex-col pt-3 max-[520px]:h-52">
        <Liveline
          data={series[0].data}
          value={series[0].value}
          series={series}
          theme={dark ? 'dark' : 'light'}
          window={window}
          grid
          pulse
          loading={up.length < 2}
          emptyText={t('downloads.chart.empty')}
          formatValue={axisSpeed}
          formatTime={fmtTime}
          padding={PAD}
          referenceLine={{ value: 0 }}
          style={{ height: 'auto', flex: 1, minHeight: 0 }}
        />
      </div>
      <Readout up={up} down={down} window={window} />
    </div>
  )
}

/** Under the chart: the speeds now and their peaks in the window. Liveline shows the ones at the cursor. */
function Readout({ up, down, window }: { up: Point[]; down: Point[]; window: number }) {
  const { t } = useTranslation()
  const from = (up.at(-1)?.t ?? 0) - window
  const peak = (pts: Point[]) => pts.reduce((m, x) => (x.t >= from ? Math.max(m, x.v) : m), 0)
  return (
    <div className="flex min-h-9 flex-wrap items-center gap-x-4 gap-y-1 border-t border-card-border px-4 py-2 text-[12px] text-muted-foreground tabular">
      <span className="font-medium text-foreground">{t('vol.speedNow', { up: speed(up.at(-1)?.v ?? 0), down: speed(down.at(-1)?.v ?? 0) })}</span>
      <span>{t('vol.peaks', { up: speed(peak(up)), down: speed(peak(down)) })}</span>
    </div>
  )
}
