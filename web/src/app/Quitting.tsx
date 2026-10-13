import { Check } from 'lucide-react'
import { motion } from 'motion/react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import type { Mount, State } from '@/api/types'
import { Hint } from '@/components/bits'
import { Spinner } from '@/components/Spinner'
import { StopProgress } from '@/components/StopProgress'
import { Button } from '@/components/ui/button'
import { nameOf } from '@/lib/mounts'
import { easeOut } from '@/lib/motion'
import { type QuitPhase, quit } from './quit'

/**
 * Shown while modalfs quits without the checklist (it was asked from the CLI
 * or the menu bar, or nothing needed unmounting): each volume's upload, and a
 * way to stop waiting. Laid out like the checklist, so the two read alike.
 */
export function Quitting({ state, phase }: { state: State | undefined; phase: QuitPhase }) {
  const { t } = useTranslation()
  const [now, setNow] = useState(false)
  const live = (state?.mounts ?? []).filter((m: Mount) => m.state === 'mounted' || m.state === 'stopping' || m.state === 'starting')
  const quit_ = phase === 'quit'
  return (
    <div className="mx-auto flex min-h-full w-full max-w-[460px] flex-col justify-center px-6 py-16">
      <div className="rounded-2xl border border-card-border bg-card px-5 pt-4 pb-3">
        <div className="text-[17px] font-semibold tracking-[-0.01em]">{quit_ ? t('quit.doneTitle') : t('quit.title')}</div>
        <p className="mt-1 text-[13px] leading-relaxed text-pretty text-muted-foreground">{quit_ ? t('quit.doneP') : t('quit.sub')}</p>
        <ul className="mt-3">
          {live.map((m, i) => (
            <motion.li
              key={m.id}
              initial={{ opacity: 0, transform: 'translateY(6px)' }}
              animate={{ opacity: 1, transform: 'translateY(0px)' }}
              transition={{ duration: 0.2, ease: easeOut, delay: i * 0.05 }}
              className="flex items-start gap-3 py-2.5"
            >
              <span className="flex size-[22px] shrink-0 items-center justify-center text-brand">
                <Spinner className="size-4 border-2" />
              </span>
              <div className="min-w-0 flex-1">
                <div className="truncate text-[14px] font-medium">{nameOf(m)}</div>
                <div className="mt-0.5">{m.state === 'stopping' ? <StopProgress m={m} /> : <span className="text-[12.5px] text-muted-foreground">{t('quit.waiting')}</span>}</div>
              </div>
            </motion.li>
          ))}
          <li className="flex items-center gap-3 py-2.5">
            <span className={quit_ ? 'flex size-[22px] shrink-0 items-center justify-center rounded-full bg-ok/14 text-ok-ink' : 'flex size-[22px] shrink-0 items-center justify-center text-brand'}>
              {quit_ ? <Check className="size-3" strokeWidth={3} /> : <Spinner className="size-4 border-2" />}
            </span>
            <span className="text-[14px] font-medium">{t('quitList.final')}</span>
          </li>
        </ul>
        {!quit_ && live.length > 0 && (
          <div className="mt-1 flex justify-start border-t border-card-border pt-2">
            <Hint text={t('quit.nowP')}>
              <Button
                variant="ghost"
                disabled={now}
                onClick={() => {
                  setNow(true)
                  void quit(true)
                }}
              >
                {now && <Spinner />}
                {t('quit.now')}
              </Button>
            </Hint>
          </div>
        )}
      </div>
    </div>
  )
}
