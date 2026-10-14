import type { TFunction } from 'i18next'
import { Copy, Eject, FolderOpen, HardDrive, Lock, RefreshCw, Settings2, Trash2, Upload, X } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { api } from '@/api/client'
import { refreshState, refreshUsage, useAction } from '@/api/queries'
import type { Mount } from '@/api/types'
import { navigate } from '@/app/nav'
import { confirm } from '@/components/confirm'
import { DropdownMenuItem, DropdownMenuSeparator } from '@/components/ui/dropdown-menu'
import { UnlockDialog } from '@/dialogs/Encryption'
import { DeleteVolumeDialog } from '@/dialogs/Volume'
import { bytes } from '@/lib/format'
import { live, nameOf } from '@/lib/mounts'
import { isMac } from '@/lib/platform'

/**
 * What can be done with a volume, as menu items for its ⋯ menu and its
 * right-click menu, and the dialogs they open. With `full`, the items start
 * with what the volume's page shows as buttons: mount, unmount, unlock and
 * Show in Finder. What can be set is on the volume's page, under Settings.
 */
export function useVolumeMenu(m: Mount, { full = false }: { full?: boolean } = {}) {
  const { t } = useTranslation()
  const [dialog, setDialog] = useState<'' | 'delete' | 'unlock'>('')
  const close = (o: boolean) => !o && setDialog('')
  const mounted = m.state === 'mounted'
  const busy = m.state === 'starting' || m.state === 'stopping'
  const volume = m.volume_id ? { id: m.volume_id, name: m.volume_name } : null
  const env = m.environment ?? ''

  // Failures come back as events (toasted) and stay on the volume, so the call itself doesn't toast.
  const start = useAction(() => api('POST', `/mounts/${m.id}/start`), {
    quiet: true,
    optimistic: (st) => {
      const x = st.mounts.find((y) => y.id === m.id)
      if (x) {
        x.state = 'starting'
        x.step = 'connect'
        x.error = undefined
      }
    },
  })
  const sync = useAction(() => api('POST', `/mounts/${m.id}/sync`), { success: () => t('toast.synced') })
  const measure = useAction(() => api('POST', `/mounts/${m.id}/usage`).then(() => refreshUsage(m.id)), { quiet: true })
  const reveal = () => api('POST', `/mounts/${m.id}/reveal`, {}).catch((e) => toast.error(e.message))

  const items = (
    <>
      {full && (
        <>
          {mounted ? (
            <>
              <DropdownMenuItem onClick={reveal}>
                <FolderOpen />
                {t(isMac ? 'vol.finder' : 'vol.openFolder')}
              </DropdownMenuItem>
              <DropdownMenuItem onClick={() => askUnmount(m, t)}>
                <Eject />
                {t('vol.unmountMenu')}
              </DropdownMenuItem>
            </>
          ) : m.locked ? (
            <DropdownMenuItem disabled={!m.volume_id} onClick={() => setDialog('unlock')}>
              <Lock />
              {t('enc.unlock')}
            </DropdownMenuItem>
          ) : (
            <DropdownMenuItem disabled={busy || start.isPending} onClick={() => start.mutate()}>
              <HardDrive />
              {t(m.state === 'error' ? 'vol.tryAgain' : 'vol.mount')}
            </DropdownMenuItem>
          )}
          <DropdownMenuSeparator />
        </>
      )}
      <DropdownMenuItem onClick={() => openSettings(m)}>
        <Settings2 />
        {t('vol.openSettings')}
      </DropdownMenuItem>
      <DropdownMenuSeparator />
      <DropdownMenuItem disabled={!mounted || sync.isPending} onClick={() => sync.mutate()}>
        <Upload />
        {t('vol.syncNow')}
      </DropdownMenuItem>
      <DropdownMenuItem disabled={!m.volume_id || m.locked || measure.isPending} onClick={() => measure.mutate()}>
        <RefreshCw />
        {t('vol.measure')}
      </DropdownMenuItem>
      {m.volume_id && (
        <DropdownMenuItem onClick={() => navigator.clipboard?.writeText(m.volume_id!).then(() => toast(t('vol.copied')))}>
          <Copy />
          {t('vol.copyId')}
        </DropdownMenuItem>
      )}
      <DropdownMenuSeparator />
      <DropdownMenuItem disabled={busy} onClick={() => askRemove(m, t)}>
        <X />
        {t('vol.remove')}
      </DropdownMenuItem>
      <DropdownMenuItem variant="destructive" disabled={!volume || live(m)} onClick={() => setDialog('delete')}>
        <Trash2 />
        {live(m) ? t('vol.deleteLive') : t('vol.delete')}
      </DropdownMenuItem>
    </>
  )

  const dialogs = (
    <>
      {m.encrypted && m.volume_id && <UnlockDialog m={m} open={dialog === 'unlock'} onOpenChange={close} />}
      <DeleteVolumeDialog volume={dialog === 'delete' ? volume : null} onClose={() => setDialog('')} account={m.account_id} env={env} onDeleted={() => navigate('volumes')} />
    </>
  )

  return { items, dialogs }
}

export function askUnmount(m: Mount, t: TFunction) {
  const left = m.status?.pending_bytes ?? 0
  confirm({
    title: t('vol.unmountQ', { name: nameOf(m) }),
    desc: left > 0 ? t('vol.unmountPending', { size: bytes(left) }) : t('vol.unmountClean'),
    action: t(left > 0 ? 'vol.unmountAfter' : 'vol.unmount'),
    dismiss: t('vol.keepMounted'),
    run: () => api('POST', `/mounts/${m.id}/stop`).catch(() => refreshState()),
  })
}

function askRemove(m: Mount, t: TFunction) {
  const s = m.status
  confirm({
    title: t('vol.removeQ', { name: nameOf(m) }),
    desc: `${t('vol.removeP')}${live(m) ? ` ${t((s?.pending_files ?? 0) > 0 ? 'vol.removeLivePending' : 'vol.removeLive', { size: bytes(s?.pending_bytes ?? 0) })}` : ''}`,
    action: t('vol.remove'),
    dismiss: t('vol.keepIt'),
    run: () =>
      api('DELETE', `/mounts/${m.id}`)
        .then(() => {
          toast(t('vol.removed', { name: nameOf(m) }))
          navigate('volumes')
        })
        .catch((e) => toast.error(e.message))
        .finally(refreshState),
  })
}

/** Opens the volume's page at its settings. */
export function openSettings(m: Mount) {
  navigate('volumes', m.id)
  setTimeout(() => document.getElementById('volume-settings')?.scrollIntoView({ behavior: 'smooth', block: 'start' }), 120)
}
