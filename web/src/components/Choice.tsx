import { cn } from 'cn'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'

/** A drop-down list of options. `value` must be one of them; an empty value shows the placeholder. */
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
  options: { value: T; label: string; hint?: string; disabled?: boolean }[]
  onChange: (value: T) => void
  label: string
  placeholder?: string
  className?: string
  disabled?: boolean
}) {
  return (
    <Select
      value={value}
      items={options.map((o) => ({ value: o.value, label: o.label }))}
      onValueChange={(v) => v != null && onChange(v as T)}
      disabled={disabled}
    >
      <SelectTrigger aria-label={label} className={cn('min-w-36 bg-secondary', className)}>
        <SelectValue placeholder={placeholder} />
      </SelectTrigger>
      <SelectContent alignItemWithTrigger={false} align="end">
        {options.map((o) => (
          <SelectItem key={o.value} value={o.value} disabled={o.disabled}>
            {o.label}
            {o.hint && <span className="ml-1.5 text-muted-foreground">{o.hint}</span>}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}
