import { useTranslation } from 'react-i18next'
import { ConnectPanel } from '@/dialogs/Connect'

/** The first run: no account yet, so the volumes page asks for one. */
export function Welcome() {
  const { t } = useTranslation()
  return (
    <div className="mx-auto w-full max-w-[600px] px-5 pt-12 pb-16 sm:px-10 sm:pt-20">
      <p className="mb-1.5 text-[13px] text-muted-foreground">{t('welcome.hello')}</p>
      <h1 className="text-[28px] leading-tight font-semibold tracking-[-0.02em] text-balance">{t('welcome.title')}</h1>
      <p className="mt-2 mb-8 text-[14.5px] text-muted-foreground">{t('welcome.sub')}</p>
      <ConnectPanel />
    </div>
  )
}
