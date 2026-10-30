import { useQuery } from '@tanstack/react-query'
import { Copy, ExternalLink } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { api } from '@/api/client'
import { refreshState } from '@/api/queries'
import type { Account } from '@/api/types'
import { Card } from '@/components/page'
import { Spinner } from '@/components/Spinner'
import { Button } from '@/components/ui/button'
import { copyText } from '@/lib/platform'

interface Login {
  id: string
  url: string
  code?: string
  expires_at: number
  state: 'waiting' | 'connected' | 'cancelled' | 'error'
  account?: Account
  error?: string
}

/** The daemon owns the token flow; this component only holds its public link. */
export function BrowserLogin({ onDone }: { onDone?: () => void }) {
  const { t } = useTranslation()
  const [login, setLogin] = useState<Login | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const active = useRef<string | null>(null)
  const mounted = useRef(true)
  const handled = useRef<string | null>(null)
  const status = useQuery({
    queryKey: ['account-login', login?.id],
    queryFn: () => api<Login>('GET', `/account-logins/${login!.id}`),
    enabled: !!login,
    // Stops when the attempt ends, or when asking fails: the daemon has quit or
    // restarted and no longer knows this attempt.
    refetchInterval: (q) => q.state.status !== 'error' && (q.state.data?.state ?? 'waiting') === 'waiting' ? 1000 : false,
    retry: false,
  })
  const current = status.data ?? login

  function connected(result: Login) {
    if (!result.account || handled.current === result.id) return
    handled.current = result.id
    active.current = null
    refreshState()
    toast.success(t('toast.accountAdded', { w: result.account.workspace || result.account.label }))
    if (mounted.current) {
      setLogin(null)
      onDone?.()
    }
  }

  function reportCleanup(result: Login) {
    if (result.state === 'connected') connected(result)
    else if (result.state === 'error' && result.error) toast.error(result.error)
  }

  useEffect(() => {
    const s = status.data
    if (s?.state === 'connected') connected(s)
    // A finished attempt has nothing left to cancel, and closing the dialog
    // mustn't report its error a second time.
    else if (s && s.state !== 'waiting' && active.current === s.id) active.current = null
  }, [status.data])

  useEffect(() => {
    mounted.current = true
    return () => {
      mounted.current = false
      if (active.current) {
        void api<Login>('DELETE', `/account-logins/${active.current}`).then(reportCleanup).catch((e) => toast.error(String(e)))
      }
    }
  }, [])

  async function openBrowser(id: string) {
    if (mounted.current && active.current === id) setError('')
    try {
      await api('POST', `/account-logins/${id}/open`)
    } catch {
      if (mounted.current && active.current === id) setError(t('connect.openFailed'))
    }
  }

  async function start() {
    setBusy(true)
    setError('')
    try {
      if (active.current) await api('DELETE', `/account-logins/${active.current}`)
      const next = await api<Login>('POST', '/account-logins', {})
      if (!mounted.current) {
        reportCleanup(await api<Login>('DELETE', `/account-logins/${next.id}`))
        return
      }
      active.current = next.id
      setLogin(next)
      void openBrowser(next.id)
    } catch (e) {
      if (mounted.current) setError(e instanceof Error ? e.message : String(e))
      else toast.error(String(e))
    } finally {
      if (mounted.current) setBusy(false)
    }
  }

  function reset(message = '') {
    active.current = null
    setLogin(null)
    setError(message)
  }

  async function cancel() {
    const id = active.current
    if (!id) return reset() // already finished
    setBusy(true)
    try {
      // Cancelling waits for a save already under way, so this is the outcome.
      const result = await api<Login>('DELETE', `/account-logins/${id}`)
      if (result.state === 'connected') connected(result)
      else reset(result.state === 'error' ? result.error : '')
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  async function copy() {
    try {
      await copyText(login!.url)
      setError('')
      toast.success(t('connect.copied'))
    } catch {
      setError(t('connect.copyFailed'))
    }
  }

  const state = status.isError ? 'error' : current?.state
  const waiting = state === 'waiting'
  return (
    <Card>
      <div className="flex flex-col gap-4 p-4">
        <div>
          <h2 className="text-[14px] font-medium">{t('connect.browserTitle')}</h2>
          <p className="mt-1 text-[13px] text-muted-foreground">{t('connect.browserDesc')}</p>
        </div>
        {!login ? (
          <Button className="self-start" disabled={busy} onClick={start}>
            {busy ? <Spinner /> : <ExternalLink className="size-4" />}
            {t('connect.browserButton')}
          </Button>
        ) : (
          <>
            <p role="status" className="flex items-center gap-2 text-[13px]">
              {waiting && <Spinner />}
              {state === 'error' ? t('connect.failed') : state === 'cancelled' ? t('connect.cancelled') : t('connect.waiting')}
            </p>
            {login.code && <p className="text-[13px]">{t('connect.code')} <strong className="font-mono select-text">{login.code}</strong></p>}
            <input aria-label={t('connect.link')} readOnly value={login.url} onFocus={(e) => e.currentTarget.select()} className="min-w-0 rounded-md border bg-background px-3 py-2 text-[12px] text-muted-foreground" />
            <p className="text-[12px] text-muted-foreground">{t('connect.expires', { time: new Date(login.expires_at * 1000).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) })}</p>
            <div className="flex flex-wrap gap-2">
              {waiting ? <Button disabled={busy} onClick={() => openBrowser(login.id)}><ExternalLink className="size-4" />{t('connect.openAgain')}</Button> : <Button disabled={busy} onClick={start}>{t('connect.tryAgain')}</Button>}
              <Button variant="outline" onClick={copy}><Copy className="size-4" />{t('connect.copyLink')}</Button>
              <Button variant="ghost" disabled={busy} onClick={cancel}>{t('form.cancel')}</Button>
            </div>
          </>
        )}
        {current?.error && <p role="alert" className="text-[13px] text-destructive">{current.error}</p>}
        {(error || status.error) && <p role="alert" className="text-[13px] text-destructive">{error || status.error?.message}</p>}
      </div>
    </Card>
  )
}
