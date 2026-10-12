// Fails when a translation is missing a key the English strings have, or has one
// they don't. Plural forms count as one key: Chinese has only `_other`.

import en from '../src/i18n/en.json'
import zhHans from '../src/i18n/zh-Hans.json'
import zhHant from '../src/i18n/zh-Hant.json'

const keys = (o: object, prefix = ''): string[] =>
  Object.entries(o).flatMap(([k, v]) => (typeof v === 'object' ? keys(v, `${prefix}${k}.`) : [`${prefix}${k.replace(/_(one|other)$/, '')}`]))

const want = new Set(keys(en))
let bad = false
for (const [name, strings] of Object.entries({ 'zh-Hant': zhHant, 'zh-Hans': zhHans })) {
  const have = new Set(keys(strings))
  for (const k of want) if (!have.has(k)) (bad = true), console.error(`${name}: missing ${k}`)
  for (const k of have) if (!want.has(k)) (bad = true), console.error(`${name}: extra ${k}`)
}
if (bad) process.exit(1)
console.log('locales ok')
