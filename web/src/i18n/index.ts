import i18n from 'i18next'
import { initReactI18next } from 'react-i18next'
import type { Language } from '@/api/types'
import en from './en.json'
import zhHans from './zh-Hans.json'
import zhHant from './zh-Hant.json'

export type Lang = 'en' | 'zh-Hant' | 'zh-Hans'

declare module 'i18next' {
  interface CustomTypeOptions {
    defaultNS: 'translation'
    resources: { translation: typeof en }
  }
}

/** The language to show for a setting: the OS's for "system" (Chinese by its script, Traditional for TW/HK/MO), English otherwise. */
export function resolveLanguage(setting: Language | undefined): Lang {
  if (setting === 'en' || setting === 'zh-Hant' || setting === 'zh-Hans') return setting
  for (const tag of navigator.languages ?? [navigator.language]) {
    const l = tag.toLowerCase()
    if (!l.startsWith('zh')) {
      if (l.startsWith('en')) return 'en'
      continue
    }
    if (/hant|-tw|-hk|-mo/.test(l)) return 'zh-Hant'
    return 'zh-Hans'
  }
  return 'en'
}

i18n.use(initReactI18next).init({
  resources: { en: { translation: en }, 'zh-Hant': { translation: zhHant }, 'zh-Hans': { translation: zhHans } },
  lng: resolveLanguage(undefined),
  fallbackLng: 'en',
  interpolation: { escapeValue: false },
  returnNull: false,
})

/** The BCP 47 tag for Intl formatting. */
export const intlTag = (l: string) => (l === 'zh-Hant' ? 'zh-TW' : l === 'zh-Hans' ? 'zh-CN' : 'en-US')

export default i18n
