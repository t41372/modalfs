import { Collapsible as CollapsiblePrimitive } from '@base-ui/react/collapsible'
import { ChevronRight, FolderOpen, MoreHorizontal, Pencil, Plus, ShieldCheck, SlidersHorizontal, Unplug, KeyRound } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { api } from '@/api/client'
import { patchSettings, refreshDownloads, refreshState, useAction, useLoadedState } from '@/api/queries'
import type { Account, Appearance, Identity, Language, Settings } from '@/api/types'
import { startQuit } from '@/app/quitFlow'
import { AccountMark, Bar, Hint } from '@/components/bits'
import { Choice } from '@/components/Choice'
import { confirm } from '@/components/confirm'
import { Field, FormDialog } from '@/components/FormDialog'
import { Card, Mono, Page, Row, Section } from '@/components/page'
import { Segmented } from '@/components/Segmented'
import { Button } from '@/components/ui/button'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { AccountEnvDialog, RenameAccountDialog, ReplaceTokenDialog } from '@/dialogs/Account'
import { ConnectDialog } from '@/dialogs/Connect'
import { ENDED, HOURLY, RUNNING } from '@/lib/downloads'
import { bytes, homeOf, tilde, untilde } from '@/lib/format'
import { live } from '@/lib/mounts'

// The cache holds clean 8 MiB blocks only (unsaved writes live elsewhere), so any size is safe;
// below the recommendation, read-ahead and reopened files get evicted and downloaded again.
const CACHE_SIZES = [1, 2, 5, 10, 20, 50, 100, 200, 500]
export const CACHE_MIN_GB = 1
// Room for each sequential reader's read-ahead twice over, plus a commit's hashing: 2 × readers × (read-ahead + 1) × 8 MiB + 256 MiB.
export const CACHE_RECOMMENDED_GB = 2
const CUSTOM = 'custom'
const READ_AHEAD = [2, 4, 8, 16, 32]
const STALL = [5, 10, 20, 30, 60]

export function SettingsPage() {
  const { t } = useTranslation()
  const st = useLoadedState()
  const s = st.settings
  const home = homeOf(s.mount_root)
  const [editingRoot, setEditingRoot] = useState(false)
  const [customCache, setCustomCache] = useState(false)
  const [connecting, setConnecting] = useState(false)

  const set = useAction((body: Settings | { transport: string }) => patchSettings(body), {
    optimistic: (state, body) => Object.assign(state.settings, body),
  })
  const service = useAction((install: boolean) => api('POST', `/service/${install ? 'install' : 'uninstall'}`), {
    optimistic: (state, install) => {
      state.service.installed = install
    },
  })
  const sizes = [...new Set([...CACHE_SIZES, s.cache_size_gb ?? 20])].sort((a, b) => a - b)
  const used = st.cache.bytes + (st.cache.pending ?? 0)
  const pct = st.cache.max ? Math.min(100, (used / st.cache.max) * 100) : 0

  /** Turning remote downloads off stops new ones; it asks about the ones running, which would go on billing otherwise. */
  const setDownloads = (on: boolean) => {
    const running = (st.downloads ?? []).filter((d) => RUNNING.has(d.state))
    if (on || running.length === 0) return set.mutate({ remote_downloads: on })
    confirm({
      title: t('settings.dlOffQ'),
      desc: t('settings.dlOffP', { count: running.length }),
      action: t('settings.dlOffPause', { count: running.length }),
      dismiss: t('settings.dlKeepOn'),
      alt: { label: t('settings.dlOffOnly'), run: () => patchSettings({ remote_downloads: false }).finally(refreshState) },
      run: async () => {
        const paused = await Promise.allSettled(running.map((d) => api('POST', `/downloads/${d.id}/pause`, {})))
        refreshDownloads()
        // one that didn't pause would go on billing: don't turn downloads off as if it had
        const failed = paused.find((p) => p.status === 'rejected')
        if (failed) throw failed.reason
        await patchSettings({ remote_downloads: false })
        refreshState()
      },
    })
  }

  const clearCache = () =>
    confirm({
      title: t('settings.clearQ'),
      desc: t('settings.clearP'),
      action: t('settings.clearCache'),
      dismiss: t('settings.keepCache'),
      run: () =>
        api('POST', '/cache/clear')
          .then(() => toast.success(t('toast.cacheCleared')))
          .catch((e) => toast.error(e.message))
          .finally(refreshState),
    })

  return (
    <Page title={t('settings.title')}>
      <Section label={t('settings.accounts')}>
        <Card>
          {st.accounts.map((a) => (
            <AccountRow key={a.id} a={a} />
          ))}
          <div className="px-3 py-2">
            <Button variant="ghost" className="text-muted-foreground" onClick={() => setConnecting(true)}>
              <Plus />
              {t('settings.connect')}
            </Button>
          </div>
        </Card>
      </Section>

      <Section label={t('settings.general')}>
        <Card>
          {st.service.supported && (
            <Row
              title={
                <Hint text={t('settings.loginD')}>
                  <span>{t('settings.login')}</span>
                </Hint>
              }
              desc={st.service.installed && st.service.binary_missing ? <span className="text-warn-ink">{t('settings.loginMissing', { path: tilde(st.service.binary, home) })}</span> : undefined}
              wrap={false}
            >
              <Switch checked={st.service.installed} disabled={service.isPending} onCheckedChange={(v) => service.mutate(v)} aria-label={t('settings.login')} />
            </Row>
          )}
          <Row title={<Hint text={t('settings.mountRootD')}><span>{t('settings.mountRoot')}</span></Hint>}>
            <Button variant="outline" className="font-mono text-[12.5px]" onClick={() => setEditingRoot(true)}>
              {tilde(s.mount_root, home)}
            </Button>
          </Row>
          <Row title={t('settings.appearance')}>
            <Segmented<Appearance>
              label={t('settings.appearance')}
              value={s.appearance || 'system'}
              onChange={(v) => set.mutate({ appearance: v })}
              options={[
                { value: 'system', label: t('settings.system') },
                { value: 'light', label: t('settings.light') },
                { value: 'dark', label: t('settings.dark') },
              ]}
            />
          </Row>
          <Row title={t('settings.language')}>
            <Choice<Language>
              label={t('settings.language')}
              className="min-w-36"
              value={s.language || 'system'}
              onChange={(v) => set.mutate({ language: v })}
              options={[
                { value: 'system', label: t('settings.system') },
                { value: 'zh-Hant', label: '繁體中文' },
                { value: 'zh-Hans', label: '简体中文' },
                { value: 'en', label: 'English' },
              ]}
            />
          </Row>
        </Card>
      </Section>

      <Section label={t('settings.downloads')}>
        <Card>
          <Row title={<Hint text={t('settings.remoteDownloadsD')}><span>{t('settings.remoteDownloads')}</span></Hint>} wrap={false}>
            <Switch checked={!!s.remote_downloads} onCheckedChange={setDownloads} aria-label={t('settings.remoteDownloads')} />
          </Row>
          {s.remote_downloads && (
            <>
              <Row title={<Hint text={t('settings.downloadSizeD')}><span>{t('settings.downloadSize')}</span></Hint>}>
                <Choice
                  label={t('settings.downloadSize')}
                  className="min-w-40"
                  value={s.download_size || 'small'}
                  onChange={(v) => set.mutate({ download_size: v })}
                  options={(['small', 'medium', 'large'] as const).map((z) => ({ value: z, label: t(`downloads.sizes.${z}`), hint: `$${HOURLY[z].toFixed(4)}/h` }))}
                />
              </Row>
              <Row title={<Hint text={t('settings.stallD')}><span>{t('settings.stall')}</span></Hint>}>
                <Choice
                  label={t('settings.stall')}
                  className="min-w-28"
                  value={String(s.download_stall_minutes || 10)}
                  onChange={(v) => set.mutate({ download_stall_minutes: Number(v) })}
                  options={STALL.map((m) => ({ value: String(m), label: t('settings.afterMin', { n: m }) }))}
                />
              </Row>
            </>
          )}
        </Card>
      </Section>

      <CollapsiblePrimitive.Root>
        <CollapsiblePrimitive.Trigger className="group/adv -mt-2 flex items-center gap-1 rounded px-1 text-[12.5px] font-medium text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50">
          <ChevronRight className="size-3.5 transition-transform duration-200 ease-out group-data-[panel-open]/adv:rotate-90" />
          {t('settings.advanced')}
        </CollapsiblePrimitive.Trigger>
        <CollapsiblePrimitive.Panel className="h-(--collapsible-panel-height) overflow-hidden transition-[height] duration-250 ease-[var(--ease-out)] data-[ending-style]:h-0 data-[starting-style]:h-0 motion-reduce:transition-none">
          <div className="pt-3">
            <Card>
              <Row
                title={<Hint text={t('settings.cacheD')}><span>{t('settings.cache')}</span></Hint>}
                desc={
                  <>
                    <span className="tabular">
                      {st.cache.pending > 0
                        ? t('settings.cacheUsedPending', { used: bytes(used), max: bytes(st.cache.max), pending: bytes(st.cache.pending) })
                        : t('settings.cacheUsed', { used: bytes(used), max: bytes(st.cache.max) })}
                    </span>
                    {(s.cache_size_gb ?? 20) < CACHE_RECOMMENDED_GB && <span className="mt-1 block text-warn-ink">{t('settings.cacheSmall', { n: CACHE_RECOMMENDED_GB })}</span>}
                    <Bar pct={pct} className="mt-2 max-w-72" label={t('settings.cache')} />
                  </>
                }
              >
                <Choice
                  label={t('settings.cacheSize')}
                  className="min-w-24"
                  value={String(s.cache_size_gb ?? 20)}
                  onChange={(v) => (v === CUSTOM ? setCustomCache(true) : set.mutate({ cache_size_gb: Number(v) }))}
                  options={[...sizes.map((g) => ({ value: String(g), label: `${g} GB` })), { value: CUSTOM, label: t('settings.custom') }]}
                />
                <Button variant="outline" disabled={!st.cache.bytes} onClick={clearCache}>
                  {t('settings.clearCache')}
                </Button>
              </Row>
              <Row title={<Hint text={t('settings.readAheadD')}><span>{t('settings.readAhead')}</span></Hint>}>
                <Choice
                  label={t('settings.readAhead')}
                  className="min-w-28"
                  value={String(s.read_ahead ?? 8)}
                  onChange={(v) => set.mutate({ read_ahead: Number(v) })}
                  options={READ_AHEAD.map((n) => ({ value: String(n), label: `${n} × 8 MiB` }))}
                />
              </Row>
              <Row title={<Hint text={t('settings.transportD')}><span>{t('settings.transport')}</span></Hint>}>
                <Segmented
                  label={t('settings.transport')}
                  value={s.transport === 'http1' ? 'http1' : 'auto'}
                  onChange={(v) => set.mutate({ transport: v })}
                  options={[
                    { value: 'auto', label: t('settings.auto') },
                    { value: 'http1', label: 'HTTP/1.1' },
                  ]}
                />
              </Row>
              <Row title={t('settings.configDir')} desc={<Mono>{tilde(st.config_dir, home)}</Mono>}>
                <Button variant="ghost" onClick={() => api('POST', '/open', { target: 'config' }).catch(() => {})}>
                  <FolderOpen />
                  {t('settings.open')}
                </Button>
              </Row>
            </Card>
          </div>
        </CollapsiblePrimitive.Panel>
      </CollapsiblePrimitive.Root>

      <div className="flex flex-wrap items-center justify-between gap-3 border-t border-card-border pt-4">
        <span className="flex items-center gap-1 text-[12.5px] text-muted-foreground">
          <span data-selectable className="font-mono text-[12px]">
            modalfs {st.version}
            {st.build && ` · ${st.build}`} · {st.platform}
          </span>
          <Button variant="ghost" size="sm" className="text-muted-foreground" onClick={() => api('POST', '/open', { target: 'logs' }).catch(() => {})}>
            {t('settings.openLogs')}
          </Button>
        </span>
        {/* Wider windows have it in the rail. */}
        <Hint text={t('settings.quitD')}>
          <Button variant="outline" className="sm:hidden" onClick={() => void startQuit()}>
            {t('settings.quitButton')}
          </Button>
        </Hint>
      </div>

      <CacheSizeDialog open={customCache} onOpenChange={setCustomCache} current={s.cache_size_gb ?? 20} onSave={(gb) => patchSettings({ cache_size_gb: gb }).then(refreshState)} />
      <MountRootDialog open={editingRoot} onOpenChange={setEditingRoot} current={tilde(s.mount_root, home)} home={home} />
      <ConnectDialog open={connecting} onOpenChange={setConnecting} />
    </Page>
  )
}

function AccountRow({ a }: { a: Account }) {
  const { t } = useTranslation()
  const st = useLoadedState()
  const [token, setToken] = useState<Account | null>(null)
  const [renaming, setRenaming] = useState<Account | null>(null)
  const [env, setEnv] = useState<Account | null>(null)
  const mounts = st.mounts.filter((m) => m.account_id === a.id)
  const liveMounts = mounts.filter(live)
  const activeDownloads = (st.downloads ?? []).filter((d) => d.account_id === a.id && !ENDED.has(d.state))
  const store = { keychain: t('acct.storeKeychain'), file: t('acct.storeFile'), env: t('acct.storeEnv') }[a.secret_store] ?? a.secret_store
  const blocked = liveMounts.length > 0 ? t('acct.disconnectMounted') : activeDownloads.length > 0 ? t('acct.disconnectDownloads') : ''

  const verify = useAction(() => api<Identity>('POST', `/accounts/${a.id}/verify`), {
    success: () => t('acct.verified', { w: a.workspace || a.label }),
  })
  const disconnect = () =>
    confirm({
      title: t('acct.disconnectQ', { name: a.label }),
      desc: mounts.length ? t('acct.disconnectP', { count: mounts.length }) : t('acct.disconnectNone'),
      action: t('acct.disconnect'),
      dismiss: t('acct.keep'),
      danger: true,
      run: () =>
        api('DELETE', `/accounts/${a.id}`)
          .catch((e) => toast.error(e.message))
          .finally(refreshState),
    })

  const about = [a.workspace && a.workspace !== a.label ? a.workspace : '', `${a.token_id.slice(0, 7)}…`, store, a.imported_from ? t('acct.fromProfile', { name: a.imported_from }) : ''].filter(Boolean).join(' · ')
  return (
    <div className="flex items-center gap-3 py-3 pr-3 pl-4">
      <AccountMark id={a.id} label={a.label} size={30} />
      <Hint text={about}>
        <div className="min-w-0 flex-1">
          <div className="truncate text-[14.5px] font-medium">{a.label}</div>
          <div className="truncate text-[12.5px] text-muted-foreground">
            {[a.environment ? t('acct.envIs', { env: a.environment }) : '', t('acct.volumes', { count: mounts.length })].filter(Boolean).join(' · ')}
          </div>
        </div>
      </Hint>
      <DropdownMenu>
        <DropdownMenuTrigger render={<Button variant="ghost" size="icon" aria-label={t('acct.more', { name: a.label })} />}>
          <MoreHorizontal className="text-muted-foreground" />
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="min-w-52">
          <DropdownMenuItem disabled={verify.isPending} onClick={() => verify.mutate()}>
            <ShieldCheck />
            {t('acct.verify')}
          </DropdownMenuItem>
          <DropdownMenuItem onClick={() => setToken(a)}>
            <KeyRound />
            {t('acct.replaceToken')}
          </DropdownMenuItem>
          <DropdownMenuItem onClick={() => setRenaming(a)}>
            <Pencil />
            {t('acct.rename')}
          </DropdownMenuItem>
          <DropdownMenuItem onClick={() => setEnv(a)}>
            <SlidersHorizontal />
            {t('acct.env')}
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem variant="destructive" disabled={!!blocked} onClick={disconnect}>
            <Unplug />
            {blocked || t('acct.disconnect')}
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      <ReplaceTokenDialog account={token} onClose={() => setToken(null)} />
      <RenameAccountDialog account={renaming} onClose={() => setRenaming(null)} />
      <AccountEnvDialog account={env} onClose={() => setEnv(null)} />
    </div>
  )
}

function MountRootDialog({ open, onOpenChange, current, home }: { open: boolean; onOpenChange: (o: boolean) => void; current: string; home: string }) {
  const { t } = useTranslation()
  const [value, setValue] = useState(current)
  useEffect(() => {
    if (open) setValue(current)
  }, [open, current])
  const full = untilde(value.trim(), home)
  const bad = value.trim() !== '' && !full.startsWith('/')
  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      title={t('settings.mountRoot')}
      desc={t('settings.mountRootD')}
      submit={t('form.save')}
      canSubmit={!!value.trim() && !bad && value.trim() !== current}
      onSubmit={async () => {
        await patchSettings({ mount_root: full })
        refreshState()
      }}
    >
      <Field label={t('form.path')} htmlFor="mount-root" error={bad ? t('form.pathBad') : undefined}>
        <Input id="mount-root" className="font-mono text-[13px]" value={value} onChange={(e) => setValue(e.target.value)} spellCheck={false} autoFocus aria-invalid={bad} />
      </Field>
    </FormDialog>
  )
}

function CacheSizeDialog({ open, onOpenChange, current, onSave }: { open: boolean; onOpenChange: (o: boolean) => void; current: number; onSave: (gb: number) => Promise<unknown> }) {
  const { t } = useTranslation()
  const [value, setValue] = useState(String(current))
  useEffect(() => {
    if (open) setValue(String(current))
  }, [open, current])
  const gb = Number(value)
  const valid = Number.isInteger(gb) && gb >= CACHE_MIN_GB
  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      title={t('settings.cacheSize')}
      desc={t('settings.cacheSizeD', { min: CACHE_MIN_GB, n: CACHE_RECOMMENDED_GB })}
      submit={t('form.save')}
      canSubmit={valid && gb !== current}
      onSubmit={async () => {
        await onSave(gb)
      }}
    >
      <Field
        label="GB"
        htmlFor="cache-gb"
        error={value !== '' && !valid ? t('settings.cacheSizeBad', { min: CACHE_MIN_GB }) : undefined}
        hint={valid && gb < CACHE_RECOMMENDED_GB ? <span className="text-warn-ink">{t('settings.cacheSmall', { n: CACHE_RECOMMENDED_GB })}</span> : undefined}
      >
        <Input id="cache-gb" type="number" inputMode="numeric" min={CACHE_MIN_GB} step={1} value={value} onChange={(e) => setValue(e.target.value)} autoFocus className="tabular" />
      </Field>
    </FormDialog>
  )
}
