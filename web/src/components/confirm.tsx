import { type ReactNode, useState, useSyncExternalStore } from 'react'
import { useTranslation } from 'react-i18next'
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
  desc?: string
  /** A control under the text, such as a checkbox for an option. */
  extra?: ReactNode
  action: string
  danger?: boolean
  /** Runs when confirmed; the dialog stays open, busy, until it settles. */
  run: () => Promise<unknown>
}

let current: Ask | null = null
const listeners = new Set<() => void>()
const set = (a: Ask | null) => {
  current = a
  listeners.forEach((l) => l())
}

/** Asks before something that can't be undone. Call from anywhere; <Confirmer /> shows it. */
export const confirm = (ask: Ask) => set(ask)

export function Confirmer() {
  const ask = useSyncExternalStore(
    (l) => (listeners.add(l), () => listeners.delete(l)),
    () => current,
  )
  const { t } = useTranslation()
  const [busy, setBusy] = useState(false)
  const close = () => !busy && set(null)
  return (
    <AlertDialog open={!!ask} onOpenChange={(o) => !o && close()}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{ask?.title}</AlertDialogTitle>
          {ask?.desc && <AlertDialogDescription>{ask.desc}</AlertDialogDescription>}
        </AlertDialogHeader>
        {ask?.extra}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>{t('form.cancel')}</AlertDialogCancel>
          <Button
            variant={ask?.danger ? 'destructive' : 'default'}
            disabled={busy}
            onClick={async () => {
              if (!ask) return
              setBusy(true)
              try {
                await ask.run()
              } finally {
                setBusy(false)
                set(null)
              }
            }}
          >
            {busy && <Spinner />}
            {ask?.action}
          </Button>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
