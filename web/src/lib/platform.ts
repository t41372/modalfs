// Where the UI runs: inside the desktop app (Wails, the system WebView) or in a
// browser tab served by the daemon.

declare global {
  interface Window {
    /** Wails' runtime, present in the desktop app. */
    runtime?: {
      Quit?: () => void
      BrowserOpenURL?: (url: string) => void
      EventsOn?: (name: string, cb: (...data: unknown[]) => void) => () => void
    }
    /** Go methods the desktop app binds (cmd/modalfs-gui). */
    go?: {
      main?: {
        App?: {
          SetAppearance?: (a: string) => Promise<void>
          Quitting?: () => Promise<void>
          QuitAck?: () => Promise<void>
          /** The UI got a close request (no menu bar icon); CloseWindow then closes only the window. */
          CloseAck?: () => Promise<void>
          CloseWindow?: () => Promise<void>
          /** Asks where to save a text file and writes it; resolves to the path, or '' when cancelled. */
          SaveText?: (name: string, text: string) => Promise<string>
        }
      }
    }
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

/** Opens a web page in the default browser: the desktop app's WebView can't open one itself. */
export function openExternal(url: string) {
  if (window.runtime?.BrowserOpenURL) window.runtime.BrowserOpenURL(url)
  else window.open(url, '_blank', 'noopener,noreferrer')
}

/**
 * Saves text as a file the person picks up: the desktop app asks where (its
 * WebView doesn't download), a browser downloads it. Resolves to false only
 * when the person cancelled.
 */
export async function saveText(name: string, text: string): Promise<boolean> {
  const save = window.go?.main?.App?.SaveText
  if (save) return (await save(name, text)) !== ''
  const url = URL.createObjectURL(new Blob([text], { type: 'text/plain;charset=utf-8' }))
  const a = Object.assign(document.createElement('a'), { href: url, download: name })
  document.body.append(a)
  a.click()
  a.remove()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
  return true
}

/** The desktop app's window follows the appearance chosen here, not only the system's. */
export function setWindowAppearance(a: string) {
  window.go?.main?.App?.SetAppearance?.(a).catch(() => {})
}
