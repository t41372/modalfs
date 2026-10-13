import { Collapsible as CollapsiblePrimitive } from '@base-ui/react/collapsible'
import { ChevronRight, TriangleAlert } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api } from '@/api/client'
import { patchMount, queryClient, refreshState, useLoadedState } from '@/api/queries'
import type { ConflictPolicy, Mount, MountMode, State } from '@/api/types'
import { Field, FormDialog } from '@/components/FormDialog'
import { Segmented } from '@/components/Segmented'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { homeOf, tilde, untilde } from '@/lib/format'
import { live } from '@/lib/mounts'

/**
 * Options read when a volume is mounted. Changing them on a mounted volume
 * unmounts it (after its uploads), saves, and mounts it again.
 */
export function MountOptionsDialog({ m, open, onOpenChange }: { m: Mount; open: boolean; onOpenChange: (o: boolean) => void }) {
  const { t } = useTranslation()
  const st = useLoadedState()
  const home = homeOf(st.settings.mount_root)
  const [where, setWhere] = useState('')
  const [readOnly, setReadOnly] = useState(false)
  const [junk, setJunk] = useState(true)
  const [policy, setPolicy] = useState<ConflictPolicy>('keep-both')
  const [mode, setMode] = useState<MountMode>('auto')
  const [understood, setUnderstood] = useState(false)
  useEffect(() => {
    if (!open) return
    setWhere(tilde(m.mountpoint, home))
    setReadOnly(!!m.read_only)
    setJunk(m.filter_junk)
    setPolicy(m.on_conflict || 'keep-both')
    setMode(m.mode || 'auto')
    setUnderstood(false)
  }, [open]) // eslint-disable-line react-hooks/exhaustive-deps

  const changes: Partial<Mount> = {}
  if (untilde(where.trim(), home) !== m.mountpoint && where.trim()) changes.mountpoint = untilde(where.trim(), home)
  if (readOnly !== !!m.read_only) changes.read_only = readOnly
  if (junk !== m.filter_junk) changes.filter_junk = junk
  if (policy !== (m.on_conflict || 'keep-both')) changes.on_conflict = policy
  if (mode !== (m.mode || 'auto')) changes.mode = mode
  const changed = Object.keys(changes).length > 0
  const risky = policy === 'overwrite' && (m.on_conflict || 'keep-both') !== 'overwrite'
  const wasLive = live(m)
  const badPath = !!where.trim() && !untilde(where.trim(), home).startsWith('/')

  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      title={t('opts.title', { name: m.volume_name })}
      desc={wasLive ? t('opts.remount') : undefined}
      submit={wasLive ? t('opts.saveRemount') : t('form.save')}
      canSubmit={changed && !badPath && (!risky || understood)}
      onSubmit={async () => {
        if (wasLive) {
          await api('POST', `/mounts/${m.id}/stop`)
          await waitStopped(m.id)
        }
        await patchMount(m.id, changes)
        if (wasLive) await api('POST', `/mounts/${m.id}/start`)
        refreshState()
      }}
    >
      <Field label={t('opts.where')} htmlFor="opt-where" error={badPath ? t('form.pathBad') : undefined}>
        <Input id="opt-where" className="font-mono text-[13px]" value={where} onChange={(e) => setWhere(e.target.value)} spellCheck={false} />
      </Field>
      <div className="flex items-center justify-between gap-4">
        <span className="min-w-0">
          <span className="block text-[12.5px] font-medium">{t('vol.readOnly')}</span>
          <span className="block text-[12px] text-muted-foreground">{t('opts.readOnlyD')}</span>
        </span>
        <Switch checked={readOnly} onCheckedChange={setReadOnly} aria-label={t('vol.readOnly')} />
      </div>
      <Field label={t('vol.onConflict')} hint={t(policy === 'overwrite' ? 'opts.overwriteD' : 'opts.keepBothD')}>
        <Segmented<ConflictPolicy>
          label={t('vol.onConflict')}
          value={policy}
          onChange={setPolicy}
          options={[
            { value: 'keep-both', label: t('vol.keepBoth') },
            { value: 'overwrite', label: t('vol.overwrite') },
          ]}
        />
      </Field>
      {risky && (
        <div className="flex flex-col gap-2 rounded-lg bg-destructive/8 px-3 py-2.5 text-[12.5px] text-destructive">
          <span className="flex gap-1.5">
            <TriangleAlert className="mt-0.5 size-3.5 shrink-0" />
            {t('opts.overwriteWarn')}
          </span>
          <label className="flex items-center gap-2 pl-5 text-foreground">
            <Checkbox checked={understood} onCheckedChange={(v) => setUnderstood(v === true)} />
            {t('opts.understood')}
          </label>
        </div>
      )}
      <CollapsiblePrimitive.Root>
        <CollapsiblePrimitive.Trigger className="group/adv flex items-center gap-1 rounded text-[12.5px] font-medium text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50">
          <ChevronRight className="size-3.5 transition-transform duration-200 ease-out group-data-[panel-open]/adv:rotate-90" />
          {t('opts.advanced')}
        </CollapsiblePrimitive.Trigger>
        <CollapsiblePrimitive.Panel className="h-(--collapsible-panel-height) overflow-hidden transition-[height] duration-200 ease-[var(--ease-out)] data-[ending-style]:h-0 data-[starting-style]:h-0 motion-reduce:transition-none">
          <div className="flex flex-col gap-4 pt-3">
            <div className="flex items-center justify-between gap-4">
              <span className="min-w-0">
                <span className="block text-[12.5px] font-medium">{t('vol.litter')}</span>
                <span className="block text-[12px] text-muted-foreground">{t('vol.litterHint')}</span>
              </span>
              <Switch checked={junk} onCheckedChange={setJunk} aria-label={t('vol.litter')} />
            </div>
            <Field label={t('vol.mode')} hint={t('opts.modeD')}>
              <Segmented<MountMode>
                label={t('vol.mode')}
                value={mode}
                onChange={setMode}
                options={[
                  { value: 'auto', label: t('vol.modeAuto') },
                  { value: 'nfs', label: 'NFS' },
                  { value: 'fuse', label: 'FUSE' },
                ]}
              />
            </Field>
          </div>
        </CollapsiblePrimitive.Panel>
      </CollapsiblePrimitive.Root>
    </FormDialog>
  )
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
