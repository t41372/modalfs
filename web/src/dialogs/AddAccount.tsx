import { ExternalLink } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { api } from '@/api/client'
import { refreshState } from '@/api/queries'
import type { Account } from '@/api/types'
import { Field, FormDialog } from '@/components/FormDialog'
import { Input } from '@/components/ui/input'

export function AddAccountDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const { t } = useTranslation()
  const [tokenId, setTokenId] = useState('')
  const [secret, setSecret] = useState('')
  const [label, setLabel] = useState('')
  useEffect(() => {
    if (open) {
      setTokenId('')
      setSecret('')
      setLabel('')
    }
  }, [open])

  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      title={t('form.addAccount')}
      desc={
        <>
          {t('form.tokenHint')}{' '}
          <button
            type="button"
            className="inline-flex items-center gap-0.5 font-medium text-foreground underline-offset-2 hover:underline"
            onClick={() => api('POST', '/open', { target: 'modal-tokens' }).catch(() => {})}
          >
            {t('form.tokenPage')}
            <ExternalLink className="size-3" />
          </button>
        </>
      }
      submit={t('form.add')}
      canSubmit={!!tokenId.trim() && !!secret.trim()}
      onSubmit={async () => {
        const a = await api<Account>('POST', '/accounts', { label: label.trim(), tokenId: tokenId.trim(), tokenSecret: secret.trim() })
        refreshState()
        toast.success(t('toast.accountAdded', { w: a.workspace || a.label }))
      }}
    >
      <Field label={t('form.tokenId')} htmlFor="acct-token-id">
        <Input id="acct-token-id" className="font-mono text-[13px]" value={tokenId} onChange={(e) => setTokenId(e.target.value)} placeholder="ak-…" spellCheck={false} autoFocus />
      </Field>
      <Field label={t('form.tokenSecret')} htmlFor="acct-token-secret">
        <Input id="acct-token-secret" className="font-mono text-[13px]" type="password" value={secret} onChange={(e) => setSecret(e.target.value)} placeholder="as-…" spellCheck={false} />
      </Field>
      <Field label={t('form.label')} htmlFor="acct-label" hint={t('form.labelHint')}>
        <Input id="acct-label" value={label} onChange={(e) => setLabel(e.target.value)} />
      </Field>
    </FormDialog>
  )
}
