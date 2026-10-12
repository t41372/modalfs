import { useQuery } from '@tanstack/react-query'
import { cn } from 'cn'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { api } from '@/api/client'
import { refreshState } from '@/api/queries'
import type { Account, Profile } from '@/api/types'
import { FormDialog } from '@/components/FormDialog'
import { Badge } from '@/components/page'
import { Spinner } from '@/components/Spinner'
import { Checkbox } from '@/components/ui/checkbox'

/** Copy tokens from the Modal CLI's ~/.modal.toml. */
export function ImportDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const { t } = useTranslation()
  const profiles = useQuery({ queryKey: ['profiles'], queryFn: () => api<Profile[]>('GET', '/import/profiles'), enabled: open, staleTime: 0 })
  const [picked, setPicked] = useState<string[]>([])
  useEffect(() => {
    if (open && profiles.data) setPicked(profiles.data.filter((p) => !p.imported).map((p) => p.name))
  }, [open, profiles.data])

  const list = profiles.data ?? []
  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      title={t('form.importTitle')}
      desc={t('form.importP')}
      submit={t('form.import')}
      canSubmit={picked.length > 0}
      onSubmit={async () => {
        const r = await api<{ added: Account[] | null; errors: string[] | null }>('POST', '/import/profiles', { names: picked })
        refreshState()
        // Some may have been added before one failed: those aren't offered again.
        await profiles.refetch()
        const added = new Set((r.added ?? []).map((a) => a.imported_from))
        setPicked((x) => x.filter((n) => !added.has(n)))
        if (r.added?.length) toast.success(t('toast.imported', { count: r.added.length }))
        if (r.errors?.length) throw new Error(r.errors.join('\n'))
      }}
    >
      {profiles.isLoading ? (
        <div className="flex items-center gap-2 py-3 text-[13px] text-muted-foreground">
          <Spinner />
          {t('form.loading')}
        </div>
      ) : list.length === 0 ? (
        <p className="rounded-lg bg-muted px-3 py-2.5 text-[13px] text-muted-foreground">{t('form.importNone')}</p>
      ) : (
        <div className="overflow-hidden rounded-xl border [&>*+*]:border-t">
          {list.map((p) => {
            const checked = picked.includes(p.name)
            return (
              <label key={p.name} className={cn('flex items-center gap-3 px-3.5 py-2.5', p.imported ? 'opacity-60' : 'cursor-pointer hover:bg-muted/50')}>
                <Checkbox
                  checked={checked || p.imported}
                  disabled={p.imported}
                  onCheckedChange={(v) => setPicked((x) => (v ? [...x, p.name] : x.filter((n) => n !== p.name)))}
                />
                <span className="min-w-0 flex-1">
                  <span className="text-[13.5px] font-medium">{p.name}</span>
                  <span className="ml-2 font-mono text-[11.5px] text-muted-foreground">{p.token_id.slice(0, 7)}…</span>
                </span>
                {p.imported ? <Badge>{t('form.imported')}</Badge> : p.active ? <Badge tone="ok">{t('form.active')}</Badge> : null}
              </label>
            )
          })}
        </div>
      )}
    </FormDialog>
  )
}
