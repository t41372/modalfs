import { Collapsible as CollapsiblePrimitive } from '@base-ui/react/collapsible'
import type { TFunction } from 'i18next'
import { ChevronRight } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { api } from '@/api/client'
import { patchMount, queryClient, refreshState, useAction, useLoadedState } from '@/api/queries'
import type { ConflictPolicy, Mount, MountMode, State } from '@/api/types'
import { Hint } from '@/components/bits'
import { confirm } from '@/components/confirm'
import { Field, FormDialog } from '@/components/FormDialog'
import { Card, Mono, Row, Section } from '@/components/page'
import { Segmented } from '@/components/Segmented'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { askLock, ShowKeyDialog } from '@/dialogs/Encryption'
import { LabelDialog, RenameVolumeDialog } from '@/dialogs/Volume'
import { homeOf, tilde, untilde } from '@/lib/format'
import { live, nameOf } from '@/lib/mounts'

/**
 * Everything about a volume that can be set, in one place, each changed
 * where it's shown. Options read when the volume is mounted apply at once
 * when it isn't; on a mounted volume they ask first, then unmount it (after
 * its uploads), save, and mount it again.
 */
export function VolumeSettings({ m }: { m: Mount }) {
  const { t } = useTranslation()
  const st = useLoadedState()
  const home = homeOf(st.settings.mount_root)
  const [dialog, setDialog] = useState<'' | 'label' | 'rename' | 'where' | 'key'>('')
  const close = (o: boolean) => !o && setDialog('')
  const busy = m.state === 'starting' || m.state === 'stopping'
  const autoMount = useAction((v: boolean) => patchMount(m.id, { auto_mount: v }), {
    optimistic: (state, v) => {
      const x = state.mounts.find((y) => y.id === m.id)
      if (x) x.auto_mount = v
    },
  })
  const set = (changes: Partial<Mount>) => apply(m, changes, t)
  const conflict = (p: ConflictPolicy) =>
    p === 'overwrite'
      ? confirm({
          title: t('opts.overwriteQ'),
          desc: live(m) ? `${t('opts.overwriteWarn')} ${t('opts.remount')}` : t('opts.overwriteWarn'),
          action: t('opts.overwriteYes'),
          dismiss: t('opts.keepBothStay'),
          danger: true,
          run: () => remount(m, { on_conflict: p }),
        })
      : set({ on_conflict: p })
  const canLabel = !!m.volume_id && !m.read_only && !m.locked

  return (
    <Section label={t('vol.settings')} className="scroll-mt-6" id="volume-settings">
      <Card>
        <Row title={t('vol.labelRow')} desc={t('vol.labelD')}>
          <Value>
            <span className={m.label ? '' : 'text-muted-foreground'}>{m.label || t('vol.noLabel')}</span>
            <Hint text={m.locked ? t('enc.labelLocked') : m.read_only ? t('vol.labelReadOnly') : undefined}>
              <Button variant="ghost" size="sm" disabled={!canLabel} onClick={() => setDialog('label')}>
                {t('vol.change')}
              </Button>
            </Hint>
          </Value>
        </Row>
        <Row title={t('vol.volumeName')} desc={t('vol.volumeNameD')}>
          <Value>
            <Mono className="text-[12.5px]">{m.volume_name}</Mono>
            <Button variant="ghost" size="sm" disabled={!m.volume_id} onClick={() => setDialog('rename')}>
              {t('vol.change')}
            </Button>
          </Value>
        </Row>
        <Row title={t('vol.autoMount')} desc={t('vol.autoMountD')} wrap={false}>
          <Switch checked={!!m.auto_mount} onCheckedChange={(v) => autoMount.mutate(v)} aria-label={t('vol.autoMount')} />
        </Row>
        <Row title={t('vol.where')} desc={m.follow_label ? t('vol.whereFollows') : undefined}>
          <Value>
            <Mono className="truncate text-[12.5px]">{tilde(m.mountpoint, home)}</Mono>
            <Button variant="ghost" size="sm" disabled={busy} onClick={() => setDialog('where')}>
              {t('vol.change')}
            </Button>
          </Value>
        </Row>
        <Row title={t('vol.readOnly')} desc={t('opts.readOnlyD')} wrap={false}>
          <Switch checked={!!m.read_only} disabled={busy} onCheckedChange={(v) => set({ read_only: v })} aria-label={t('vol.readOnly')} />
        </Row>
        <Row title={t('vol.onConflict')} desc={t(m.on_conflict === 'overwrite' ? 'opts.overwriteD' : 'opts.keepBothD')}>
          <Segmented<ConflictPolicy>
            label={t('vol.onConflict')}
            value={m.on_conflict || 'keep-both'}
            onChange={conflict}
            options={[
              { value: 'keep-both', label: t('vol.keepBoth') },
              { value: 'overwrite', label: t('vol.overwrite') },
            ]}
          />
        </Row>
        {m.encrypted && m.volume_id && (
          <Row title={t('enc.row')} desc={t('enc.rowD')}>
            <Value>
              <Hint text={!m.key_remembered && !m.locked ? t('enc.showNotRemembered') : undefined}>
                <Button variant="ghost" size="sm" disabled={m.locked || !m.key_remembered} onClick={() => setDialog('key')}>
                  {t('enc.menuShow')}
                </Button>
              </Hint>
              <Hint text={live(m) ? t('enc.lockLive') : undefined}>
                <Button variant="ghost" size="sm" disabled={m.locked || live(m)} onClick={() => askLock(m, t)}>
                  {t('enc.menuLock')}
                </Button>
              </Hint>
            </Value>
          </Row>
        )}
        <CollapsiblePrimitive.Root>
          <CollapsiblePrimitive.Trigger className="group/adv flex w-full items-center gap-1 px-5 py-3 text-[12.5px] font-medium text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50">
            <ChevronRight className="size-3.5 transition-transform duration-200 ease-out group-data-[panel-open]/adv:rotate-90" />
            {t('opts.advanced')}
          </CollapsiblePrimitive.Trigger>
          <CollapsiblePrimitive.Panel className="h-(--collapsible-panel-height) overflow-hidden transition-[height] duration-200 ease-[var(--ease-out)] data-[ending-style]:h-0 data-[starting-style]:h-0 motion-reduce:transition-none">
            <Row title={t('vol.litter')} desc={t('vol.litterHint')} wrap={false}>
              <Switch checked={m.filter_junk} disabled={busy} onCheckedChange={(v) => set({ filter_junk: v })} aria-label={t('vol.litter')} />
            </Row>
            <Row title={t('vol.mode')} desc={`${t('opts.modeD')}${m.active_mode ? ` ${t('opts.modeNow', { mode: m.active_mode.toUpperCase() })}` : ''}`}>
              <Segmented<MountMode>
                label={t('vol.mode')}
                value={m.mode || 'auto'}
                onChange={(v) => set({ mode: v })}
                options={[
                  { value: 'auto', label: t('vol.modeAuto') },
                  { value: 'nfs', label: 'NFS' },
                  { value: 'fuse', label: 'FUSE' },
                ]}
              />
            </Row>
          </CollapsiblePrimitive.Panel>
        </CollapsiblePrimitive.Root>
      </Card>
      <LabelDialog m={m} open={dialog === 'label'} onOpenChange={close} />
      <RenameVolumeDialog volume={dialog === 'rename' && m.volume_id ? { id: m.volume_id, name: m.volume_name } : null} onClose={() => setDialog('')} account={m.account_id} env={m.environment ?? ''} />
      <WhereDialog m={m} open={dialog === 'where'} onOpenChange={close} />
      {m.encrypted && m.volume_id && <ShowKeyDialog m={m} open={dialog === 'key'} onOpenChange={close} />}
    </Section>
  )
}

/** A setting's current value with the buttons that change it. */
function Value({ children }: { children: React.ReactNode }) {
  return <div className="flex min-w-0 items-center justify-end gap-1 text-[13px]">{children}</div>
}

/** Where the volume is mounted. A folder chosen here is kept, rather than named after the label. */
function WhereDialog({ m, open, onOpenChange }: { m: Mount; open: boolean; onOpenChange: (o: boolean) => void }) {
  const { t } = useTranslation()
  const home = homeOf(useLoadedState().settings.mount_root)
  const [where, setWhere] = useState('')
  useEffect(() => {
    if (open) setWhere(tilde(m.mountpoint, home))
  }, [open]) // eslint-disable-line react-hooks/exhaustive-deps
  const p = untilde(where.trim(), home)
  const bad = !!where.trim() && !p.startsWith('/')
  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      title={t('opts.whereTitle', { name: nameOf(m) })}
      desc={live(m) ? t('opts.remount') : undefined}
      submit={live(m) ? t('opts.saveRemount') : t('form.save')}
      canSubmit={!!where.trim() && !bad && p !== m.mountpoint}
      onSubmit={() => remount(m, { mountpoint: p })}
    >
      <Field label={t('vol.where')} htmlFor="opt-where" error={bad ? t('form.pathBad') : undefined} hint={t('opts.whereD')}>
        <Input id="opt-where" className="font-mono text-[13px]" value={where} onChange={(e) => setWhere(e.target.value)} spellCheck={false} autoFocus />
      </Field>
    </FormDialog>
  )
}

/** Saves options read at mount time: at once on an unmounted volume, after asking on a mounted one. */
function apply(m: Mount, changes: Partial<Mount>, t: TFunction) {
  if (!live(m)) {
    patchMount(m.id, changes).then(refreshState, (e) => toast.error(e.message))
    return
  }
  confirm({
    title: t('opts.remountQ', { name: nameOf(m) }),
    desc: t('opts.remount'),
    action: t('opts.remountNow'),
    dismiss: t('opts.notNow'),
    run: () => remount(m, changes),
  })
}

/** Unmounts a mounted volume (after its uploads), saves the changes, and mounts it again. */
async function remount(m: Mount, changes: Partial<Mount>) {
  const wasLive = live(m)
  if (wasLive) {
    await api('POST', `/mounts/${m.id}/stop`)
    await waitStopped(m.id)
  }
  await patchMount(m.id, changes)
  if (wasLive) await api('POST', `/mounts/${m.id}/start`)
  refreshState()
}

/** Waits until a mount has stopped (its uploads done), or failed to. */
async function waitStopped(id: string) {
  for (;;) {
    await new Promise((r) => setTimeout(r, 500))
    await refreshState()
    const m = queryClient.getQueryData<State>(['state'])?.mounts.find((x) => x.id === id)
    if (!m || m.state === 'stopped') return
    if (m.state === 'mounted' && m.unmount_error) throw new Error(m.unmount_error)
    if (m.state === 'error') throw new Error(m.error || 'unmount failed')
  }
}
