// Where the UI runs: inside the desktop app (Wails, the system WebView) or in a
// browser tab served by the daemon.

const params = new URLSearchParams(location.search)

// Only the native asset server adds this marker. Importing Wails has side
// effects, so the daemon's browser UI must never infer it from its browser.
export const inApp = document.querySelector<HTMLMetaElement>('meta[name="modalfs-desktop"]')?.content === '1'
// The app serves the Wails runtime that matches its Go side; the npm package
// only provides the types, so the two can't drift apart in separate updates.
const runtimeURL = '/wails/runtime.js'
const runtime: typeof import('@wailsio/runtime') | undefined = inApp ? await import(/* @vite-ignore */ runtimeURL) : undefined

function call<T = void>(method: string, ...args: unknown[]): Promise<T> {
  return runtime!.Call.ByName(`main.App.${method}`, ...args)
}

/** The desktop bridge is absent in the daemon's browser UI. */
export const desktop = runtime ? {
  quitting: () => call('Quitting'),
  quitAck: () => call('QuitAck'),
  closeAck: () => call('CloseAck'),
  closeWindow: () => call('CloseWindow'),
  on: (name: string, callback: (...data: unknown[]) => void) => runtime.Events.On(name, (event) => {
    const data: unknown = event.data
    callback(...(Array.isArray(data) ? data : data == null ? [] : [data]))
  }),
} : undefined

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
  if (runtime) void runtime.Browser.OpenURL(url)
  else window.open(url, '_blank', 'noopener,noreferrer')
}

/**
 * Saves text as a file the person picks up: the desktop app asks where (its
 * WebView doesn't download), a browser downloads it. Resolves to false only
 * when the person cancelled.
 */
export async function saveText(name: string, text: string): Promise<boolean> {
  if (runtime) return (await call<string>('SaveText', name, text)) !== ''
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
  if (runtime) void call('SetAppearance', a).catch(() => {})
}
