import { cn } from 'cn'
import { Activity, CloudDownload, HardDrive, type LucideIcon, Settings } from 'lucide-react'
import { motion } from 'motion/react'
import { useTranslation } from 'react-i18next'
import { useFeed } from '@/api/events'
import { useAppState } from '@/api/queries'
import { GlyphIcon, Hint, INK } from '@/components/bits'
import { bytes } from '@/lib/format'
import { NEEDS_YOU } from '@/lib/downloads'
import { easeOut } from '@/lib/motion'
import { summary } from '@/lib/mounts'
import { trafficLights } from '@/lib/platform'
import { navigate, type Section, useRoute } from './nav'

const TOP: { id: Section; icon: LucideIcon }[] = [
  { id: 'volumes', icon: HardDrive },
  { id: 'downloads', icon: CloudDownload },
  { id: 'activity', icon: Activity },
]

/** The icon rail on the left; below 640 px it becomes a tab bar along the bottom. */
export function Rail() {
  return (
    <>
      <nav aria-label="modalfs" className="drag-region hidden flex-col items-center gap-2 pt-3 pb-3.5 sm:flex">
        {/* Room for the macOS traffic lights. */}
        {trafficLights && <div className="h-10 shrink-0" />}
        {TOP.map((p) => (
          <RailItem key={p.id} id={p.id} icon={p.icon} />
        ))}
        <div className="flex-1" />
        <Sync />
        <RailItem id="settings" icon={Settings} />
      </nav>

      <nav
        aria-label="modalfs"
        className="fixed inset-x-0 bottom-0 z-40 flex justify-around border-t border-win-border bg-background/95 px-2 pt-1.5 pb-[max(env(safe-area-inset-bottom),8px)] backdrop-blur-xl sm:hidden"
      >
        {[...TOP, { id: 'settings' as const, icon: Settings }].map((p) => (
          <RailItem key={p.id} id={p.id} icon={p.icon} compact />
        ))}
      </nav>
    </>
  )
}

/** Whether everything is on Modal, at a glance; the details on hover, the volumes on a click. */
function Sync() {
  const { t } = useTranslation()
  const st = useAppState().data
  if (!st) return null
  const s = summary(st.mounts)
  if (s.tone === 'off') return null
  const word = s.tone === 'bad' ? t('sync.attention') : s.tone === 'up' ? t('sync.uploading') : t('sync.synced')
  const detail = [
    t('sync.mounted', { count: s.on }),
    s.files > 0 ? t('sync.pending', { size: bytes(s.pending), count: s.files }) : t('sync.allOnModal'),
    s.problems > 0 ? t('sync.problems', { count: s.problems }) : '',
    s.files > 0 ? t('sync.dontSleep') : '',
  ]
    .filter(Boolean)
    .join(' · ')
  return (
    <Hint text={detail} side="right">
      <button
        onMouseDown={(e) => e.preventDefault()}
        onClick={() => navigate('volumes')}
        aria-label={`${word}. ${detail}`}
        className="pressable flex w-16 flex-col items-center gap-1 rounded-xl py-1.5 text-[10px] font-medium text-foreground/65 hover:text-foreground"
      >
        <span className={cn('flex', INK[s.tone])}>
          <GlyphIcon glyph={s.tone === 'bad' ? 'alert' : s.tone === 'up' ? 'up' : 'check'} className="size-3.5" />
        </span>
        {word}
      </button>
    </Hint>
  )
}

function RailItem({ id, icon: Icon, compact }: { id: Section; icon: LucideIcon; compact?: boolean }) {
  const { t } = useTranslation()
  const active = useRoute().section === id
  const unseen = useFeed().unseen
  const downloads = useAppState().data?.downloads ?? []
  const count = id === 'activity' && !active ? unseen : id === 'downloads' ? downloads.filter((d) => NEEDS_YOU.has(d.state)).length : 0
  return (
    <button
      // Like a macOS sidebar, a click doesn't take focus; Tab still does.
      onMouseDown={(e) => e.preventDefault()}
      onClick={() => navigate(id)}
      aria-current={active ? 'page' : undefined}
      // Muted grey vanishes when a dark desktop turns the glass grey, so the rail uses a faded foreground.
      className={cn(
        'pressable relative flex flex-col items-center justify-center gap-0.5 rounded-[14px]',
        compact ? 'h-12 min-w-14 flex-1' : 'min-h-12 w-16 py-1.5',
        active ? 'text-foreground' : 'text-foreground/65 hover:text-foreground',
      )}
    >
      {active && (
        <motion.span
          layoutId={compact ? 'tab-active' : 'rail-active'}
          transition={{ duration: 0.2, ease: easeOut }}
          className="absolute inset-0 rounded-[14px] bg-accent shadow-[0_1px_3px_rgb(0_0_0/0.1)]"
        />
      )}
      <span className="relative">
        <Icon className="size-5" strokeWidth={1.8} />
        {count > 0 && (
          <span className="absolute -top-1 -right-2 flex h-3.5 min-w-3.5 items-center justify-center rounded-full bg-destructive px-1 text-[9px] leading-none font-semibold text-white tabular">
            {count > 9 ? '9+' : count}
          </span>
        )}
      </span>
      <span className={cn('relative max-w-full px-0.5 text-center text-[10px] leading-[1.15] font-medium', compact ? 'truncate' : 'line-clamp-2')}>{t(`nav.${id}`)}</span>
    </button>
  )
}
