import { cn } from 'cn'
import type { ReactNode } from 'react'

/** A page: a centered column with a title, a line under it, and actions on the right. */
export function Page({ title, sub, actions, children }: { title: string; sub?: string; actions?: ReactNode; children: ReactNode }) {
  return (
    <div className="mx-auto w-full max-w-[760px] px-5 pt-8 pb-16 sm:px-10 sm:pt-12">
      <header className="mb-7 flex flex-wrap items-end justify-between gap-x-6 gap-y-3">
        <div className="min-w-0">
          <h1 className="text-[28px] leading-tight font-semibold tracking-[-0.02em] text-balance">{title}</h1>
          {sub && <p className="mt-1.5 max-w-[52ch] text-[14px] leading-relaxed text-pretty text-muted-foreground">{sub}</p>}
        </div>
        {actions && <div className="flex shrink-0 flex-wrap items-center gap-2">{actions}</div>}
      </header>
      <div className="flex flex-col gap-7">{children}</div>
    </div>
  )
}

/** A labelled group: a small quiet label, then a card. */
export function Section({ label, aside, children, className }: { label?: ReactNode; aside?: ReactNode; children: ReactNode; className?: string }) {
  return (
    <section className={className}>
      {(label || aside) && (
        <div className="mb-2 flex min-h-5 items-center justify-between gap-3 px-1">
          <h2 className="text-[12.5px] font-medium text-muted-foreground">{label}</h2>
          {aside}
        </div>
      )}
      {children}
    </section>
  )
}

/** The white card rows sit in. Rows are separated by hairlines. */
export function Card({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <div
      className={cn(
        'overflow-hidden rounded-2xl border border-card-border bg-card shadow-[0_1px_2px_rgb(20_24_60/0.04),0_4px_16px_-8px_rgb(20_24_60/0.08)] dark:shadow-none',
        '[&>*+*]:border-t [&>*+*]:border-card-border',
        className,
      )}
    >
      {children}
    </div>
  )
}

/** A row in a card: what it is on the left, the control on the right. Narrow windows put the control under the text. */
export function Row({
  title,
  desc,
  children,
  className,
  wrap = true,
}: {
  title: ReactNode
  desc?: ReactNode
  children?: ReactNode
  className?: string
  /** Let the control drop below the text when the row is narrow. */
  wrap?: boolean
}) {
  return (
    <div className={cn('flex items-center gap-x-6 gap-y-2.5 px-5 py-3.5', wrap && 'max-[520px]:flex-col max-[520px]:items-stretch', className)}>
      <div className="min-w-0 flex-1">
        <div className="text-[14px] font-medium">{title}</div>
        {desc && <div className="mt-0.5 text-[12.5px] leading-relaxed text-pretty text-muted-foreground">{desc}</div>}
      </div>
      {children && <div className="flex shrink-0 items-center gap-2 max-[520px]:justify-start">{children}</div>}
    </div>
  )
}

/** Nothing here yet: an icon, a line, what to do about it. */
export function Empty({ icon, title, desc, children }: { icon: ReactNode; title: string; desc?: string; children?: ReactNode }) {
  return (
    <div className="flex flex-col items-center px-6 py-12 text-center">
      <div className="mb-4 flex size-12 items-center justify-center rounded-2xl bg-muted text-muted-foreground [&_svg]:size-5.5">{icon}</div>
      <div className="text-[15px] font-medium">{title}</div>
      {desc && <p className="mt-1.5 max-w-[40ch] text-[13px] leading-relaxed text-pretty text-muted-foreground">{desc}</p>}
      {children && <div className="mt-5 flex flex-wrap justify-center gap-2">{children}</div>}
    </div>
  )
}

/** A small rounded label next to a title. */
export function Badge({ children, tone = 'neutral' }: { children: ReactNode; tone?: 'neutral' | 'ok' | 'warn' | 'bad' }) {
  return (
    <span
      className={cn(
        'inline-flex h-5 shrink-0 items-center gap-1 rounded-full px-2 text-[11px] font-medium whitespace-nowrap [&_svg]:size-3',
        tone === 'neutral' && 'bg-muted text-muted-foreground',
        tone === 'ok' && 'bg-ok/12 text-ok-ink',
        tone === 'warn' && 'bg-warn/15 text-warn-ink',
        tone === 'bad' && 'bg-destructive/10 text-destructive',
      )}
    >
      {children}
    </span>
  )
}

/** Text in the mono face that can be selected and copied: paths, IDs. */
export function Mono({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <span data-selectable className={cn('font-mono text-[12px] break-all', className)}>
      {children}
    </span>
  )
}
