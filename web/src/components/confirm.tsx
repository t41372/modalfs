import { type ReactNode, useRef, useState, useSyncExternalStore } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { Spinner } from './Spinner'

interface Ask {
  title: string
  desc?: ReactNode
  /** A control under the text, such as a checkbox for an option. */
  extra?: ReactNode
  action: string
  /** The dismiss button, when "Cancel" would be ambiguous: it never repeats a word of `action`. */
  dismiss?: string
  /** A second way out, left of the dismiss button, such as "Pause instead". */
  alt?: { label: string; run: () => Promise<unknown> }
  danger?: boolean
  /** Focus the action button rather than the first one, when `alt` would be the wrong default for Enter. */
  focusAction?: boolean
  /** Runs when confirmed; the dialog stays open, busy, until it settles. If it fails, the error shows and the dialog stays, to try again or dismiss. */
  run: () => Promise<unknown>
}

let current: Ask | null = null
let running = false
const listeners = new Set<() => void>()
const set = (a: Ask | null) => {
  current = a
  listeners.forEach((l) => l())
}

/** Asks before something that can't be undone. Call from anywhere; <Confirmer /> shows it. */
export const confirm = (ask: Ask) => set(ask)

/** Whether a confirmed action is still running; a new question would replace its dialog. */
export const confirmBusy = () => running

export function Confirmer() {
  const ask = useSyncExternalStore(
    (l) => (listeners.add(l), () => listeners.delete(l)),
    () => current,
  )
  const { t } = useTranslation()
  const [busy, setBusy] = useState(false)
  const actionRef = useRef<HTMLButtonElement>(null)
  const close = () => !busy && set(null)
  const settle = async (run: () => Promise<unknown>) => {
    setBusy(true)
    running = true
    try {
      await run()
      set(null)
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e))
    } finally {
      running = false
      setBusy(false)
    }
  }
  return (
    <AlertDialog open={!!ask} onOpenChange={(o) => !o && close()}>
      <AlertDialogContent initialFocus={ask?.focusAction ? actionRef : undefined}>
        <AlertDialogHeader>
          <AlertDialogTitle>{ask?.title}</AlertDialogTitle>
          {ask?.desc && <AlertDialogDescription>{ask.desc}</AlertDialogDescription>}
        </AlertDialogHeader>
        {ask?.extra}
        <AlertDialogFooter>
          {ask?.alt && (
            <Button
              variant="ghost"
              className="sm:mr-auto"
              disabled={busy}
              onClick={() => settle(ask.alt!.run)}
            >
              {ask.alt.label}
            </Button>
          )}
          <AlertDialogCancel disabled={busy}>{ask?.dismiss ?? t('form.cancel')}</AlertDialogCancel>
          <Button
            ref={actionRef}
            variant={ask?.danger ? 'destructive' : 'default'}
            disabled={busy}
            onClick={() => ask && settle(ask.run)}
          >
            {busy && <Spinner />}
            {ask?.action}
          </Button>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
