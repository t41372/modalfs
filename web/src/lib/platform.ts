// Where the UI runs: inside the desktop app (Wails, the system WebView) or in a
// browser tab served by the daemon.

declare global {
  interface Window {
    /** Wails' runtime, present in the desktop app. */
    runtime?: { Quit?: () => void }
    /** Go methods the desktop app binds (cmd/modalfs-gui). */
    go?: { main?: { App?: { SetAppearance?: (a: string) => Promise<void>; Quitting?: () => Promise<void> } } }
  }
}

const params = new URLSearchParams(location.search)

export const inApp = typeof window.runtime !== 'undefined' || typeof window.go !== 'undefined'
export const isMac = /Mac/.test(navigator.platform) || /Mac OS X/.test(navigator.userAgent)

/**
 * Whether the window is see-through glass over the desktop. Only the macOS app
 * has the material behind it; a browser tab or Linux has nothing to show
 * through, so it gets solid colors. The screenshots ask for glass with ?glass.
 */
export const glass = (inApp && isMac) || params.has('glass')

/** Room for the macOS traffic lights at the top of the rail. */
export const trafficLights = (inApp && isMac) || params.has('glass')

export function markPlatform() {
  const root = document.documentElement
  root.dataset.glass = glass ? 'on' : 'off'
  root.dataset.platform = isMac ? 'darwin' : 'other'
  if (inApp) root.dataset.app = ''
}

/** The desktop app's window follows the appearance chosen here, not only the system's. */
export function setWindowAppearance(a: string) {
  window.go?.main?.App?.SetAppearance?.(a).catch(() => {})
}
