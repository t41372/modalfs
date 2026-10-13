import { Collapsible as CollapsiblePrimitive } from '@base-ui/react/collapsible'
import { useQuery } from '@tanstack/react-query'
import { cn } from 'cn'
import { ChevronDown, ChevronRight, CornerLeftUp, Folder, FolderPlus, Globe, Info } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { api } from '@/api/client'
import { refreshDownloads, refreshState, useBilling, useLoadedState } from '@/api/queries'
import type { Download, DownloadSize, DownloadTool } from '@/api/types'
import { AccountMark, Hint } from '@/components/bits'
import { Choice } from '@/components/Choice'
import { Field, FormDialog } from '@/components/FormDialog'
import { Segmented } from '@/components/Segmented'
import { Spinner } from '@/components/Spinner'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { AddVolumeDialog } from '@/dialogs/AddVolume'
import { hourlyFor, isTorrent, warpByDefault } from '@/lib/downloads'

type ToolChoice = 'auto' | DownloadTool
/** How a download reaches the site: auto is Cloudflare WARP for YouTube, direct for the rest. */
type Network = 'auto' | 'warp' | 'direct'
const OTHER = '__other__'

/** Paste links, pick one of modalfs's volumes and a folder in it, and download them there in Modal. */
export function AddDownloadDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const { t } = useTranslation()
  const st = useLoadedState()
  const [links, setLinks] = useState('')
  const [mountId, setMountId] = useState('')
  const [folder, setFolder] = useState('')
  const [tool, setTool] = useState<ToolChoice>('auto')
  const [size, setSize] = useState<DownloadSize>('small')
  const [cookies, setCookies] = useState<{ name: string; text: string } | null>(null)
  const [net, setNet] = useState<Network>('auto')
  const [addingVolume, setAddingVolume] = useState(false)
  const [picking, setPicking] = useState(false)

  // Only volumes modalfs manages, and only once they've been mounted (so their ID is pinned).
  const usable = st.mounts.filter((m) => m.volume_id)
  useEffect(() => {
    if (!open) return
    setLinks('')
    setMountId((id) => (usable.some((m) => m.id === id) ? id : usable[0]?.id || ''))
    setFolder('')
    setTool('auto')
    setSize(st.settings.download_size || 'small')
    setCookies(null)
    setNet('auto')
  }, [open]) // eslint-disable-line react-hooks/exhaustive-deps
  const mount = usable.find((m) => m.id === mountId)
  useEffect(() => setFolder(''), [mountId])
  const billing = useBilling(open ? mount?.account_id : undefined, mount?.environment ?? '')

  const urls = links
    .split(/\s+/)
    .map((l) => l.trim())
    .filter(Boolean)
  const bad = urls.find((u) => !/^(https?|ftp):\/\/\S+$/i.test(u) && !/^magnet:\?\S+$/i.test(u))
  const torrents = urls.filter(isTorrent).length
  const viaWarp = net === 'warp' ? urls.length - torrents : net === 'auto' ? urls.filter(warpByDefault).length : 0
  const warpTorrent = net === 'warp' && torrents > 0
  const acct = st.accounts.find((a) => a.id === mount?.account_id)
  const many = new Set(usable.map((m) => m.account_id)).size > 1
  const price = hourlyFor(size, billing.data)

  return (
    <>
      <FormDialog
        open={open}
        onOpenChange={onOpenChange}
        title={t('dl.addTitle')}
        submit={urls.length > 1 ? t('dl.startN', { count: urls.length }) : t('dl.start')}
        canSubmit={urls.length > 0 && !bad && !warpTorrent && !!mount}
        aside={
          <Hint text={t('dl.costHint2', { workspace: acct?.workspace || acct?.label || '…' })}>
            <span className="inline-flex items-center gap-1 text-[12px] text-muted-foreground tabular">
              {t('dl.costLine', { price: price.toFixed(4), count: Math.max(1, urls.length) })}
              <Info className="size-3.5" />
            </span>
          </Hint>
        }
        onSubmit={async () => {
          const added = await api<Download[]>('POST', '/downloads', {
            account: mount!.account_id,
            environment: mount!.environment ?? '',
            volume: mount!.volume_name,
            folder,
            urls,
            tool: tool === 'auto' ? '' : tool,
            size,
            cookies: cookies?.text ?? '',
            ...(net === 'auto' ? {} : { warp: net === 'warp' }),
          })
          refreshDownloads()
          refreshState()
          toast(t('dl.added', { count: added.length }))
        }}
      >
        <Field
          label={t('dl.links')}
          htmlFor="dl-links"
          hint={
            bad || warpTorrent ? undefined : viaWarp || torrents ? (
              <span className="flex flex-col gap-0.5">
                {viaWarp > 0 && (
                  <span className="flex items-center gap-1.5">
                    <Globe className="size-3.5 shrink-0" />
                    {net === 'auto' ? t('dl.warpAuto', { count: viaWarp }) : t('dl.warpAll', { count: viaWarp })}
                  </span>
                )}
                {torrents > 0 && t('dl.torrentDirect', { count: torrents })}
              </span>
            ) : (
              t('dl.linksHint')
            )
          }
          error={bad ? t('dl.notALink', { link: bad }) : warpTorrent ? t('dl.noWarpTorrent') : undefined}
        >
          <textarea
            id="dl-links"
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
        <Field
          label={t('dl.saveTo')}
          hint={
            mount && (
              <>
                {t('dl.willSave')}{' '}
                <span className="font-mono text-[12px] font-medium text-foreground">
                  {mount.volume_name}:/{folder}
                </span>
                {' · '}
                {t('dl.neverOverwrites')}
              </>
            )
          }
        >
          {usable.length === 0 ? (
            <p className="rounded-lg bg-muted px-3 py-2.5 text-[12.5px] text-muted-foreground">{t('dl.noVolumes')}</p>
          ) : (
            <div className="grid grid-cols-2 gap-2 max-sm:grid-cols-1">
              <Choice
                label={t('dl.saveTo')}
                className="h-9 w-full min-w-0"
                value={mountId}
                onChange={(v) => (v === OTHER ? setAddingVolume(true) : setMountId(v))}
                options={[
                  ...usable.map((m) => ({ value: m.id, label: m.volume_name, hint: many ? m.account_label : undefined, icon: many ? <AccountMark id={m.account_id} label={m.account_label} size={16} /> : undefined })),
                  { value: OTHER, label: t('dl.otherVolume') },
                ]}
              />
              {mount && (
                <Popover open={picking} onOpenChange={setPicking}>
                  <PopoverTrigger render={<Button type="button" variant="outline" className="h-9 min-w-0 justify-between bg-secondary font-normal" aria-label={t('dl.folder')} />}>
                    <span className="flex min-w-0 items-center gap-2">
                      <Folder className="text-muted-foreground" />
                      <span className="truncate font-mono text-[12.5px]">/{folder}</span>
                    </span>
                    <ChevronDown className="text-muted-foreground" />
                  </PopoverTrigger>
                  <PopoverContent className="w-(--anchor-width) min-w-64">
                    <FolderPicker account={mount.account_id} env={mount.environment ?? ''} volumeId={mount.volume_id!} volume={mount.volume_name} value={folder} onChange={setFolder} onDone={() => setPicking(false)} />
                  </PopoverContent>
                </Popover>
              )}
            </div>
          )}
        </Field>
        <CollapsiblePrimitive.Root>
          <CollapsiblePrimitive.Trigger className="group/adv flex items-center gap-1 rounded text-[12.5px] font-medium text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50">
            <ChevronRight className="size-3.5 transition-transform duration-200 ease-out group-data-[panel-open]/adv:rotate-90" />
            {t('dl.advanced')}
          </CollapsiblePrimitive.Trigger>
          <CollapsiblePrimitive.Panel className="h-(--collapsible-panel-height) overflow-hidden transition-[height] duration-200 ease-[var(--ease-out)] data-[ending-style]:h-0 data-[starting-style]:h-0 motion-reduce:transition-none">
            <div className="flex flex-col gap-4 pt-3">
              <Field label={t('dl.tool')} hint={t(`downloads.toolHint.${tool}`)}>
                <Segmented<ToolChoice>
                  label={t('dl.tool')}
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
              <Field label={t('dl.network')} hint={t(`dl.netHint.${net}`)}>
                <Segmented<Network>
                  label={t('dl.network')}
                  value={net}
                  onChange={setNet}
                  options={[
                    { value: 'auto', label: t('downloads.auto') },
                    { value: 'warp', label: 'Cloudflare WARP' },
                    { value: 'direct', label: t('dl.direct') },
                  ]}
                />
              </Field>
              <Field label={t('dl.size')} hint={t('downloads.sizeHint')}>
                <Segmented<DownloadSize>
                  label={t('dl.size')}
                  value={size}
                  onChange={setSize}
                  options={(['small', 'medium', 'large'] as const).map((z) => ({ value: z, label: t(`downloads.sizes.${z}`) }))}
                />
              </Field>
              <Field label={t('downloads.cookies')} htmlFor="dl-cookies" hint={cookies ? t('downloads.cookiesLoaded', { name: cookies.name }) : t('downloads.cookiesHint')}>
                <Input
                  id="dl-cookies"
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
      </FormDialog>
      <AddVolumeDialog open={addingVolume} onOpenChange={setAddingVolume} />
    </>
  )
}

/** The folders of a volume, one level at a time, and a new folder's name typed in. */
function FolderPicker({ account, env, volumeId, volume, value, onChange, onDone }: { account: string; env: string; volumeId: string; volume: string; value: string; onChange: (p: string) => void; onDone: () => void }) {
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
    <div>
      <div className="flex min-h-9 flex-wrap items-center border-b border-input bg-muted/50 px-2.5 py-1 font-mono text-[12px]">
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
      <div className="max-h-56 overflow-y-auto py-1 text-[13px]">
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
      <div className="flex justify-end border-t border-input px-2.5 py-2">
        <Button type="button" size="sm" onClick={onDone}>
          {t('dl.saveHere')}
        </Button>
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
