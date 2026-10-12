import { useEffect, useSyncExternalStore } from 'react'
import { useTranslation } from 'react-i18next'
import type { Appearance, Language } from '@/api/types'
import { resolveLanguage } from '@/i18n'
import { setWindowAppearance } from '@/lib/platform'

const darkQuery = matchMedia('(prefers-color-scheme: dark)')
const useSystemDark = () =>
  useSyncExternalStore(
    (l) => (darkQuery.addEventListener('change', l), () => darkQuery.removeEventListener('change', l)),
    () => darkQuery.matches,
  )

/** Whether the UI is dark for an appearance setting. */
export function useIsDark(appearance: Appearance | undefined) {
  const system = useSystemDark()
  return appearance === 'dark' || (appearance !== 'light' && system)
}

/** Applies the appearance and language settings to the document (and the app window). */
export function useLook(appearance: Appearance | undefined, language: Language | undefined) {
  const dark = useIsDark(appearance)
  useEffect(() => {
    document.documentElement.classList.toggle('dark', dark)
    document.documentElement.style.colorScheme = dark ? 'dark' : 'light'
  }, [dark])
  useEffect(() => {
    if (appearance) setWindowAppearance(appearance)
  }, [appearance])

  const { i18n } = useTranslation()
  const lang = resolveLanguage(language)
  useEffect(() => {
    if (i18n.language !== lang) i18n.changeLanguage(lang)
    document.documentElement.lang = lang
  }, [lang, i18n])
  return dark
}
