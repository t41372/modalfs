import type { TFunction } from 'i18next'
import { ChevronLeft, FolderOpen, Info, Lock, MoreHorizontal, Pencil, TriangleAlert } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { api } from '@/api/client'
import { patchMount, patchSettings, refreshState, refreshUsage, useAction, useConflicts, useLoadedState, usePending, useTraffic, useUsage, useVolumes } from '@/api/queries'
import type { Mount } from '@/api/types'
import { navigate } from '@/app/nav'
import { useIsDark } from '@/app/theme'
import { AccountMark, Bar, Chip, glyphOf, Hint, Tile } from '@/components/bits'
import { confirm } from '@/components/confirm'
import { Card, Mono, Page, Section } from '@/components/page'
import { Segmented } from '@/components/Segmented'
import { Spinner } from '@/components/Spinner'
import { duration, StopProgress } from '@/components/StopProgress'
import { TrafficChart, type TrafficRange } from '@/components/TrafficChart'
import { VolumeSettings } from '@/components/VolumeSettings'
import { askUnmount, useVolumeMenu } from '@/components/VolumeMenu'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { DropdownMenu, DropdownMenuContent, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { encryptionHint, UnlockDialog } from '@/dialogs/Encryption'
import { ago, bytes, date, homeOf, speed, tilde } from '@/lib/format'
import { live, look, nameOf, toneOf } from '@/lib/mounts'
import { stateText } from './Home'
import { mountError } from '@/lib/mountError'

/** One volume: where it is, what it holds, what's moving, and what can be done with it. */
export function VolumePage({ id }: { id: string }) {
  const { t } = useTranslation()
  const st = useLoadedState()
  const m = st.mounts.find((x) => x.id === id)
  // Removed (here or from the CLI): back to the list.
  useEffect(() => {
    if (!m) navigate('volumes')
  }, [m])
  if (!m) return null
  return <Volume m={m} key={m.id} t={t} />
}

function Volume({ m, t }: { m: Mount; t: TFunction }) {
  const { i18n } = useTranslation()
  const lang = i18n.language
  const st = useLoadedState()
  const home = homeOf(st.settings.mount_root)
  const dark = useIsDark(st.settings.appearance)
  const l = look(m)
  const mounted = m.state === 'mounted'
  const s = m.status
  const [labeling, setLabeling] = useState(false)
  const [unlocking, setUnlocking] = useState(false)
  const menu = useVolumeMenu(m)

  const vols = useVolumes(m.account_id, m.environment ?? '')
  const info = vols.data?.find((v) => (m.volume_id ? v.id === m.volume_id : v.name === m.volume_name))
  const usage = useUsage(m.id)
  const traffic = useTraffic(m.id, mounted || m.state === 'stopping')
  // The range last chosen, kept by the daemon (the page's origin changes with its port).
  const range: TrafficRange = st.settings.chart_range ?? 'minute'
  const setRange = useAction((r: TrafficRange) => patchSettings({ chart_range: r }), {
    quiet: true,
    optimistic: (state, r) => {
      state.settings.chart_range = r
    },
  }).mutate
  const pending = usePending(m.id, (s?.pending_files ?? 0) > 0)
  // The speed now, from the same samples as the chart, so the two agree.
  const samples = traffic.data ?? []
  const now = samples.length >= 2 ? rates(samples[samples.length - 2], samples[samples.length - 1]) : { up: m.up_speed ?? 0, down: m.down_speed ?? 0 }
  const conflicts = useConflicts(m.id, m.open_conflicts ?? 0)

  // A volume never measured is measured when its page opens; a measured one only when asked.
  const measure = useAction(() => api('POST', `/mounts/${m.id}/usage`).then(() => refreshUsage(m.id)), { quiet: true })
  const u = usage.data
  useEffect(() => {
    if (u && !u.last && !u.running && !u.error && m.volume_id && !m.locked && !measure.isPending && !measure.isError) measure.mutate()
  }, [u, m.volume_id]) // eslint-disable-line react-hooks/exhaustive-deps

  const start = useAction(() => api('POST', `/mounts/${m.id}/start`), {
    quiet: true,
    optimistic: (state) => {
      const x = state.mounts.find((y) => y.id === m.id)
      if (x) {
        x.state = 'starting'
        x.step = 'connect'
        x.error = undefined
      }
    },
  })
  const repin = useAction(async () => {
    await patchMount(m.id, { volume_id: '' })
    await api('POST', `/mounts/${m.id}/start`)
  })
  const reveal = (path = '') => api('POST', `/mounts/${m.id}/reveal`, { path }).catch((e) => toast.error(e.message))

  const createdHint = info
    ? [info.format === 'v1' ? 'v1' : 'v2', info.created_at ? t(info.created_by ? 'vol.createdBy' : 'vol.created', { date: date(info.created_at, lang), who: info.created_by }) : '']
        .filter(Boolean)
        .join(' · ')
    : ''
  const nowSec = Date.now() / 1000
  const needsRepin = m.state === 'error' && /re-?pin/i.test(m.error || '')
  const folders = u?.last?.folders ?? []
  const rest = u?.last ? u.last.total - folders.reduce((a, f) => a + f.bytes, 0) : 0
  const top = Math.max(1, ...folders.map((f) => f.bytes), rest)

  return (
    <Page
      title={<LabelTitle m={m} editing={labeling} onEditing={setLabeling} t={t} />}
      back={
        <Button variant="ghost" size="sm" onClick={() => navigate('volumes')} className="text-muted-foreground">
          <ChevronLeft />
          {t('home.title')}
        </Button>
      }
      sub={
        <span className="flex flex-wrap gap-1.5 pt-1">
          {m.label && (
            <Chip mono hint={t('vol.modalName')}>
              {m.volume_name}
            </Chip>
          )}
          <Chip tone={toneOf[l]} glyph={glyphOf[l]} hint={l === 'locked' ? encryptionHint(m, t) : stateText(m, l, t)}>
            {live(m) ? (
              <>
                {t(`vol.chip.${l}`)} <span className="font-mono text-[12px] text-muted-foreground">{tilde(m.mountpoint, home)}</span>
              </>
            ) : (
              t(`vol.chip.${l}`)
            )}
          </Chip>
          <Hint text={[m.workspace && m.workspace !== m.account_label ? m.workspace : '', m.volume_id, createdHint].filter(Boolean).join(' · ') || undefined}>
            <span className="inline-flex h-[26px] items-center gap-1.5 rounded-full bg-muted pr-2.5 pl-1 text-[12.5px]">
              <AccountMark id={m.account_id} label={m.account_label} size={18} />
              {m.account_label}
              {m.environment && <span className="text-muted-foreground">· {m.environment}</span>}
            </span>
          </Hint>
          {m.encrypted && !m.locked && (
            <Chip glyph="lock" hint={encryptionHint(m, t)}>
              {t('enc.chip')}
            </Chip>
          )}
          {m.read_only && <Chip>{t('vol.readOnly')}</Chip>}
          {m.on_conflict === 'overwrite' && <Chip tone="up" glyph="alert">{t('vol.overwrites')}</Chip>}
        </span>
      }
      actions={
        <>
          {mounted ? (
            <>
              <Button onClick={() => reveal()}>
                <FolderOpen />
                {t('vol.finder')}
              </Button>
              <Button variant="outline" onClick={() => askUnmount(m, t)}>
                {t('vol.unmountMenu')}
              </Button>
            </>
          ) : l === 'locked' ? (
            <Button onClick={() => setUnlocking(true)}>
              <Lock />
              {t('enc.unlock')}
            </Button>
          ) : m.state === 'stopped' || m.state === 'error' ? (
            <Button disabled={start.isPending} onClick={() => start.mutate()}>
              {start.isPending && <Spinner />}
              {t(m.state === 'error' ? 'vol.tryAgain' : 'vol.mount')}
            </Button>
          ) : null}
          <DropdownMenu>
            <DropdownMenuTrigger render={<Button variant="outline" size="icon" aria-label={t('vol.more')} />}>
              <MoreHorizontal />
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="min-w-52">
              {menu.items}
            </DropdownMenuContent>
          </DropdownMenu>
        </>
      }
    >
      {/* What needs attention first. */}
      {m.state === 'error' && (
        <Notice tone="bad" title={t('vol.failed')}>
          {mountError(m.error, t).known ? (
            <>
              <div className="text-[13px]">{mountError(m.error, t).text}</div>
              <Mono className="mt-1 block text-[11.5px] text-muted-foreground">{m.error}</Mono>
            </>
          ) : (
            <Mono className="text-[12px]">{m.error}</Mono>
          )}
          {m.retry_at && m.retry_at > nowSec && <div className="mt-1 text-[12px] text-muted-foreground tabular">{t('vol.retryIn', { time: duration(m.retry_at - nowSec, t) })}</div>}
          {needsRepin && (
            <Button size="sm" variant="outline" className="mt-2.5" disabled={repin.isPending} onClick={() => repin.mutate()}>
              {t('vol.repin')}
            </Button>
          )}
        </Notice>
      )}
      {l === 'stalled' && s && (
        <Notice tone="bad" title={t('vol.stalledTitle')}>
          <div className="text-[13px]">
            {mountError(s.upload_error, t).known ? `${mountError(s.upload_error, t).text} ` : ''}
            {t('vol.stalledKept', { size: bytes(s.pending_bytes) })}
            {s.waiting_for_room && ` ${t('vol.stalledFull')}`}
          </div>
          <Mono className="mt-1 block text-[11.5px] text-muted-foreground">
            {s.upload_error}
            {s.upload_error_at ? ` · ${t('vol.stalledSince', { time: duration(Math.max(1, nowSec - s.upload_error_at), t) })}` : ''}
          </Mono>
        </Notice>
      )}
      {m.state === 'starting' && (
        <Notice tone="busy" title={t(`vol.step.${m.step ?? 'connect'}`)}>
          {m.started_at && nowSec - m.started_at >= 3 && <span className="text-[12px] text-muted-foreground tabular">{duration(nowSec - m.started_at, t)}</span>}
        </Notice>
      )}
      {m.state === 'stopping' && (
        <Notice tone="up" title={t(m.unmounted ? 'vol.stoppingBackground' : 'vol.stopping')}>
          <StopProgress m={m} />
          {!m.unmounted && (s?.pending_files ?? 0) > 0 && (
            <div className="mt-2 text-[12px] text-muted-foreground">
              {t('vol.cantWait')}{' '}
              <button className="font-medium text-foreground underline underline-offset-2" onClick={() => askNow(m, t)}>
                {t('vol.unmountNow')}
              </button>
            </div>
          )}
        </Notice>
      )}
      {mounted && m.unmount_error && (
        <Notice tone="up" title={m.in_use_by?.length ? t('vol.busyBy', { apps: m.in_use_by.join(', ') }) : t('vol.busyTitle')}>
          <div className="text-[12.5px] text-muted-foreground">{t('vol.busyP')}</div>
          {!m.in_use_by?.length && <Mono className="mt-1 block text-[11px] text-muted-foreground">{m.unmount_error}</Mono>}
          <div className="mt-2.5 flex flex-wrap gap-2">
            <Button size="sm" variant="outline" onClick={() => api('POST', `/mounts/${m.id}/stop`).catch(() => refreshState())}>
              {t('vol.tryAgain')}
            </Button>
            <Button size="sm" variant="ghost" className="text-destructive hover:bg-destructive/10 hover:text-destructive" onClick={() => askNow(m, t)}>
              {t('vol.force')}
            </Button>
          </div>
        </Notice>
      )}

      <div className="grid grid-cols-1 gap-2.5 sm:grid-cols-3">
        <Tile
          label={t('vol.size')}
          value={u?.last ? bytes(u.last.total).split(' ')[0] : u?.running ? <Spinner className="size-4 text-muted-foreground" /> : '—'}
          unit={u?.last ? bytes(u.last.total).split(' ')[1] : undefined}
          hint={
            u?.running
              ? t('vol.measuring', { size: bytes(u.counted ?? 0), count: u.counted_files ?? 0 })
              : u?.last
                ? t('vol.measuredHint', { ago: ago(u.last.at, lang, t('time.justNow')), count: u.last.files })
                : !m.volume_id
                  ? t('vol.sizeAfterMount')
                  : u?.error || t('vol.sizeUnknown')
          }
        >
          {u?.last && u.last.total > 0 ? (
            <span className="mt-2.5 flex h-1.5 gap-0.5 overflow-hidden rounded-full">
              {folders.slice(0, 4).map((f, i) => (
                <span key={f.name} className="h-full rounded-full bg-brand" style={{ width: `${(f.bytes / u.last!.total) * 100}%`, opacity: 1 - i * 0.2 }} />
              ))}
              <span className="h-full flex-1 rounded-full bg-muted" />
            </span>
          ) : (
            <div className="mt-1 truncate text-[12px] text-muted-foreground">{u?.running ? t('vol.measuringShort', { count: u.counted_files ?? 0 }) : u?.error ? t('vol.measureFailed') : ' '}</div>
          )}
        </Tile>
        <Tile
          label={t('vol.speedTile')}
          value={live(m) && s ? `↑ ${speed(now.up)}` : '—'}
          hint={live(m) && s ? t('vol.sessionHint', { up: bytes(s.bytes_up), down: bytes(s.bytes_down), commits: s.commits, ago: m.started_at ? ago(m.started_at, lang, t('time.justNow')) : '' }) : undefined}
        >
          <div className="mt-1 truncate text-[12px] text-muted-foreground tabular">{live(m) && s ? t('vol.readSpeed', { speed: speed(now.down) }) : m.last_used ? t('home.lastUsed', { ago: ago(m.last_used, lang, t('time.justNow')) }) : t('vol.state.off')}</div>
        </Tile>
        <Tile
          label={t('vol.waiting')}
          value={live(m) && s ? bytes(s.pending_bytes) : '—'}
          tone={(s?.pending_files ?? 0) > 0 ? 'warn' : undefined}
          hint={live(m) && s?.last_commit_at ? t('vol.lastUpload', { ago: ago(s.last_commit_at, lang, t('time.justNow')) }) : undefined}
        >
          <div className="mt-1 truncate text-[12px] text-muted-foreground">
            {!live(m) || !s ? ' ' : s.waiting_for_room ? t('vol.waitingRoom') : s.pending_files > 0 ? t('vol.filesWaiting', { count: s.pending_files }) : t('vol.allOnModal')}
          </div>
        </Tile>
      </div>

      {(m.open_conflicts ?? 0) > 0 && (
        <Section
          label={
            <Hint text={t('vol.conflictsP')}>
              <span className="inline-flex items-center gap-1 text-destructive">
                {t('vol.conflictsTitle', { count: m.open_conflicts })}
                <Info className="size-3.5" />
              </span>
            </Hint>
          }
          aside={
            <Button variant="ghost" size="sm" className="text-muted-foreground" onClick={() => api('POST', `/mounts/${m.id}/conflicts/clear`, {}).finally(refreshState)}>
              {t('vol.allHandled')}
            </Button>
          }
        >
          <Card>
            {(conflicts.data ?? []).map((c) => (
              <div key={c.copy} className="flex flex-wrap items-center gap-x-3 gap-y-2 px-5 py-3">
                <div className="min-w-0 flex-1 basis-60">
                  <Mono className="text-[12.5px]">{c.path}</Mono>
                  <div className="mt-0.5 text-[12px] text-muted-foreground">
                    {t('vol.yoursAs')} <Mono className="text-[11.5px]">{c.copy.split('/').pop()}</Mono> · {ago(c.at, lang, t('time.justNow'))}
                  </div>
                </div>
                {mounted && (
                  <Button size="sm" variant="outline" onClick={() => reveal(c.copy)}>
                    {t('vol.compare')}
                  </Button>
                )}
                <Button size="sm" variant="ghost" onClick={() => api('POST', `/mounts/${m.id}/conflicts/clear`, { copy: c.copy }).finally(refreshState)}>
                  {t('vol.handled')}
                </Button>
              </div>
            ))}
          </Card>
        </Section>
      )}

      {(pending.data?.length ?? 0) > 0 && (
        <Section label={t('vol.waitingFiles')}>
          <Card>
            {pending.data!.slice(0, 6).map((p) => (
              <div key={p.path} className="flex items-center gap-3 px-5 py-2.5">
                <Mono className="min-w-0 flex-1 truncate text-[12.5px]">{p.path}</Mono>
                <span className="shrink-0 text-[12px] text-muted-foreground tabular">{bytes(p.bytes)}</span>
              </div>
            ))}
            {pending.data!.length > 6 && <div className="px-5 py-2.5 text-[12px] text-muted-foreground">{t('vol.moreFiles', { count: pending.data!.length - 6 })}</div>}
          </Card>
        </Section>
      )}

      {(mounted || m.state === 'stopping') && (
        <Section
          label={t('vol.speedOver')}
          aside={
            <Segmented<TrafficRange>
              label={t('vol.speedOver')}
              value={range}
              onChange={setRange}
              options={[
                { value: 'minute', label: t('downloads.range.minute') },
                { value: 'tenMinutes', label: t('downloads.range.tenMinutes') },
                { value: 'hour', label: t('vol.rangeHour') },
              ]}
            />
          }
        >
          <Card>
            <TrafficChart samples={traffic.data ?? []} range={range} dark={dark} />
          </Card>
        </Section>
      )}

      <VolumeSettings m={m} />

      {folders.length > 0 && (
        <Section label={t('vol.byFolder')} aside={<span className="text-[12px] text-muted-foreground">{t('vol.measuredAgo', { ago: ago(u!.last!.at, lang, t('time.justNow')) })}</span>}>
          {/* A volume can hold many folders: past a dozen rows, the list scrolls. */}
          <Card className="max-h-[22rem] overflow-y-auto py-2 [&>*+*]:border-t-0">
            {folders.map((f) => (
              <FolderBar key={f.name} name={`${f.name}/`} size={f.bytes} pct={(f.bytes / top) * 100} hint={t('vol.folderFiles', { count: f.files })} />
            ))}
            {rest > 0 && <FolderBar name={t('vol.everythingElse')} size={rest} pct={(rest / top) * 100} muted />}
          </Card>
        </Section>
      )}

      {m.locked && <UnlockDialog m={m} open={unlocking} onOpenChange={setUnlocking} />}
      {menu.dialogs}
    </Page>
  )
}

/** Unmount without waiting for uploads (and by force if apps use it), after saying what that means. */
function askNow(m: Mount, t: TFunction) {
  const busy = m.state === 'mounted' && !!m.unmount_error
  const pending = (m.status?.pending_files ?? 0) > 0
  const apps = m.in_use_by?.join(', ')
  confirm({
    title: t(busy ? 'vol.forceQ' : 'vol.nowQ', { name: nameOf(m) }),
    desc: busy ? `${apps ? t('vol.forceApps', { apps }) : t('vol.forceAppsUnknown')} ${t(pending ? 'vol.forcePending' : 'vol.forceUploaded')}` : t('vol.nowP'),
    action: t(busy ? 'vol.force' : 'vol.unmountNow'),
    dismiss: t(busy ? 'vol.keepMounted' : 'vol.wait'),
    danger: busy,
    run: () => api('POST', `/mounts/${m.id}/stop?force=1`).catch((e) => toast.error(e.message)).finally(refreshState),
  })
}

function Notice({ tone, title, children }: { tone: 'bad' | 'up' | 'busy'; title: string; children?: React.ReactNode }) {
  return (
    <div className={tone === 'bad' ? 'rounded-2xl bg-destructive/8 px-4 py-3.5' : tone === 'up' ? 'rounded-2xl bg-warn/12 px-4 py-3.5' : 'rounded-2xl bg-brand/8 px-4 py-3.5'}>
      <div className={`flex items-center gap-2 text-[13.5px] font-medium ${tone === 'bad' ? 'text-destructive' : tone === 'up' ? 'text-warn-ink' : 'text-brand'}`}>
        {tone === 'busy' ? <Spinner className="size-3" /> : <TriangleAlert className="size-3.5" />}
        {title}
      </div>
      {children && <div className="mt-1.5 min-w-0 pl-5.5">{children}</div>}
    </div>
  )
}

function FolderBar({ name, size, pct, hint, muted }: { name: string; size: number; pct: number; hint?: string; muted?: boolean }) {
  const row = (
    <div className="grid grid-cols-[minmax(0,9rem)_minmax(0,1fr)_4.5rem] items-center gap-3 px-5 py-1.5 text-[12.5px]">
      <span className={`truncate font-mono text-[12px] ${muted ? 'text-muted-foreground' : ''}`}>{name}</span>
      <Bar pct={pct} tone={muted ? 'muted' : 'brand'} className="h-2" />
      <span className="text-right text-muted-foreground tabular">{bytes(size)}</span>
    </div>
  )
  return hint ? <Hint text={hint}>{row}</Hint> : row
}

/**
 * The page's title: the volume's label, or its name. It's edited in place:
 * Enter or leaving the field saves, Escape keeps the old one, and an empty
 * label goes back to the name. The label is written into the volume, so a
 * read-only mount can't change it.
 */
function LabelTitle({ m, editing, onEditing, t }: { m: Mount; editing: boolean; onEditing: (on: boolean) => void; t: TFunction }) {
  const [text, setText] = useState('')
  const cancelled = useRef(false)
  const save = useAction(
    (label: string) => api('POST', `/accounts/${m.account_id}/volumes/${m.volume_id}/label`, { label, environment: m.environment ?? '' }),
    { success: (label) => t(label ? 'toast.labeled' : 'toast.labelCleared'), volumes: true },
  )
  useEffect(() => {
    if (editing) {
      cancelled.current = false
      setText(m.label ?? '')
    }
  }, [editing]) // eslint-disable-line react-hooks/exhaustive-deps
  const finish = () => {
    onEditing(false)
    const next = text.trim()
    if (!cancelled.current && next !== (m.label ?? '')) save.mutate(next)
  }
  if (editing)
    return (
      <Input
        autoFocus
        value={text}
        maxLength={80}
        placeholder={t('vol.labelPlaceholder', { name: m.volume_name })}
        aria-label={t('vol.editLabel')}
        onChange={(e) => setText(e.target.value)}
        onBlur={finish}
        onKeyDown={(e) => {
          if (e.key === 'Enter') e.currentTarget.blur()
          if (e.key === 'Escape') {
            cancelled.current = true
            e.currentTarget.blur()
          }
        }}
        className="-ml-2.5 h-11 w-[min(26rem,100%)] px-2.5 text-[28px] font-semibold tracking-[-0.02em] md:text-[28px]"
      />
    )
  const shown = save.isPending ? save.variables || m.volume_name : nameOf(m)
  const canEdit = !!m.volume_id && !m.read_only
  return (
    <span className="group/title inline-flex max-w-full items-center gap-1.5">
      <span className="min-w-0 break-words">{shown}</span>
      <Hint text={m.read_only ? t('vol.labelReadOnly') : t('vol.labelHint')}>
        <Button
          variant="ghost"
          size="icon-sm"
          aria-label={t('vol.editLabel')}
          disabled={!canEdit}
          onClick={() => onEditing(true)}
          className="text-muted-foreground opacity-0 transition-opacity group-hover/title:opacity-100 focus-visible:opacity-100 max-sm:opacity-100"
        >
          <Pencil />
        </Button>
      </Hint>
    </span>
  )
}

/** Upload and read speeds between two traffic samples ([time, bytes up, bytes down]). */
function rates(a: [number, number, number], b: [number, number, number]) {
  const dt = Math.max(1, b[0] - a[0])
  return { up: Math.max(0, (b[1] - a[1]) / dt), down: Math.max(0, (b[2] - a[2]) / dt) }
}
