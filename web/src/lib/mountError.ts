// A mount's error in the words of the person using it. The daemon's own text stays available as
// the detail (hover, or under it on the volume's page): it's what a bug report needs.

import type { TFunction } from 'i18next'

const KINDS: [RegExp, string][] = [
  [/rejected this account's token|Unauthenticated|PermissionDenied/i, 'token'],
  [/couldn't reach Modal|Unavailable|connection refused|no such host|network is unreachable|i\/o timeout|DeadlineExceeded/i, 'offline'],
  [/now belongs to a different volume|re-?pin/i, 'recreated'],
  [/no longer exists/i, 'gone'],
  [/is not empty; refusing/i, 'notEmpty'],
  [/already a mount point/i, 'taken'],
  [/macFUSE/i, 'fuse'],
  [/NFS mount failed/i, 'nfs'],
  [/Volume v2 only/i, 'v1'],
  [/shutting down/i, 'quitting'],
]

/** Why uploads fail, in a few words: for a row that has little room. */
export function uploadProblem(err: string | undefined, t: TFunction): string {
  const kind = KINDS.find(([re]) => re.test(err ?? ''))?.[1]
  return t(kind === 'offline' ? 'vol.stall.offline' : kind === 'token' ? 'vol.stall.token' : 'vol.stall.other')
}

/** The plain sentence for a mount's error, or the error itself when it isn't one modalfs knows. */
export function mountError(err: string | undefined, t: TFunction): { text: string; known: boolean } {
  if (!err) return { text: '', known: false }
  for (const [re, kind] of KINDS) {
    if (re.test(err)) return { text: t(`mountErr.${kind}` as 'mountErr.token'), known: true }
  }
  return { text: err, known: false }
}
