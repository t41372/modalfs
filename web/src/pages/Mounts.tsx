import { Collapsible as CollapsiblePrimitive } from '@base-ui/react/collapsible'
import type { TFunction } from 'i18next'
import { cn } from 'cn'
import { ArrowUp, ChevronRight, FolderOpen, HardDrive, Plus, RefreshCw, Trash2, TriangleAlert } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { api } from '@/api/client'
import { patchMount, refreshState, useAction, useLoadedState } from '@/api/queries'
import type { ConflictPolicy, Mount, MountMode } from '@/api/types'
import { confirm } from '@/components/confirm'
import { Badge, Card, Empty, Mono, Page, Row, Section } from '@/components/page'
import { Segmented } from '@/components/Segmented'
import { Spinner } from '@/components/Spinner'
import { duration, StopProgress } from '@/components/StopProgress'
import { Button } from '@/components/ui/button'
import { Switch } from '@/components/ui/switch'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { NewMountDialog } from '@/dialogs/NewMount'
import { AddAccountDialog } from '@/dialogs/AddAccount'
import { ago, bytes, homeOf, tilde } from '@/lib/format'
import { isMac } from '@/lib/platform'

const live = (m: Mount) => m.state === 'mounted' || m.state === 'starting' || m.state === 'stopping'

export function Mounts() {
  const { t } = useTranslation()
  const st = useLoadedState()
  const [adding, setAdding] = useState(false)
  const [addingAccount, setAddingAccount] = useState(false)
  const [open, setOpen] = useState<Record<string, boolean>>({})
  const hasAccounts = st.accounts.length > 0

  const on = st.mounts.filter(live)
  const off = st.mounts.filter((m) => !live(m))
  const toggle = (id: string) => setOpen((o) => ({ ...o, [id]: !o[id] }))

  return (
    <Page
      title={t('mounts.title')}
      sub={t('mounts.sub')}
      actions={
        hasAccounts &&
        st.mounts.length > 0 && (
          <Button variant="outline" onClick={() => setAdding(true)}>
            <Plus />
            {t('mounts.add')}
          </Button>
        )
      }
    >
      {st.mounts.length === 0 ? (
        <Card>
          <Empty icon={<HardDrive />} title={t('mounts.empty')} desc={hasAccounts ? t('mounts.emptyP') : t('mounts.emptyNoAccount')}>
            {hasAccounts ? (
              <Button onClick={() => setAdding(true)}>
                <Plus />
                {t('mounts.pickVolume')}
              </Button>
            ) : (
              <Button onClick={() => setAddingAccount(true)}>
                <Plus />
                {t('accounts.add')}
              </Button>
            )}
          </Empty>
        </Card>
      ) : (
        <>
          {on.length > 0 && (
            <Section label={t('mounts.sectionOn')}>
              <Card>
                {on.map((m) => (
                  <MountRow key={m.id} m={m} open={!!open[m.id]} onToggle={() => toggle(m.id)} />
                ))}
              </Card>
            </Section>
          )}
          {off.length > 0 && (
            <Section label={t('mounts.sectionOff')}>
              <Card>
                {off.map((m) => (
                  <MountRow key={m.id} m={m} open={!!open[m.id]} onToggle={() => toggle(m.id)} />
                ))}
              </Card>
            </Section>
          )}
        </>
      )}
      <NewMountDialog open={adding} onOpenChange={setAdding} />
      <AddAccountDialog open={addingAccount} onOpenChange={setAddingAccount} />
    </Page>
  )
}

function StateDot({ state }: { state: Mount['state'] }) {
  return (
    <span className="relative flex size-2.5 shrink-0 items-center justify-center" aria-hidden>
      {(state === 'starting' || state === 'stopping') && <span className="absolute inset-0 animate-ping rounded-full bg-warn/60 [animation-duration:1.4s]" />}
      <span
        className={cn(
          'relative size-2 rounded-full',
          state === 'mounted' && 'bg-ok shadow-[0_0_0_3px] shadow-ok/15',
          (state === 'starting' || state === 'stopping') && 'bg-warn',
          state === 'stopped' && 'bg-foreground/20',
          state === 'error' && 'bg-destructive shadow-[0_0_0_3px] shadow-destructive/15',
        )}
      />
    </span>
  )
}

/** Unmount without waiting for uploads (and by force if it's in use), after saying what that means. */
function askUnmountNow(m: Mount, t: TFunction) {
  const apps = m.in_use_by?.join(', ')
  const busy = m.state === 'mounted' && !!m.unmount_error
  const pending = (m.status?.pending_files ?? 0) > 0
  confirm({
    title: t(busy ? 'mounts.forceQ' : 'mounts.nowQ', { name: m.volume_name }),
    desc: busy
      ? `${apps ? t('mounts.forceApps', { apps }) : t('mounts.forceAppsUnknown')} ${t(pending ? 'mounts.forcePending' : 'mounts.forceUploaded')}`
      : t('mounts.nowP'),
    action: t(busy ? 'mounts.forceUnmount' : 'mounts.unmountNow'),
    danger: true,
    run: () => api('POST', `/mounts/${m.id}/stop?force=1`).catch((e) => toastError(e)).finally(refreshState),
  })
}

function MountRow({ m, open, onToggle }: { m: Mount; open: boolean; onToggle: () => void }) {
  const { t, i18n } = useTranslation()
  const st = useLoadedState()
  const home = homeOf(st.settings.mount_root)
  const busy = m.state === 'starting' || m.state === 'stopping'
  const s = m.status

  // Failures come back as events (toasted) and stay on the row, so the call itself doesn't toast.
  const power = useAction(
    (start: boolean) => api('POST', `/mounts/${m.id}/${start ? 'start' : 'stop'}`),
    {
      quiet: true,
      optimistic: (state, start) => {
        const x = state.mounts.find((y) => y.id === m.id)
        if (!x) return
        x.state = start ? 'starting' : 'stopping'
        x.unmount_error = undefined
        if (start) x.step = 'connect'
        else if (x.status) {
          x.stopping_since = Math.floor(Date.now() / 1000)
          x.stop_bytes_up = x.status.bytes_up
          x.stop_pending_bytes = x.status.pending_bytes
        }
      },
    },
  )
  const reveal = () => api('POST', `/mounts/${m.id}/reveal`).catch(() => {})
  const nowSec = Date.now() / 1000

  return (
    <CollapsiblePrimitive.Root open={open} onOpenChange={onToggle}>
      <div className="flex items-center gap-3 pr-4 pl-1.5">
        <CollapsiblePrimitive.Trigger className="group/trigger flex min-w-0 flex-1 items-center gap-3 rounded-xl py-3.5 pl-3.5 text-left outline-none focus-visible:ring-2 focus-visible:ring-ring/50">
          <StateDot state={m.state} />
          <div className="min-w-0 flex-1">
            <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
              <span className="truncate text-[15px] font-semibold tracking-[-0.01em]">{m.volume_name}</span>
              {m.state === 'mounted' && s && s.pending_files > 0 && (
                <Badge tone="warn">
                  <ArrowUp />
                  {s.pending_files}
                </Badge>
              )}
              {m.state === 'mounted' && s && s.conflicts > 0 && <Badge tone="bad">{t('mounts.conflicts', { count: s.conflicts })}</Badge>}
              {m.read_only && <Badge>{t('mounts.readOnly')}</Badge>}
              {(busy || m.state === 'error') && (
                <Badge tone={m.state === 'error' ? 'bad' : 'warn'}>{m.unmounted ? t('mounts.uploadingBadge') : t(`state.${m.state}`)}</Badge>
              )}
            </div>
            {/* Narrow windows put the folder on a line of its own rather than cutting both short. */}
            <div className="mt-0.5 flex min-w-0 flex-col text-[12.5px] text-muted-foreground sm:flex-row sm:items-center sm:gap-1.5">
              <span className="truncate">
                {m.workspace || m.account_label}
                {m.environment ? ` · ${m.environment}` : ''}
              </span>
              <span aria-hidden className="hidden sm:inline">
                ·
              </span>
              <span className="truncate font-mono text-[11.5px]">{tilde(m.mountpoint, home)}</span>
            </div>
            {m.state === 'starting' && (
              <div className="mt-1 text-[12px] text-muted-foreground tabular">
                {t(`mounts.step.${m.step ?? 'connect'}`)}
                {m.started_at && nowSec - m.started_at >= 3 ? ` · ${duration(nowSec - m.started_at, t)}` : ''}
              </div>
            )}
            {m.state === 'error' && m.error && (
              <div data-selectable className="mt-1.5 line-clamp-2 text-[12px] leading-relaxed text-destructive">
                {m.error}
              </div>
            )}
            {m.state === 'error' && m.retry_at && m.retry_at > nowSec && (
              <div className="mt-1 text-[12px] text-muted-foreground tabular">{t('mounts.retryIn', { time: duration(m.retry_at - nowSec, t) })}</div>
            )}
          </div>
          <ChevronRight
            aria-label={t('mounts.details')}
            className="size-4 shrink-0 text-muted-foreground/70 transition-transform duration-200 ease-out group-data-[panel-open]/trigger:rotate-90"
          />
        </CollapsiblePrimitive.Trigger>
        {m.state === 'mounted' && (
          <Tooltip>
            <TooltipTrigger
              render={
                <Button variant="ghost" size="icon" onClick={reveal} aria-label={t(isMac ? 'mounts.showFinder' : 'mounts.openFolder')}>
                  <FolderOpen className="text-muted-foreground" />
                </Button>
              }
            />
            <TooltipContent>{t(isMac ? 'mounts.showFinder' : 'mounts.openFolder')}</TooltipContent>
          </Tooltip>
        )}
        <Switch
          checked={m.state === 'mounted' || m.state === 'starting'}
          disabled={busy || power.isPending}
          onCheckedChange={(v) => power.mutate(v)}
          aria-label={t(m.state === 'mounted' ? 'mounts.unmount' : 'mounts.mount', { name: m.volume_name })}
        />
      </div>
      {m.state === 'stopping' && (
        <div className="-mt-1.5 flex flex-wrap items-end justify-between gap-x-4 gap-y-2 pr-4 pb-3.5 pl-[42px]">
          <div className="min-w-0 flex-1 basis-56">
            <StopProgress m={m} />
            {!m.unmounted && (s?.pending_files ?? 0) > 0 && <p className="mt-1 text-[12px] text-muted-foreground">{t('mounts.stopWaits')}</p>}
          </div>
          {!m.unmounted && (
            <Button variant="outline" size="sm" onClick={() => askUnmountNow(m, t)}>
              {t('mounts.unmountNow')}
            </Button>
          )}
        </div>
      )}
      {m.state === 'mounted' && m.unmount_error && (
        <div className="-mt-1.5 pr-4 pb-3.5 pl-[42px]">
          <div className="flex flex-wrap items-center gap-x-4 gap-y-2.5 rounded-lg bg-warn/12 px-3 py-2.5">
            <div className="flex min-w-0 flex-1 basis-56 gap-1.5 text-[12px] leading-relaxed">
              <TriangleAlert className="mt-0.5 size-3.5 shrink-0 text-warn-ink" />
              <div className="min-w-0">
                <div className="font-medium text-warn-ink">
                  {m.in_use_by?.length ? t('mounts.busyBy', { apps: m.in_use_by.join(', ') }) : t('mounts.busyTitle')}
                </div>
                <div className="text-muted-foreground">{t('mounts.busyP')}</div>
                {!m.in_use_by?.length && (
                  <div data-selectable className="mt-0.5 line-clamp-2 font-mono text-[11px] text-muted-foreground">
                    {m.unmount_error}
                  </div>
                )}
              </div>
            </div>
            <div className="flex shrink-0 gap-2">
              <Button variant="outline" size="sm" disabled={power.isPending} onClick={() => power.mutate(false)}>
                {t('mounts.tryAgain')}
              </Button>
              <Button variant="outline" size="sm" className="text-destructive hover:text-destructive" onClick={() => askUnmountNow(m, t)}>
                {t('mounts.forceUnmount')}
              </Button>
            </div>
          </div>
        </div>
      )}
      <CollapsiblePrimitive.Panel className="h-(--collapsible-panel-height) overflow-hidden transition-[height] duration-250 ease-[var(--ease-out)] data-[ending-style]:h-0 data-[starting-style]:h-0 motion-reduce:transition-none">
        <MountDetail m={m} lang={i18n.language} home={home} />
      </CollapsiblePrimitive.Panel>
    </CollapsiblePrimitive.Root>
  )
}

function MountDetail({ m, lang, home }: { m: Mount; lang: string; home: string }) {
  const { t } = useTranslation()
  const s = m.status
  const opt = useAction((body: Partial<Mount>) => patchMount(m.id, body), {
    optimistic: (state, body) => {
      const x = state.mounts.find((y) => y.id === m.id)
      if (x) Object.assign(x, body)
    },
  })
  const sync = useAction(() => api('POST', `/mounts/${m.id}/sync`), { success: () => t('toast.synced') })
  const repin = useAction(async () => {
    await patchMount(m.id, { volume_id: '' })
    await api('POST', `/mounts/${m.id}/start`)
  })
  const needsRepin = m.state === 'error' && /re-?pin/i.test(m.error || '')

  const askRemove = () =>
    confirm({
      title: t('mounts.removeQ', { name: m.volume_name }),
      desc: t('mounts.removeP'),
      action: t('form.remove'),
      danger: true,
      run: () => api('DELETE', `/mounts/${m.id}`).catch((e) => toastError(e)),
    })

  const modeLabel: Record<string, string> = { auto: t('mounts.modeAuto'), nfs: 'NFS', fuse: 'FUSE' }

  return (
    <div className="flex flex-col gap-5 px-5 pt-1 pb-5 sm:pl-[42px]">
      {(m.state === 'mounted' || m.state === 'stopping') && s && (
        <div>
          <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
            <Stat value={String(s.pending_files)} label={t('mounts.pending')} warn={s.pending_files > 0} />
            <Stat value={bytes(s.bytes_down)} label={t('mounts.downloaded')} />
            <Stat value={bytes(s.bytes_up)} label={t('mounts.uploaded')} />
            <Stat value={String(s.commits)} label={t('mounts.commits')} />
          </div>
          <div className="mt-2.5 flex flex-wrap items-center gap-x-1.5 px-0.5 text-[12px] text-muted-foreground">
            {s.pending_files > 0 ? (
              <span className="text-warn-ink">{t('mounts.pendingN', { count: s.pending_files })}</span>
            ) : (
              <span className="text-ok-ink">{t('mounts.allSynced')}</span>
            )}
            {s.last_commit_at ? <span>· {t('mounts.lastUpload', { ago: ago(s.last_commit_at, lang, t('mounts.justNow')) })}</span> : null}
            {s.local_only_files > 0 && <span>· {t('mounts.localOnly', { count: s.local_only_files })}</span>}
          </div>
          {s.last_error && (
            <div data-selectable className="mt-2 flex gap-1.5 rounded-lg bg-destructive/8 px-3 py-2 text-[12px] leading-relaxed text-destructive">
              <TriangleAlert className="mt-0.5 size-3.5 shrink-0" />
              {s.last_error}
            </div>
          )}
        </div>
      )}

      <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-6 gap-y-2 text-[12.5px]">
        <dt className="text-muted-foreground">{t('mounts.path')}</dt>
        <dd>
          <Mono>{tilde(m.mountpoint, home)}</Mono>
        </dd>
        <dt className="text-muted-foreground">{t('mounts.account')}</dt>
        <dd data-selectable>
          {m.account_label}
          {m.workspace && m.workspace !== m.account_label && <span className="text-muted-foreground"> · {m.workspace}</span>}
        </dd>
        <dt className="text-muted-foreground">{t('mounts.volumeId')}</dt>
        <dd>
          <Mono>{m.volume_id || '—'}</Mono>
        </dd>
        <dt className="text-muted-foreground">{t('mounts.mode')}</dt>
        <dd>
          {modeLabel[m.mode || 'auto']}
          {m.active_mode && <span className="text-muted-foreground"> → {m.active_mode.toUpperCase()}</span>}
        </dd>
      </dl>

      <div className="overflow-hidden rounded-xl border border-card-border bg-muted/40">
        <Row title={t('mounts.autoMount')} desc={t('mounts.autoMountD')} wrap={false} className="px-4 py-3">
          <Switch checked={!!m.auto_mount} onCheckedChange={(v) => opt.mutate({ auto_mount: v })} aria-label={t('mounts.autoMount')} />
        </Row>
      </div>

      {/* Read when the volume is mounted, so they can only change while it isn't. */}
      <div>
        <div className="mb-1.5 flex items-baseline justify-between gap-3 px-1">
          <span className="text-[12px] font-medium text-muted-foreground">{t('mounts.atMount')}</span>
          {live(m) && <span className="text-[12px] text-muted-foreground">{t('mounts.unmountToChange')}</span>}
        </div>
        <div className="overflow-hidden rounded-xl border border-card-border bg-muted/40 [&>*+*]:border-t [&>*+*]:border-card-border">
          <Row title={t('mounts.filterJunk')} desc={t('mounts.filterJunkD')} wrap={false} className="px-4 py-3">
            <Switch checked={m.filter_junk} disabled={live(m)} onCheckedChange={(v) => opt.mutate({ filter_junk: v })} aria-label={t('mounts.filterJunk')} />
          </Row>
          <Row title={t('mounts.readOnly')} desc={t('mounts.readOnlyD')} wrap={false} className="px-4 py-3">
            <Switch checked={!!m.read_only} disabled={live(m)} onCheckedChange={(v) => opt.mutate({ read_only: v })} aria-label={t('mounts.readOnly')} />
          </Row>
          <Row title={t('mounts.onConflict')} className="px-4 py-3">
            <Segmented<ConflictPolicy>
              label={t('mounts.onConflict')}
              disabled={live(m)}
              value={m.on_conflict || 'keep-both'}
              onChange={(v) => opt.mutate({ on_conflict: v })}
              options={[
                { value: 'keep-both', label: t('mounts.keepBoth') },
                { value: 'overwrite', label: t('mounts.overwrite') },
              ]}
            />
          </Row>
          <Row title={t('mounts.mode')} className="px-4 py-3">
            <Segmented<MountMode>
              label={t('mounts.mode')}
              disabled={live(m)}
              value={m.mode || 'auto'}
              onChange={(v) => opt.mutate({ mode: v })}
              options={[
                { value: 'auto', label: t('mounts.modeAuto') },
                { value: 'nfs', label: 'NFS' },
                { value: 'fuse', label: 'FUSE' },
              ]}
            />
          </Row>
        </div>
      </div>

      <div className="flex flex-wrap items-center gap-2">
        {m.state === 'mounted' && (
          <>
            <Button variant="outline" size="sm" onClick={() => api('POST', `/mounts/${m.id}/reveal`).catch(() => {})}>
              <FolderOpen />
              {t(isMac ? 'mounts.showFinder' : 'mounts.openFolder')}
            </Button>
            <Button variant="outline" size="sm" disabled={sync.isPending} onClick={() => sync.mutate()}>
              {sync.isPending ? <Spinner className="size-3" /> : <RefreshCw />}
              {t('mounts.syncNow')}
            </Button>
            <Button variant="ghost" size="sm" onClick={() => askUnmountNow(m, t)}>
              {t('mounts.unmountNow')}
            </Button>
          </>
        )}
        {needsRepin && (
          <Button variant="outline" size="sm" disabled={repin.isPending} onClick={() => repin.mutate()}>
            {repin.isPending && <Spinner className="size-3" />}
            {t('mounts.repin')}
          </Button>
        )}
        <span className="flex-1" />
        <Button
          variant="ghost"
          size="sm"
          className="text-destructive hover:bg-destructive/10 hover:text-destructive"
          disabled={m.state === 'starting' || m.state === 'stopping'}
          onClick={askRemove}
        >
          <Trash2 />
          {t('mounts.remove')}
        </Button>
      </div>
    </div>
  )
}

function Stat({ value, label, warn }: { value: string; label: string; warn?: boolean }) {
  return (
    <div className="rounded-xl bg-muted/60 px-3.5 py-2.5">
      <div className={cn('text-[17px] font-semibold tracking-[-0.01em] tabular', warn && 'text-warn-ink')}>{value}</div>
      <div className="mt-0.5 text-[11.5px] text-muted-foreground">{label}</div>
    </div>
  )
}

function toastError(e: unknown) {
  toast.error(e instanceof Error ? e.message : String(e))
}
