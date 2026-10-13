import { cn } from 'cn'
import type { TFunction } from 'i18next'
import { ChevronLeft, Copy, ExternalLink, FileText, MoreHorizontal, Pause, Play, RotateCcw, Trash2, TriangleAlert, X } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { api, apiText } from '@/api/client'
import { queryClient, refreshDownloads, refreshState, useAction, useAppState, useBilling, useDownloadRecords, useDownloads, useLive } from '@/api/queries'
import type { Billing, Download, DownloadStatus } from '@/api/types'
import { navigate } from '@/app/nav'
import { useIsDark } from '@/app/theme'
import { AccountName, Bar, Chip, Hint, Tile } from '@/components/bits'
import { confirm } from '@/components/confirm'
import { Card, Mono, Page, Section } from '@/components/page'
import { Segmented } from '@/components/Segmented'
import { type Range, SpeedChart } from '@/components/SpeedChart'
import { Spinner } from '@/components/Spinner'
import { duration } from '@/components/StopProgress'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { BUSY, dollars, ENDED, happenings, hourlyFor, permanent, progressText, RESUMABLE, RUNNING, runSeconds, SILENT_WARN, series, speed, type Tone, when } from '@/lib/downloads'
import { bytes } from '@/lib/format'
import { openExternal } from '@/lib/platform'
import { downloadGlyph } from './Downloads'

/** A download's page, by its ID in the address. */
export function DownloadRoute({ id }: { id: string }) {
  const st = useAppState().data
  const list = useDownloads(true)
  const d = (list.data ?? st?.downloads ?? []).find((x) => x.id === id)
  const loaded = !!list.data
  useEffect(() => {
    if (loaded && !d) navigate('downloads')
  }, [loaded, d])
  return d ? <DownloadPage d={d} key={d.id} /> : null
}

/** One download: where it stands, its speed over the whole run, and everything that happened to it. */
function DownloadPage({ d }: { d: Download }) {
  const { t, i18n } = useTranslation()
  const lang = i18n.language
  const dark = useIsDark(useAppState().data?.settings.appearance)
  const running = RUNNING.has(d.state) || d.state === 'pausing'
  const records = useDownloadRecords(d.id, running)
  const live = useLive(d.id, running)
  const [range, setRange] = useState<Range>('all')
  const [logOpen, setLogOpen] = useState(false)
  const billing = useBilling(d.account_id, d.environment)
  const act = useAction(({ action }: { action: 'pause' | 'resume' }) => api('POST', `/downloads/${d.id}/${action}`, {}).finally(refreshDownloads))

  const s = live.view?.status ?? d.status
  const points = useMemo(() => series(records.history, running ? live.samples : []), [records.history, live.samples, running])
  const events = records.detail?.events ?? []
  const what = useMemo(() => happenings(events, t, lang), [events, t, lang])
  const pct = s && s.total > 0 ? Math.min(100, (s.done / s.total) * 100) : undefined
  const started = events.find((e) => e.ev === 'start')?.t
  const finished = events.findLast((e) => e.ev === 'finished')?.t
  const silent = !ENDED.has(d.state) && (d.silent ?? 0) >= SILENT_WARN
  // Progress, figures and the chart only once something was downloaded or it's
  // downloading now: a download that failed at once, or one Modal hasn't
  // started yet, has nothing to chart or count, and empty tiles say nothing.
  const ran = (s?.done ?? 0) > 0 || records.history.length > 0 || (d.state === 'running' && !silent)
  const { glyph, tone } = downloadGlyph(d.state)
  const hourly = hourlyFor(d.size, billing.data)
  const cost = (runSeconds(events, running, Date.now() / 1000) / 3600) * hourly
  const partial = d.state !== 'done' && d.state !== 'cancelled' && (s?.done ?? 0) > 0
  const gone = permanent(d)

  return (
    <Page
      title={d.name}
      back={
        <Button variant="ghost" size="sm" onClick={() => navigate('downloads')} className="text-muted-foreground">
          <ChevronLeft />
          {t('dl.title')}
        </Button>
      }
      sub={
        <span className="flex flex-wrap gap-1.5 pt-1">
          <Chip tone={tone} glyph={glyph}>
            {t(`dl.state.${d.state}`)}
            {(s?.attempt ?? 1) > 1 && !ENDED.has(d.state) ? ` · ${t('dl.attempt', { n: s!.attempt })}` : ''}
          </Chip>
          <Chip mono hint={s?.files?.length ? s.files.map((f) => `${d.volume_name}:/${f.path}`).join('\n') : undefined}>
            → {d.volume_name}:/{d.folder}
          </Chip>
          <Chip mono hint={d.url} className="max-w-[18rem]">
            {d.url.replace(/^https?:\/\//, '')}
          </Chip>
        </span>
      }
      actions={
        <>
          {(running || d.state === 'cancelling') && (
            <Hint text={t('dl.pauseHint')}>
              <Button variant="outline" disabled={!running || d.state === 'pausing' || d.state === 'finishing' || act.isPending} onClick={() => act.mutate({ action: 'pause' })}>
                {d.state === 'pausing' ? <Spinner /> : <Pause />}
                {t(d.state === 'pausing' ? 'dl.state.pausing' : 'dl.pause')}
              </Button>
            </Hint>
          )}
          {RESUMABLE.has(d.state) && (
            <Hint text={t('dl.resumeHint', { price: hourly.toFixed(4) })}>
              <Button variant={gone ? 'outline' : 'default'} disabled={act.isPending} onClick={() => act.mutate({ action: 'resume' })}>
                {ran ? <Play /> : <RotateCcw />}
                {t(ran && !gone ? 'dl.resume' : 'dl.retry')}
              </Button>
            </Hint>
          )}
          <DropdownMenu>
            <DropdownMenuTrigger render={<Button variant="outline" size="icon" aria-label={t('dl.more')} />}>
              <MoreHorizontal />
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="min-w-48">
              <DropdownMenuItem onClick={() => navigator.clipboard?.writeText(d.url).then(() => toast(t('dl.copied')))}>
                <Copy />
                {t('dl.copyLink')}
              </DropdownMenuItem>
              <DropdownMenuItem disabled={!ran} onClick={() => setLogOpen(true)}>
                <FileText />
                {t('dl.log')}
              </DropdownMenuItem>
              <DropdownMenuSeparator />
              {partial ? (
                <DropdownMenuItem variant="destructive" disabled={BUSY.has(d.state)} onClick={() => askDelete(d, t)}>
                  <Trash2 />
                  {t('dl.delete')}
                </DropdownMenuItem>
              ) : (
                <DropdownMenuItem disabled={!ENDED.has(d.state)} onClick={() => askRemove(d, t)}>
                  <X />
                  {t('dl.remove')}
                </DropdownMenuItem>
              )}
            </DropdownMenuContent>
          </DropdownMenu>
        </>
      }
    >
      {(d.state === 'failed' || d.state === 'stalled') && (d.error || s?.log_tail?.length) && <Problem d={d} s={s} billing={billing.data} />}
      {silent && <Silent d={d} billing={billing.data} />}

      {ran && (
        <>
          <Card className="px-5 py-4">
            <div className="flex flex-wrap items-baseline justify-between gap-2">
              <span className="text-[22px] font-semibold tracking-[-0.01em] tabular">{pct !== undefined ? `${pct.toFixed(pct < 10 ? 1 : 0)}%` : s ? bytes(s.done) : '—'}</span>
              <span className="text-[12.5px] text-muted-foreground tabular">{s && s.total > 0 ? progressText(d, t) : ''}</span>
            </div>
            {pct !== undefined && <Bar pct={pct} tone={d.state === 'running' ? 'brand' : d.state === 'interrupted' ? 'warn' : 'muted'} className="mt-2.5 h-2" label={t('dl.progress')} />}
          </Card>
          <div className="grid grid-cols-3 gap-2.5">
            <Tile label={t('dl.speed')} value={d.state === 'running' && s ? speed(s.speed).split(' ')[0] : '—'} unit={d.state === 'running' && s ? speed(s.speed).split(' ')[1] : undefined} />
            {d.state === 'done' && started && finished ? (
              <Tile label={t('dl.took')} value={duration(finished - started, t)} />
            ) : (
              <Tile label={t('dl.timeLeft')} value={running && s?.eta ? duration(s.eta, t) : '—'} />
            )}
            <Tile label={t('dl.cost')} value={`≈ ${dollars(cost)}`} hint={t('dl.costHint', { price: hourly.toFixed(4), time: duration(Math.max(1, runSeconds(events, running, Date.now() / 1000)), t) })} />
          </div>

          <Section
            label={t('dl.speedOver')}
            aside={
              <Segmented<Range>
                label={t('dl.speedOver')}
                value={range}
                onChange={setRange}
                options={[
                  { value: 'minute', label: t('downloads.range.minute') },
                  { value: 'tenMinutes', label: t('downloads.range.tenMinutes') },
                  { value: 'all', label: t('downloads.range.all') },
                ]}
              />
            }
          >
            <Card>
              {points.length < 2 && !running ? (
                <div className="px-5 py-10 text-center text-[13px] text-muted-foreground">{records.detail || records.error ? t('downloads.chart.none') : t('form.loading')}</div>
              ) : (
                <SpeedChart points={points} running={running} value={s?.speed ?? 0} range={range} happenings={what} dark={dark} />
              )}
            </Card>
            {(records.error || records.detail?.records_error || live.view?.error) && (
              <p data-selectable className="mt-2 px-1 text-[12px] text-muted-foreground">
                {t('downloads.recordsError', { error: records.error || records.detail?.records_error || live.view?.error })}
              </p>
            )}
          </Section>
        </>
      )}

      {what.length > 0 && (
        <Section label={t('dl.happened')}>
          <Card>
            <ol className="flex flex-col py-1.5">
              {what.map((h, i) => (
                <li key={i} className="flex gap-3 px-5 py-2">
                  <span className="min-w-[3.25rem] shrink-0 pt-px text-right text-[12px] whitespace-nowrap text-muted-foreground tabular">{when(h.t, lang)}</span>
                  <Dot tone={h.tone} />
                  <span data-selectable className="min-w-0 text-[13px] leading-relaxed text-pretty break-words">
                    {h.text}
                  </span>
                </li>
              ))}
            </ol>
          </Card>
        </Section>
      )}

      <Section label={t('dl.details')}>
        <Card>
          <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-6 gap-y-2.5 px-5 py-4 text-[12.5px]">
            {s?.files && s.files.length > 0 ? (
              <>
                <dt className="text-muted-foreground">{t('dl.savedAs')}</dt>
                <dd className="flex flex-col gap-1">
                  {s.files.map((f) => (
                    <span key={f.path}>
                      <Mono>
                        {d.volume_name}:/{f.path}
                      </Mono>
                      <span className="ml-2 text-muted-foreground">{bytes(f.size)}</span>
                      {f.sha256 && (
                        <Hint text={`SHA-256 ${f.sha256}`}>
                          <span className="ml-2 cursor-default text-[11px] text-muted-foreground underline decoration-dotted underline-offset-2">SHA-256</span>
                        </Hint>
                      )}
                    </span>
                  ))}
                </dd>
              </>
            ) : (
              <>
                <dt className="text-muted-foreground">{t('dl.into')}</dt>
                <dd>
                  <Mono>
                    {d.volume_name}:/{d.folder}
                  </Mono>
                </dd>
              </>
            )}
            <dt className="text-muted-foreground">{t('dl.tool')}</dt>
            <dd>
              {s?.tool || d.tool}
              {s?.tool && s.tool !== d.tool && <span className="text-muted-foreground"> {t('downloads.insteadOf', { tool: d.tool })}</span>}
            </dd>
            <dt className="text-muted-foreground">{t('dl.size')}</dt>
            <dd>
              {t(`downloads.sizes.${d.size}`)}
              <span className="text-muted-foreground"> · {t('downloads.perHour', { price: hourly.toFixed(4) })}</span>
            </dd>
            <dt className="text-muted-foreground">{t('form.account')}</dt>
            <dd data-selectable className="flex min-w-0 items-center gap-1">
              <AccountName id={d.account_id} label={d.account_label} />
              {d.environment && <span className="text-muted-foreground">· {d.environment}</span>}
            </dd>
            <dt className="text-muted-foreground">{t('dl.addedAt')}</dt>
            <dd>{when(d.created, lang)}</dd>
          </dl>
        </Card>
      </Section>

      <div className="flex flex-wrap items-center gap-2 pt-2">
        {partial ? (
          <Button variant="ghost" size="sm" className="text-destructive hover:bg-destructive/10 hover:text-destructive" disabled={BUSY.has(d.state)} onClick={() => askDelete(d, t)}>
            <Trash2 />
            {t('dl.deleteN', { size: bytes(s?.done ?? 0) })}
          </Button>
        ) : (
          ENDED.has(d.state) && (
            <Button variant="ghost" size="sm" className="text-muted-foreground" onClick={() => askRemove(d, t)}>
              <X />
              {t('dl.remove')}
            </Button>
          )
        )}
      </div>
      <LogDialog id={d.id} name={d.name} open={logOpen} onOpenChange={setLogOpen} />
    </Page>
  )
}

/** Stop it and delete what it downloaded so far, then forget it. Pausing is offered instead. */
function askDelete(d: Download, t: TFunction) {
  const size = bytes(d.status?.done ?? 0)
  const running = RUNNING.has(d.state)
  confirm({
    title: t('dl.deleteQ', { name: d.name }),
    desc: (
      <>
        {t('dl.deleteP', { size })}
        <span className="mt-1.5 block">{t('dl.deletePause')}</span>
      </>
    ),
    action: t('dl.deleteN', { size }),
    dismiss: t('dl.keep'),
    alt: running ? { label: t('dl.pauseInstead'), run: () => api('POST', `/downloads/${d.id}/pause`, {}).finally(refreshDownloads) } : undefined,
    danger: true,
    run: async () => {
      try {
        if (d.state !== 'cancelled') await api('POST', `/downloads/${d.id}/cancel`, { keep: false })
        await forget(d.id)
        navigate('downloads')
      } catch (e) {
        toast.error(e instanceof Error ? e.message : String(e))
      } finally {
        refreshDownloads()
        refreshState()
      }
    },
  })
}

/** Take it off the list: its files stay where they are. */
function askRemove(d: Download, t: TFunction) {
  confirm({
    title: t('dl.removeQ', { name: d.name }),
    desc: t(d.state === 'done' ? 'dl.removeDoneP' : 'dl.removeEmptyP'),
    action: t('dl.remove'),
    dismiss: t('dl.keep'),
    run: () =>
      forget(d.id)
        .then(() => navigate('downloads'))
        .catch((e) => toast.error(e.message))
        .finally(() => {
          refreshDownloads()
          refreshState()
        }),
  })
}

/** Forgets a download, once its last run has ended (a cancel takes a moment). */
async function forget(id: string) {
  for (let i = 0; ; i++) {
    try {
      await api('DELETE', `/downloads/${id}`)
      return
    } catch (e) {
      if (i >= 30 || !/still ending|pause or cancel/i.test(e instanceof Error ? e.message : '')) throw e
      await new Promise((r) => setTimeout(r, 1000))
      await queryClient.invalidateQueries({ queryKey: ['downloads'] })
    }
  }
}

/** Why it failed, in plain words, with Modal's own message under it when Modal was the reason. */
function Problem({ d, s, billing }: { d: Download; s?: DownloadStatus; billing?: Billing }) {
  const { t } = useTranslation()
  const kind = d.error_kind
  const gone = permanent(d)
  return (
    <div data-selectable className="flex gap-2 rounded-2xl bg-destructive/8 px-4 py-3.5 text-[12.5px] leading-relaxed text-destructive">
      <TriangleAlert className="mt-0.5 size-4 shrink-0" />
      <div className="min-w-0">
        <div className="font-medium text-pretty">{kind ? t(`downloads.why.${kind}`) : gone ? t('dl.goneLong') : d.error || t(`dl.state.${d.state}`)}</div>
        {(kind || gone) && d.error && <div className="mt-1 font-mono text-[11px] break-words text-destructive/80">{t('downloads.modalSaid', { message: d.error })}</div>}
        {!kind && s?.log_tail && s.log_tail.length > 0 && (
          <details className="mt-1.5">
            <summary className="cursor-pointer text-[12px] text-destructive/80">{t('dl.lastLines')}</summary>
            <pre className="mt-1 max-h-28 overflow-auto font-mono text-[11px] whitespace-pre-wrap text-destructive/80">{s.log_tail.slice(-6).join('\n')}</pre>
          </details>
        )}
        {kind && <BillingLink billing={billing} className="mt-2" />}
      </div>
    </div>
  )
}

/** Running, but nothing heard from its container for longer than Modal takes to move one. */
function Silent({ d, billing }: { d: Download; billing?: Billing }) {
  const { t } = useTranslation()
  return (
    <div className="flex gap-2 rounded-2xl bg-warn/15 px-4 py-3.5 text-[12.5px] leading-relaxed text-warn-ink">
      <TriangleAlert className="mt-0.5 size-4 shrink-0 text-warn" />
      <div className="min-w-0">
        <div className="text-pretty">{t('downloads.silent', { time: duration(d.silent ?? 0, t) })}</div>
        <BillingLink billing={billing} className="mt-2" />
      </div>
    </div>
  )
}

/** Modal's Usage & Billing page, where the spend limit and the credit are (the API doesn't tell them). */
export function BillingLink({ billing, className }: { billing?: Billing; className?: string }) {
  const { t } = useTranslation()
  return (
    <Button variant="outline" size="sm" className={cn('bg-background/60', className)} onClick={() => openExternal(billing?.usage_url || 'https://modal.com/settings/usage')}>
      <ExternalLink />
      {t('downloads.usagePage')}
    </Button>
  )
}

function Dot({ tone }: { tone: Tone }) {
  return (
    <span
      aria-hidden
      className={cn(
        'mt-[7px] size-2 shrink-0 rounded-full',
        tone === 'ok' && 'bg-ok',
        tone === 'warn' && 'bg-warn',
        tone === 'bad' && 'bg-destructive',
        tone === 'neutral' && 'bg-foreground/25',
      )}
    />
  )
}

function LogDialog({ id, name, open, onOpenChange }: { id: string; name: string; open: boolean; onOpenChange: (o: boolean) => void }) {
  const { t } = useTranslation()
  const [log, setLog] = useState<{ text?: string; error?: string } | null>(null)
  useEffect(() => {
    if (!open) return
    setLog(null)
    apiText(`/downloads/${id}/log`).then(
      (text) => setLog({ text }),
      (e) => setLog({ error: e.message }),
    )
  }, [open, id])
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-[680px]">
        <DialogHeader>
          <DialogTitle>{t('downloads.logOf', { name })}</DialogTitle>
        </DialogHeader>
        {!log ? (
          <div className="flex items-center gap-2 py-6 text-[13px] text-muted-foreground">
            <Spinner />
            {t('form.loading')}
          </div>
        ) : log.error ? (
          <p className="py-4 text-[13px] text-muted-foreground">{log.error}</p>
        ) : (
          <pre data-selectable className="max-h-[60vh] overflow-auto rounded-lg bg-muted/60 p-3 font-mono text-[11px] leading-relaxed whitespace-pre-wrap">
            {log.text || t('downloads.logEmpty')}
          </pre>
        )}
      </DialogContent>
    </Dialog>
  )
}
