import { cn } from 'cn'
import { Activity, Boxes, HardDrive, KeyRound, type LucideIcon, Moon, Settings, Sun } from 'lucide-react'
import { AnimatePresence, motion } from 'motion/react'
import { useTranslation } from 'react-i18next'
import { useFeed } from '@/api/events'
import { easeOut } from '@/lib/motion'
import { trafficLights } from '@/lib/platform'
import { navigate, type PageId, usePage } from './nav'

const TOP: { id: PageId; icon: LucideIcon }[] = [
  { id: 'mounts', icon: HardDrive },
  { id: 'volumes', icon: Boxes },
  { id: 'accounts', icon: KeyRound },
  { id: 'activity', icon: Activity },
]

/** The icon rail on the left; below 640 px it becomes a tab bar along the bottom. */
export function Rail({ dark, onToggleDark }: { dark: boolean; onToggleDark: () => void }) {
  const { t } = useTranslation()
  return (
    <>
      <nav aria-label="modalfs" className="drag-region hidden flex-col items-center gap-2 pt-3 pb-3.5 sm:flex">
        {/* Room for the macOS traffic lights. */}
        {trafficLights && <div className="h-10 shrink-0" />}
        {TOP.map((p) => (
          <RailItem key={p.id} id={p.id} icon={p.icon} />
        ))}
        <div className="flex-1" />
        <button
          onMouseDown={(e) => e.preventDefault()}
          onClick={onToggleDark}
          title={dark ? t('nav.toLight') : t('nav.toDark')}
          aria-label={dark ? t('nav.toLight') : t('nav.toDark')}
          className="pressable flex size-10 items-center justify-center rounded-xl text-foreground/65 hover:text-foreground"
        >
          {/* The icons turn and swap, a small flourish for something toggled rarely. */}
          <AnimatePresence mode="popLayout" initial={false}>
            <motion.span
              key={dark ? 'sun' : 'moon'}
              initial={{ opacity: 0, transform: 'rotate(-90deg) scale(0.8)' }}
              animate={{ opacity: 1, transform: 'rotate(0deg) scale(1)' }}
              exit={{ opacity: 0, transform: 'rotate(90deg) scale(0.8)' }}
              transition={{ duration: 0.2, ease: easeOut }}
            >
              {dark ? <Sun className="size-4" /> : <Moon className="size-4" />}
            </motion.span>
          </AnimatePresence>
        </button>
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

function RailItem({ id, icon: Icon, compact }: { id: PageId; icon: LucideIcon; compact?: boolean }) {
  const { t } = useTranslation()
  const active = usePage() === id
  const unseen = useFeed().unseen
  const badge = id === 'activity' && unseen > 0 && !active
  return (
    <button
      // Like a macOS sidebar, a click doesn't take focus; Tab still does.
      onMouseDown={(e) => e.preventDefault()}
      onClick={() => navigate(id)}
      aria-current={active ? 'page' : undefined}
      // Muted grey vanishes when a dark desktop turns the glass grey, so the rail uses a faded foreground.
      className={cn(
        'pressable relative flex flex-col items-center justify-center gap-0.5 rounded-[14px]',
        compact ? 'h-12 min-w-14 flex-1' : 'h-12 w-16',
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
        {badge && (
          <span className="absolute -top-1 -right-2 flex h-3.5 min-w-3.5 items-center justify-center rounded-full bg-destructive px-1 text-[9px] leading-none font-semibold text-white tabular">
            {unseen > 9 ? '9+' : unseen}
          </span>
        )}
      </span>
      <span className="relative max-w-full truncate px-0.5 text-[10px] font-medium">{t(`nav.${id}`)}</span>
    </button>
  )
}
