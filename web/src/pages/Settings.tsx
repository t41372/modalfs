import { ExternalLink, FolderOpen, Power } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { api } from '@/api/client'
import { patchSettings, refreshState, useAction, useLoadedState } from '@/api/queries'
import type { Appearance, Language, Settings } from '@/api/types'
import { quit as startQuit } from '@/app/quit'
import { Choice } from '@/components/Choice'
import { confirm } from '@/components/confirm'
import { Field, FormDialog } from '@/components/FormDialog'
import { Card, Mono, Page, Row, Section } from '@/components/page'
import { Segmented } from '@/components/Segmented'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { HOURLY } from '@/lib/downloads'
import { bytes, homeOf, tilde, untilde } from '@/lib/format'

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

  const set = useAction((body: Settings | { transport: string }) => patchSettings(body), {
    optimistic: (state, body) => Object.assign(state.settings, body),
  })
  const service = useAction((install: boolean) => api('POST', `/service/${install ? 'install' : 'uninstall'}`), {
    optimistic: (state, install) => {
      state.service.installed = install
    },
  })
  const sizes = [...new Set([...CACHE_SIZES, s.cache_size_gb ?? 20])].sort((a, b) => a - b)
  const pct = st.cache.max ? Math.min(100, (st.cache.bytes / st.cache.max) * 100) : 0

  const clearCache = () =>
    confirm({
      title: t('settings.clearQ'),
      desc: t('settings.clearP'),
      action: t('settings.clearCache'),
      run: () =>
        api('POST', '/cache/clear')
          .then(() => toast.success(t('toast.cacheCleared')))
          .catch((e) => toast.error(e.message))
          .finally(refreshState),
    })
  const quit = () =>
    confirm({
      title: t('settings.quitQ'),
      desc: t('settings.quitP'),
      action: t('settings.quit'),
      danger: true,
      // The window then shows each volume's upload until the daemon is gone.
      run: () => startQuit(),
    })

  return (
    <Page title={t('settings.title')}>
      <Section label={t('settings.general')}>
        <Card>
          <Row title={t('settings.language')}>
            <Segmented<Language>
              label={t('settings.language')}
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
          {st.service.supported && (
            <Row
              title={t('settings.login')}
              desc={
                <>
                  {t('settings.loginD')}
                  {st.service.installed && st.service.binary_missing && (
                    <span data-selectable className="mt-1 block text-warn-ink">
                      {t('settings.loginMissing', { path: tilde(st.service.binary, home) })}
                    </span>
                  )}
                </>
              }
              wrap={false}
            >
              <Switch checked={st.service.installed} disabled={service.isPending} onCheckedChange={(v) => service.mutate(v)} aria-label={t('settings.login')} />
            </Row>
          )}
        </Card>
      </Section>

      <Section label={t('settings.storage')}>
        <Card>
          <Row
            title={t('settings.cache')}
            desc={
              <>
                {t('settings.cacheD', { used: bytes(st.cache.bytes), max: bytes(st.cache.max) })}
                {(s.cache_size_gb ?? 20) < CACHE_RECOMMENDED_GB && (
                  <span className="mt-1 block text-warn-ink">{t('settings.cacheSmall', { n: CACHE_RECOMMENDED_GB })}</span>
                )}
                <span className="mt-2 block h-1.5 max-w-80 overflow-hidden rounded-full bg-muted" role="meter" aria-valuenow={Math.round(pct)} aria-valuemin={0} aria-valuemax={100}>
                  <span className="block h-full rounded-full bg-brand transition-[width] duration-500 ease-out" style={{ width: `${pct}%` }} />
                </span>
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
          <Row title={t('settings.readAhead')} desc={t('settings.readAheadD')}>
            <Choice
              label={t('settings.readAhead')}
              className="min-w-28"
              value={String(s.read_ahead ?? 8)}
              onChange={(v) => set.mutate({ read_ahead: Number(v) })}
              options={READ_AHEAD.map((n) => ({ value: String(n), label: `${n} × 8 MiB` }))}
            />
          </Row>
          <Row
            title={t('settings.mountRoot')}
            desc={
              <>
                <Mono className="text-foreground/80">{tilde(s.mount_root, home)}</Mono>
                <span className="mt-0.5 block">{t('settings.mountRootD')}</span>
              </>
            }
          >
            <Button variant="outline" onClick={() => setEditingRoot(true)}>
              {t('settings.change')}
            </Button>
          </Row>
        </Card>
      </Section>

      <Section label={t('settings.downloads')}>
        <Card>
          <Row title={t('settings.remoteDownloads')} desc={t('settings.remoteDownloadsD')} wrap={false}>
            <Switch checked={!!s.remote_downloads} onCheckedChange={(v) => set.mutate({ remote_downloads: v })} aria-label={t('settings.remoteDownloads')} />
          </Row>
          <Row title={t('settings.downloadSize')} desc={t('settings.downloadSizeD')}>
            <Choice
              label={t('settings.downloadSize')}
              className="min-w-40"
              value={s.download_size || 'small'}
              onChange={(v) => set.mutate({ download_size: v })}
              options={(['small', 'medium', 'large'] as const).map((z) => ({ value: z, label: t(`downloads.sizes.${z}`), hint: t('downloads.perHour', { price: HOURLY[z].toFixed(4) }) }))}
            />
          </Row>
          <Row title={t('settings.stall')} desc={t('settings.stallD')}>
            <Choice
              label={t('settings.stall')}
              className="min-w-28"
              value={String(s.download_stall_minutes || 10)}
              onChange={(v) => set.mutate({ download_stall_minutes: Number(v) })}
              options={STALL.map((m) => ({ value: String(m), label: t('time.min', { n: m }) }))}
            />
          </Row>
        </Card>
      </Section>

      <Section label={t('settings.connection')}>
        <Card>
          <Row title={t('settings.transport')} desc={t('settings.transportD')}>
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
        </Card>
      </Section>

      <Section label={t('settings.about')}>
        <Card>
          <Row title={t('settings.version')} wrap={false}>
            <span data-selectable className="rounded-md bg-muted px-2 py-1 font-mono text-[11.5px] text-muted-foreground">
              modalfs {st.version} · {st.platform}
            </span>
          </Row>
          <Row title={t('settings.configDir')} desc={<Mono>{tilde(st.config_dir, home)}</Mono>}>
            <Button variant="ghost" onClick={() => api('POST', '/open', { target: 'config' }).catch(() => {})}>
              <FolderOpen />
              {t('settings.open')}
            </Button>
          </Row>
          <Row title={t('settings.openLogs')} wrap={false}>
            <Button variant="ghost" onClick={() => api('POST', '/open', { target: 'logs' }).catch(() => {})}>
              <ExternalLink />
              {t('settings.open')}
            </Button>
          </Row>
          <Row title={t('settings.quit')} desc={t('settings.quitD')}>
            <Button variant="destructive" onClick={quit}>
              <Power />
              {t('settings.quit')}
            </Button>
          </Row>
        </Card>
      </Section>

      <CacheSizeDialog open={customCache} onOpenChange={setCustomCache} current={s.cache_size_gb ?? 20} onSave={(gb) => patchSettings({ cache_size_gb: gb }).then(refreshState)} />
      <MountRootDialog open={editingRoot} onOpenChange={setEditingRoot} current={tilde(s.mount_root, home)} home={home} />
    </Page>
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
      <Field label={t('form.path')} error={bad ? t('form.pathBad') : undefined}>
        <Input className="font-mono text-[13px]" value={value} onChange={(e) => setValue(e.target.value)} spellCheck={false} autoFocus aria-invalid={bad} />
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
        error={value !== '' && !valid ? t('settings.cacheSizeBad', { min: CACHE_MIN_GB }) : undefined}
        hint={valid && gb < CACHE_RECOMMENDED_GB ? <span className="text-warn-ink">{t('settings.cacheSmall', { n: CACHE_RECOMMENDED_GB })}</span> : undefined}
      >
        <Input type="number" inputMode="numeric" min={CACHE_MIN_GB} step={1} value={value} onChange={(e) => setValue(e.target.value)} autoFocus className="tabular" />
      </Field>
    </FormDialog>
  )
}
