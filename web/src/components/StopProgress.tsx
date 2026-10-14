import type { TFunction } from 'i18next'
import { useTranslation } from 'react-i18next'
import { useAppState } from '@/api/queries'
import type { Mount } from '@/api/types'
import { bytes } from '@/lib/format'

// Upload speed from bytes_up across polls, over the last minute: blocks finish in bursts (eight
// 8 MiB uploads at a time), so a short window swings between zero and double.
const samples = new Map<string, { t: number; up: number }[]>()
const WINDOW = 60_000

/** Bytes per second, and over how many milliseconds it was measured; undefined until there's enough to go on. */
function uploadRate(id: string, t: number, up: number): { rate: number; span: number } | undefined {
  const list = samples.get(id) ?? []
  if (list.length && up < list[list.length - 1].up) list.length = 0 // a new mount
  if (!list.length || list[list.length - 1].t !== t) list.push({ t, up })
  while (list.length > 2 && t - list[0].t > WINDOW) list.shift()
  samples.set(id, list)
  const span = t - list[0].t
  if (span < 5000) return undefined
  return { rate: ((up - list[0].up) / span) * 1000, span }
}

/** "40 s", "3 min", "1 h 20 min". */
export function duration(sec: number, t: TFunction) {
  sec = Math.max(1, Math.round(sec))
  if (sec < 60) return t('time.s', { n: sec })
  // Rounded to the minute first, so 59 min 40 s is "1 h 0 min", never "60 min".
  const min = Math.round(sec / 60)
  if (min < 60) return t('time.min', { n: min })
  return t('time.h', { h: Math.floor(min / 60), m: min % 60 })
}

/** What a stopping mount still has to upload: an estimate, since pending_bytes counts whole 8 MiB blocks and only drops when a file's commit finishes. */
export function stopLeft(m: Mount) {
  const s = m.status
  if (!s) return { left: 0, total: 0 }
  const total = m.stop_pending_bytes ?? 0
  const sent = s.bytes_up - (m.stop_bytes_up ?? 0)
  const left = total > 0 ? Math.min(s.pending_bytes, Math.max(0, total - sent)) : s.pending_bytes
  return { left, total: Math.max(total, left) }
}

/** A stopping mount's upload: a bar, what's left, how fast, and about how long. */
export function StopProgress({ m }: { m: Mount }) {
  const { t } = useTranslation()
  const { dataUpdatedAt } = useAppState()
  const s = m.status
  const { left, total } = stopLeft(m)
  const measured = s ? uploadRate(m.id, dataUpdatedAt, s.bytes_up) : undefined
  // Nothing sent yet: it's still reading and hashing the changed blocks, or finding out which Modal already has.
  const preparing = !!s && s.bytes_up === (m.stop_bytes_up ?? 0)
  const stalled = !!measured && measured.rate < 1024 && measured.span >= 45_000
  // Until this page has watched for a while, the average since the stop began is steadier.
  const since = s && m.stopping_since ? Date.now() / 1000 - m.stopping_since : 0
  const average = s && since > 5 ? (s.bytes_up - (m.stop_bytes_up ?? 0)) / since : 0
  const rate = measured && measured.span >= 20_000 ? (measured.rate >= 1024 ? measured.rate : undefined) : average >= 1024 ? average : measured && measured.rate >= 1024 ? measured.rate : undefined
  const uploading = !!s && (left > 0 || s.pending_files > 0)
  const done = total > 0 ? Math.min(1, Math.max(0, 1 - left / total)) : 0

  return (
    <div className="flex min-w-0 flex-col gap-1.5">
      {uploading && (
        <span
          className="block h-1.5 w-full max-w-80 overflow-hidden rounded-full bg-muted"
          role="progressbar"
          aria-label={t('mounts.stopUploading')}
          aria-valuenow={Math.round(done * 100)}
          aria-valuemin={0}
          aria-valuemax={100}
        >
          <span className="block h-full rounded-full bg-brand transition-[width] duration-700 ease-out" style={{ width: `${Math.max(2, done * 100)}%` }} />
        </span>
      )}
      <span className="text-[12px] text-muted-foreground tabular">
        {m.unmounted && <span className="text-foreground/80">{t('mounts.stopBackground')} </span>}
        {uploading ? (
          <>
            {t('mounts.stopLeft', { left: bytes(left) })}
            {' · '}
            {preparing ? (
              t('mounts.preparing')
            ) : stalled ? (
              t('mounts.stalled')
            ) : rate === undefined ? (
              t('mounts.measuring')
            ) : (
              <>
                {t('mounts.speed', { speed: bytes(rate) })}
                {left > 0 && ` · ${t('mounts.eta', { time: duration(left / rate, t) })}`}
              </>
            )}
          </>
        ) : (
          !m.unmounted && t('mounts.stopFinishing')
        )}
      </span>
    </div>
  )
}
