// Dialogs for end-to-end encrypted volumes: what encryption means, saving the
// key, unlocking, showing the key again and locking. The model, in the words
// the UI uses: a volume has one key; the key and the encrypted files are all
// it takes to read them; without the key they're gone, for everyone.

import type { TFunction } from 'i18next'
import { Check, Copy, Cpu, Download, EyeOff, Lock, ShieldOff, TriangleAlert } from 'lucide-react'
import { type ReactNode, useEffect, useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { api } from '@/api/client'
import { lock, normalizeKey, showKey, unlock } from '@/api/encryption'
import { refreshState, refreshVolumes } from '@/api/queries'
import type { Mount } from '@/api/types'
import { Hint } from '@/components/bits'
import { confirm } from '@/components/confirm'
import { Field, FormDialog } from '@/components/FormDialog'
import { Spinner } from '@/components/Spinner'
import { Checkbox } from '@/components/ui/checkbox'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { nameOf } from '@/lib/mounts'
import { isMac, saveText } from '@/lib/platform'

/** What turning encryption on means, one short line each. */
export function EncryptFacts() {
  const { t } = useTranslation()
  const facts: [ReactNode, string, boolean?][] = [
    [<EyeOff key="i" />, t('enc.fact.modal')],
    [<Cpu key="i" />, t('enc.fact.functions')],
    [<ShieldOff key="i" />, t('enc.fact.downloads')],
    [<TriangleAlert key="i" />, t('enc.fact.loss'), true],
  ]
  return (
    <ul className="flex flex-col gap-2 rounded-xl bg-muted/60 px-3.5 py-3 text-[12.5px] leading-snug">
      {facts.map(([icon, text, bad]) => (
        <li key={text} className={`flex items-start gap-2.5 ${bad ? 'font-medium text-destructive' : ''}`}>
          <span className={`mt-px flex shrink-0 [&_svg]:size-3.5 ${bad ? '' : 'text-muted-foreground'}`}>{icon}</span>
          {text}
        </li>
      ))}
    </ul>
  )
}

type KeyState = Pick<Mount, 'locked' | 'key_remembered'>

/** What encryption hides from Modal and what it doesn't, and where this computer keeps the key. */
export function encryptionHint(m: KeyState | undefined, t: TFunction) {
  const where = !m ? '' : m.locked ? t('enc.markLocked') : m.key_remembered ? t('enc.markRemembered') : t('enc.markSession')
  return (
    <span className="flex flex-col gap-1">
      <span className="font-medium">{t('enc.markTitle')}</span>
      <span>{t('enc.markHidden')}</span>
      <span className="opacity-75">{t('enc.markVisible')}</span>
      {where && <span className="opacity-75">{where}</span>}
      <span className="opacity-75">{t('enc.markNoChange')}</span>
    </span>
  )
}

/** A small lock after an encrypted volume's name; the hover says what Modal can and can't see. */
export function EncryptedMark({ m, className }: { m?: KeyState; className?: string }) {
  const { t } = useTranslation()
  return (
    <Hint text={encryptionHint(m, t)}>
      <span role="img" aria-label={t('enc.markTitle')} className={`inline-flex shrink-0 text-muted-foreground ${className ?? ''}`}>
        <Lock className="size-[0.8em]" strokeWidth={2.4} />
      </span>
    </Hint>
  )
}

/** The key in groups, selectable, with Copy and Save as file. */
export function KeyBlock({ value, volume }: { value: string; volume: string }) {
  const { t } = useTranslation()
  const [copied, setCopied] = useState(false)
  const [prefix, ...groups] = value.split('-')
  const copy = () =>
    navigator.clipboard?.writeText(value).then(
      () => {
        setCopied(true)
        setTimeout(() => setCopied(false), 1600)
      },
      () => toast.error(t('enc.copyFailed')),
    )
  const save = () =>
    saveText(`modalfs-key-${volume}.txt`, t('enc.file', { volume, key: value }))
      .then((done) => done && toast(t('enc.savedFile')))
      .catch((e) => toast.error(e instanceof Error ? e.message : String(e)))
  return (
    <div className="flex flex-col gap-2">
      <div
        data-selectable
        aria-label={t('enc.key')}
        className="flex flex-wrap justify-center gap-x-2 gap-y-1.5 rounded-xl border border-card-border bg-card px-3 py-3.5 font-mono text-[13.5px] font-medium tracking-wide break-all select-all"
      >
        {groups.length ? (
          <>
            <span className="text-muted-foreground">{prefix}</span>
            {groups.map((g, i) => (
              <span key={i}>{g}</span>
            ))}
          </>
        ) : (
          value
        )}
      </div>
      <div className="flex gap-2">
        <Button type="button" variant="outline" size="sm" className="flex-1" onClick={copy}>
          {copied ? <Check /> : <Copy />}
          {t(copied ? 'enc.copied' : 'enc.copy')}
        </Button>
        <Button type="button" variant="outline" size="sm" className="flex-1" onClick={save}>
          <Download />
          {t('enc.saveFile')}
        </Button>
      </div>
    </div>
  )
}

/** "Remember on this computer", and where the key goes when it is. */
export function RememberBox({ value, onChange }: { value: boolean; onChange: (v: boolean) => void }) {
  const { t } = useTranslation()
  const id = useId()
  return (
    <div className="flex items-start gap-2.5">
      <Checkbox id={id} checked={value} onCheckedChange={(v) => onChange(!!v)} className="mt-0.5" />
      <div className="flex flex-col gap-0.5">
        <label htmlFor={id} className="text-[13px]">
          {t('enc.remember')}
        </label>
        <span className="text-[12px] text-muted-foreground">{t(value ? (isMac ? 'enc.rememberMac' : 'enc.rememberOther') : 'enc.rememberOff')}</span>
      </div>
    </div>
  )
}

/** The confirmation that comes before a volume depends on a key. */
export function SavedBox({ value, onChange }: { value: boolean; onChange: (v: boolean) => void }) {
  const { t } = useTranslation()
  const id = useId()
  return (
    <div className="flex items-start gap-2.5 rounded-xl bg-destructive/8 px-3.5 py-3">
      <Checkbox id={id} checked={value} onCheckedChange={(v) => onChange(!!v)} className="mt-0.5" />
      <label htmlFor={id} className="text-[12.5px] leading-snug">
        {t('enc.savedConfirm')}
      </label>
    </div>
  )
}

/** Unlock with the key; a stopped mount then mounts. */
export function UnlockDialog({ m, open, onOpenChange }: { m: Mount; open: boolean; onOpenChange: (o: boolean) => void }) {
  const { t } = useTranslation()
  const [key, setKey] = useState('')
  const [remember, setRemember] = useState(true)
  useEffect(() => {
    if (open) {
      setKey('')
      setRemember(true)
    }
  }, [open])
  const mountAfter = m.state === 'stopped' || m.state === 'error'
  const bad = key.trim() !== '' && !normalizeKey(key)
  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      title={t('enc.unlockTitle', { name: nameOf(m) })}
      desc={t('enc.unlockP')}
      submit={t(mountAfter ? 'enc.unlockMount' : 'enc.unlock')}
      dismiss={t('enc.keepLocked')}
      canSubmit={!!normalizeKey(key) && !!m.volume_id}
      onSubmit={async () => {
        await unlock(m.account_id, m.environment ?? '', m.volume_id!, key.trim(), remember)
        toast(t('enc.unlocked', { name: m.volume_name }))
        if (mountAfter) await api('POST', `/mounts/${m.id}/start`).catch((e) => toast.error(e.message))
        refreshState()
        refreshVolumes()
      }}
    >
      <Field label={t('enc.key')} htmlFor="unlock-key" hint={t('enc.keyPasteHint')} error={bad ? t('enc.notAKey') : undefined}>
        <Input
          id="unlock-key"
          value={key}
          onChange={(e) => setKey(e.target.value)}
          autoFocus
          autoComplete="off"
          spellCheck={false}
          aria-invalid={bad}
          placeholder="mfs1-…"
          className="font-mono text-[13px]"
        />
      </Field>
      <RememberBox value={remember} onChange={setRemember} />
    </FormDialog>
  )
}

/** The key, again, from a computer that remembers it. */
export function ShowKeyDialog({ m, open, onOpenChange }: { m: Mount; open: boolean; onOpenChange: (o: boolean) => void }) {
  const { t } = useTranslation()
  const [key, setKey] = useState('')
  const [error, setError] = useState('')
  useEffect(() => {
    if (!open) return
    setKey('')
    setError('')
    showKey(m.account_id, m.environment ?? '', m.volume_id!).then(setKey, (e) => setError(e instanceof Error ? e.message : String(e)))
  }, [open]) // eslint-disable-line react-hooks/exhaustive-deps
  return (
    <FormDialog open={open} onOpenChange={onOpenChange} title={t('enc.showTitle', { name: nameOf(m) })} desc={t('enc.showP')} submit={t('form.done')} dismiss={t('enc.close')} onSubmit={async () => {}}>
      {key ? (
        <KeyBlock value={key} volume={m.volume_name} />
      ) : error ? (
        <p role="alert" className="rounded-lg bg-destructive/8 px-3 py-2 text-[12.5px] text-destructive">
          {error}
        </p>
      ) : (
        <div className="flex justify-center py-4">
          <Spinner className="text-muted-foreground" />
        </div>
      )}
    </FormDialog>
  )
}

/** Forget the key on this computer: it then needs the key pasted to mount again. */
export function askLock(m: Mount, t: TFunction) {
  confirm({
    title: t('enc.lockQ', { name: nameOf(m) }),
    desc: t('enc.lockP'),
    action: t('enc.lock'),
    dismiss: t('enc.keepUnlocked'),
    run: () =>
      lock(m.account_id, m.environment ?? '', m.volume_id!)
        .then(() => toast(t('enc.lockedToast', { name: m.volume_name })))
        .catch((e) => toast.error(e.message))
        .finally(() => {
          refreshState()
          refreshVolumes()
        }),
  })
}
