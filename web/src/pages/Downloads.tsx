import { ChevronDown, CloudDownload, ExternalLink, Plus } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api } from '@/api/client'
import { patchSettings, refreshDownloads, useAction, useBilling, useDownloads, useLoadedState } from '@/api/queries'
import type { Download, DownloadState } from '@/api/types'
import { navigate } from '@/app/nav'
import { Bar, type Glyph, StatusIcon, Tile } from '@/components/bits'
import { Card, Empty, Page, Section } from '@/components/page'
import { duration } from '@/components/StopProgress'
import { Button } from '@/components/ui/button'
import { AddDownloadDialog } from '@/dialogs/AddDownload'
import { dollars, ENDED, HOURLY, hourlyFor, NEEDS_YOU, permanent, RESUMABLE, RUNNING, SILENT_WARN, speed } from '@/lib/downloads'
import { bytes } from '@/lib/format'
import type { Tone } from '@/lib/mounts'
import { openExternal } from '@/lib/platform'

/** A download's state as an icon: the shape says what it's doing, the color backs it up. */
export function downloadGlyph(s: DownloadState): { glyph: Glyph; tone: Tone } {
  if (s === 'running') return { glyph: 'down', tone: 'busy' }
  if (s === 'interrupted' || s === 'retrying') return { glyph: 'moving', tone: 'up' }
  if (s === 'paused' || s === 'time limit') return { glyph: 'pause', tone: 'off' }
  if (s === 'failed' || s === 'stalled') return { glyph: 'alert', tone: 'bad' }
  if (s === 'done') return { glyph: 'check', tone: 'ok' }
  if (s === 'cancelled') return { glyph: 'off', tone: 'off' }
  return { glyph: 'spin', tone: 'busy' }
}

/** Downloads Modal runs into a volume: what's running and what it costs, what needs a decision, and what's done. */
export function Downloads() {
  const { t } = useTranslation()
  const st = useLoadedState()
  const on = !!st.settings.remote_downloads
  const list = useDownloads(on)
  const downloads = list.data ?? st.downloads ?? []
  const [adding, setAdding] = useState(false)
  const [allDone, setAllDone] = useState(false)

  if (!on) return <TurnOn />

  const newest = [...downloads].reverse()
  const attention = newest.filter((d) => NEEDS_YOU.has(d.state) || (!ENDED.has(d.state) && (d.silent ?? 0) >= SILENT_WARN))
  const going = newest.filter((d) => !attention.includes(d) && (!ENDED.has(d.state) || d.state === 'paused'))
  const ended = newest.filter((d) => !attention.includes(d) && !going.includes(d))
  const running = downloads.filter((d) => RUNNING.has(d.state))
  const canAdd = st.accounts.length > 0

  return (
    <Page
      title={t('dl.title')}
      sub={t('dl.sub')}
      actions={
        canAdd && (
          <Button onClick={() => setAdding(true)}>
            <Plus />
            {t('dl.add')}
          </Button>
        )
      }
    >
      {downloads.length > 0 && <Figures running={running} account={running[0]?.account_id ?? downloads.at(-1)?.account_id} env={running[0]?.environment ?? downloads.at(-1)?.environment} />}
      {downloads.length === 0 ? (
        <Card>
          <Empty icon={<CloudDownload />} title={t('dl.empty')} desc={canAdd ? t('dl.emptyP') : t('dl.emptyNoAccount')}>
            {canAdd && (
              <Button onClick={() => setAdding(true)}>
                <Plus />
                {t('dl.add')}
              </Button>
            )}
          </Empty>
        </Card>
      ) : (
        <>
          {attention.length > 0 && (
            <Section label={<span className="text-destructive">{t('dl.attention')}</span>}>
              <Card>
                {attention.map((d) => (
                  <DownloadRow key={d.id} d={d} />
                ))}
              </Card>
            </Section>
          )}
          {going.length > 0 && (
            <Section label={t('dl.going')} aside={<span className="text-[12px] text-muted-foreground">{t('dl.pausedFree')}</span>}>
              <Card>
                {going.map((d) => (
                  <DownloadRow key={d.id} d={d} />
                ))}
              </Card>
            </Section>
          )}
          {ended.length > 0 && (
            <Section label={t('dl.done')} aside={<span className="text-[12px] text-muted-foreground tabular">{t('dl.doneTotal', { count: ended.length })}</span>}>
              <Card>
                {(allDone ? ended : ended.slice(0, 3)).map((d) => (
                  <DownloadRow key={d.id} d={d} />
                ))}
                {ended.length > 3 && (
                  <button className="flex w-full items-center gap-1.5 px-5 py-2.5 text-left text-[12.5px] text-muted-foreground hover:text-foreground" onClick={() => setAllDone((x) => !x)}>
                    <ChevronDown className={`size-3.5 transition-transform ${allDone ? 'rotate-180' : ''}`} />
                    {allDone ? t('dl.showFewer') : t('dl.showAll', { count: ended.length })}
                  </button>
                )}
              </Card>
            </Section>
          )}
        </>
      )}
      <AddDownloadDialog open={adding} onOpenChange={setAdding} />
    </Page>
  )
}

/** The numbers at the top: how many run, how fast in all, and what it costs now. */
function Figures({ running, account, env }: { running: Download[]; account?: string; env?: string }) {
  const { t } = useTranslation()
  const billing = useBilling(account, env ?? '')
  const total = running.reduce((n, d) => n + (d.state === 'running' ? (d.status?.speed ?? 0) : 0), 0)
  const hourly = running.reduce((n, d) => n + hourlyFor(d.size, billing.data), 0)
  const month = billing.data?.downloads
  return (
    <div className="grid grid-cols-3 gap-2.5">
      <Tile label={t('dl.running')} value={running.length} />
      <Tile label={t('dl.totalSpeed')} value={running.length ? speed(total).split(' ')[0] : '—'} unit={running.length ? speed(total).split(' ')[1] : undefined} />
      <button className="text-left" onClick={() => openExternal(billing.data?.usage_url || 'https://modal.com/settings/usage')}>
        <Tile
          label={t('dl.perHour')}
          value={dollars(hourly)}
          hint={
            billing.data
              ? `${t('dl.monthDownloads', { amount: month !== undefined ? dollars(month) : '—' })} · ${t('dl.monthWorkspace', { amount: dollars(billing.data.metered) })} · ${t('dl.noLimitInfo')}`
              : t('dl.noLimitInfo')
          }
          className="h-full transition-colors hover:bg-muted/50"
        >
          <div className="mt-1 flex items-center gap-1 truncate text-[12px] text-muted-foreground">
            {month !== undefined ? t('dl.thisMonth', { amount: dollars(month) }) : t('dl.billing')}
            <ExternalLink className="size-3 shrink-0" />
          </div>
        </Tile>
      </button>
    </div>
  )
}

/** Remote downloads are off until turned on: what they do, what they cost, and the one button. */
function TurnOn() {
  const { t } = useTranslation()
  const set = useAction((on: boolean) => patchSettings({ remote_downloads: on }), {
    optimistic: (s, on) => {
      s.settings.remote_downloads = on
    },
  })
  return (
    <Page title={t('dl.title')}>
      <Card className="max-w-[540px]">
        <div className="px-6 pt-6 pb-5">
          <div className="mb-4 flex size-11 items-center justify-center rounded-2xl bg-brand/10 text-brand">
            <CloudDownload className="size-5.5" />
          </div>
          <div className="text-[17px] font-semibold tracking-[-0.01em]">{t('dl.offTitle')}</div>
          <p className="mt-1.5 text-[13.5px] leading-relaxed text-pretty text-muted-foreground">{t('dl.offP')}</p>
          <dl className="mt-4 grid grid-cols-3 gap-2 text-center">
            <div className="rounded-xl bg-muted/60 px-2 py-2.5">
              <dt className="text-[11.5px] text-muted-foreground">{t('dl.offPrice')}</dt>
              <dd className="mt-0.5 text-[15px] font-semibold tabular">${HOURLY.small.toFixed(4)}</dd>
            </div>
            <div className="rounded-xl bg-muted/60 px-2 py-2.5">
              <dt className="text-[11.5px] text-muted-foreground">{t('dl.offBilled')}</dt>
              <dd className="mt-0.5 text-[15px] font-semibold">{t('dl.offPerSecond')}</dd>
            </div>
            <div className="rounded-xl bg-muted/60 px-2 py-2.5">
              <dt className="text-[11.5px] text-muted-foreground">{t('dl.offPaused')}</dt>
              <dd className="mt-0.5 text-[15px] font-semibold">$0</dd>
            </div>
          </dl>
        </div>
        <div className="flex flex-wrap items-center justify-between gap-3 px-6 py-4">
          <span className="text-[12.5px] text-muted-foreground">{t('dl.offMounting')}</span>
          <Button disabled={set.isPending} onClick={() => set.mutate(true)}>
            {t('dl.turnOn')}
          </Button>
        </div>
      </Card>
    </Page>
  )
}

function DownloadRow({ d }: { d: Download }) {
  const { t } = useTranslation()
  const resume = useAction(() => api('POST', `/downloads/${d.id}/resume`, {}).finally(refreshDownloads), { success: () => t('dl.resuming', { name: d.name }) })
  const s = d.status
  const { glyph, tone } = downloadGlyph(d.state)
  const pct = s && s.total > 0 ? Math.min(100, (s.done / s.total) * 100) : undefined
  const going = !ENDED.has(d.state) || d.state === 'paused'
  const silent = !ENDED.has(d.state) && (d.silent ?? 0) >= SILENT_WARN
  const size = d.state === 'done' && s ? bytes(s.total > 0 ? s.total : s.done) : s && (s.done > 0 || s.total > 0) ? (s.total > 0 ? `${bytes(s.done)} / ${bytes(s.total)}` : bytes(s.done)) : ''
  const why =
    d.state === 'failed' || d.state === 'stalled'
      ? d.error_kind
        ? t(`dl.whyShort.${d.error_kind}`)
        : permanent(d)
          ? t('dl.gone')
          : d.error || t(`dl.state.${d.state}`)
      : silent
        ? t('dl.silentShort', { time: duration(d.silent ?? 0, t) })
        : ''

  return (
    <div className="flex items-center gap-3 pr-3 pl-4">
      <StatusIcon glyph={glyph} tone={tone} label={t(`dl.state.${d.state}`)} />
      <button onClick={() => navigate('downloads', d.id)} className="group/row flex min-w-0 flex-1 items-center gap-3 rounded-lg py-3 text-left outline-none focus-visible:ring-2 focus-visible:ring-ring/50">
        <span className="min-w-0 flex-1">
          <span className="block truncate text-[14.5px] font-semibold tracking-[-0.01em]">{d.name}</span>
          <span className="mt-0.5 flex min-w-0 items-center gap-2 text-[12.5px] text-muted-foreground">
            <span className="truncate font-mono text-[11.5px]">
              → {d.volume_name}:/{d.folder}
            </span>
            {size && <span className="shrink-0 tabular">{size}</span>}
            {d.warp && (
              <span className="shrink-0 rounded bg-muted px-1 text-[10.5px] font-medium tracking-wide" title={t('dl.warpHint')}>
                WARP
              </span>
            )}
          </span>
          {why && <span className="mt-0.5 block truncate text-[12px] text-destructive">{why}</span>}
          {going && pct !== undefined && <Bar pct={pct} tone={d.state === 'running' ? 'brand' : d.state === 'interrupted' ? 'warn' : 'muted'} className="mt-2 h-1 max-w-md" />}
        </span>
      </button>
      <div className="flex w-[104px] shrink-0 flex-col items-end text-right">
        {d.state === 'running' && s ? (
          <>
            <span className="text-[13px] font-medium tabular">{speed(s.speed)}</span>
            {s.eta ? <span className="text-[12px] text-muted-foreground tabular">{t('dl.left', { time: duration(s.eta, t) })}</span> : null}
          </>
        ) : RESUMABLE.has(d.state) && !permanent(d) && d.state !== 'failed' ? (
          <Button size="sm" variant="outline" disabled={resume.isPending} onClick={() => resume.mutate()}>
            {t('dl.resume')}
          </Button>
        ) : (
          <span className="text-[12px] text-muted-foreground">{t(`dl.state.${d.state}`)}</span>
        )}
      </div>
    </div>
  )
}
