// Small pieces the pages share: a state icon, chips, number tiles, an account's
// picture and name, and a hint that shows on hover.

import { cn } from 'cn'
import { ArrowDown, ArrowUp, Check, Lock, type LucideIcon, Minus, Pause, RefreshCw, TriangleAlert } from 'lucide-react'
import { AnimatePresence, motion } from 'motion/react'
import type { ReactElement, ReactNode } from 'react'
import { Spinner } from '@/components/Spinner'
import { useAvatar } from '@/api/queries'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { easeOut } from '@/lib/motion'
import type { Look, Tone } from '@/lib/mounts'

const TONE: Record<Tone, string> = {
  ok: 'bg-ok/14 text-ok-ink',
  up: 'bg-warn/18 text-warn-ink',
  busy: 'bg-brand/12 text-brand',
  bad: 'bg-destructive/12 text-destructive',
  off: 'bg-muted text-muted-foreground',
}

/** What a status icon shows. The shape carries the meaning; the color only backs it up. */
export type Glyph = 'check' | 'up' | 'down' | 'spin' | 'alert' | 'off' | 'pause' | 'moving' | 'lock'

const GLYPH: Record<Exclude<Glyph, 'spin'>, LucideIcon> = { check: Check, up: ArrowUp, down: ArrowDown, alert: TriangleAlert, off: Minus, pause: Pause, moving: RefreshCw, lock: Lock }

/** A mount's look, as a glyph. */
export const glyphOf: Record<Look, Glyph> = {
  synced: 'check',
  uploading: 'up',
  stopping: 'up',
  starting: 'spin',
  busy: 'moving',
  conflict: 'alert',
  error: 'alert',
  locked: 'lock',
  off: 'off',
}

/**
 * The glyph itself. When it changes (synced → uploading, say) the old one
 * fades and shrinks out as the new one comes in, so the change is noticed;
 * the same glyph with new numbers around it doesn't move.
 */
export function GlyphIcon({ glyph, className }: { glyph: Glyph; className?: string }) {
  const Icon = glyph === 'spin' ? null : GLYPH[glyph]
  return (
    <span className={cn('relative inline-flex size-3 shrink-0 items-center justify-center', className)}>
      <AnimatePresence initial={false}>
        <motion.span
          key={glyph}
          className="absolute inset-0 flex items-center justify-center"
          initial={{ opacity: 0, scale: 0.6, filter: 'blur(2px)' }}
          animate={{ opacity: 1, scale: 1, filter: 'blur(0px)' }}
          exit={{ opacity: 0, scale: 0.6, filter: 'blur(2px)' }}
          transition={{ duration: 0.2, ease: easeOut }}
        >
          {Icon ? <Icon className="size-full" strokeWidth={glyph === 'alert' || glyph === 'moving' || glyph === 'pause' || glyph === 'lock' ? 2.6 : 3} /> : <Spinner className="size-full border-2" />}
        </motion.span>
      </AnimatePresence>
    </span>
  )
}

/** Text that shows when the pointer rests on `children` (or it gets focus). */
export function Hint({ text, children, side }: { text: ReactNode; children: ReactElement; side?: 'top' | 'bottom' | 'left' | 'right' }) {
  if (!text) return children
  return (
    <Tooltip>
      <TooltipTrigger render={children} />
      <TooltipContent side={side} className="max-w-72 text-pretty">
        {text}
      </TooltipContent>
    </Tooltip>
  )
}

/** A round status icon; its meaning is in `label` (read out, and shown on hover). */
export function StatusIcon({ glyph, tone, label, className }: { glyph: Glyph; tone: Tone; label: string; className?: string }) {
  return (
    <Hint text={label}>
      <span role="img" aria-label={label} className={cn('flex size-[22px] shrink-0 items-center justify-center rounded-full transition-colors duration-200', TONE[tone], className)}>
        <GlyphIcon glyph={glyph} />
      </span>
    </Hint>
  )
}

/** A mount's state as a round icon. */
export function StateIcon({ look, tone, label, className }: { look: Look; tone: Tone; label: string; className?: string }) {
  return <StatusIcon glyph={glyphOf[look]} tone={tone} label={label} className={className} />
}

/** A small rounded label: a fact at a glance, with more on hover. */
export function Chip({ children, tone, glyph, hint, className, mono }: { children: ReactNode; tone?: Tone; glyph?: Glyph; hint?: ReactNode; className?: string; mono?: boolean }) {
  const chip = (
    <span
      className={cn(
        'inline-flex h-[26px] max-w-full min-w-0 items-center gap-1.5 rounded-full bg-muted px-2.5 text-[12.5px] whitespace-nowrap [&_svg]:size-3.5 [&_svg]:shrink-0',
        mono && 'font-mono text-[12px]',
        className,
      )}
    >
      {glyph && (
        <span aria-hidden className={cn('flex', tone ? INK[tone] : 'text-muted-foreground')}>
          <GlyphIcon glyph={glyph} className="size-[11px]" />
        </span>
      )}
      <span className="min-w-0 truncate">{children}</span>
    </span>
  )
  return hint ? <Hint text={hint}>{chip}</Hint> : chip
}

/** Text colors for a tone. */
export const INK: Record<Tone, string> = { ok: 'text-ok-ink', up: 'text-warn-ink', busy: 'text-brand', bad: 'text-destructive', off: 'text-muted-foreground' }

/** A number with its label above, for the row of figures at the top of a page. */
export function Tile({ label, value, unit, hint, tone, children, className }: { label: string; value: ReactNode; unit?: string; hint?: ReactNode; tone?: 'warn' | 'bad'; children?: ReactNode; className?: string }) {
  const tile = (
    <div className={cn('min-w-0 rounded-2xl border border-card-border bg-card px-4 py-3.5', className)}>
      <div className="truncate text-[12px] text-muted-foreground">{label}</div>
      <div className={cn('mt-0.5 truncate text-[22px] leading-tight font-semibold tracking-[-0.01em] tabular', tone === 'warn' && 'text-warn-ink', tone === 'bad' && 'text-destructive')}>
        {value}
        {unit && <span className="ml-1 text-[13px] font-medium text-muted-foreground">{unit}</span>}
      </div>
      {children}
    </div>
  )
  return hint ? <Hint text={hint}>{tile}</Hint> : tile
}

/** A thin bar, filled to `pct` percent. */
export function Bar({ pct, tone = 'brand', className, label }: { pct: number; tone?: 'brand' | 'warn' | 'muted'; className?: string; label?: string }) {
  return (
    <span
      role="progressbar"
      aria-label={label}
      aria-valuenow={Math.round(pct)}
      aria-valuemin={0}
      aria-valuemax={100}
      className={cn('block h-1.5 overflow-hidden rounded-full bg-muted', className)}
    >
      <span
        className={cn('block h-full rounded-full transition-[width] duration-500 ease-out', tone === 'brand' && 'bg-brand', tone === 'warn' && 'bg-warn', tone === 'muted' && 'bg-foreground/25')}
        style={{ width: `${Math.max(0, Math.min(100, pct))}%` }}
      />
    </span>
  )
}

const MARKS = ['oklch(0.6 0.13 258)', 'oklch(0.62 0.12 160)', 'oklch(0.65 0.13 60)', 'oklch(0.6 0.14 320)', 'oklch(0.6 0.12 200)', 'oklch(0.6 0.15 25)']

/**
 * An account's picture: its owner's Modal profile picture, or, when there is
 * none, a colored initial (the same color wherever it appears).
 */
export function AccountMark({ id, label, size = 20 }: { id: string; label: string; size?: number }) {
  const { data: src } = useAvatar(id)
  if (src) return <img src={src} alt="" aria-hidden width={size} height={size} draggable={false} className="shrink-0 rounded-full bg-muted object-cover ring-1 ring-foreground/10" style={{ width: size, height: size }} />
  let h = 0
  for (const c of id) h = (h * 31 + c.charCodeAt(0)) >>> 0
  return (
    <span
      aria-hidden
      className="inline-flex shrink-0 items-center justify-center rounded-full font-semibold text-white"
      style={{ width: size, height: size, fontSize: size * 0.5, background: MARKS[h % MARKS.length] }}
    >
      {(label.trim()[0] ?? '?').toUpperCase()}
    </span>
  )
}

/** An account as people recognize it: its picture and its name. */
export function AccountName({ id, label, size = 16, className }: { id: string; label: string; size?: number; className?: string }) {
  return (
    <span className={cn('inline-flex min-w-0 items-center gap-1.5', className)}>
      <AccountMark id={id} label={label} size={size} />
      <span className="truncate">{label}</span>
    </span>
  )
}
