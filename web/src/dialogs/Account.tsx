import { ExternalLink } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { api } from '@/api/client'
import { refreshState, useEnvironments } from '@/api/queries'
import type { Account } from '@/api/types'
import { Choice } from '@/components/Choice'
import { Field, FormDialog } from '@/components/FormDialog'
import { Input } from '@/components/ui/input'

/** A new token for an account whose token expired or was revoked: it must be of the same workspace. */
export function ReplaceTokenDialog({ account, onClose }: { account: Account | null; onClose: () => void }) {
  const { t } = useTranslation()
  const [tokenId, setTokenId] = useState('')
  const [secret, setSecret] = useState('')
  useEffect(() => {
    if (account) {
      setTokenId('')
      setSecret('')
    }
  }, [account])
  return (
    <FormDialog
      open={!!account}
      onOpenChange={(o) => !o && onClose()}
      title={t('acct.tokenTitle', { name: account?.label })}
      desc={
        <>
          {t('acct.tokenDesc', { workspace: account?.workspace || account?.label })}{' '}
          <button type="button" className="inline-flex items-center gap-0.5 font-medium text-foreground underline-offset-2 hover:underline" onClick={() => api('POST', '/open', { target: 'modal-tokens' }).catch(() => {})}>
            {t('form.tokenPage')}
            <ExternalLink className="size-3" />
          </button>
        </>
      }
      submit={t('acct.tokenSave')}
      canSubmit={!!tokenId.trim() && !!secret.trim()}
      onSubmit={async () => {
        await api('POST', `/accounts/${account!.id}/token`, { tokenId: tokenId.trim(), tokenSecret: secret.trim() })
        refreshState()
        toast.success(t('acct.tokenSaved'))
      }}
    >
      <Field label={t('form.tokenId')} htmlFor="new-token-id">
        <Input id="new-token-id" className="font-mono text-[13px]" value={tokenId} onChange={(e) => setTokenId(e.target.value)} placeholder="ak-…" spellCheck={false} autoFocus />
      </Field>
      <Field label={t('form.tokenSecret')} htmlFor="new-token-secret">
        <Input id="new-token-secret" className="font-mono text-[13px]" type="password" value={secret} onChange={(e) => setSecret(e.target.value)} placeholder="as-…" spellCheck={false} />
      </Field>
    </FormDialog>
  )
}

export function RenameAccountDialog({ account, onClose }: { account: Account | null; onClose: () => void }) {
  const { t } = useTranslation()
  const [name, setName] = useState('')
  useEffect(() => {
    if (account) setName(account.label)
  }, [account])
  return (
    <FormDialog
      open={!!account}
      onOpenChange={(o) => !o && onClose()}
      title={t('acct.renameTitle')}
      submit={t('form.save')}
      canSubmit={!!name.trim() && name.trim() !== account?.label}
      onSubmit={async () => {
        await api('PATCH', `/accounts/${account!.id}`, { label: name.trim() })
        refreshState()
      }}
    >
      <Field label={t('form.label')} htmlFor="acct-rename">
        <Input id="acct-rename" value={name} onChange={(e) => setName(e.target.value)} autoFocus />
      </Field>
    </FormDialog>
  )
}

const DEFAULT = '__default__'

/** The environment new volumes of this account are looked up in. */
export function AccountEnvDialog({ account, onClose }: { account: Account | null; onClose: () => void }) {
  const { t } = useTranslation()
  const envs = useEnvironments(account?.id)
  const [env, setEnv] = useState(DEFAULT)
  useEffect(() => {
    if (account) setEnv(account.environment || DEFAULT)
  }, [account])
  return (
    <FormDialog
      open={!!account}
      onOpenChange={(o) => !o && onClose()}
      title={t('acct.envTitle', { name: account?.label })}
      desc={t('acct.envDesc')}
      submit={t('form.save')}
      canSubmit={(env === DEFAULT ? '' : env) !== (account?.environment || '')}
      onSubmit={async () => {
        await api('PATCH', `/accounts/${account!.id}`, { environment: env === DEFAULT ? '' : env })
        refreshState()
      }}
    >
      <Field label={t('form.environment')}>
        <Choice
          label={t('form.environment')}
          className="w-full"
          value={env}
          onChange={setEnv}
          options={[{ value: DEFAULT, label: t('acct.workspaceDefault') }, ...(envs.data ?? []).map((e) => ({ value: e.name, label: e.name, hint: e.default ? t('acct.isDefault') : undefined }))]}
        />
      </Field>
    </FormDialog>
  )
}
