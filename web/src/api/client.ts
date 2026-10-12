// Talks to the daemon's API at /api: served by the daemon on localhost (which
// wants the session cookie this page gets for the link `modalfs ui` opens), or
// proxied over its Unix socket by the desktop app. A build with VITE_MOCK=1 (development, screenshots) sends every
// call to an in-memory fake instead; other builds leave the fake out.

import i18n from '@/i18n'

export const MOCK = import.meta.env.VITE_MOCK === '1'
const mock = MOCK ? import('./mock') : null

// `modalfs ui` opens /#token=…, a one-time token. Trade it for the session cookie (HttpOnly, so this
// page never holds a key) and take it out of the address bar; the link left in the history is then useless.
let session = Promise.resolve()
const ticket = location.hash.match(/token=([0-9a-f]+)/)?.[1]
if (ticket && !MOCK) {
  history.replaceState(null, '', location.pathname + location.search)
  session = fetch('/api/session', { method: 'POST', headers: { 'X-Modalfs-Token': ticket, 'X-Modalfs-Request': '1' } }).then(
    () => {},
    () => {},
  )
}

/** An error the daemon reported, with its message. */
export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
  ) {
    super(message)
  }
}

export async function api<T = unknown>(method: string, path: string, body?: unknown): Promise<T> {
  if (mock) return (await mock).mockRequest(method, path, body) as Promise<T>
  await session
  // Other pages can't send this header without a CORS preflight, which the daemon never allows.
  const headers: Record<string, string> = { 'Content-Type': 'application/json', 'X-Modalfs-Request': '1' }
  let r: Response
  try {
    r = await fetch('/api' + path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) })
  } catch {
    throw new ApiError(i18n.t('shell.offline'), 0)
  }
  let data: unknown = null
  try {
    data = await r.json()
  } catch {
    // an empty or non-JSON body
  }
  // The daemon's port wants the token from the link `modalfs ui` opened; a bookmark or a new tab doesn't have it.
  if (r.status === 401) throw new ApiError(i18n.t('shell.noToken'), 401)
  if (!r.ok) {
    const msg = (data as { error?: string } | null)?.error || r.statusText || `HTTP ${r.status}`
    throw new ApiError(msg.trim(), r.status)
  }
  return data as T
}
