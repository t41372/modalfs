import { Check, Plus, Search } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { api } from '@/api/client'
import { refreshState, refreshVolumes, useEnvironments, useLoadedState, useVolumes } from '@/api/queries'
import type { Mount } from '@/api/types'
import { Choice } from '@/components/Choice'
import { Spinner } from '@/components/Spinner'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { CreateVolumeDialog } from '@/dialogs/Volume'
import { date } from '@/lib/format'

const DEFAULT = '__default__'
/** At most this many rows; typing narrows the rest. A workspace can hold hundreds of volumes. */
const SHOWN = 40

/** Find a volume in an account by name and let modalfs manage it (mounted under the default location). */
export function AddVolumeDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const { t, i18n } = useTranslation()
  const st = useLoadedState()
  const [account, setAccount] = useState('')
  const [env, setEnv] = useState(DEFAULT)
  const [query, setQuery] = useState('')
  const [added, setAdded] = useState<string[]>([])
  const [busy, setBusy] = useState('')
  const [creating, setCreating] = useState(false)
  useEffect(() => {
    if (!open) return
    setAccount((a) => (st.accounts.some((x) => x.id === a) ? a : st.accounts[0]?.id || ''))
    setQuery('')
    setAdded([])
  }, [open]) // eslint-disable-line react-hooks/exhaustive-deps

  const envName = env === DEFAULT ? '' : env
  const envs = useEnvironments(open ? account : undefined)
  const vols = useVolumes(open ? account : undefined, envName)
  const managed = (id: string, name: string) =>
    st.mounts.some((m) => m.account_id === account && (m.volume_id ? m.volume_id === id : m.volume_name === name && (m.environment || '') === envName))
  const { list, matches, total } = useMemo(() => {
    const q = query.trim().toLowerCase()
    const all = (vols.data ?? []).filter((v) => !managed(v.id, v.name) || added.includes(v.id))
    const hits = all
      .filter((v) => !q || v.name.toLowerCase().includes(q))
      // Exact and prefix matches first, then the newest.
      .sort((a, b) => rank(a.name, q) - rank(b.name, q) || (b.created_at ?? 0) - (a.created_at ?? 0))
    return { list: hits.slice(0, SHOWN), matches: hits.length, total: all.length }
  }, [vols.data, query, added, st.mounts, account, envName]) // eslint-disable-line react-hooks/exhaustive-deps

  async function add(name: string, id: string) {
    setBusy(id)
    try {
      const r = await api<{ mount: Mount; error?: string }>('POST', '/mounts', { account_id: account, environment: envName, volume_name: name, filter_junk: true, start: true })
      setAdded((x) => [...x, id])
      if (r.error) toast.error(r.error)
      else toast(t('toast.mounting', { name }))
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy('')
      refreshState()
      refreshVolumes()
    }
  }

  return (
    <>
      <Dialog open={open} onOpenChange={onOpenChange}>
        <DialogContent className="max-h-[calc(100dvh-2rem)] gap-4 overflow-y-auto overscroll-contain sm:max-w-[520px]" showCloseButton={false}>
          <DialogHeader>
            <DialogTitle>{t('addVol.title')}</DialogTitle>
            <DialogDescription>{t('addVol.desc')}</DialogDescription>
          </DialogHeader>
          <div className="flex flex-wrap gap-2">
            <div className="flex h-9 min-w-0 flex-1 basis-60 items-center gap-2 rounded-lg border border-input bg-transparent px-2.5 focus-within:border-ring focus-within:ring-3 focus-within:ring-ring/50 dark:bg-input/30">
              <Search className="size-4 shrink-0 text-muted-foreground" />
              <input
                autoFocus
                type="search"
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                placeholder={t('addVol.search')}
                aria-label={t('addVol.search')}
                spellCheck={false}
                className="min-w-0 flex-1 bg-transparent text-[13.5px] outline-none placeholder:text-muted-foreground"
              />
            </div>
            {st.accounts.length > 1 && (
              <Choice
                label={t('form.account')}
                className="h-9 min-w-0"
                value={account}
                onChange={(v) => {
                  setAccount(v)
                  setEnv(DEFAULT)
                }}
                options={st.accounts.map((a) => ({ value: a.id, label: a.label }))}
              />
            )}
            {(envs.data?.length ?? 0) > 1 && (
              <Choice
                label={t('form.environment')}
                className="h-9 min-w-0"
                value={env}
                onChange={setEnv}
                options={[{ value: DEFAULT, label: t('addVol.defaultEnv') }, ...(envs.data ?? []).map((e) => ({ value: e.name, label: e.name }))]}
              />
            )}
          </div>

          <div className="max-h-[300px] min-h-[120px] overflow-y-auto rounded-xl border border-card-border [&>*+*]:border-t [&>*+*]:border-card-border">
            {vols.isLoading ? (
              <div className="flex items-center justify-center gap-2 py-10 text-[13px] text-muted-foreground">
                <Spinner />
                {t('form.loading')}
              </div>
            ) : vols.isError ? (
              <div className="px-4 py-8 text-center text-[13px] text-muted-foreground">{vols.error.message}</div>
            ) : list.length === 0 ? (
              <div className="px-4 py-10 text-center text-[13px] text-muted-foreground">{query ? t('addVol.noMatch', { query }) : t('addVol.none')}</div>
            ) : (
              list.map((v) => {
                const done = added.includes(v.id)
                return (
                  <div key={v.id} className="flex items-center gap-3 px-3.5 py-2.5">
                    <div className="min-w-0 flex-1">
                      <div className="truncate text-[13.5px] font-medium">{v.name}</div>
                      <div className="truncate text-[12px] text-muted-foreground">
                        {v.format !== 'v2' ? t('addVol.v1') : v.created_by ? t('vol.createdBy', { date: date(v.created_at, i18n.language), who: v.created_by }) : t('vol.created', { date: date(v.created_at, i18n.language) })}
                      </div>
                    </div>
                    {done ? (
                      <span className="flex items-center gap-1 text-[12.5px] text-ok-ink">
                        <Check className="size-3.5" />
                        {t('addVol.added')}
                      </span>
                    ) : (
                      <Button size="sm" variant="outline" disabled={v.format !== 'v2' || !!busy} onClick={() => add(v.name, v.id)}>
                        {busy === v.id && <Spinner className="size-3" />}
                        {t('addVol.add')}
                      </Button>
                    )}
                  </div>
                )
              })
            )}
          </div>
          {!vols.isLoading && total > 0 && (
            <p className="-mt-2 px-1 text-[12px] text-muted-foreground">
              {matches > SHOWN ? t('addVol.narrow', { shown: SHOWN, count: matches }) : query ? t('addVol.matches', { count: matches, total }) : t('addVol.count', { count: total })}
            </p>
          )}

          <DialogFooter className="items-center sm:justify-between">
            <Button variant="ghost" onClick={() => setCreating(true)} className="text-muted-foreground">
              <Plus />
              {t('addVol.create')}
            </Button>
            <Button variant="outline" onClick={() => onOpenChange(false)}>
              {t('form.done')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
      <CreateVolumeDialog open={creating} onOpenChange={setCreating} account={account} env={envName} />
    </>
  )
}

const rank = (name: string, q: string) => {
  if (!q) return 0
  const n = name.toLowerCase()
  return n === q ? 0 : n.startsWith(q) ? 1 : 2
}
