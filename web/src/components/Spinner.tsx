import { cn } from 'cn'

/** A small spinner that inherits the text color. Fast, so waiting feels shorter. */
export function Spinner({ className }: { className?: string }) {
  return (
    <span
      aria-hidden
      className={cn('inline-block size-3.5 shrink-0 animate-spin rounded-full border-[1.5px] border-current border-r-transparent [animation-duration:600ms]', className)}
    />
  )
}
