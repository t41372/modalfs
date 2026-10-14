// The README's pictures, made the same way each time:
//
//   bun run screenshots              (writes docs/screenshots/<language>/<shot>.jpg)
//   bun run screenshots volumes      (only the shots named)
//
// 1. The UI is built against the mock API (src/api/mock.ts) in its calm
//    ?scenario=demo, so the pictures show an ordinary good day on sample data
//    that belongs to no one, and it's served from a temporary folder.
// 2. Each page is shot in Chromium with its glass left see-through (?glass), at
//    the app's own window size (cmd/modalfs-gui: 1000 × 680). Its clock starts
//    at the same moment in the same time zone every time (CLOCK), so the
//    pictures don't change from one run to the next, and the times on them
//    don't depend on when or where they were taken.
// 3. It's laid over a painting from docs/screenshots/backdrops/, the way macOS
//    shows the window over a wallpaper: the painting behind the window blurred
//    and tinted like the full-screen UI material, with the window's corners,
//    shadow and traffic lights. Adapted from Jezo's screenshot script, whose
//    paintings these are (painted by OpenAI's image model through Codex).
// 4. The hero adds a real Finder window showing the same volume mounted, from
//    docs/screenshots/inputs/ (scripts/capture-finder.sh makes it).
//
// A new picture is an entry in SHOTS; a new language one in LANGUAGES.

import { execFileSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { createServer } from 'node:http'
import { tmpdir } from 'node:os'
import { extname, join } from 'node:path'
import { chromium, type Page } from '@playwright/test'

type Theme = 'light' | 'dark'

interface Shot {
  name: string
  page: string
  theme: Theme
  /** A file in docs/screenshots/backdrops, without `.jpg`. */
  backdrop: string
  /** Gets the page to what the picture shows. */
  show?: (page: Page) => Promise<void>
  /** A Finder window from docs/screenshots/inputs, in front of the app's, lower right, as if just opened with Show in Finder. */
  finder?: string
}

const SHOTS: Shot[] = [
  // The README's first picture: a volume in Finder, and the app that put it there.
  { name: 'hero', page: 'volumes', theme: 'dark', backdrop: 'rain', finder: 'finder-datasets-dark.png' },
  {
    name: 'volume',
    page: 'volumes&id=m-datasets',
    theme: 'dark',
    backdrop: 'rain',
    // Its traffic chart and folders have drawn.
    show: (page) => page.waitForTimeout(1200),
  },
  { name: 'activity', page: 'activity', theme: 'light', backdrop: 'sky' },
  { name: 'settings', page: 'settings', theme: 'dark', backdrop: 'rain' },
  {
    name: 'download',
    page: 'downloads&id=dl-train',
    theme: 'light',
    backdrop: 'sky',
    // The running download that Modal moved to a new container, once its chart has drawn.
    show: (page) => page.waitForTimeout(2500),
  },
]

/** The folder name under docs/screenshots, and the language the UI is set to. */
const LANGUAGES = [
  { dir: 'en', lang: 'en' },
  { dir: 'zh-CN', lang: 'zh-Hans' },
]

/** The app's window at its default size (cmd/modalfs-gui/main.go). */
const WINDOW = { width: 1000, height: 680 }
/** The picture: a 1440 × 900 desktop, so the window takes the share of the screen it really does. */
const STAGE = { width: 1440, height: 900 }

/** What macOS does behind a full-screen UI window: a wide blur that keeps the colors, and a light or dark tint. From Jezo, matched by eye. */
const MATERIAL: Record<Theme, { filter: string; tint: string }> = {
  light: { filter: 'blur(30px) saturate(1.7) brightness(1.06)', tint: 'rgb(246 246 250 / 0.28)' },
  dark: { filter: 'blur(30px) saturate(1.5) brightness(0.8)', tint: 'rgb(22 20 34 / 0.38)' },
}

const web = join(import.meta.dirname, '..')
const out = join(web, '../docs/screenshots')
const only = process.argv.slice(2)
const shots = only.length ? SHOTS.filter((s) => only.includes(s.name)) : SHOTS

// ─── 1. The UI on the mock API ───

const build = mkdtempSync(join(tmpdir(), 'modalfs-screenshots-'))
execFileSync('bunx', ['vite', 'build', '--outDir', build, '--emptyOutDir', '--logLevel', 'warn'], { cwd: web, env: { ...process.env, VITE_MOCK: '1' }, stdio: 'inherit' })

const types: Record<string, string> = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.woff2': 'font/woff2', '.png': 'image/png' }
const server = createServer((req, res) => {
  const path = new URL(req.url ?? '/', 'http://x').pathname
  try {
    const body = readFileSync(join(build, path === '/' ? 'index.html' : path))
    res.writeHead(200, { 'Content-Type': types[extname(path) || '.html'] ?? 'application/octet-stream' }).end(body)
  } catch {
    res.writeHead(404).end()
  }
})
await new Promise<void>((r) => server.listen(0, '127.0.0.1', r))
const port = (server.address() as { port: number }).port

// ─── 2. Each page ───

/** The moment every picture is taken at: an ordinary afternoon. */
const CLOCK = { time: new Date('2026-10-14T14:10:00+08:00'), timezoneId: 'Asia/Taipei' }

const browser = await chromium.launch()
const stage = await browser.newPage({ viewport: STAGE, deviceScaleFactor: 2 })

for (const { dir, lang } of LANGUAGES) {
  mkdirSync(join(out, dir), { recursive: true })
  for (const shot of shots) {
    const page = await browser.newPage({ viewport: WINDOW, deviceScaleFactor: 2, colorScheme: shot.theme, timezoneId: CLOCK.timezoneId })
    await page.clock.install({ time: CLOCK.time })
    await page.goto(`http://127.0.0.1:${port}/?glass&scenario=demo&lang=${lang}&theme=${shot.theme}&page=${shot.page}`)
    await page.locator('main h1').waitFor()
    await page.waitForTimeout(400)
    await shot.show?.(page)
    // Off the page, so whatever was clicked last isn't shown hovered.
    await page.mouse.move(WINDOW.width - 2, WINDOW.height - 2)
    // Lets the page settle: fonts, a fold opening, the rail's highlight moving.
    await page.waitForTimeout(800)
    await compose(await page.screenshot({ omitBackground: true }), shot, join(out, dir, `${shot.name}.jpg`))
    await page.close()
  }
  console.log(`docs/screenshots/${dir}: ${shots.map((s) => `${s.name}.jpg`).join(', ')}`)
}

await browser.close()
server.close()
rmSync(build, { recursive: true, force: true })

// ─── 3. Over the painting ───

async function compose(window: Buffer, shot: Shot, path: string) {
  const backdrop = readFileSync(join(out, 'backdrops', `${shot.backdrop}.jpg`)).toString('base64')
  const { filter, tint } = MATERIAL[shot.theme]
  const light = shot.theme === 'light'
  const button = (color: string, x: number) =>
    `<span style="position:absolute;left:${x}px;top:0;width:12px;height:12px;border-radius:50%;background:${color};box-shadow:inset 0 0 0 0.5px rgb(0 0 0 / 0.2)"></span>`
  // Centred, or with a Finder window in front: the app up and to the left, Finder down and to the right.
  const at = shot.finder ? { left: 100, top: 64 } : { left: (STAGE.width - WINDOW.width) / 2, top: (STAGE.height - WINDOW.height) / 2 }
  let finder = ''
  if (shot.finder) {
    // A screencapture at 2x, corners already transparent; macOS's own window shadow added here.
    const png = readFileSync(join(out, 'inputs', shot.finder))
    const w = png.readUInt32BE(16) / 2
    const h = png.readUInt32BE(20) / 2
    finder = `<img src="data:image/png;base64,${png.toString('base64')}" style="position:absolute;left:${STAGE.width - w - 90}px;top:${STAGE.height - h - 72}px;width:${w}px;height:${h}px;
      border-radius:12px;box-shadow:0 30px 80px rgb(0 0 0 / 0.5), 0 0 0 0.5px rgb(0 0 0 / 0.6)">`
  }
  await stage.setContent(`<!doctype html>
<body style="margin:0;overflow:hidden">
  <div style="position:relative;width:${STAGE.width}px;height:${STAGE.height}px;background:url(data:image/jpeg;base64,${backdrop}) center/cover">
    <div style="position:absolute;left:${at.left}px;top:${at.top}px;width:${WINDOW.width}px;height:${WINDOW.height}px;
      border-radius:12px;overflow:hidden;box-shadow:0 28px 70px rgb(0 0 0 / 0.38), 0 0 0 0.5px rgb(0 0 0 / ${light ? 0.18 : 0.6})">
      <div style="position:absolute;inset:0;backdrop-filter:${filter};background:${tint}"></div>
      <img src="data:image/png;base64,${window.toString('base64')}" style="position:absolute;inset:0;width:100%;height:100%">
      <div style="position:absolute;left:20px;top:19px">${button('#ff5f57', 0)}${button('#febc2e', 20)}${button('#28c840', 40)}</div>
      <div style="position:absolute;inset:0;border-radius:12px;box-shadow:inset 0 0 0 0.5px rgb(255 255 255 / ${light ? 0.55 : 0.14})"></div>
    </div>
    ${finder}
  </div>
</body>`)
  await stage.waitForFunction(() => [...document.images].every((img) => img.complete))
  await stage.screenshot({ path, type: 'jpeg', quality: 86 })
}
