import { cn } from 'cn'
import { ChevronLeft, ExternalLink, FileText, Pause, Play, RotateCcw, Trash2, TriangleAlert, X } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { api, apiText } from '@/api/client'
import { refreshDownloads, refreshState, useAppState, useBilling, useDownloadRecords, useLive } from '@/api/queries'
import type { Billing, Download, DownloadStatus } from '@/api/types'
import { useIsDark } from '@/app/theme'
import { confirm } from '@/components/confirm'
import { Card, Mono, Page, Section } from '@/components/page'
import { Segmented } from '@/components/Segmented'
import { type Range, SpeedChart } from '@/components/SpeedChart'
import { Spinner } from '@/components/Spinner'
import { duration } from '@/components/StopProgress'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Dialog, DialogContent, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { BUSY, ENDED, happenings, hourlyFor, progressText, RESUMABLE, RUNNING, SILENT_WARN, series, speed, type Tone, when } from '@/lib/downloads'
import { bytes } from '@/lib/format'
import { openExternal } from '@/lib/platform'
import { StateBadge, useDownloadAction } from './Downloads'

/** One download: where it stands, its speed over the whole run, and everything that happened to it. */
export function DownloadPage({ d, onBack }: { d: Download; onBack: () => void }) {
  const { t, i18n } = useTranslation()
  const lang = i18n.language
  const appearance = useAppState().data?.settings.appearance
  const dark = useIsDark(appearance)
  const running = RUNNING.has(d.state) || d.state === 'pausing'
  const records = useDownloadRecords(d.id, running)
  const live = useLive(d.id, running)
  const [range, setRange] = useState<Range>('all')
  const [logOpen, setLogOpen] = useState(false)
  const act = useDownloadAction()
  const billing = useBilling(d.account_id, d.environment)

  const s = live.view?.status ?? d.status
  const points = useMemo(() => series(records.history, running ? live.samples : []), [records.history, live.samples, running])
  const events = records.detail?.events ?? []
  const what = useMemo(() => happenings(events, t, lang), [events, t, lang])
  const pct = s && s.total > 0 ? Math.min(100, (s.done / s.total) * 100) : undefined
  const started = events.find((e) => e.ev === 'start')?.t
  const finished = events.findLast((e) => e.ev === 'finished')?.t
  // A download Modal refused to start never ran: there's nothing to chart or count.
  const ran = !!s || records.history.length > 0 || events.length > 0 || running
  const silent = !ENDED.has(d.state) && (d.silent ?? 0) >= SILENT_WARN

  const askCancel = () => cancelDialog(d, t)
  const askRemove = () =>
    confirm({
      title: t('downloads.removeQ', { name: d.name }),
      desc: t(d.state === 'done' ? 'downloads.removeDoneP' : 'downloads.removeP'),
      action: t('form.remove'),
      danger: d.state !== 'done',
      run: () =>
        api('DELETE', `/downloads/${d.id}`)
          .then(() => {
            onBack()
            refreshDownloads()
            refreshState()
          })
          .catch((e) => toast.error(e.message)),
    })

  return (
    <Page
      title={d.name}
      back={
        <Button variant="ghost" size="sm" onClick={onBack} className="text-muted-foreground">
          <ChevronLeft />
          {t('downloads.title')}
        </Button>
      }
      sub={
        <span className="flex flex-wrap items-center gap-2">
          <StateBadge d={d} />
          <Mono className="text-[12.5px] text-muted-foreground">{d.url}</Mono>
        </span>
      }
      actions={
        <>
          {(running || d.state === 'pausing' || d.state === 'cancelling') && (
            <Button variant="outline" disabled={!running || d.state === 'pausing' || d.state === 'finishing' || act.isPending} onClick={() => act.mutate({ id: d.id, action: 'pause' })}>
              {d.state === 'pausing' ? <Spinner /> : <Pause />}
              {t(d.state === 'pausing' ? 'downloads.state.pausing' : 'downloads.pause')}
            </Button>
          )}
          {RESUMABLE.has(d.state) && (
            <Button disabled={act.isPending} onClick={() => act.mutate({ id: d.id, action: 'resume' })}>
              {ran ? <Play /> : <RotateCcw />}
              {t(ran ? 'downloads.resume' : 'downloads.retry')}
            </Button>
          )}
        </>
      }
    >
      {(d.state === 'failed' || d.state === 'stalled') && (d.error || s?.log_tail?.length) && <Problem d={d} s={s} billing={billing.data} />}
      {silent && <Silent d={d} billing={billing.data} />}

      {ran && (
      <div>
        <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
          <Stat value={s ? (pct !== undefined && d.state !== 'done' ? `${pct.toFixed(pct < 10 ? 1 : 0)}%` : bytes(s.done)) : '—'} label={s && s.total > 0 && d.state !== 'done' ? progressText(d, t) : t('downloads.downloaded')} />
          <Stat value={running && s ? speed(s.speed) : '—'} label={t('downloads.speed')} live={running} />
          {d.state === 'done' && started && finished ? (
            <Stat value={duration(finished - started, t)} label={t('downloads.took')} />
          ) : (
            <Stat value={running && s?.eta ? duration(s.eta, t) : '—'} label={t('downloads.timeLeft')} />
          )}
          <Stat value={String(s?.attempt ?? (events.filter((e) => e.ev === 'start').length || 1))} label={t('downloads.attempts')} />
        </div>
        {pct !== undefined && d.state !== 'done' && (
          <span className="mt-3 block h-1.5 overflow-hidden rounded-full bg-muted" role="progressbar" aria-valuenow={Math.round(pct)} aria-valuemin={0} aria-valuemax={100}>
            <span className={cn('block h-full rounded-full transition-[width] duration-300 ease-linear', running ? 'bg-brand' : 'bg-foreground/25')} style={{ width: `${pct}%` }} />
          </span>
        )}
      </div>
      )}

      {ran && (
      <Section
        label={t('downloads.speedOver')}
        aside={
          <Segmented<Range>
            label={t('downloads.speedOver')}
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
      )}

      {what.length > 0 && (
        <Section label={t('downloads.happened')}>
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

      <Section label={t('downloads.about')}>
        <Card>
          <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-6 gap-y-2.5 px-5 py-4 text-[12.5px]">
            {s?.files && s.files.length > 0 ? (
              <>
                <dt className="text-muted-foreground">{t('downloads.savedAs')}</dt>
                <dd className="flex flex-col gap-1">
                  {s.files.map((f) => (
                    <span key={f.path}>
                      <Mono>
                        {d.volume_name}:/{f.path}
                      </Mono>
                      <span className="ml-2 text-muted-foreground">{bytes(f.size)}</span>
                      {f.sha256 && <Mono className="block text-[11px] text-muted-foreground">SHA-256 {f.sha256}</Mono>}
                    </span>
                  ))}
                </dd>
              </>
            ) : (
              <>
                <dt className="text-muted-foreground">{t('downloads.into')}</dt>
                <dd>
                  <Mono>
                    {d.volume_name}:/{d.folder}
                  </Mono>
                </dd>
              </>
            )}
            <dt className="text-muted-foreground">{t('downloads.tool')}</dt>
            <dd>
              {s?.tool || d.tool}
              {s?.tool && s.tool !== d.tool && <span className="text-muted-foreground"> {t('downloads.insteadOf', { tool: d.tool })}</span>}
            </dd>
            <dt className="text-muted-foreground">{t('downloads.size')}</dt>
            <dd>
              {t(`downloads.sizes.${d.size}`)}
              <span className="text-muted-foreground"> · {t('downloads.perHour', { price: hourlyFor(d.size, billing.data).toFixed(4) })}</span>
            </dd>
            <dt className="text-muted-foreground">{t('form.account')}</dt>
            <dd data-selectable>
              {d.account_label}
              {d.environment && <span className="text-muted-foreground"> · {d.environment}</span>}
            </dd>
            <dt className="text-muted-foreground">{t('downloads.addedAt')}</dt>
            <dd>{when(d.created, lang)}</dd>
          </dl>
        </Card>
      </Section>

      <div className="flex flex-wrap items-center gap-2">
        {ran && (
          <Button variant="outline" size="sm" onClick={() => setLogOpen(true)}>
            <FileText />
            {t('downloads.log')}
          </Button>
        )}
        <span className="flex-1" />
        {ran && (!ENDED.has(d.state) || RESUMABLE.has(d.state)) ? (
          <Button variant="ghost" size="sm" className="text-destructive hover:bg-destructive/10 hover:text-destructive" disabled={BUSY.has(d.state)} onClick={askCancel}>
            <X />
            {t('downloads.cancel')}
          </Button>
        ) : null}
        {ENDED.has(d.state) && (
          <Button variant="ghost" size="sm" className={cn(d.state !== 'done' && 'text-destructive hover:bg-destructive/10 hover:text-destructive')} onClick={askRemove}>
            <Trash2 />
            {t('downloads.remove')}
          </Button>
        )}
      </div>
      <LogDialog id={d.id} name={d.name} open={logOpen} onOpenChange={setLogOpen} />
    </Page>
  )
}

/** Why it failed, in plain words, with Modal's own message under it when Modal was the reason. */
function Problem({ d, s, billing }: { d: Download; s?: DownloadStatus; billing?: Billing }) {
  const { t } = useTranslation()
  const kind = d.error_kind
  return (
    <div data-selectable className="-mt-2 flex gap-2 rounded-xl bg-destructive/8 px-4 py-3 text-[12.5px] leading-relaxed text-destructive">
      <TriangleAlert className="mt-0.5 size-4 shrink-0" />
      <div className="min-w-0">
        <div className="font-medium text-pretty">{kind ? t(`downloads.why.${kind}`) : d.error || t(`downloads.state.${d.state}`)}</div>
        {kind && d.error && <div className="mt-1 font-mono text-[11px] break-words text-destructive/80">{t('downloads.modalSaid', { message: d.error })}</div>}
        {s?.log_tail && s.log_tail.length > 0 && <pre className="mt-1.5 max-h-28 overflow-auto font-mono text-[11px] whitespace-pre-wrap text-destructive/80">{s.log_tail.slice(-6).join('\n')}</pre>}
        {kind && <BillingLink billing={billing} className="mt-2" />}
      </div>
    </div>
  )
}

/** Running, but nothing heard from its container for longer than Modal takes to move one. */
function Silent({ d, billing }: { d: Download; billing?: Billing }) {
  const { t } = useTranslation()
  return (
    <div className="-mt-2 flex gap-2 rounded-xl bg-warn/15 px-4 py-3 text-[12.5px] leading-relaxed text-warn-ink">
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

function Stat({ value, label, live }: { value: string; label: string; live?: boolean }) {
  return (
    <div className="rounded-xl bg-muted/60 px-3.5 py-2.5">
      <div className="flex items-center gap-1.5 text-[17px] font-semibold tracking-[-0.01em] tabular">
        {value}
        {live && <span className="size-1.5 animate-pulse rounded-full bg-ok" aria-hidden />}
      </div>
      <div className="mt-0.5 truncate text-[11.5px] text-muted-foreground">{label}</div>
    </div>
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

/** Cancel: stop it, and delete what it downloaded so far unless asked to keep it. */
function cancelDialog(d: Download, t: ReturnType<typeof useTranslation>['t']) {
  let keep = false
  confirm({
    title: t('downloads.cancelQ', { name: d.name }),
    desc: t('downloads.cancelP'),
    extra: (
      <label className="flex items-center gap-2 text-[13px]">
        <Checkbox onCheckedChange={(v) => (keep = v === true)} />
        {t('downloads.keepPartial')}
      </label>
    ),
    action: t('downloads.cancel'),
    danger: true,
    run: () =>
      api('POST', `/downloads/${d.id}/cancel`, { keep })
        .catch((e) => toast.error(e.message))
        .finally(refreshDownloads),
  })
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
