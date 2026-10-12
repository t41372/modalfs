import { motion } from 'motion/react'
import { useId } from 'react'
import { ToggleGroup, ToggleGroupItem } from '@/components/ui/toggle-group'
import { easeOut } from '@/lib/motion'

/**
 * Pick one of a few options, like Light / Dark. One is always picked. The white
 * pill slides to the new choice, so it's clear where the choice moved from.
 */
export function Segmented<T extends string>({
  value,
  options,
  onChange,
  label,
  disabled,
}: {
  value: T
  options: { value: T; label: string }[]
  onChange: (value: T) => void
  label: string
  disabled?: boolean
}) {
  const pill = useId()
  return (
    <ToggleGroup
      aria-label={label}
      value={[value]}
      disabled={disabled}
      onValueChange={(next) => next.length && onChange(next[0] as T)}
      className="max-w-full flex-wrap gap-0.5 rounded-[9px] bg-muted p-[3px]"
    >
      {options.map((o) => (
        <ToggleGroupItem
          key={o.value}
          value={o.value}
          size="sm"
          // Like a macOS segmented control, a click doesn't take focus; Tab still does.
          onMouseDown={(e) => e.preventDefault()}
          className="relative h-7 rounded-[7px] px-3 text-[12.5px] font-normal text-muted-foreground hover:bg-transparent aria-pressed:bg-transparent aria-pressed:text-foreground"
        >
          {o.value === value && (
            <motion.span
              layoutId={pill}
              transition={{ duration: 0.2, ease: easeOut }}
              className="absolute inset-0 rounded-[7px] bg-card shadow-[0_1px_2px_rgb(0_0_0/0.1)] dark:bg-white/14"
            />
          )}
          <span className="relative">{o.label}</span>
        </ToggleGroupItem>
      ))}
    </ToggleGroup>
  )
}
