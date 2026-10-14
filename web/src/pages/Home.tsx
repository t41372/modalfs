import { cn } from 'cn'
import type { TFunction } from 'i18next'
import { ChevronRight, FolderOpen, HardDrive, Lock, MoreHorizontal, Plus } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api } from '@/api/client'
import { refreshState, useAction, useLoadedState } from '@/api/queries'
import type { Mount } from '@/api/types'
import { navigate } from '@/app/nav'
import { AccountName, Chip, Hint, StateIcon } from '@/components/bits'
import { Card, Empty, Page } from '@/components/page'
import { Spinner } from '@/components/Spinner'
import { duration, StopProgress } from '@/components/StopProgress'
import { Button } from '@/components/ui/button'
import { ContextMenu, ContextMenuContent, ContextMenuTrigger } from '@/components/ui/context-menu'
import { DropdownMenu, DropdownMenuContent, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { useVolumeMenu } from '@/components/VolumeMenu'
import { AddVolumeDialog } from '@/dialogs/AddVolume'
import { EncryptedMark, UnlockDialog } from '@/dialogs/Encryption'
import { ago, bytes, homeOf, speed, tilde } from '@/lib/format'
import { look, type Look, nameOf, summary, toneOf } from '@/lib/mounts'
import { isMac } from '@/lib/platform'
import { mountError, uploadProblem } from '@/lib/mountError'

/** The volumes modalfs manages: each one's state at a glance, and the one thing to do next. */
export function Home() {
  const { t } = useTranslation()
  const st = useLoadedState()
  const [adding, setAdding] = useState(false)
  const s = summary(st.mounts)
  const sizes = st.mounts.map((m) => m.size_bytes)
  const known = sizes.filter((x): x is number => x !== undefined)
  const biggest = Math.max(1, ...known)
  // With one account, it's named once at the top; with several, on each row.
  const manyAccounts = new Set(st.mounts.map((m) => m.account_id)).size > 1
  const only = !manyAccounts ? st.mounts[0] : undefined

  return (
    <Page
      title={t('home.title')}
      sub={
        st.mounts.length > 0 && (
          <span className="flex flex-wrap gap-1.5 pt-1">
            {only && (
              <Chip hint={only.environment ? t('home.accountEnv', { env: only.environment }) : undefined} className="pl-1.5">
                <AccountName id={only.account_id} label={only.account_label} size={16} />
              </Chip>
            )}
            {s.on > 0 && <Chip tone="ok" glyph="check">{t('home.mounted', { count: s.on })}</Chip>}
            {/* What's moving now; the totals since each was mounted are on hover. */}
            {s.on > 0 && (
              <Chip tone={s.traffic.up > 0 || s.traffic.down > 0 ? 'up' : undefined} hint={t('home.speedHint', { up: bytes(s.traffic.sent), down: bytes(s.traffic.read) })}>
                <span className="tabular">{`↑ ${speed(s.traffic.up)} · ↓ ${speed(s.traffic.down)}`}</span>
              </Chip>
            )}
            {s.files > 0 && (
              <Chip tone="up" glyph="up" hint={t('home.uploadingHint', { count: s.files })}>
                {t('home.uploading', { size: bytes(s.pending) })}
              </Chip>
            )}
            {s.problems > 0 && <Chip tone="bad" glyph="alert">{t('home.attention', { count: s.problems })}</Chip>}
            {known.length === sizes.length && known.length > 1 && (
              <Chip hint={t('home.totalHint')}>{t('home.total', { size: bytes(known.reduce((a, b) => a + b, 0)) })}</Chip>
            )}
          </span>
        )
      }
      actions={
        st.mounts.length > 0 && (
          <Button variant="outline" onClick={() => setAdding(true)}>
            <Plus />
            {t('home.add')}
          </Button>
        )
      }
    >
      {st.mounts.length === 0 ? (
        <Card>
          <Empty icon={<HardDrive />} title={t('home.empty')} desc={t('home.emptyP')}>
            <Button onClick={() => setAdding(true)}>
              <Plus />
              {t('home.add')}
            </Button>
          </Empty>
        </Card>
      ) : (
        <Card>
          {st.mounts.map((m) => (
            <VolumeRow key={m.id} m={m} biggest={biggest} showAccount={manyAccounts} />
          ))}
        </Card>
      )}
      <AddVolumeDialog open={adding} onOpenChange={setAdding} />
    </Page>
  )
}

/** A mount's state in a few words, for the icon's hover and the screen reader. */
export function stateText(m: Mount, l: Look, t: TFunction) {
  switch (l) {
    case 'synced':
      return t('vol.state.synced')
    case 'uploading':
      return t('vol.state.uploading', { size: bytes(m.status?.pending_bytes ?? 0), count: m.status?.pending_files ?? 0 })
    case 'conflict':
      return t('vol.state.conflict', { count: m.open_conflicts ?? 0 })
    case 'stalled':
      return t('vol.state.stalled', { why: uploadProblem(m.status?.upload_error, t), size: bytes(m.status?.pending_bytes ?? 0) })
    case 'busy':
      return t('vol.state.busy')
    case 'starting':
      return t(`vol.step.${m.step ?? 'connect'}`)
    case 'stopping':
      return t('vol.state.stopping')
    case 'error':
      return t('vol.state.error')
    case 'locked':
      return t('vol.state.locked')
    case 'off':
      return t('vol.state.off')
  }
}

function VolumeRow({ m, biggest, showAccount }: { m: Mount; biggest: number; showAccount: boolean }) {
  const { t, i18n } = useTranslation()
  const st = useLoadedState()
  const home = homeOf(st.settings.mount_root)
  const l = look(m)
  // Failures come back as events (toasted) and stay on the row, so the call itself doesn't toast.
  const start = useAction(() => api('POST', `/mounts/${m.id}/start`), {
    quiet: true,
    optimistic: (s) => {
      const x = s.mounts.find((y) => y.id === m.id)
      if (x) {
        x.state = 'starting'
        x.step = 'connect'
        x.error = undefined
      }
    },
  })
  const reveal = () => api('POST', `/mounts/${m.id}/reveal`, {}).catch(() => refreshState())
  const open = () => navigate('volumes', m.id)
  const [unlocking, setUnlocking] = useState(false)
  const menu = useVolumeMenu(m, { full: true })
  const nowSec = Date.now() / 1000
  const pending = m.status?.pending_bytes ?? 0

  const line =
    l === 'locked' ? (
      <span className="truncate">{t('enc.rowLocked')}</span>
    ) : l === 'error' ? (
      <span className="truncate text-destructive" title={m.error}>
        {mountError(m.error, t).text}
        {m.retry_at && m.retry_at > nowSec ? ` · ${t('vol.retryIn', { time: duration(m.retry_at - nowSec, t) })}` : ''}
      </span>
    ) : l === 'off' ? (
      <span className="truncate">{m.last_used ? t('home.lastUsed', { ago: ago(m.last_used, i18n.language, t('time.justNow')) }) : t('vol.state.off')}</span>
    ) : l === 'starting' ? (
      <span className="truncate">{t(`vol.step.${m.step ?? 'connect'}`)}</span>
    ) : (
      <>
        <span className="truncate font-mono text-[11.5px]">{tilde(m.mountpoint, home)}</span>
        {l === 'uploading' && <span className="shrink-0 text-warn-ink tabular">↑ {bytes(pending)}</span>}
        {l === 'conflict' && <span className="shrink-0 text-destructive">{t('vol.conflicts', { count: m.open_conflicts ?? 0 })}</span>}
        {l === 'conflict' && pending > 0 && <span className="shrink-0 text-warn-ink tabular">↑ {bytes(pending)}</span>}
        {l === 'busy' && <span className="shrink-0 text-warn-ink">{t('vol.state.busy')}</span>}
        {l === 'stalled' && <span className="shrink-0 text-destructive">{uploadProblem(m.status?.upload_error, t)}</span>}
        {l === 'stalled' && <span className="shrink-0 text-warn-ink tabular">↑ {bytes(pending)}</span>}
        {/* Too narrow for them: the total is at the top. */}
        {!!m.up_speed && <span className="shrink-0 text-warn-ink tabular max-sm:hidden">↑ {speed(m.up_speed)}</span>}
        {!!m.down_speed && <span className="shrink-0 tabular max-sm:hidden">↓ {speed(m.down_speed)}</span>}
      </>
    )

  const action =
    l === 'locked' ? (
      <Button size="sm" variant="outline" onClick={() => setUnlocking(true)}>
        <Lock />
        {t('enc.unlock')}
      </Button>
    ) : l === 'conflict' ? (
      <Button size="sm" onClick={open}>
        {t('home.handle')}
      </Button>
    ) : l === 'error' ? (
      <Button size="sm" variant="outline" disabled={start.isPending} onClick={() => start.mutate()}>
        {t('vol.tryAgain')}
      </Button>
    ) : l === 'off' ? (
      <Button size="sm" variant="outline" disabled={start.isPending} onClick={() => start.mutate()}>
        {t('vol.mount')}
      </Button>
    ) : l === 'starting' || l === 'stopping' ? (
      <Spinner className="mx-2 size-3.5 text-muted-foreground" />
    ) : (
      <Button size="sm" variant="outline" onClick={reveal} aria-label={t(isMac ? 'vol.finder' : 'vol.openFolder')}>
        <FolderOpen />
        <span className="max-sm:hidden">{t(isMac ? 'vol.finder' : 'vol.openFolder')}</span>
      </Button>
    )

  return (
    <ContextMenu>
      <ContextMenuTrigger className="flex items-center gap-3 pr-3 pl-4 data-popup-open:bg-muted/50">
        <StateIcon look={l} tone={toneOf[l]} label={stateText(m, l, t)} />
        <button onClick={open} className="group/row flex min-w-0 flex-1 items-center gap-3 rounded-lg py-3 text-left outline-none focus-visible:ring-2 focus-visible:ring-ring/50">
          <span className="min-w-0 flex-1">
            <span className={cn('block truncate text-[14.5px] font-semibold tracking-[-0.01em]', (l === 'off' || l === 'locked') && 'text-foreground/70')}>
              {nameOf(m)}
              {m.encrypted && <EncryptedMark m={m} className="ml-1.5 align-[-1px]" />}
              {m.label && <span className="ml-2 font-mono text-[12px] font-normal tracking-normal text-muted-foreground">{m.volume_name}</span>}
            </span>
            <span className="mt-0.5 flex min-w-0 items-center gap-2 text-[12.5px] text-muted-foreground">
              {showAccount && (
                <Hint text={m.environment ? t('home.accountEnv', { env: m.environment }) : undefined}>
                  <span className="inline-flex max-w-[40%] shrink-0 items-center gap-2 text-foreground/75">
                    <AccountName id={m.account_id} label={m.account_label} size={15} />
                    <span aria-hidden className="text-muted-foreground/50">·</span>
                  </span>
                </Hint>
              )}
              {line}
            </span>
          </span>
          {m.size_bytes !== undefined && (
            <Hint text={t('vol.sizeMeasured', { ago: ago(m.size_at, i18n.language, t('time.justNow')) })}>
              <span className="hidden w-24 shrink-0 flex-col items-end gap-1.5 sm:flex">
                <span className="text-[12.5px] text-muted-foreground tabular">{bytes(m.size_bytes)}</span>
                <span className="block h-1 w-full overflow-hidden rounded-full bg-muted">
                  <span className="block h-full rounded-full bg-foreground/25" style={{ width: `${Math.max(3, (m.size_bytes / biggest) * 100)}%` }} />
                </span>
              </span>
            </Hint>
          )}
          <ChevronRight className="size-4 shrink-0 text-muted-foreground/60 transition-transform group-hover/row:translate-x-0.5" />
        </button>
        <div className="flex shrink-0 items-center justify-end gap-1 sm:w-[168px]">
          {action}
          <DropdownMenu>
            <DropdownMenuTrigger render={<Button variant="ghost" size="icon-sm" className="text-muted-foreground" aria-label={t('vol.moreFor', { name: nameOf(m) })} />}>
              <MoreHorizontal />
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="min-w-52">
              {menu.items}
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      </ContextMenuTrigger>
      <ContextMenuContent className="min-w-52">{menu.items}</ContextMenuContent>
      {m.state === 'stopping' && (
        <div className="-mt-1 pr-4 pb-3 pl-[50px]">
          <StopProgress m={m} />
        </div>
      )}
      {m.locked && <UnlockDialog m={m} open={unlocking} onOpenChange={setUnlocking} />}
      {menu.dialogs}
    </ContextMenu>
  )
}
