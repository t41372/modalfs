import { Collapsible as CollapsiblePrimitive } from '@base-ui/react/collapsible'
import { useQuery } from '@tanstack/react-query'
import { cn } from 'cn'
import { ChevronRight, CornerLeftUp, Folder, FolderPlus } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { api } from '@/api/client'
import { refreshDownloads, refreshState, useEnvironments, useLoadedState, useVolumes } from '@/api/queries'
import type { Download, DownloadSize, DownloadTool } from '@/api/types'
import { Choice } from '@/components/Choice'
import { Field, FormDialog } from '@/components/FormDialog'
import { Segmented } from '@/components/Segmented'
import { Spinner } from '@/components/Spinner'
import { Input } from '@/components/ui/input'
import { HOURLY } from '@/lib/downloads'

const NONE = '__default__'
type ToolChoice = 'auto' | DownloadTool

/** Paste links, pick a volume and a folder in it, and download them there in Modal. */
export function AddDownloadDialog({ open, onOpenChange, onAdded }: { open: boolean; onOpenChange: (o: boolean) => void; onAdded?: (d: Download[]) => void }) {
  const { t } = useTranslation()
  const st = useLoadedState()
  const [links, setLinks] = useState('')
  const [account, setAccount] = useState('')
  const [env, setEnv] = useState(NONE)
  const [volume, setVolume] = useState('')
  const [folder, setFolder] = useState('')
  const [tool, setTool] = useState<ToolChoice>('auto')
  const [size, setSize] = useState<DownloadSize>('small')
  const [cookies, setCookies] = useState<{ name: string; text: string } | null>(null)

  useEffect(() => {
    if (!open) return
    setLinks('')
    setAccount(st.accounts[0]?.id || '')
    setEnv(NONE)
    setVolume('')
    setFolder('')
    setTool('auto')
    setSize(st.settings.download_size || 'small')
    setCookies(null)
  }, [open]) // eslint-disable-line react-hooks/exhaustive-deps

  const envs = useEnvironments(open ? account : undefined)
  const vols = useVolumes(open ? account : undefined, env === NONE ? '' : env)
  const usable = (vols.data ?? []).filter((v) => v.format === 'v2')
  const vol = usable.find((v) => v.name === volume)
  useEffect(() => {
    if (volume && vols.data && !vol) setVolume('')
  }, [vols.data, vol, volume])
  useEffect(() => setFolder(''), [volume])

  const urls = links
    .split(/\s+/)
    .map((l) => l.trim())
    .filter(Boolean)
  const bad = urls.find((u) => !/^(https?|ftp):\/\/\S+$/i.test(u) && !/^magnet:\?\S+$/i.test(u))
  const acct = st.accounts.find((a) => a.id === account)

  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      title={t('downloads.addTitle')}
      desc={t('downloads.addP')}
      submit={urls.length > 1 ? t('downloads.addN', { count: urls.length }) : t('downloads.add')}
      canSubmit={urls.length > 0 && !bad && !!account && !!volume}
      onSubmit={async () => {
        const added = await api<Download[]>('POST', '/downloads', {
          account,
          environment: env === NONE ? '' : env,
          volume,
          folder,
          urls,
          tool: tool === 'auto' ? '' : tool,
          size,
          cookies: cookies?.text ?? '',
        })
        refreshDownloads()
        refreshState()
        toast(t('downloads.added', { count: added.length }))
        onAdded?.(added)
      }}
    >
      <Field label={t('downloads.links')} hint={bad ? undefined : t('downloads.linksHint')} error={bad ? t('downloads.notALink', { link: bad }) : undefined}>
        <textarea
          value={links}
          onChange={(e) => setLinks(e.target.value)}
          rows={3}
          autoFocus
          spellCheck={false}
          aria-invalid={!!bad}
          placeholder="https://…"
          className="min-h-18 w-full resize-y rounded-lg border border-input bg-transparent px-2.5 py-2 font-mono text-[12.5px] leading-relaxed outline-none placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 aria-invalid:border-destructive aria-invalid:ring-3 aria-invalid:ring-destructive/20 dark:bg-input/30"
        />
      </Field>
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label={t('form.account')}>
          <Choice
            label={t('form.account')}
            className="w-full min-w-0"
            value={account}
            onChange={(v) => {
              setAccount(v)
              setEnv(NONE)
              setVolume('')
            }}
            options={st.accounts.map((a) => ({ value: a.id, label: a.label, hint: a.workspace && a.workspace !== a.label ? a.workspace : undefined }))}
          />
        </Field>
        <Field label={t('form.environment')}>
          <Choice
            label={t('form.environment')}
            className="w-full min-w-0"
            value={env}
            onChange={(v) => {
              setEnv(v)
              setVolume('')
            }}
            options={[
              { value: NONE, label: t('volumes.defaultEnv') },
              ...(envs.data ?? []).map((e) => ({ value: e.name, label: e.name, hint: e.default ? t('volumes.default') : undefined })),
            ]}
          />
        </Field>
      </div>
      <Field label={t('form.volume')} error={vols.error?.message}>
        <Choice
          label={t('form.volume')}
          className="w-full"
          value={volume}
          onChange={setVolume}
          disabled={vols.isLoading || usable.length === 0}
          placeholder={vols.isLoading ? t('form.loading') : usable.length ? t('downloads.pickVolume') : t('downloads.noVolumes')}
          options={usable.map((v) => ({ value: v.name, label: v.name }))}
        />
      </Field>
      {vol && (
        <Field label={t('downloads.folder')}>
          <FolderPicker account={account} env={env === NONE ? '' : env} volumeId={vol.id} volume={vol.name} value={folder} onChange={setFolder} />
        </Field>
      )}
      <Field label={t('downloads.tool')} hint={t(`downloads.toolHint.${tool}`)}>
        <Segmented<ToolChoice>
          label={t('downloads.tool')}
          value={tool}
          onChange={setTool}
          options={[
            { value: 'auto', label: t('downloads.auto') },
            { value: 'gopeed', label: 'gopeed' },
            { value: 'aria2', label: 'aria2' },
            { value: 'yt-dlp', label: 'yt-dlp' },
          ]}
        />
      </Field>
      <CollapsiblePrimitive.Root>
        <CollapsiblePrimitive.Trigger className="group/adv flex items-center gap-1 rounded text-[12.5px] font-medium text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50">
          <ChevronRight className="size-3.5 transition-transform duration-200 ease-out group-data-[panel-open]/adv:rotate-90" />
          {t('downloads.more')}
        </CollapsiblePrimitive.Trigger>
        <CollapsiblePrimitive.Panel className="h-(--collapsible-panel-height) overflow-hidden transition-[height] duration-200 ease-[var(--ease-out)] data-[ending-style]:h-0 data-[starting-style]:h-0 motion-reduce:transition-none">
          <div className="flex flex-col gap-4 pt-3">
            <Field label={t('downloads.size')} hint={t('downloads.sizeHint')}>
              <Segmented<DownloadSize>
                label={t('downloads.size')}
                value={size}
                onChange={setSize}
                options={(['small', 'medium', 'large'] as const).map((s) => ({ value: s, label: t(`downloads.sizes.${s}`) }))}
              />
            </Field>
            <Field label={t('downloads.cookies')} hint={cookies ? t('downloads.cookiesLoaded', { name: cookies.name }) : t('downloads.cookiesHint')}>
              <Input
                type="file"
                accept=".txt,text/plain"
                className="h-auto py-1.5 text-[12.5px]"
                onChange={async (e) => {
                  const f = e.target.files?.[0]
                  setCookies(f ? { name: f.name, text: await f.text() } : null)
                }}
              />
            </Field>
          </div>
        </CollapsiblePrimitive.Panel>
      </CollapsiblePrimitive.Root>
      <p className="rounded-lg bg-muted/70 px-3 py-2 text-[12px] leading-relaxed text-muted-foreground">
        {t('downloads.costNote', { workspace: acct?.workspace || acct?.label || '…', price: HOURLY[size].toFixed(4), size: t(`downloads.sizes.${size}`) })}
      </p>
    </FormDialog>
  )
}

/** The folders of a volume, one level at a time, and a new folder's name typed in. */
function FolderPicker({ account, env, volumeId, volume, value, onChange }: { account: string; env: string; volumeId: string; volume: string; value: string; onChange: (p: string) => void }) {
  const { t } = useTranslation()
  const [creating, setCreating] = useState(false)
  const [name, setName] = useState('')
  const list = useQuery({
    queryKey: ['folders', account, env, volumeId, value],
    queryFn: () => api<string[]>('GET', `/accounts/${account}/volumes/${volumeId}/folders?env=${encodeURIComponent(env)}&path=${encodeURIComponent(value)}`),
    staleTime: 10_000,
    retry: false,
  })
  const parts = value ? value.split('/') : []
  const go = (p: string) => {
    setCreating(false)
    onChange(p)
  }
  const add = () => {
    const n = name.trim().replace(/^\/+|\/+$/g, '')
    if (!n) return
    go(value ? `${value}/${n}` : n)
    setName('')
  }
  return (
    <div className="overflow-hidden rounded-lg border border-input">
      <div className="flex min-h-8 flex-wrap items-center border-b border-input bg-muted/50 px-2.5 py-1 font-mono text-[12px]">
        <button type="button" className="rounded px-0.5 hover:underline" onClick={() => go('')}>
          {volume}:/
        </button>
        {parts.map((p, i) => (
          <span key={i} className="flex items-center">
            {i > 0 && <span className="text-muted-foreground">/</span>}
            <button type="button" className="rounded px-0.5 hover:underline" onClick={() => go(parts.slice(0, i + 1).join('/'))}>
              {p}
            </button>
          </span>
        ))}
      </div>
      <div className="max-h-36 overflow-y-auto py-1 text-[13px]">
        {value && (
          <Item onClick={() => go(parts.slice(0, -1).join('/'))} icon={<CornerLeftUp />}>
            ..
          </Item>
        )}
        {list.isLoading && (
          <div className="flex items-center gap-2 px-3 py-1.5 text-muted-foreground">
            <Spinner className="size-3" />
            {t('form.loading')}
          </div>
        )}
        {list.error && <div className="px-3 py-1.5 text-[12px] text-muted-foreground">{t('downloads.folderNew')}</div>}
        {list.data?.map((f) => (
          <Item key={f} onClick={() => go(value ? `${value}/${f}` : f)} icon={<Folder />}>
            {f}
          </Item>
        ))}
        {list.data?.length === 0 && !value && <div className="px-3 py-1.5 text-[12px] text-muted-foreground">{t('downloads.noFolders')}</div>}
        {creating ? (
          <div className="flex items-center gap-2 px-2 py-1">
            <Input
              autoFocus
              value={name}
              onChange={(e) => setName(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') {
                  e.preventDefault()
                  add()
                }
                if (e.key === 'Escape') {
                  e.stopPropagation()
                  setCreating(false)
                }
              }}
              onBlur={add}
              placeholder={t('downloads.folderName')}
              className="h-7 text-[13px]"
            />
          </div>
        ) : (
          <Item onClick={() => setCreating(true)} icon={<FolderPlus />} muted>
            {t('downloads.newFolder')}
          </Item>
        )}
      </div>
    </div>
  )
}

function Item({ onClick, icon, muted, children }: { onClick: () => void; icon: React.ReactNode; muted?: boolean; children: React.ReactNode }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn('flex w-full items-center gap-2 px-3 py-1.5 text-left hover:bg-muted [&_svg]:size-3.5 [&_svg]:shrink-0', muted ? 'text-muted-foreground' : '[&_svg]:text-muted-foreground')}
    >
      {icon}
      <span className="truncate">{children}</span>
    </button>
  )
}
