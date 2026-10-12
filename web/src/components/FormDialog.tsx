import { type FormEvent, type ReactNode, useState } from 'react'
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
  canSubmit = true,
  onSubmit,
  children,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: string
  desc?: ReactNode
  submit: string
  danger?: boolean
  canSubmit?: boolean
  /** Return normally to close; throw to show the error. */
  onSubmit: () => Promise<void>
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
      await onSubmit()
      onOpenChange(false)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Dialog open={open} onOpenChange={change}>
      <DialogContent className="sm:max-w-[440px]" showCloseButton={false}>
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
          <DialogFooter>
            <Button type="button" variant="outline" disabled={busy} onClick={() => change(false)}>
              {t('form.cancel')}
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

/** A labelled form field. */
export function Field({ label, hint, error, children }: { label: string; hint?: ReactNode; error?: string; children: ReactNode }) {
  return (
    <label className="flex flex-col gap-1.5">
      <span className="text-[12.5px] font-medium">{label}</span>
      {children}
      {(error || hint) && <span className={error ? 'text-[12px] text-destructive' : 'text-[12px] leading-relaxed text-muted-foreground'}>{error || hint}</span>}
    </label>
  )
}
