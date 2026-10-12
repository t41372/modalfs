import { Boxes, Check, MoreHorizontal, Pencil, Plus, RefreshCw, Trash2 } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { api } from '@/api/client'
import { refreshState, refreshVolumes, useEnvironments, useLoadedState, useVolumes } from '@/api/queries'
import type { Mount, Volume } from '@/api/types'
import { Choice } from '@/components/Choice'
import { Badge, Card, Empty, Page, Section } from '@/components/page'
import { Spinner } from '@/components/Spinner'
import { Button } from '@/components/ui/button'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { AddAccountDialog } from '@/dialogs/AddAccount'
import { CreateVolumeDialog, DeleteVolumeDialog, RenameVolumeDialog } from '@/dialogs/Volume'
import { date } from '@/lib/format'

const DEFAULT = '__default__'
// Remembered while the window is open, so coming back shows the same list.
const picked = { account: '', env: DEFAULT }

export function Volumes() {
  const { t, i18n } = useTranslation()
  const st = useLoadedState()
  const remembered = st.accounts.some((a) => a.id === picked.account)
  const [account, setAccountState] = useState(() => (remembered ? picked.account : st.accounts[0]?.id || ''))
  // The remembered environment belongs to the remembered account only.
  const [env, setEnvState] = useState(() => (remembered ? picked.env : DEFAULT))
  const setAccount = (a: string) => {
    picked.account = a
    picked.env = DEFAULT
    setAccountState(a)
    setEnvState(DEFAULT)
  }
  const setEnv = (e: string) => {
    picked.env = e
    setEnvState(e)
  }
  // The account was removed elsewhere: fall back to the first one.
  useEffect(() => {
    if (!st.accounts.some((a) => a.id === account) && st.accounts[0]) setAccount(st.accounts[0].id)
  }, [st.accounts, account])

  const envName = env === DEFAULT ? '' : env
  const envs = useEnvironments(account)
  const vols = useVolumes(account, envName)
  const [creating, setCreating] = useState(false)
  const [renaming, setRenaming] = useState<Volume | null>(null)
  const [deleting, setDeleting] = useState<Volume | null>(null)
  const [addingAccount, setAddingAccount] = useState(false)

  if (!st.accounts.length) {
    return (
      <Page title={t('volumes.title')} sub={t('volumes.sub')}>
        <Card>
          <Empty icon={<Boxes />} title={t('accounts.empty')} desc={t('mounts.emptyNoAccount')}>
            <Button onClick={() => setAddingAccount(true)}>
              <Plus />
              {t('accounts.add')}
            </Button>
          </Empty>
        </Card>
        <AddAccountDialog open={addingAccount} onOpenChange={setAddingAccount} />
      </Page>
    )
  }

  // Whether each volume is mounted comes from the polled state, not the volume list, which is only
  // fetched now and then: a mount removed or stopped a moment ago must not show here as current.
  const mountFor = (v: Volume) =>
    st.mounts.find((m) => m.account_id === account && (m.volume_id ? m.volume_id === v.id : m.volume_name === v.name && (m.environment || '') === envName))
  // Newest first; v1 volumes, which can't be mounted, last.
  const list = [...(vols.data ?? [])]
    .map((v) => {
      const m = mountFor(v)
      return { ...v, mount_id: m?.id, mounted: m?.state === 'mounted' }
    })
    .sort((a, b) => Number(a.format !== 'v2') - Number(b.format !== 'v2') || (b.created_at ?? 0) - (a.created_at ?? 0))

  return (
    <Page
      title={t('volumes.title')}
      sub={t('volumes.sub')}
      actions={
        <Button variant="outline" onClick={() => setCreating(true)}>
          <Plus />
          {t('volumes.create')}
        </Button>
      }
    >
      <Section
        label={
          <span className="flex flex-wrap items-center gap-2">
            <Choice
              label={t('volumes.account')}
              className="h-7 min-w-0 rounded-full border-transparent bg-muted px-3 text-[12.5px] font-medium text-foreground"
              value={account}
              onChange={setAccount}
              options={st.accounts.map((a) => ({ value: a.id, label: a.label, hint: a.workspace && a.workspace !== a.label ? a.workspace : undefined }))}
            />
            <Choice
              label={t('volumes.environment')}
              className="h-7 min-w-0 rounded-full border-transparent bg-muted px-3 text-[12.5px] font-medium text-foreground"
              value={env}
              onChange={setEnv}
              options={[
                { value: DEFAULT, label: t('volumes.defaultEnv') },
                ...(envs.data ?? []).map((e) => ({ value: e.name, label: e.name, hint: e.default ? t('volumes.default') : undefined })),
              ]}
            />
          </span>
        }
        aside={
          <Button variant="ghost" size="icon-sm" aria-label={t('volumes.refresh')} title={t('volumes.refresh')} disabled={vols.isFetching} onClick={() => vols.refetch()}>
            {vols.isFetching ? <Spinner className="size-3.5" /> : <RefreshCw className="text-muted-foreground" />}
          </Button>
        }
      >
        <Card>
          {vols.isLoading ? (
            <SkeletonRows />
          ) : vols.isError ? (
            <Empty icon={<Boxes />} title={t('volumes.loadFailed')} desc={vols.error.message}>
              <Button variant="outline" onClick={() => vols.refetch()}>
                {t('volumes.retry')}
              </Button>
            </Empty>
          ) : list.length === 0 ? (
            <Empty icon={<Boxes />} title={t('volumes.empty')} desc={t('volumes.emptyP')}>
              <Button onClick={() => setCreating(true)}>
                <Plus />
                {t('volumes.create')}
              </Button>
            </Empty>
          ) : (
            list.map((v) => (
              <VolumeRow key={v.id} v={v} account={account} env={envName} lang={i18n.language} onRename={() => setRenaming(v)} onDelete={() => setDeleting(v)} />
            ))
          )}
        </Card>
      </Section>
      <CreateVolumeDialog open={creating} onOpenChange={setCreating} account={account} env={envName} />
      <RenameVolumeDialog volume={renaming} onClose={() => setRenaming(null)} account={account} env={envName} />
      <DeleteVolumeDialog volume={deleting} onClose={() => setDeleting(null)} account={account} env={envName} />
    </Page>
  )
}

function VolumeRow({ v, account, env, lang, onRename, onDelete }: { v: Volume; account: string; env: string; lang: string; onRename: () => void; onDelete: () => void }) {
  const { t } = useTranslation()
  const [busy, setBusy] = useState(false)
  const v2 = v.format === 'v2'

  async function mount() {
    setBusy(true)
    try {
      if (v.mount_id) {
        await api('POST', `/mounts/${v.mount_id}/start`)
      } else {
        const r = await api<{ mount: Mount; error?: string }>('POST', '/mounts', { account_id: account, environment: env, volume_name: v.name, filter_junk: true, start: true })
        if (r.error) throw new Error(r.error)
        toast(t('toast.mounting', { name: r.mount.volume_name }))
      }
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
      refreshState()
      refreshVolumes()
    }
  }

  const when = date(v.created_at, lang)
  return (
    <div className="flex items-center gap-3 py-3 pr-3 pl-5">
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span data-selectable className="truncate text-[14.5px] font-medium">
            {v.name}
          </span>
          {!v2 && <Badge tone="warn">v1</Badge>}
        </div>
        <div className="mt-0.5 truncate text-[12.5px] text-muted-foreground">
          {!v2 ? t('volumes.v1') : v.created_by ? t('volumes.createdBy', { date: when, who: v.created_by }) : t('volumes.created', { date: when })}
        </div>
      </div>
      {v.mounted ? (
        <Badge tone="ok">
          <Check />
          {t('volumes.mounted')}
        </Badge>
      ) : (
        v2 && (
          <Button variant="outline" size="sm" disabled={busy} onClick={mount}>
            {busy && <Spinner className="size-3" />}
            {t('volumes.mount')}
          </Button>
        )
      )}
      <DropdownMenu>
        <DropdownMenuTrigger render={<Button variant="ghost" size="icon-sm" aria-label={t('volumes.more', { name: v.name })} />}>
          <MoreHorizontal className="text-muted-foreground" />
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="min-w-40">
          <DropdownMenuItem onClick={onRename}>
            <Pencil />
            {t('volumes.rename')}
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem variant="destructive" disabled={v.mounted} onClick={onDelete}>
            <Trash2 />
            {v.mounted ? t('volumes.deleteMounted') : t('volumes.delete')}
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  )
}

function SkeletonRows() {
  return (
    <>
      {[0, 1, 2].map((i) => (
        <div key={i} className="flex items-center gap-3 px-5 py-3.5" aria-hidden>
          <div className="flex-1">
            <div className="h-3.5 w-32 animate-pulse rounded bg-muted" style={{ animationDelay: `${i * 120}ms` }} />
            <div className="mt-2 h-3 w-52 animate-pulse rounded bg-muted" style={{ animationDelay: `${i * 120}ms` }} />
          </div>
          <div className="h-7 w-16 animate-pulse rounded-lg bg-muted" />
        </div>
      ))}
    </>
  )
}
