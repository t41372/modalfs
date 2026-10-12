import { Check, Power } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import type { Mount, State } from '@/api/types'
import { Card, Page, Row } from '@/components/page'
import { Spinner } from '@/components/Spinner'
import { StopProgress } from '@/components/StopProgress'
import { Button } from '@/components/ui/button'
import { type QuitPhase, quit } from './quit'

/** Shown while modalfs quits: each volume's upload, and a way to stop waiting. */
export function Quitting({ state, phase }: { state: State | undefined; phase: QuitPhase }) {
  const { t } = useTranslation()
  const [now, setNow] = useState(false)
  if (phase === 'quit') {
    return (
      <div className="mx-auto flex h-full max-w-md flex-col items-center justify-center px-6 text-center">
        <div className="mb-4 flex size-12 items-center justify-center rounded-2xl bg-ok/12 text-ok-ink">
          <Check className="size-5.5" />
        </div>
        <div className="text-[17px] font-semibold">{t('quit.doneTitle')}</div>
        <p className="mt-2 text-[13px] leading-relaxed text-pretty text-muted-foreground">{t('quit.doneP')}</p>
      </div>
    )
  }
  const live = (state?.mounts ?? []).filter((m: Mount) => m.state === 'mounted' || m.state === 'stopping' || m.state === 'starting')
  return (
    <Page title={t('quit.title')} sub={t('quit.sub')}>
      {live.length > 0 && (
        <Card>
          {live.map((m) => (
            <div key={m.id} className="flex items-start gap-3 px-5 py-3.5">
              <Spinner className="mt-1 size-3 text-muted-foreground" />
              <div className="min-w-0 flex-1">
                <div className="truncate text-[14px] font-medium">{m.volume_name}</div>
                <div className="mt-1">{m.state === 'stopping' ? <StopProgress m={m} /> : <span className="text-[12px] text-muted-foreground">{t('quit.waiting')}</span>}</div>
              </div>
            </div>
          ))}
        </Card>
      )}
      <Card>
        <Row title={t('quit.nowTitle')} desc={t('quit.nowP')}>
          <Button
            variant="destructive"
            disabled={now}
            onClick={() => {
              setNow(true)
              void quit(true)
            }}
          >
            {now ? <Spinner /> : <Power />}
            {t('quit.now')}
          </Button>
        </Row>
      </Card>
    </Page>
  )
}
