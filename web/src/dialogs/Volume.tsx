import { Lock } from 'lucide-react'
import { useEffect, useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { api } from '@/api/client'
import { createEncrypted, draftKey } from '@/api/encryption'
import { refreshState, refreshVolumes } from '@/api/queries'
import type { Mount, Volume } from '@/api/types'
import { Field, FormDialog } from '@/components/FormDialog'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { EncryptFacts, KeyBlock, RememberBox, SavedBox } from './Encryption'

const NAME = /^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$/

type VolumeRef = Pick<Volume, 'id' | 'name'>

/**
 * A new volume. An encrypted one takes one more step: its key, which has to be
 * saved (and that confirmed) before the volume exists, because nothing else can
 * ever read its files.
 */
export function CreateVolumeDialog({
  open,
  onOpenChange,
  account,
  env,
  onCreated,
}: {
  open: boolean
  onOpenChange: (o: boolean) => void
  account: string
  env: string
  /** After it's created: with the new mount's ID when it's being mounted. */
  onCreated?: (mountId?: string) => void
}) {
  const { t } = useTranslation()
  const [step, setStep] = useState<'name' | 'key'>('name')
  const [name, setName] = useState('')
  const [label, setLabel] = useState('')
  const [mount, setMount] = useState(true)
  const [encrypt, setEncrypt] = useState(false)
  const [remember, setRemember] = useState(true)
  const [key, setKey] = useState('')
  const [saved, setSaved] = useState(false)
  const encId = useId()
  useEffect(() => {
    if (open) {
      setStep('name')
      setName('')
      setLabel('')
      setMount(true)
      setEncrypt(false)
      setRemember(true)
      setKey('')
      setSaved(false)
    }
  }, [open])
  const bad = name !== '' && !NAME.test(name)

  async function mountIt() {
    try {
      const r = await api<{ mount: Mount; error?: string }>('POST', '/mounts', { account_id: account, environment: env, volume_name: name, filter_junk: true, start: true })
      if (r.error) toast.error(r.error)
      else toast(t('toast.mounting', { name: r.mount.volume_name }))
      return r.mount.id
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e))
    } finally {
      refreshState()
      refreshVolumes()
    }
  }

  // The volume exists now, so the dialog closes whatever happens to the mount.
  async function created() {
    toast.success(t('toast.created', { name }))
    refreshVolumes()
    const id = mount ? await mountIt() : undefined
    // Its page needs the new mount in the state first.
    await refreshState()
    onCreated?.(id)
  }

  const steps = {
    name: {
      title: t('form.createVolume'),
      submit: encrypt ? t('enc.next') : t('form.create'),
      canSubmit: NAME.test(name),
      run: async () => {
        if (encrypt) {
          // The same key comes back if the person goes back and forth, so a
          // copy they already saved stays the right one.
          if (!key) setKey(await draftKey())
          setStep('key')
          return 'stay' as const
        }
        await api('POST', `/accounts/${account}/volumes`, { name, label: label.trim(), environment: env })
        await created()
      },
    },
    key: {
      title: t('enc.keyTitle', { name }),
      submit: t('enc.createEncrypted'),
      canSubmit: saved && !!key,
      run: async () => {
        await createEncrypted(account, env, name, label.trim(), key, remember)
        await created()
      },
    },
  }[step]

  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      title={steps.title}
      desc={step === 'key' ? t('enc.keyP') : undefined}
      submit={steps.submit}
      canSubmit={steps.canSubmit}
      onSubmit={steps.run}
      aside={
        step === 'key' ? (
          <button type="button" className="text-[12.5px] text-muted-foreground hover:text-foreground" onClick={() => setStep('name')}>
            {t('enc.back')}
          </button>
        ) : undefined
      }
    >
      {step === 'name' && (
        <>
          <Field label={t('form.volName')} htmlFor="vol-name" hint={t('form.volNameHint')} error={bad ? t('form.volNameBad') : undefined}>
            <Input id="vol-name" className="font-mono text-[13px]" value={name} onChange={(e) => setName(e.target.value.trim())} aria-invalid={bad} spellCheck={false} autoFocus maxLength={64} />
          </Field>
          <Field label={t('form.volLabel')} htmlFor="vol-label" hint={t('form.volLabelHint')}>
            <Input id="vol-label" value={label} onChange={(e) => setLabel(e.target.value)} placeholder={t('form.optional')} maxLength={80} />
          </Field>
          <label className="flex items-center gap-2.5 text-[13px]">
            <Checkbox checked={mount} onCheckedChange={(v) => setMount(!!v)} />
            {t('form.mountNow')}
          </label>
          <div className="flex flex-col gap-2.5">
            <div className="flex items-center gap-3">
              <label htmlFor={encId} className="flex min-w-0 flex-1 items-center gap-2 text-[13px] font-medium">
                <Lock className="size-3.5 text-muted-foreground" />
                {t('enc.encrypt')}
              </label>
              <Switch id={encId} checked={encrypt} onCheckedChange={setEncrypt} />
            </div>
            {encrypt ? <EncryptFacts /> : <span className="-mt-1 text-[12px] text-muted-foreground">{t('enc.encryptOff')}</span>}
          </div>
        </>
      )}
      {step === 'key' && (
        <>
          <KeyBlock value={key} volume={name} />
          <RememberBox value={remember} onChange={setRemember} />
          <SavedBox value={saved} onChange={setSaved} />
        </>
      )}
    </FormDialog>
  )
}

export function RenameVolumeDialog({ volume, onClose, account, env }: { volume: VolumeRef | null; onClose: () => void; account: string; env: string }) {
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
export function DeleteVolumeDialog({ volume, onClose, account, env, onDeleted }: { volume: VolumeRef | null; onClose: () => void; account: string; env: string; onDeleted?: () => void }) {
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

/** The volume's label, for where it can't be edited in place. "" goes back to the volume's name. */
export function LabelDialog({ m, open, onOpenChange }: { m: Mount; open: boolean; onOpenChange: (o: boolean) => void }) {
  const { t } = useTranslation()
  const [label, setLabel] = useState('')
  useEffect(() => {
    if (open) setLabel(m.label ?? '')
  }, [open]) // eslint-disable-line react-hooks/exhaustive-deps
  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      title={t('vol.labelTitle', { name: m.volume_name })}
      desc={t('vol.labelHint')}
      submit={t('form.save')}
      canSubmit={label.trim() !== (m.label ?? '')}
      onSubmit={async () => {
        const next = label.trim()
        await api('POST', `/accounts/${m.account_id}/volumes/${m.volume_id}/label`, { label: next, environment: m.environment ?? '' })
        toast(t(next ? 'toast.labeled' : 'toast.labelCleared'))
        refreshState()
        refreshVolumes()
      }}
    >
      <Input value={label} onChange={(e) => setLabel(e.target.value)} placeholder={t('vol.labelPlaceholder', { name: m.volume_name })} aria-label={t('vol.editLabel')} maxLength={80} autoFocus />
    </FormDialog>
  )
}
