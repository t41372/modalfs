import { AnimatePresence, MotionConfig, motion } from 'motion/react'
import { useCallback, useEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { markSeen, useEventSync } from '@/api/events'
import { ApiError } from '@/api/client'
import { useAppState } from '@/api/queries'
import type { DaemonEvent } from '@/api/types'
import { Confirmer } from '@/components/confirm'
import { Spinner } from '@/components/Spinner'
import { Toaster } from '@/components/ui/sonner'
import { TooltipProvider } from '@/components/ui/tooltip'
import { setPlatform } from '@/i18n'
import { eventText, homeOf } from '@/lib/format'
import { easeOut } from '@/lib/motion'
import { trafficLights } from '@/lib/platform'
import { ActivityPage } from '@/pages/Activity'
import { DownloadRoute } from '@/pages/Download'
import { Downloads } from '@/pages/Downloads'
import { Home } from '@/pages/Home'
import { SettingsPage } from '@/pages/Settings'
import { VolumePage } from '@/pages/VolumePage'
import { Welcome } from '@/pages/Welcome'
import { ErrorBoundary } from './ErrorBoundary'
import { navigate, type Route, SECTIONS, type Section, useRoute } from './nav'
import { askClose } from './closeWindow'
import { quitDone, useQuitPhase } from './quit'
import { QuitDialog } from './QuitDialog'
import { checklistOpen, flowActive, startQuit, useQuitFlow } from './quitFlow'
import { Quitting } from './Quitting'
import { Rail } from './Rail'
import { useLook } from './theme'

/** The page for where the window is. Without an account, the volumes are a welcome instead. */
function Current({ route, accounts }: { route: Route; accounts: number }) {
  switch (route.section) {
    case 'volumes':
      if (!accounts) return <Welcome />
      return route.id ? <VolumePage id={route.id} /> : <Home />
    case 'downloads':
      return route.id ? <DownloadRoute id={route.id} /> : <Downloads />
    case 'activity':
      return <ActivityPage />
    case 'settings':
      return <SettingsPage />
  }
}

export function App() {
  const { t } = useTranslation()
  const { data: state, dataUpdatedAt, isError, error } = useAppState()
  const route = useRoute()
  const page = route.section
  const dark = useLook(state?.settings.appearance, state?.settings.language)
  useEffect(() => setPlatform(state?.platform), [state?.platform])
  const home = homeOf(state?.settings.mount_root)
  const quitPhase = useQuitPhase()
  const checklist = flowActive(useQuitFlow())
  // The quit checklist shows its own progress; the page behind it stays as it was.
  const quitting = quitPhase !== 'idle' || (!!state?.quitting && !checklist)

  // The desktop app asks for the quit checklist (⌘Q, the menu bar menu, the Dock), before its window
  // closes where there's no menu bar icon (Linux), and for pages (a click in the menu bar menu).
  useEffect(() => {
    const offs = [
      window.runtime?.EventsOn?.('quit:request', () => void startQuit()),
      window.runtime?.EventsOn?.('close:request', () => void askClose()),
      window.runtime?.EventsOn?.('nav', (section, id) => {
        if ((SECTIONS as readonly unknown[]).includes(section)) navigate(section as Section, typeof id === 'string' && id ? id : undefined)
      }),
    ]
    return () => offs.forEach((off) => off?.())
  }, [])

  // Quitting from this window: once the daemon stops answering, it's done.
  useEffect(() => {
    if (isError && quitPhase === 'quitting') quitDone()
  }, [isError, quitPhase])

  // Conflicts and errors become notifications; a recovery is worth a quiet one too. Many at once
  // (back from a hidden window, the network down for a while) become one that points to Activity.
  const onNew = useCallback(
    (events: DaemonEvent[]) => {
      if (checklistOpen()) return // the quit checklist shows what's happening
      const problems = events.filter((e) => e.kind === 'conflict' || e.kind === 'error')
      if (problems.length > 3) {
        toast.error(t('toast.problems', { count: problems.length }), {
          duration: 10000,
          action: { label: t('nav.activity'), onClick: () => navigate('activity') },
        })
      }
      for (const e of events) {
        const text = (e.volume ? `${e.volume} · ` : '') + eventText(e, t, home)
        if ((e.kind === 'conflict' || e.kind === 'error') && problems.length <= 3) toast.error(text, { duration: 8000 })
        else if (e.kind === 'recovered') toast(text)
      }
    },
    [t, home],
  )
  useEventSync(state, dataUpdatedAt, onNew, page === 'activity')
  useEffect(() => {
    if (page === 'activity') markSeen()
  }, [page])

  // The daemon came back after being down: say so once.
  const wasDown = useRef(false)
  useEffect(() => {
    if (quitPhase !== 'idle') return
    if (isError && state) wasDown.current = true
    else if (!isError && wasDown.current) {
      wasDown.current = false
      toast.success(t('toast.restarted'))
    }
  }, [isError, state, t, quitPhase])

  return (
    <MotionConfig reducedMotion="user">
      <TooltipProvider delay={500}>
        {/* The title bar: the window drags by its top edge. Wails only checks the element under the
            mouse, so the strip must be on top of <main> (z-10), not merely before it in the DOM. Page
            content starts below it; dialogs, menus and the offline banner sit above it. */}
        {trafficLights && <div className="drag-region fixed inset-x-0 top-0 z-10 h-10" />}
        <div className="grid h-full grid-cols-1 bg-win sm:grid-cols-[88px_minmax(0,1fr)]">
          <Rail />
          <div className="flex min-h-0 min-w-0 sm:py-2.5 sm:pr-2.5">
            <main className="relative flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden border-win-border bg-background sm:rounded-xl sm:border">
              <AnimatePresence initial={false}>
                {isError && state && !quitting && !checklist && (
                  <motion.div
                    initial={{ opacity: 0, transform: 'translateY(-100%)' }}
                    animate={{ opacity: 1, transform: 'translateY(0)' }}
                    exit={{ opacity: 0, transform: 'translateY(-100%)' }}
                    transition={{ duration: 0.2, ease: easeOut }}
                    role="status"
                    className="absolute inset-x-0 top-0 z-20 flex items-center justify-center gap-2 bg-warn/90 px-4 py-1.5 text-[12.5px] font-medium text-black/80 backdrop-blur"
                  >
                    <Spinner className="size-3" />
                    {t('shell.down')}
                  </motion.div>
                )}
              </AnimatePresence>
              <div className="min-h-0 flex-1 overflow-y-auto overscroll-contain pb-20 sm:pb-0" key={`${page}/${route.id ?? ''}`}>
                {quitting ? (
                  <Quitting state={state} phase={quitPhase} />
                ) : state ? (
                  <ErrorBoundary resetKey={`${page}/${route.id ?? ''}`}>
                    <Current route={route} accounts={state.accounts.length} />
                  </ErrorBoundary>
                ) : isError ? (
                  <NotRunning noToken={error instanceof ApiError && error.status === 401} />
                ) : (
                  <Loading />
                )}
              </div>
            </main>
          </div>
        </div>
        <Confirmer />
        <QuitDialog />
        <Toaster theme={dark ? 'dark' : 'light'} position="bottom-center" offset={{ bottom: 24 }} mobileOffset={{ bottom: 84 }} />
      </TooltipProvider>
    </MotionConfig>
  )
}

function Loading() {
  const { t } = useTranslation()
  return (
    <div className="flex h-full items-center justify-center gap-2.5 text-[13px] text-muted-foreground">
      <Spinner />
      {t('shell.loading')}
    </div>
  )
}

function NotRunning({ noToken }: { noToken: boolean }) {
  const { t } = useTranslation()
  return (
    <div className="mx-auto flex h-full max-w-md flex-col items-center justify-center px-6 text-center">
      <div className="text-[17px] font-semibold">{noToken ? t('shell.noTokenTitle') : t('shell.downFirst')}</div>
      <p data-selectable className="mt-2 text-[13px] leading-relaxed text-pretty text-muted-foreground">
        {noToken ? t('shell.noToken') : t('shell.downFirstP')}
      </p>
      {!noToken && (
        <div className="mt-5 flex items-center gap-2 text-[12px] text-muted-foreground">
          <Spinner className="size-3" />
          {t('shell.down')}
        </div>
      )}
    </div>
  )
}
