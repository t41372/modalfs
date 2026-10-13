import { useQuery } from '@tanstack/react-query'
import { cn } from 'cn'
import { Lock } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { api } from '@/api/client'
import { refreshState } from '@/api/queries'
import type { Account, Profile } from '@/api/types'
import { Badge, Card } from '@/components/page'
import { Spinner } from '@/components/Spinner'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { AddAccountDialog } from './AddAccount'

/**
 * Connecting a Modal account: the Modal CLI's profiles found on this computer,
 * ticked and imported at once, or a token pasted in.
 */
export function ConnectPanel({ onDone }: { onDone?: () => void }) {
  const { t } = useTranslation()
  const profiles = useQuery({ queryKey: ['profiles'], queryFn: () => api<Profile[]>('GET', '/import/profiles'), staleTime: 0 })
  const [picked, setPicked] = useState<string[]>([])
  const [busy, setBusy] = useState(false)
  const [pasting, setPasting] = useState(false)
  useEffect(() => {
    if (profiles.data) setPicked(profiles.data.filter((p) => !p.imported).map((p) => p.name))
  }, [profiles.data])
  const list = profiles.data ?? []
  const fresh = list.filter((p) => !p.imported)

  async function importPicked() {
    setBusy(true)
    try {
      const r = await api<{ added: Account[] | null; errors: string[] | null }>('POST', '/import/profiles', { names: picked })
      refreshState()
      await profiles.refetch()
      if (r.added?.length) toast.success(t('toast.imported', { count: r.added.length }))
      if (r.errors?.length) toast.error(r.errors.join('\n'))
      if (r.added?.length && !r.errors?.length) onDone?.()
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="flex flex-col gap-6">
      {profiles.isLoading ? (
        <div className="flex items-center gap-2 text-[13px] text-muted-foreground">
          <Spinner />
          {t('form.loading')}
        </div>
      ) : (
        fresh.length > 0 && (
          <section>
            <h2 className="mb-2 px-1 text-[12.5px] font-medium text-muted-foreground">{t('connect.found')}</h2>
            <Card>
              {list.map((p) => {
                const checked = picked.includes(p.name)
                return (
                  <label key={p.name} className={cn('flex items-center gap-3 px-4 py-3', p.imported ? 'opacity-60' : 'cursor-pointer hover:bg-muted/40')}>
                    <Checkbox checked={checked || p.imported} disabled={p.imported} onCheckedChange={(v) => setPicked((x) => (v ? [...x, p.name] : x.filter((n) => n !== p.name)))} />
                    <span className="min-w-0 flex-1 truncate text-[14px] font-medium">{p.name}</span>
                    {p.imported ? <Badge>{t('form.imported')}</Badge> : p.active ? <Badge tone="ok">{t('form.active')}</Badge> : null}
                  </label>
                )
              })}
              <div className="flex justify-end px-4 py-3">
                <Button disabled={!picked.length || busy} onClick={importPicked}>
                  {busy && <Spinner />}
                  {t('connect.importN', { count: picked.length })}
                </Button>
              </div>
            </Card>
          </section>
        )
      )}
      <section>
        {fresh.length > 0 && <h2 className="mb-2 px-1 text-[12.5px] font-medium text-muted-foreground">{t('connect.orElse')}</h2>}
        <Card>
          <div className="flex items-center gap-3 px-4 py-3">
            <span className="min-w-0 flex-1 text-[14px] font-medium">{t('connect.paste')}</span>
            <Button variant={fresh.length ? 'outline' : 'default'} onClick={() => setPasting(true)}>
              {t('connect.pasteButton')}
            </Button>
          </div>
        </Card>
      </section>
      <p className="flex items-center gap-2 px-1 text-[12.5px] text-muted-foreground">
        <Lock className="size-3.5 shrink-0" />
        {t('connect.safe')}
      </p>
      <AddAccountDialog
        open={pasting}
        onOpenChange={(o) => {
          setPasting(o)
          if (!o) refreshState()
        }}
      />
    </div>
  )
}

export function ConnectDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const { t } = useTranslation()
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-[480px]">
        <DialogHeader>
          <DialogTitle>{t('connect.title')}</DialogTitle>
          <DialogDescription>{t('connect.desc')}</DialogDescription>
        </DialogHeader>
        <ConnectPanel onDone={() => onOpenChange(false)} />
      </DialogContent>
    </Dialog>
  )
}
