import { type FormEvent, type ReactNode, useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Spinner } from './Spinner'

/**
 * A dialog around a form. Submitting runs `onSubmit`; while it runs the button
 * spins and the dialog can't be dismissed, and an error shows above the
 * buttons instead of closing it, so nothing typed is lost.
 */
export function FormDialog({
  open,
  onOpenChange,
  title,
  desc,
  submit,
  danger,
  dismiss,
  canSubmit = true,
  onSubmit,
  aside,
  children,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: string
  desc?: ReactNode
  submit: string
  danger?: boolean
  /** The dismiss button; never a word that's also in `submit`. */
  dismiss?: string
  canSubmit?: boolean
  /** Return normally to close, 'stay' to go on to a next step; throw to show the error. */
  onSubmit: () => Promise<void | 'stay'>
  /** A quiet note at the start of the footer, such as what submitting costs. */
  aside?: ReactNode
  children?: ReactNode
}) {
  const { t } = useTranslation()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const change = (o: boolean) => {
    if (busy) return
    if (!o) setError('')
    onOpenChange(o)
  }
  async function submitForm(e: FormEvent) {
    e.preventDefault()
    if (!canSubmit || busy) return
    setBusy(true)
    setError('')
    try {
      if ((await onSubmit()) !== 'stay') onOpenChange(false)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Dialog open={open} onOpenChange={change}>
      <DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto overscroll-contain sm:max-w-[460px]" showCloseButton={false}>
        <form onSubmit={submitForm} className="flex flex-col gap-5" autoComplete="off">
          <DialogHeader>
            <DialogTitle>{title}</DialogTitle>
            {desc && <DialogDescription>{desc}</DialogDescription>}
          </DialogHeader>
          {children && <div className="flex flex-col gap-4">{children}</div>}
          {error && (
            <p role="alert" data-selectable className="-mt-1 rounded-lg bg-destructive/8 px-3 py-2 text-[12.5px] leading-relaxed text-destructive">
              {error}
            </p>
          )}
          <DialogFooter className={aside ? 'sm:items-center' : undefined}>
            {aside && <div className="mr-auto min-w-0 max-sm:order-last max-sm:text-center">{aside}</div>}
            <Button type="button" variant="outline" disabled={busy} onClick={() => change(false)}>
              {dismiss ?? t('form.cancel')}
            </Button>
            <Button type="submit" variant={danger ? 'destructive' : 'default'} disabled={!canSubmit || busy}>
              {busy && <Spinner />}
              {submit}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/**
 * A labelled form field. Its label is a <label> only for a single input
 * (`htmlFor` its id): wrapped around anything else, a click on the label or
 * the hint would press the first button inside, and quietly change the choice.
 */
export function Field({ label, hint, error, htmlFor, children }: { label: string; hint?: ReactNode; error?: string; htmlFor?: string; children: ReactNode }) {
  const id = useId()
  return (
    <div className="flex flex-col gap-1.5" role={htmlFor ? undefined : 'group'} aria-labelledby={htmlFor ? undefined : id}>
      {htmlFor ? (
        <label htmlFor={htmlFor} className="text-[12.5px] font-medium">
          {label}
        </label>
      ) : (
        <span id={id} className="text-[12.5px] font-medium">
          {label}
        </span>
      )}
      {children}
      {(error || hint) && <span className={error ? 'text-[12px] text-destructive' : 'text-[12px] leading-relaxed text-muted-foreground'}>{error || hint}</span>}
    </div>
  )
}
