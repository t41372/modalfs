import { cn } from 'cn'
import { Fragment, type ReactNode } from 'react'
import { Select, SelectContent, SelectGroup, SelectItem, SelectLabel, SelectSeparator, SelectTrigger, SelectValue } from '@/components/ui/select'

type Option<T> = { value: T; label: string; hint?: string; disabled?: boolean; group?: string; icon?: ReactNode }

/**
 * A drop-down list of options. `value` must be one of them; an empty value shows the placeholder.
 * Options with a `group` are listed under its label, groups in the order they first appear;
 * an `icon` (an account's picture, say) shows before the label in the list and on the button.
 */
export function Choice<T extends string>({
  value,
  options,
  onChange,
  label,
  placeholder,
  className,
  disabled,
}: {
  value: T
  options: Option<T>[]
  onChange: (value: T) => void
  label: string
  placeholder?: string
  className?: string
  disabled?: boolean
}) {
  const groups: { label?: string; options: Option<T>[] }[] = []
  for (const o of options) {
    const g = groups.find((x) => x.label === o.group)
    if (g) g.options.push(o)
    else groups.push({ label: o.group, options: [o] })
  }
  const item = (o: Option<T>) => (
    <SelectItem key={o.value} value={o.value} disabled={o.disabled}>
      {o.icon}
      {o.label}
      {o.hint && <span className="ml-1.5 text-muted-foreground">{o.hint}</span>}
    </SelectItem>
  )
  return (
    <Select
      value={value}
      items={options.map((o) => ({ value: o.value, label: o.label }))}
      onValueChange={(v) => v != null && onChange(v as T)}
      disabled={disabled}
    >
      <SelectTrigger aria-label={label} className={cn('min-w-36 bg-secondary', className)}>
        {options.find((o) => o.value === value)?.icon}
        <SelectValue placeholder={placeholder} className="flex-1 text-left" />
      </SelectTrigger>
      <SelectContent alignItemWithTrigger={false} align="end">
        {groups.map((g, i) =>
          g.label === undefined ? (
            <Fragment key={i}>{g.options.map(item)}</Fragment>
          ) : (
            <Fragment key={i}>
              {i > 0 && <SelectSeparator />}
              <SelectGroup className="p-0">
                <SelectLabel>{g.label}</SelectLabel>
                {g.options.map(item)}
              </SelectGroup>
            </Fragment>
          ),
        )}
      </SelectContent>
    </Select>
  )
}
