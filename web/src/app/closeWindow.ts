// The window's close button in the desktop app without a menu bar icon (Linux). Only the window
// closes: the daemon keeps the volumes mounted, and nothing on screen says so afterwards. So the
// first time it happens with something mounted, the window says it and offers to quit instead.

import { api } from '@/api/client'
import type { State } from '@/api/types'
import { confirm, confirmBusy } from '@/components/confirm'
import i18n from '@/i18n'
import { live } from '@/lib/mounts'
import { checklistOpen, startQuit } from './quitFlow'

const KEY = 'modalfs.closeExplained'

const app = () => window.go?.main?.App
const close = () => app()?.CloseWindow?.()

function explained() {
  try {
    return localStorage.getItem(KEY) === '1'
  } catch {
    return false
  }
}

export async function askClose() {
  void app()?.CloseAck?.().catch(() => {})
  // This page is unmounting the volumes, or running what was just confirmed: closing it now would
  // stop that halfway. The window stays; it can close once that's done.
  if (checklistOpen() || confirmBusy()) return
  const mounted = await api<State>('GET', '/state')
    .then((st) => st.mounts.filter(live).length)
    .catch(() => 0)
  if (!mounted || explained()) return void close()
  confirm({
    title: i18n.t('closeWindow.q'),
    desc: i18n.t('closeWindow.p', { count: mounted }),
    action: i18n.t('closeWindow.close'),
    dismiss: i18n.t('closeWindow.keep'),
    alt: { label: i18n.t('closeWindow.quit'), run: async () => void startQuit() }, // the checklist takes over
    focusAction: true,
    run: async () => {
      try {
        localStorage.setItem(KEY, '1')
      } catch {
        // asked again next time
      }
      await close()
    },
  })
}
