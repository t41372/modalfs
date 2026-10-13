import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { api } from '@/api/client'
import { refreshState, refreshVolumes } from '@/api/queries'
import type { Mount, Volume } from '@/api/types'
import { Field, FormDialog } from '@/components/FormDialog'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'

const NAME = /^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$/

export function CreateVolumeDialog({ open, onOpenChange, account, env }: { open: boolean; onOpenChange: (o: boolean) => void; account: string; env: string }) {
  const { t } = useTranslation()
  const [name, setName] = useState('')
  const [mount, setMount] = useState(true)
  useEffect(() => {
    if (open) {
      setName('')
      setMount(true)
    }
  }, [open])
  const bad = name !== '' && !NAME.test(name)

  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      title={t('form.createVolume')}
      submit={t('form.create')}
      canSubmit={NAME.test(name)}
      onSubmit={async () => {
        await api('POST', `/accounts/${account}/volumes`, { name, environment: env })
        toast.success(t('toast.created', { name }))
        refreshVolumes()
        // The volume exists now, so the dialog closes whatever happens to the mount.
        if (mount) {
          try {
            const r = await api<{ mount: Mount; error?: string }>('POST', '/mounts', { account_id: account, environment: env, volume_name: name, filter_junk: true, start: true })
            if (r.error) toast.error(r.error)
            else toast(t('toast.mounting', { name: r.mount.volume_name }))
          } catch (e) {
            toast.error(e instanceof Error ? e.message : String(e))
          } finally {
            refreshState()
            refreshVolumes()
          }
        }
      }}
    >
      <Field label={t('form.volName')} htmlFor="vol-name" hint={t('form.volNameHint')} error={bad ? t('form.volNameBad') : undefined}>
        <Input id="vol-name" className="font-mono text-[13px]" value={name} onChange={(e) => setName(e.target.value.trim())} aria-invalid={bad} spellCheck={false} autoFocus maxLength={64} />
      </Field>
      <label className="flex items-center gap-2.5 text-[13px]">
        <Checkbox checked={mount} onCheckedChange={(v) => setMount(!!v)} />
        {t('form.mountNow')}
      </label>
    </FormDialog>
  )
}

export function RenameVolumeDialog({ volume, onClose, account, env }: { volume: Volume | null; onClose: () => void; account: string; env: string }) {
  const { t } = useTranslation()
  const [name, setName] = useState('')
  useEffect(() => {
    if (volume) setName(volume.name)
  }, [volume])
  const bad = name !== '' && !NAME.test(name)
  return (
    <FormDialog
      open={!!volume}
      onOpenChange={(o) => !o && onClose()}
      title={t('form.renameVolume', { name: volume?.name })}
      submit={t('form.save')}
      canSubmit={NAME.test(name) && name !== volume?.name}
      onSubmit={async () => {
        await api('POST', `/accounts/${account}/volumes/${volume!.id}/rename`, { name, environment: env })
        toast.success(t('toast.renamed', { name }))
        refreshVolumes()
        refreshState()
      }}
    >
      <Field label={t('form.volName')} htmlFor="vol-rename" hint={t('form.renameHint')} error={bad ? t('form.volNameBad') : undefined}>
        <Input id="vol-rename" className="font-mono text-[13px]" value={name} onChange={(e) => setName(e.target.value.trim())} aria-invalid={bad} spellCheck={false} autoFocus maxLength={64} />
      </Field>
    </FormDialog>
  )
}

/** Deleting asks for the volume's name to be typed, like GitHub's repository deletion. */
export function DeleteVolumeDialog({ volume, onClose, account, env, onDeleted }: { volume: Volume | null; onClose: () => void; account: string; env: string; onDeleted?: () => void }) {
  const { t } = useTranslation()
  const [typed, setTyped] = useState('')
  useEffect(() => {
    if (volume) setTyped('')
  }, [volume])
  return (
    <FormDialog
      open={!!volume}
      onOpenChange={(o) => !o && onClose()}
      title={t('form.deleteVolume', { name: volume?.name })}
      desc={t('form.deleteP')}
      submit={t('form.deleteForever')}
      dismiss={t('form.keep')}
      danger
      canSubmit={typed === volume?.name}
      onSubmit={async () => {
        await api('POST', `/accounts/${account}/volumes/${volume!.id}/delete`, { confirm: typed, name: volume!.name, environment: env })
        toast.success(t('toast.deleted', { name: volume!.name }))
        refreshVolumes()
        refreshState()
        onDeleted?.()
      }}
    >
      <Field label={t('form.deleteType', { name: volume?.name })} htmlFor="vol-delete">
        <Input id="vol-delete" className="font-mono text-[13px]" value={typed} onChange={(e) => setTyped(e.target.value)} placeholder={volume?.name} spellCheck={false} autoFocus />
      </Field>
    </FormDialog>
  )
}
