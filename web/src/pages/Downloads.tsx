import { cn } from 'cn'
import { ArrowDownToLine, ChevronRight, Pause, Play, Plus } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { api } from '@/api/client'
import { patchSettings, refreshDownloads, useAction, useBilling, useDownloads, useLoadedState } from '@/api/queries'
import type { Download } from '@/api/types'
import { Badge, Card, Empty, Page, Row, Section } from '@/components/page'
import { duration } from '@/components/StopProgress'
import { Button } from '@/components/ui/button'
import { Switch } from '@/components/ui/switch'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { AddDownloadDialog } from '@/dialogs/AddDownload'
import { BUSY, dollars, ENDED, HOURLY, progressText, RESUMABLE, RUNNING, SILENT_WARN, speed } from '@/lib/downloads'
import { bytes } from '@/lib/format'
import { BillingLink, DownloadPage } from './Download'

export function Downloads() {
  const { t } = useTranslation()
  const st = useLoadedState()
  const on = !!st.settings.remote_downloads
  const list = useDownloads(true)
  const downloads = list.data ?? st.downloads ?? []
  const [openId, setOpenId] = useState<string | null>(null)
  const [adding, setAdding] = useState(false)

  const open = openId ? downloads.find((d) => d.id === openId) : undefined
  if (open) return <DownloadPage d={open} onBack={() => setOpenId(null)} />

  const newest = [...downloads].reverse()
  const going = newest.filter((d) => !ENDED.has(d.state))
  const ended = newest.filter((d) => ENDED.has(d.state))
  const canAdd = on && st.accounts.length > 0

  return (
    <Page
      title={t('downloads.title')}
      sub={t('downloads.sub')}
      actions={
        canAdd &&
        downloads.length > 0 && (
          <Button variant="outline" onClick={() => setAdding(true)}>
            <Plus />
            {t('downloads.add')}
          </Button>
        )
      }
    >
      {!on && <TurnOn />}
      {downloads.length === 0
        ? on && (
            <Card>
              <Empty icon={<ArrowDownToLine />} title={t('downloads.empty')} desc={st.accounts.length ? t('downloads.emptyP') : t('downloads.emptyNoAccount')}>
                {canAdd && (
                  <Button onClick={() => setAdding(true)}>
                    <Plus />
                    {t('downloads.add')}
                  </Button>
                )}
              </Empty>
            </Card>
          )
        : (
            <>
              {going.length > 0 && (
                <Section label={t('downloads.sectionGoing')}>
                  <Card>
                    {going.map((d) => (
                      <DownloadRow key={d.id} d={d} onOpen={() => setOpenId(d.id)} />
                    ))}
                  </Card>
                </Section>
              )}
              {ended.length > 0 && (
                <Section label={t('downloads.sectionEnded')}>
                  <Card>
                    {ended.map((d) => (
                      <DownloadRow key={d.id} d={d} onOpen={() => setOpenId(d.id)} />
                    ))}
                  </Card>
                </Section>
              )}
              <Spending downloads={downloads} />
            </>
          )}
      <AddDownloadDialog open={adding} onOpenChange={setAdding} onAdded={(added) => added.length === 1 && setOpenId(added[0].id)} />
    </Page>
  )
}

/** Off until turned on: what a download runs on, and what it costs. */
function TurnOn() {
  const { t } = useTranslation()
  const set = useAction((on: boolean) => patchSettings({ remote_downloads: on }), {
    optimistic: (s, on) => {
      s.settings.remote_downloads = on
    },
  })
  return (
    <Card>
      <Row title={t('downloads.turnOn')} desc={t('downloads.turnOnD', { price: HOURLY.small.toFixed(4) })} wrap={false}>
        <Switch checked={false} disabled={set.isPending} onCheckedChange={(v) => set.mutate(v)} aria-label={t('downloads.turnOn')} />
      </Row>
    </Card>
  )
}

export function StateBadge({ d }: { d: Download }) {
  const { t } = useTranslation()
  const s = d.state
  if (s === 'running') return null
  const tone = s === 'done' ? 'ok' : s === 'failed' || s === 'stalled' ? 'bad' : s === 'interrupted' || s === 'retrying' || BUSY.has(s) || s === 'starting' || s === 'finishing' ? 'warn' : 'neutral'
  return <Badge tone={tone}>{t(`downloads.state.${s}`)}</Badge>
}

/** Pause or resume, from the row or the detail view. Refusals (a run still ending) come back as the daemon's reason. */
export function useDownloadAction() {
  return useAction(({ id, action }: { id: string; action: 'pause' | 'resume' }) => api('POST', `/downloads/${id}/${action}`, {}).finally(refreshDownloads))
}

function DownloadRow({ d, onOpen }: { d: Download; onOpen: () => void }) {
  const { t } = useTranslation()
  const act = useDownloadAction()
  const s = d.status
  const running = RUNNING.has(d.state)
  const pct = s && s.total > 0 ? Math.min(100, (s.done / s.total) * 100) : undefined
  const meta: string[] = []
  if (d.state === 'done' && s) meta.push(bytes(s.total > 0 ? s.total : s.done))
  else if (s && (s.done > 0 || s.total > 0)) meta.push(progressText(d, t))
  if (running && s) {
    meta.push(speed(s.speed))
    if (s.eta) meta.push(t('downloads.left', { time: duration(s.eta, t) }))
  }

  return (
    <div className="flex items-center gap-2 pr-3 pl-1.5">
      <button
        onClick={onOpen}
        className="group/row flex min-w-0 flex-1 items-center gap-3 rounded-xl py-3.5 pl-3.5 text-left outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
      >
        <div className="min-w-0 flex-1">
          <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
            <span className="truncate text-[15px] font-semibold tracking-[-0.01em]">{d.name}</span>
            <StateBadge d={d} />
            {(s?.attempt ?? 1) > 1 && !ENDED.has(d.state) && <Badge>{t('downloads.attempt', { n: s!.attempt })}</Badge>}
          </div>
          {pct !== undefined && d.state !== 'done' && d.state !== 'cancelled' && (
            <span className="mt-2 block h-1 max-w-md overflow-hidden rounded-full bg-muted" role="progressbar" aria-valuenow={Math.round(pct)} aria-valuemin={0} aria-valuemax={100}>
              <span className={cn('block h-full rounded-full transition-[width] duration-1000 ease-linear', running ? 'bg-brand' : 'bg-foreground/25')} style={{ width: `${pct}%` }} />
            </span>
          )}
          {/* Narrow windows put where it goes on a line of its own rather than cutting both short. */}
          <div className="mt-1 flex min-w-0 flex-col text-[12.5px] text-muted-foreground tabular sm:flex-row sm:gap-1.5">
            {meta.length > 0 && <span className="shrink-0 truncate">{meta.join(' · ')}</span>}
            {meta.length > 0 && (
              <span aria-hidden className="hidden sm:inline">
                ·
              </span>
            )}
            <span className="truncate">
              {d.volume_name}:/{d.folder}
              <span className="ml-1.5 text-muted-foreground/70">{d.status?.tool || d.tool}</span>
            </span>
          </div>
          {(d.state === 'failed' || d.state === 'stalled') && d.error && (
            <div data-selectable className="mt-1 line-clamp-1 text-[12px] text-destructive">
              {d.error_kind ? t(`downloads.whyShort.${d.error_kind}`) : d.error}
            </div>
          )}
          {!ENDED.has(d.state) && (d.silent ?? 0) >= SILENT_WARN && (
            <div className="mt-1 line-clamp-1 text-[12px] text-warn-ink">{t('downloads.silentShort', { time: duration(d.silent ?? 0, t) })}</div>
          )}
        </div>
        <ChevronRight aria-label={t('downloads.details')} className="size-4 shrink-0 text-muted-foreground/70" />
      </button>
      {(running || RESUMABLE.has(d.state)) && (
        <Tooltip>
          <TooltipTrigger
            render={
              <Button
                variant="ghost"
                size="icon"
                disabled={act.isPending || d.state === 'finishing'}
                onClick={() => act.mutate({ id: d.id, action: running ? 'pause' : 'resume' }, { onSuccess: () => toast(t(running ? 'downloads.pausing' : 'downloads.resuming', { name: d.name })) })}
                aria-label={t(running ? 'downloads.pause' : 'downloads.resume')}
              >
                {running ? <Pause className="text-muted-foreground" /> : <Play className="text-muted-foreground" />}
              </Button>
            }
          />
          <TooltipContent>{t(running ? 'downloads.pause' : 'downloads.resume')}</TooltipContent>
        </Tooltip>
      )}
    </div>
  )
}

/**
 * What the accounts that downloads run in have spent this month. Modal's API
 * tells spending, not the credit left or the spend limit, so it says so and
 * links to where those are.
 */
function Spending({ downloads }: { downloads: Download[] }) {
  const { t } = useTranslation()
  const seen = new Set<string>()
  const places: { account: string; label: string; env: string }[] = []
  for (const d of downloads) {
    const key = `${d.account_id}\u0000${d.environment ?? ''}`
    if (seen.has(key)) continue
    seen.add(key)
    places.push({ account: d.account_id, label: d.account_label, env: d.environment ?? '' })
  }
  if (!places.length) return null
  return (
    <Section label={t('downloads.spending')}>
      <Card>
        {places.map((p) => (
          <SpendingRow key={`${p.account}/${p.env}`} {...p} showEnv={places.some((q) => q.account === p.account && q.env !== p.env)} />
        ))}
      </Card>
      <p className="mt-2 px-1 text-[12px] text-muted-foreground">{t('downloads.spendingNote')}</p>
    </Section>
  )
}

function SpendingRow({ account, label, env, showEnv }: { account: string; label: string; env: string; showEnv: boolean }) {
  const { t } = useTranslation()
  const b = useBilling(account, env)
  const lines: string[] = []
  if (b.data) {
    if (b.data.downloads !== undefined) lines.push(t('downloads.spentDownloads', { amount: dollars(b.data.downloads) }))
    lines.push(t('downloads.spentWorkspace', { used: dollars(b.data.metered), charged: dollars(b.data.billed) }))
  }
  return (
    <Row
      title={showEnv && env ? `${label} · ${env}` : label}
      desc={b.isLoading ? t('form.loading') : b.error ? b.error.message : lines.join(' · ')}
    >
      <BillingLink billing={b.data} />
    </Row>
  )
}
