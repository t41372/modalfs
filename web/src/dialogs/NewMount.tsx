import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { api } from '@/api/client'
import { refreshState, refreshVolumes, useEnvironments, useLoadedState, useVolumes } from '@/api/queries'
import type { Mount } from '@/api/types'
import { navigate } from '@/app/nav'
import { Choice } from '@/components/Choice'
import { Field, FormDialog } from '@/components/FormDialog'
import { Input } from '@/components/ui/input'
import { homeOf, tilde, untilde } from '@/lib/format'

const NONE = '__default__'

/** Pick an account, an environment and a volume, and mount it. */
export function NewMountDialog({
  open,
  onOpenChange,
  preset,
}: {
  open: boolean
  onOpenChange: (o: boolean) => void
  preset?: { account?: string; env?: string }
}) {
  const { t } = useTranslation()
  const st = useLoadedState()
  const home = homeOf(st.settings.mount_root)
  const [account, setAccount] = useState('')
  const [env, setEnv] = useState(NONE)
  const [volume, setVolume] = useState('')
  const [mountpoint, setMountpoint] = useState('')

  // Start from the preset (or the first account) each time it opens.
  useEffect(() => {
    if (!open) return
    setAccount(preset?.account || st.accounts[0]?.id || '')
    setEnv(preset?.env || NONE)
    setVolume('')
    setMountpoint('')
  }, [open]) // eslint-disable-line react-hooks/exhaustive-deps

  const envs = useEnvironments(open ? account : undefined)
  const vols = useVolumes(open ? account : undefined, env === NONE ? '' : env)
  const free = (vols.data ?? []).filter((v) => v.format === 'v2' && !v.mount_id)
  useEffect(() => {
    if (volume && !free.some((v) => v.name === volume)) setVolume('')
  }, [free, volume])

  const fallback = `${tilde(st.settings.mount_root, home)}/${volume || '…'}`

  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      title={t('form.newMount')}
      desc={t('form.newMountP')}
      submit={t('volumes.mount')}
      canSubmit={!!account && !!volume}
      onSubmit={async () => {
        const r = await api<{ mount: Mount; error?: string }>('POST', '/mounts', {
          account_id: account,
          environment: env === NONE ? '' : env,
          volume_name: volume,
          mountpoint: untilde(mountpoint.trim(), home),
          filter_junk: true,
          start: true,
        })
        // The daemon answers at once and mounts in the background: the mount's row shows how it goes,
        // and a failure stays on the row and in Activity.
        refreshState()
        refreshVolumes()
        navigate('mounts')
        if (r.error) toast.error(r.error)
        else toast(t('toast.mounting', { name: r.mount.volume_name }))
      }}
    >
      <Field label={t('form.account')}>
        <Choice
          label={t('form.account')}
          className="w-full"
          value={account}
          onChange={(v) => {
            setAccount(v)
            setEnv(NONE)
          }}
          options={st.accounts.map((a) => ({ value: a.id, label: a.label, hint: a.workspace && a.workspace !== a.label ? a.workspace : undefined }))}
        />
      </Field>
      <Field label={t('form.environment')}>
        <Choice
          label={t('form.environment')}
          className="w-full"
          value={env}
          onChange={setEnv}
          options={[
            { value: NONE, label: t('volumes.defaultEnv') },
            ...(envs.data ?? []).map((e) => ({ value: e.name, label: e.name, hint: e.default ? t('volumes.default') : undefined })),
          ]}
        />
      </Field>
      <Field label={t('form.volume')} error={vols.error?.message}>
        <Choice
          label={t('form.volume')}
          className="w-full"
          value={volume}
          onChange={setVolume}
          disabled={vols.isLoading || free.length === 0}
          placeholder={vols.isLoading ? t('form.loading') : free.length ? t('mounts.pickVolume') : t('form.noFreeVolumes')}
          options={free.map((v) => ({ value: v.name, label: v.name }))}
        />
      </Field>
      <Field label={t('form.mountpoint')} hint={t('form.mountpointHint', { path: fallback })}>
        <Input className="font-mono text-[13px]" value={mountpoint} onChange={(e) => setMountpoint(e.target.value)} placeholder={fallback} spellCheck={false} />
      </Field>
    </FormDialog>
  )
}
