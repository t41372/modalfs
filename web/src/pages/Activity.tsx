import { cn } from 'cn'
import { Activity, ArrowUp, Check, CircleAlert, HardDrive, Info, RotateCcw, TriangleAlert } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useFeed } from '@/api/events'
import { useLoadedState } from '@/api/queries'
import type { DaemonEvent, EventKind } from '@/api/types'
import { Card, Empty, Page, Section } from '@/components/page'
import { Segmented } from '@/components/Segmented'
import { eventText, homeOf } from '@/lib/format'
import { intlTag } from '@/i18n'

const ICON: Record<EventKind, { icon: typeof Info; tone: string }> = {
  mounted: { icon: Check, tone: 'bg-ok/12 text-ok-ink' },
  unmounted: { icon: HardDrive, tone: 'bg-muted text-muted-foreground' },
  commit: { icon: ArrowUp, tone: 'bg-brand/10 text-brand' },
  conflict: { icon: TriangleAlert, tone: 'bg-warn/15 text-warn-ink' },
  error: { icon: CircleAlert, tone: 'bg-destructive/10 text-destructive' },
  recovered: { icon: RotateCcw, tone: 'bg-brand/10 text-brand' },
  info: { icon: Info, tone: 'bg-muted text-muted-foreground' },
}

const MAX = 300

export function ActivityPage() {
  const { t, i18n } = useTranslation()
  const { events, loaded, failed } = useFeed()
  const home = homeOf(useLoadedState().settings.mount_root)
  const [filter, setFilter] = useState<'all' | 'problems'>('all')

  const lang = i18n.language
  // Grouped by day, newest first. Only redone when the events, the filter or the language change, not on every poll.
  const { shown, groups } = useMemo(() => {
    const shown = events
      .filter((e) => filter === 'all' || e.kind === 'conflict' || e.kind === 'error')
      .slice(-MAX)
      .reverse()
    const days = dayLabeler(lang, t)
    const time = new Intl.DateTimeFormat(intlTag(lang), { hour: '2-digit', minute: '2-digit', hour12: false })
    const groups: { day: string; events: (DaemonEvent & { time: string })[] }[] = []
    for (const e of shown) {
      const day = days(e.at)
      if (groups.at(-1)?.day !== day) groups.push({ day, events: [] })
      groups.at(-1)!.events.push({ ...e, time: time.format(e.at * 1000) })
    }
    return { shown, groups }
  }, [events, filter, lang, t])

  return (
    <Page
      title={t('activity.title')}
      sub={t('activity.sub')}
      actions={
        events.length > 0 && (
          <Segmented
            label={t('activity.title')}
            value={filter}
            onChange={setFilter}
            options={[
              { value: 'all', label: t('activity.all') },
              { value: 'problems', label: t('activity.problems') },
            ]}
          />
        )
      }
    >
      {!loaded ? (
        failed && (
          <Card>
            <Empty icon={<Activity />} title={t('activity.loadFailed')} desc={t('activity.loadFailedP')} />
          </Card>
        )
      ) : shown.length === 0 ? (
        <Card>
          {filter === 'problems' && events.length ? (
            <Empty icon={<Check />} title={t('activity.noProblems')} />
          ) : (
            <Empty icon={<Activity />} title={t('activity.empty')} desc={t('activity.emptyP')} />
          )}
        </Card>
      ) : (
        groups.map((g) => (
          <Section key={g.day} label={g.day}>
            <Card>
              {g.events.map((e) => {
                const { icon: Icon, tone } = ICON[e.kind] ?? ICON.info
                return (
                  <div key={e.seq} className="flex items-start gap-3 px-4 py-3">
                    <span className={cn('mt-px flex size-7 shrink-0 items-center justify-center rounded-full', tone)}>
                      <Icon className="size-3.5" strokeWidth={2.2} />
                    </span>
                    <div data-selectable className="min-w-0 flex-1 pt-[5px] text-[13.5px] leading-snug break-words">
                      {e.volume && <span className="font-semibold">{e.volume}</span>}
                      {e.volume && <span className="mx-1.5 text-muted-foreground/60">·</span>}
                      <span className={e.kind === 'error' ? 'text-destructive' : undefined}>{eventText(e, t, home)}</span>
                    </div>
                    <time className="shrink-0 pt-[6px] text-[12px] text-muted-foreground tabular" dateTime={new Date(e.at * 1000).toISOString()}>
                      {e.time}
                    </time>
                  </div>
                )
              })}
            </Card>
          </Section>
        ))
      )}
    </Page>
  )
}

/** Names a timestamp's day: today, yesterday, or its date. */
function dayLabeler(lang: string, t: (k: 'activity.today' | 'activity.yesterday') => string) {
  const today = new Date().toDateString()
  const yesterday = new Date(Date.now() - 86400_000).toDateString()
  const date = new Intl.DateTimeFormat(intlTag(lang), { weekday: 'short', month: 'short', day: 'numeric' })
  return (ts: number) => {
    const d = new Date(ts * 1000)
    const key = d.toDateString()
    return key === today ? t('activity.today') : key === yesterday ? t('activity.yesterday') : date.format(d)
  }
}
