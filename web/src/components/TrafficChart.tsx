import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import type { TrafficSample } from '@/api/types'
import { bytes } from '@/lib/format'

/**
 * What a mount uploaded and read over the last hour: the rate between each
 * sample the daemon took (every 10 s), as two lines over the same scale.
 */
export function TrafficChart({ samples }: { samples: TrafficSample[] }) {
  const { t } = useTranslation()
  const { up, down, peak, from, to } = useMemo(() => {
    const up: [number, number][] = []
    const down: [number, number][] = []
    for (let i = 1; i < samples.length; i++) {
      const [t0, u0, d0] = samples[i - 1]
      const [t1, u1, d1] = samples[i]
      const dt = Math.max(1, t1 - t0)
      up.push([t1, Math.max(0, (u1 - u0) / dt)])
      down.push([t1, Math.max(0, (d1 - d0) / dt)])
    }
    const peak = Math.max(1, ...up.map((p) => p[1]), ...down.map((p) => p[1]))
    const to = samples.at(-1)?.[0] ?? Date.now() / 1000
    return { up, down, peak, from: to - 3600, to }
  }, [samples])

  const W = 700
  const H = 96
  const x = (ts: number) => ((ts - from) / (to - from)) * W
  const y = (v: number) => H - 4 - (v / peak) * (H - 12)
  const line = (pts: [number, number][]) => pts.map(([ts, v], i) => `${i ? 'L' : 'M'}${x(ts).toFixed(1)} ${y(v).toFixed(1)}`).join(' ')
  const area = (pts: [number, number][]) => (pts.length ? `${line(pts)} L${x(pts.at(-1)![0]).toFixed(1)} ${H} L${x(pts[0][0]).toFixed(1)} ${H} Z` : '')

  if (up.length < 2) return <div className="px-5 py-8 text-center text-[12.5px] text-muted-foreground">{t('vol.trafficSoon')}</div>
  return (
    <div className="px-4 pt-3 pb-2">
      <svg viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" className="block h-24 w-full" role="img" aria-label={t('vol.trafficLabel', { peak: bytes(peak) })}>
        <line x1="0" x2={W} y1={H - 0.5} y2={H - 0.5} stroke="currentColor" strokeOpacity="0.1" />
        <path d={area(up)} fill="var(--brand)" fillOpacity="0.1" />
        <path d={line(down)} fill="none" stroke="currentColor" strokeOpacity="0.4" strokeWidth="1.5" strokeDasharray="3 3" vectorEffect="non-scaling-stroke" />
        <path d={line(up)} fill="none" stroke="var(--brand)" strokeWidth="1.8" strokeLinejoin="round" vectorEffect="non-scaling-stroke" />
      </svg>
      <div className="mt-1 flex justify-between text-[11px] text-muted-foreground tabular">
        <span>{t('vol.hourAgo')}</span>
        <span>{t('vol.peak', { speed: bytes(peak) })}</span>
        <span>{t('vol.now')}</span>
      </div>
    </div>
  )
}
