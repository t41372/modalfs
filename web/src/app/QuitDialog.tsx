import { cn } from 'cn'
import { Check, CircleDashed, Clock, CloudDownload, Minus, TriangleAlert } from 'lucide-react'
import { AnimatePresence, motion } from 'motion/react'
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { useAppState } from '@/api/queries'
import { Hint } from '@/components/bits'
import { Spinner } from '@/components/Spinner'
import { AlertDialog, AlertDialogContent, AlertDialogDescription, AlertDialogTitle } from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { bytes } from '@/lib/format'
import { easeOut } from '@/lib/motion'
import { force, keepRunning, quitNow, type Row, type RowPhase, retry, useQuitFlow } from './quitFlow'

type IconLook = RowPhase | 'final-wait' | 'final-run' | 'final-done' | 'cloud'

const RING = 2 * Math.PI * 8

/** A row's icon. When the phase changes the old one shrinks and fades as the new one comes in. */
function RowIcon({ look, progress }: { look: IconLook; progress?: number }) {
  let icon: ReactNode
  let tone = 'text-muted-foreground'
  switch (look) {
    case 'uploading':
      tone = 'text-warn-ink'
      icon = (
        <svg viewBox="0 0 22 22" className="size-[22px] -rotate-90" aria-hidden>
          <circle cx="11" cy="11" r="8" fill="none" stroke="currentColor" strokeOpacity="0.2" strokeWidth="2.5" />
          <circle cx="11" cy="11" r="8" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeDasharray={RING} strokeDashoffset={RING * (1 - Math.min(1, Math.max(0.04, progress ?? 0)))} />
        </svg>
      )
      break
    case 'unmounting':
    case 'final-run':
      tone = 'text-brand'
      icon = <Spinner className="size-4 border-2" />
      break
    case 'done':
    case 'final-done':
      tone = 'bg-ok/14 text-ok-ink rounded-full'
      icon = <Check className="size-3" strokeWidth={3} />
      break
    case 'blocked':
      tone = 'bg-warn/18 text-warn-ink rounded-full'
      icon = <TriangleAlert className="size-3" strokeWidth={2.6} />
      break
    case 'failed':
      tone = 'bg-destructive/12 text-destructive rounded-full'
      icon = <TriangleAlert className="size-3" strokeWidth={2.6} />
      break
    case 'skipped':
      icon = <Clock className="size-[18px]" strokeWidth={2} />
      break
    case 'cloud':
      icon = <CloudDownload className="size-[18px]" strokeWidth={2} />
      break
    case 'kept':
      icon = <Minus className="size-[18px]" strokeWidth={2.4} />
      break
    default:
      icon = <CircleDashed className="size-[18px]" strokeWidth={2} />
  }
  return (
    <span className="relative flex size-[22px] shrink-0">
      <AnimatePresence initial={false}>
        <motion.span
          key={look}
          className={cn('absolute inset-0 flex items-center justify-center', tone)}
          initial={{ opacity: 0, scale: 0.6 }}
          animate={{ opacity: 1, scale: 1 }}
          exit={{ opacity: 0, scale: 0.6 }}
          transition={{ duration: 0.2, ease: easeOut }}
        >
          {icon}
        </motion.span>
      </AnimatePresence>
    </span>
  )
}

function Line({ i, look, progress, title, detail, done, children }: { i: number; look: IconLook; progress?: number; title: ReactNode; detail?: ReactNode; done?: boolean; children?: ReactNode }) {
  return (
    <motion.li
      initial={{ opacity: 0, transform: 'translateY(6px)' }}
      animate={{ opacity: 1, transform: 'translateY(0px)' }}
      transition={{ duration: 0.2, ease: easeOut, delay: i * 0.05 }}
      className="flex items-start gap-3 py-2.5"
    >
      <RowIcon look={look} progress={progress} />
      <div className="min-w-0 flex-1">
        <div className={cn('truncate text-[14px] font-medium transition-colors duration-200', done && 'text-muted-foreground')}>{title}</div>
        {detail && <div className="mt-0.5 text-[12.5px] text-pretty text-muted-foreground tabular">{detail}</div>}
        {children}
      </div>
    </motion.li>
  )
}

/** The checklist shown while modalfs quits: every volume, then modalfs itself. */
export function QuitDialog() {
  const { t } = useTranslation()
  const f = useQuitFlow()
  const { data: state } = useAppState()
  const open = f.stage !== 'closed'

  const detail = (r: Row): { text: ReactNode; progress?: number } => {
    switch (r.phase) {
      case 'uploading': {
        // pending_bytes only drops when a file's commit finishes; what was sent since shows the progress in between.
        const s = state?.mounts.find((m) => m.id === r.id)?.status
        const sent = Math.max(0, (s?.bytes_up ?? 0) - (r.up0 ?? 0))
        const total = r.total ?? 0
        const left = Math.min(s?.pending_bytes ?? 0, Math.max(0, total - sent))
        return { text: t('quitList.uploading', { size: bytes(left) }), progress: total ? 1 - left / total : 0 }
      }
      case 'unmounting':
        return { text: t('quitList.unmounting') }
      case 'done':
        return { text: t('quitList.done') }
      case 'blocked':
        return { text: t('quitList.inUse', { apps: (r.apps ?? []).join(t('quitList.and')) }) }
      case 'failed':
        return { text: r.error }
      case 'skipped':
        return { text: t('quitList.skipped') }
      case 'kept':
        return { text: t('quitList.kept') }
      default:
        return { text: t('quitList.waiting') }
    }
  }

  const done = f.rows.filter((r) => r.phase === 'done').map((r) => r.name)
  const kept = f.rows.filter((r) => r.phase !== 'done').map((r) => r.name)
  const finalLook: IconLook = f.stage === 'quit' ? 'final-done' : f.stage === 'quitting' ? 'final-run' : 'final-wait'
  const busy = f.stage === 'quitting' || f.stage === 'quit'

  return (
    <AlertDialog
      open={open}
      onOpenChange={(o) => {
        // Escape is "Keep running"; there's nothing to click outside of.
        if (!o && !busy && f.stage !== 'stopping') keepRunning()
      }}
    >
      <AlertDialogContent className="gap-0 p-5 data-[size=default]:max-w-[calc(100%-2rem)] data-[size=default]:sm:max-w-md">
        <AlertDialogTitle className="text-[17px] font-semibold">{f.stage === 'stopped' ? t('quitList.stoppedTitle') : f.stage === 'quit' ? t('quit.doneTitle') : t('quitList.title')}</AlertDialogTitle>
        <AlertDialogDescription className="mt-1 text-[13px] text-muted-foreground">
          {f.stage === 'stopped' ? (
            <>
              {done.length > 0 && <span className="block">{t('quitList.stoppedDone', { names: done.join(t('quitList.and')) })}</span>}
              {kept.length > 0 && <span className="block">{t('quitList.stoppedKept', { names: kept.join(t('quitList.and')) })}</span>}
            </>
          ) : f.stage === 'quit' ? (
            t('quit.doneP')
          ) : (
            t('quitList.sub')
          )}
        </AlertDialogDescription>

        <ul className="mt-3 flex flex-col">
          {f.rows.map((r, i) => {
            const d = detail(r)
            return (
              <Line key={r.id} i={i} look={r.phase} progress={d.progress} title={r.name} detail={d.text} done={r.phase === 'done' || r.phase === 'skipped'}>
                {(r.phase === 'blocked' || r.phase === 'failed') && f.stage !== 'stopped' && (
                  <div className="mt-2 flex flex-wrap gap-2">
                    <Button size="sm" variant="outline" onClick={() => void retry(r.id)}>
                      {t('quitList.retry')}
                    </Button>
                    <Hint text={t('quitList.forceHint')}>
                      <Button size="sm" variant="ghost" className="text-destructive" onClick={() => void force(r.id)}>
                        {t('quitList.force')}
                      </Button>
                    </Hint>
                  </div>
                )}
              </Line>
            )
          })}
          {f.downloads > 0 && (
            <Line i={f.rows.length} look="cloud" title={t('quitList.downloads', { count: f.downloads })} done />
          )}
          {f.stage !== 'stopped' && (
            <Line i={f.rows.length + 1} look={finalLook} title={f.stage === 'quit' ? t('quitList.finalDone') : t('quitList.final')} done={f.stage === 'quit'} />
          )}
        </ul>

        <div className="mt-4 flex flex-wrap items-center justify-between gap-2">
          {f.stage === 'quit' ? null : f.stage === 'stopped' ? (
            <Button className="ml-auto" onClick={keepRunning}>
              {t('quitList.close')}
            </Button>
          ) : (
            <>
              <Hint text={t('quitList.quitNowHint')}>
                <Button variant="ghost" className="text-muted-foreground" disabled={busy || f.stage === 'stopping'} onClick={quitNow}>
                  {t('quitList.quitNow')}
                </Button>
              </Hint>
              <Button variant="outline" disabled={busy || f.stage === 'stopping'} onClick={keepRunning}>
                {f.stage === 'stopping' ? t('quitList.stopping') : t('quitList.keep')}
              </Button>
            </>
          )}
        </div>
      </AlertDialogContent>
    </AlertDialog>
  )
}
