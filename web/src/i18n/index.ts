import i18n from 'i18next'
import { initReactI18next } from 'react-i18next'
import type { Language } from '@/api/types'
import { isMac } from '@/lib/platform'
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

type Strings = { [k: string]: string | Strings }

/** a with b's strings in their place. */
function merge<T extends Strings>(a: T, b: Strings): T {
  const out: Strings = { ...a }
  for (const [k, v] of Object.entries(b)) out[k] = typeof v === 'string' ? v : merge((a[k] ?? {}) as Strings, v)
  return out as T
}

const bases = { en, 'zh-Hant': zhHant, 'zh-Hans': zhHans }

// Whether the daemon runs on a Mac. The browser's guess holds until the daemon says (setPlatform).
let mac = isMac

/** Each language's strings for that computer: off a Mac, its "linux" block replaces the ones about the Mac, Finder and the Keychain. */
const forHere = <T extends Strings & { linux: Strings }>(r: T) => (mac ? r : merge(r, r.linux))

i18n.use(initReactI18next).init({
  resources: { en: { translation: forHere(en) }, 'zh-Hant': { translation: forHere(zhHant) }, 'zh-Hans': { translation: forHere(zhHans) } },
  lng: resolveLanguage(undefined),
  fallbackLng: 'en',
  interpolation: { escapeValue: false },
  returnNull: false,
})

/**
 * Words the UI for the computer the daemon runs on (its platform, "darwin/arm64"). A browser can't
 * tell: it may reach a Linux server's daemon from a Mac through an SSH tunnel.
 */
export function setPlatform(platform: string | undefined) {
  if (!platform || platform.startsWith('darwin/') === mac) return
  mac = !mac
  for (const [lng, r] of Object.entries(bases)) i18n.addResourceBundle(lng, 'translation', forHere(r), true, true)
  void i18n.changeLanguage(i18n.language) // shows the new strings
}

/** The BCP 47 tag for Intl formatting. */
export const intlTag = (l: string) => (l === 'zh-Hant' ? 'zh-TW' : l === 'zh-Hans' ? 'zh-CN' : 'en-US')

export default i18n
