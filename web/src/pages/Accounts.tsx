import { Download, KeyRound, Plus, ShieldCheck, Trash2 } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { api } from '@/api/client'
import { refreshState, useLoadedState } from '@/api/queries'
import type { Account, Identity } from '@/api/types'
import { confirm } from '@/components/confirm'
import { Badge, Card, Empty, Page, Section } from '@/components/page'
import { Spinner } from '@/components/Spinner'
import { Button } from '@/components/ui/button'
import { AddAccountDialog } from '@/dialogs/AddAccount'
import { ImportDialog } from '@/dialogs/Import'

export function Accounts() {
  const { t } = useTranslation()
  const st = useLoadedState()
  const [adding, setAdding] = useState(false)
  const [importing, setImporting] = useState(false)
  const buttons = (
    <>
      <Button variant="ghost" onClick={() => setImporting(true)}>
        <Download />
        {t('accounts.import')}
      </Button>
      <Button variant="outline" onClick={() => setAdding(true)}>
        <Plus />
        {t('accounts.add')}
      </Button>
    </>
  )
  return (
    <Page title={t('accounts.title')} sub={t('accounts.sub')} actions={st.accounts.length > 0 && buttons}>
      {st.accounts.length === 0 ? (
        <Card>
          <Empty icon={<KeyRound />} title={t('accounts.empty')} desc={t('accounts.emptyP')}>
            <Button onClick={() => setAdding(true)}>
              <Plus />
              {t('accounts.add')}
            </Button>
            <Button variant="outline" onClick={() => setImporting(true)}>
              <Download />
              {t('accounts.import')}
            </Button>
          </Empty>
        </Card>
      ) : (
        <Section>
          <Card>
            {st.accounts.map((a) => (
              <AccountRow
                key={a.id}
                a={a}
                mounts={st.mounts.filter((m) => m.account_id === a.id).length}
                busy={st.mounts.some((m) => m.account_id === a.id && (m.state === 'starting' || m.state === 'stopping'))}
              />
            ))}
          </Card>
        </Section>
      )}
      <AddAccountDialog open={adding} onOpenChange={setAdding} />
      <ImportDialog open={importing} onOpenChange={setImporting} />
    </Page>
  )
}

function AccountRow({ a, mounts, busy }: { a: Account; mounts: number; busy: boolean }) {
  const { t } = useTranslation()
  const [checking, setChecking] = useState(false)
  const store = { keychain: t('accounts.storeKeychain'), file: t('accounts.storeFile'), env: t('accounts.storeEnv') }[a.secret_store] ?? a.secret_store

  async function verify() {
    setChecking(true)
    try {
      const id = await api<Identity>('POST', `/accounts/${a.id}/verify`)
      toast.success(t('accounts.verified', { w: id.workspace_name }))
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e))
    } finally {
      setChecking(false)
    }
  }
  const remove = () =>
    confirm({
      title: t('accounts.removeQ', { name: a.label }),
      desc: t('accounts.removeP'),
      action: t('accounts.remove'),
      danger: true,
      run: () =>
        api('DELETE', `/accounts/${a.id}`)
          .catch((e) => toast.error(e.message))
          .finally(refreshState),
    })

  const initial = (a.workspace || a.label).slice(0, 1).toUpperCase()
  return (
    <div className="flex items-center gap-3.5 py-3.5 pr-3 pl-5 max-[520px]:flex-wrap">
      <div aria-hidden className="flex size-9 shrink-0 items-center justify-center rounded-xl bg-gradient-to-br from-brand/20 to-brand/5 text-[14px] font-semibold text-brand">
        {initial}
      </div>
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-[14.5px] font-medium">{a.label}</span>
          {a.workspace && a.workspace !== a.label && <span className="text-[13px] text-muted-foreground">{a.workspace}</span>}
          {mounts > 0 && <Badge>{t('accounts.mounts', { count: mounts })}</Badge>}
        </div>
        <div className="mt-0.5 flex min-w-0 flex-wrap items-center gap-x-1.5 text-[12px] text-muted-foreground">
          <span data-selectable className="font-mono text-[11.5px]">
            {a.token_id.slice(0, 7)}…
          </span>
          <span aria-hidden>·</span>
          <span>{store}</span>
          {a.imported_from && (
            <>
              <span aria-hidden>·</span>
              <span>{t('accounts.fromProfile', { name: a.imported_from })}</span>
            </>
          )}
        </div>
      </div>
      <div className="flex shrink-0 items-center gap-1 max-[520px]:ml-12">
        <Button variant="ghost" size="sm" disabled={checking} onClick={verify}>
          {checking ? <Spinner className="size-3" /> : <ShieldCheck />}
          {t('accounts.verify')}
        </Button>
        <Button variant="ghost" size="icon-sm" aria-label={t('accounts.remove')} title={t('accounts.remove')} disabled={busy} onClick={remove} className="text-muted-foreground hover:bg-destructive/10 hover:text-destructive">
          <Trash2 />
        </Button>
      </div>
    </div>
  )
}
