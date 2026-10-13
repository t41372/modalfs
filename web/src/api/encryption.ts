// End-to-end encrypted volumes (docs/design/encryption.md): every call the UI
// makes about keys, in one place. A volume has one key, shown to the person as
// text (mfs1-…); the key and the encrypted files are all it takes to read
// them, and without the key they're gone.

import { api } from './client'
import type { Volume } from './types'

const base = (account: string, volumeId: string) => `/accounts/${account}/volumes/${volumeId}`

/**
 * A fresh key for a volume about to be created, kept nowhere yet. The UI shows
 * it, the person saves it and says so, and only then is it used, so no volume
 * ever exists with a key nobody has.
 */
export const draftKey = () => api<{ key: string }>('POST', '/encryption/key').then((r) => r.key)

/** Creates an empty volume encrypted with `key`; `remember` keeps the key on this computer. */
export const createEncrypted = (account: string, env: string, name: string, key: string, remember: boolean) =>
  api<Volume>('POST', `/accounts/${account}/volumes`, { name, environment: env, encrypted: true, key, remember })

/** Unlocks a volume on this computer with its key; `remember` keeps it past modalfs quitting. */
export const unlock = (account: string, env: string, volumeId: string, key: string, remember: boolean) =>
  api('POST', `${base(account, volumeId)}/unlock`, { environment: env, key, remember })

/** Forgets the key on this computer (the volume must be unmounted). */
export const lock = (account: string, env: string, volumeId: string) => api('POST', `${base(account, volumeId)}/lock`, { environment: env })

/** The key, from a computer that remembers it. */
export const showKey = (account: string, env: string, volumeId: string) =>
  api<{ key: string }>('GET', `${base(account, volumeId)}/key?env=${encodeURIComponent(env)}`).then((r) => r.key)

/** The key as typed or pasted: spaces, dashes and case don't matter. Empty when it can't be a modalfs key. */
export function normalizeKey(text: string) {
  const k = text.replace(/[\s-]+/g, '').toUpperCase()
  return /^MFS1[0-9A-Z]{20,}$/.test(k) ? k : ''
}
