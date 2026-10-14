// The README's "How it works" picture, an isometric line drawing, in each
// README language:
//
//   bun run diagrams          (writes docs/diagrams/how-it-works.<language>.svg)
//
// Plain SVG with system fonts, so GitHub shows it as an image, and colors that
// follow the reader's light or dark theme. Change the words in WORDS, the
// shapes below them.

import { writeFileSync } from 'node:fs'
import { join } from 'node:path'

const WORDS = {
  en: {
    computer: 'Your computer',
    computerSub: ['Finder, editors and agents', 'use an ordinary folder'],
    modalfs: 'modalfs',
    modalfsSub: ['serves the folder over NFS or FUSE,', 'keeps a cache and a journal on disk'],
    modal: 'Modal',
    api: 'Volume API',
    blocks: 'Block store',
    fn: 'Your Functions',
    files: 'files',
    meta: 'list, commit',
    data: '8 MiB blocks',
  },
  'zh-CN': {
    computer: '你的电脑',
    computerSub: ['访达、编辑器和 agent', '像用普通文件夹一样用它'],
    modalfs: 'modalfs',
    modalfsSub: ['通过 NFS 或 FUSE 提供文件夹，', '在本机磁盘上保存缓存和日志'],
    modal: 'Modal',
    api: 'Volume API',
    blocks: '块存储',
    fn: '你的 Function',
    files: '文件',
    meta: '列目录、提交',
    data: '8 MiB 的块',
  },
}

type Words = (typeof WORDS)['en']
type Pt = [number, number]

const C = Math.cos(Math.PI / 6)

/** A point in the isometric world (x right-down, y left-down, z up), seen from an anchor on the page. */
const iso =
  (ax: number, ay: number) =>
  (x: number, y: number, z = 0): Pt => [ax + (x - y) * C, ay + (x + y) / 2 - z]

const path = (pts: Pt[], close = true) => 'M' + pts.map(([x, y]) => `${x.toFixed(1)} ${y.toFixed(1)}`).join(' L') + (close ? ' Z' : '')

/** A box with its three visible faces: top, front-left (+y) and front-right (+x). */
function box(p: ReturnType<typeof iso>, x: number, y: number, z: number, w: number, d: number, h: number, cls = '') {
  const top = [p(x, y, z + h), p(x + w, y, z + h), p(x + w, y + d, z + h), p(x, y + d, z + h)]
  const left = [p(x, y + d, z), p(x + w, y + d, z), p(x + w, y + d, z + h), p(x, y + d, z + h)]
  const right = [p(x + w, y, z), p(x + w, y + d, z), p(x + w, y + d, z + h), p(x + w, y, z + h)]
  return `<g class="box ${cls}"><path class="top" d="${path(top)}"/><path class="left" d="${path(left)}"/><path class="right" d="${path(right)}"/></g>`
}

/** An upright cylinder: a disk on the floor of radius r at (x, y). */
function cylinder(p: ReturnType<typeof iso>, x: number, y: number, r: number, h: number) {
  const [cx, cy] = p(x, y, 0)
  const rx = Math.SQRT2 * r * C
  const ry = (Math.SQRT2 * r) / 2
  const side = `M${cx - rx} ${cy - h} L${cx - rx} ${cy} A${rx} ${ry} 0 0 0 ${cx + rx} ${cy} L${cx + rx} ${cy - h}`
  const ring = (z: number) => `M${cx - rx} ${cy - z} A${rx} ${ry} 0 0 0 ${cx + rx} ${cy - z}`
  return `<g class="box"><path class="left" d="${side} Z"/>${[h / 3, (2 * h) / 3].map((z) => `<path class="line" d="${ring(z)}"/>`).join('')}<ellipse class="top" cx="${cx}" cy="${cy - h}" rx="${rx}" ry="${ry}"/></g>`
}

function text(x: number, y: number, s: string, cls: string, anchor = 'middle') {
  return `<text x="${x}" y="${y}" class="${cls}" text-anchor="${anchor}">${s}</text>`
}

/** A labelled arrow along the page, with heads where the data goes. */
function arrow(x1: number, x2: number, y: number, label: string, both = true) {
  const head = (x: number, dir: number) => `<path class="arrow" d="M${x - 7 * dir} ${y - 4.5} L${x} ${y} L${x - 7 * dir} ${y + 4.5}"/>`
  return `<g><path class="arrow" d="M${x1} ${y} L${x2} ${y}"/>${head(x2, 1)}${both ? head(x1, -1) : ''}${text((x1 + x2) / 2, y - 9, label, 'small')}</g>`
}

function draw(w: Words) {
  const out: string[] = []

  // Your computer: a laptop with a Finder window on its screen.
  const lap = iso(150, 250)
  out.push(box(lap, 0, 0, 0, 120, 84, 6))
  out.push(box(lap, 0, 0, 6, 120, 5, 82))
  const scr = (a: number, b: number): Pt => lap(a, 5, 6 + b)
  out.push(`<path class="line" d="${path([scr(10, 72), scr(110, 72), scr(110, 12), scr(10, 12)])}"/>`)
  out.push(`<path class="line" d="${path([scr(10, 62), scr(110, 62)], false)}"/>`)
  for (const [i, j] of [[0, 0], [1, 0], [2, 0], [0, 1], [1, 1]]) {
    const a = 22 + i * 30
    const b = 46 - j * 24
    out.push(`<path class="accent" d="${path([scr(a, b), scr(a + 7, b), scr(a + 9, b - 2), scr(a + 18, b - 2), scr(a + 18, b - 14), scr(a, b - 14)])}"/>`)
  }
  const kb = (a: number, b: number): Pt => lap(a, b, 6)
  out.push(`<path class="line" d="${path([kb(14, 16), kb(106, 16), kb(106, 50), kb(14, 50)])}"/>`)
  for (const b of [24.5, 33, 41.5]) out.push(`<path class="line" d="${path([kb(14, b), kb(106, b)], false)}"/>`)
  out.push(`<path class="line" d="${path([kb(44, 57), kb(76, 57), kb(76, 77), kb(44, 77)])}"/>`)

  // modalfs: the daemon, and its cache and journal on disk.
  const mfs = iso(470, 236)
  out.push(box(mfs, 0, 0, 0, 70, 70, 62, 'accent-box'))
  out.push(box(mfs, 0, 0, 62, 70, 70, 4, 'accent-box'))
  for (const z of [16, 30, 44]) out.push(`<path class="line" d="${path([mfs(70, 12, z), mfs(70, 58, z)], false)}"/>`)
  out.push(cylinder(mfs, 118, 36, 22, 40))

  // Modal: the Volume API, the block store, and Functions that mount the same volume.
  const mdl = iso(800, 150)
  out.push(`<path class="platform" d="${path([mdl(0, 0), mdl(300, 0), mdl(300, 90), mdl(0, 90)])}"/>`)
  out.push(box(mdl, 10, 20, 0, 45, 45, 40))
  const cubes: [number, number, number][] = []
  for (let i = 0; i < 3; i++) for (let j = 0; j < 3; j++) for (let k = 0; k < (i + j <= 1 ? 2 : 1); k++) cubes.push([i, j, k])
  cubes.sort((a, b) => a[0] + a[1] - b[0] - b[1] || a[2] - b[2])
  for (const [i, j, k] of cubes) out.push(box(mdl, 118 + i * 30, 10 + j * 30, k * 26, 26, 26, 26, 'block'))
  out.push(box(mdl, 248, 28, 0, 42, 42, 34))
  const fn = mdl(269, 49, 34)
  out.push(`<text x="${fn[0]}" y="${fn[1] + 6}" class="glyph" text-anchor="middle">ƒ</text>`)

  // Labels and arrows.
  const under = (x: number, title: string, sub: string[]) => [text(x, 384, title, 'title'), ...sub.map((s, i) => text(x, 406 + i * 18, s, 'sub'))]
  out.push(...under(205, w.computer, w.computerSub), ...under(500, w.modalfs, w.modalfsSub), ...under(890, w.modal, []))
  const api = mdl(32, 42, 40)
  out.push(text(api[0], api[1] - 30, w.api, 'small'))
  const blocks = mdl(190, 98, 0)
  out.push(text(blocks[0] - 10, blocks[1] + 26, w.blocks, 'small'))
  out.push(text(1040, 352, w.fn, 'small'))
  out.push(arrow(300, 410, 250, w.files))
  out.push(arrow(615, 748, 180, w.meta, false))
  out.push(arrow(615, 805, 236, w.data))
  return out.join('\n')
}

const STYLE = `
  svg { --ink: #1f2430; --muted: #5b6272; --accent: #3478f6; --top: #ffffff; --left: #f1f3f7; --right: #e3e7ee; --accent-top: #eef4ff; --accent-left: #dbe7fd; --accent-right: #c9dbfb; }
  @media (prefers-color-scheme: dark) {
    svg { --ink: #d9dde6; --muted: #9aa1b0; --accent: #6ea2ff; --top: #2a2f3a; --left: #22262f; --right: #1b1f27; --accent-top: #23324d; --accent-left: #1d2a42; --accent-right: #182337; }
  }
  .box path, .box ellipse { stroke: var(--ink); stroke-width: 1.3; stroke-linejoin: round; }
  .box .top { fill: var(--top); } .box .left { fill: var(--left); } .box .right { fill: var(--right); }
  .accent-box .top { fill: var(--accent-top); } .accent-box .left { fill: var(--accent-left); } .accent-box .right { fill: var(--accent-right); }
  .accent-box path { stroke: var(--accent); }
  .block path { stroke-width: 1.1; }
  .line { fill: none; stroke: var(--ink); stroke-width: 1.1; stroke-linecap: round; }
  .accent { fill: none; stroke: var(--accent); stroke-width: 1.2; stroke-linejoin: round; }
  .platform { fill: none; stroke: var(--muted); stroke-width: 1.2; stroke-dasharray: 5 5; }
  .arrow { fill: none; stroke: var(--accent); stroke-width: 1.6; stroke-linecap: round; stroke-linejoin: round; }
  text { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC", "Noto Sans CJK SC", "Microsoft YaHei", Helvetica, Arial, sans-serif; fill: var(--ink); }
  .title { font-size: 17px; font-weight: 600; }
  .sub { font-size: 13px; fill: var(--muted); }
  .small { font-size: 12.5px; fill: var(--muted); }
  .glyph { font-size: 20px; font-style: italic; fill: var(--accent); }
`

const out = join(import.meta.dirname, '../../docs/diagrams')
for (const [lang, w] of Object.entries(WORDS)) {
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 92 1100 348" width="1100" height="348" role="img" aria-label="${w.computer} → ${w.modalfs} → ${w.modal}">
<style>${STYLE}</style>
${draw(w)}
</svg>
`
  writeFileSync(join(out, `how-it-works.${lang}.svg`), svg)
}
console.log('docs/diagrams: ' + Object.keys(WORDS).map((l) => `how-it-works.${l}.svg`).join(', '))
