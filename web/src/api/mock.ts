// An in-memory stand-in for the daemon, for `bun run dev:mock` and the README's
// screenshots. It answers the same routes as internal/daemon/api.go with sample
// data that belongs to no one, and actions change it the way the daemon would
// (mounting takes a moment, deleting a mounted volume is refused).
//
// URL parameters set what the screenshots need: ?lang=en|zh-Hant|zh-Hans and
// ?theme=light|dark.

import { isTorrent, warpByDefault } from '@/lib/links'
import type { Account, Billing, Conflict, DaemonEvent, Download, DownloadEvent, Environment, Identity, Mount, PendingFile, Profile, Sample, State, TrafficSample, Usage, Volume } from './types'

const HOME = '/Users/you'
const ROOT = `${HOME}/ModalVolumes`
const MiB = 1 << 20
const GiB = 1 << 30
const now = () => Math.floor(Date.now() / 1000)
const params = new URLSearchParams(location.search)

const accounts: Account[] = [
  {
    id: 'acc-research',
    label: 'research',
    token_id: 'ak-r3s3arch0000000000',
    workspace: 'acme-research',
    username: 'sam',
    secret_store: 'keychain',
    added_at: now() - 86400 * 40,
  },
  {
    id: 'acc-personal',
    label: 'personal',
    token_id: 'ak-p3rs0nal000000000',
    workspace: 'sam-dev',
    username: 'sam',
    imported_from: 'default',
    secret_store: 'keychain',
    added_at: now() - 86400 * 12,
  },
]

/**
 * A GitHub-style identicon for the sample accounts (Modal's member list often
 * has a GitHub picture); accounts added in the mock have none, so they show
 * the initial instead.
 */
function identicon(id: string): string {
  if (!accounts.slice(0, 2).some((a) => a.id === id)) return ''
  let h = 2166136261
  for (const c of id) h = Math.imul(h ^ c.charCodeAt(0), 16777619) >>> 0
  const hue = h % 360
  let cells = ''
  for (let y = 0; y < 5; y++)
    for (let x = 0; x < 3; x++) {
      h = Math.imul(h ^ (x * 7 + y), 16777619) >>> 0
      if (h % 2) for (const cx of x === 2 ? [2] : [x, 4 - x]) cells += `<rect x="${cx + 1}" y="${y + 1}" width="1" height="1"/>`
    }
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 7 7"><rect width="7" height="7" fill="#f0f0f0"/><g fill="hsl(${hue} 55% 55%)">${cells}</g></svg>`
  return 'data:image/svg+xml;base64,' + btoa(svg)
}

const status = (o: Partial<NonNullable<Mount['status']>> = {}): NonNullable<Mount['status']> => ({
  pending_files: 0,
  pending_bytes: 0,
  bytes_down: 0,
  bytes_up: 0,
  commits: 0,
  conflicts: 0,
  errors: 0,
  local_only_files: 0,
  ...o,
})

const mounts: Mount[] = [
  {
    id: 'm-datasets',
    account_id: 'acc-research',
    account_label: 'research',
    workspace: 'acme-research',
    environment: 'main',
    volume_name: 'datasets',
    volume_id: 'vo-0000000000datasets',
    mountpoint: `${ROOT}/datasets`,
    mode: 'auto',
    active_mode: 'nfs',
    auto_mount: true,
    filter_junk: true,
    on_conflict: 'keep-both',
    state: 'mounted',
    started_at: now() - 3600 * 5,
    status: status({ bytes_down: 3.4 * GiB, bytes_up: 182 * MiB, commits: 14, last_commit_at: now() - 140, local_only_files: 3 }),
    size_bytes: 214.3 * GiB,
    size_at: now() - 2 * 3600,
  },
  {
    id: 'm-checkpoints',
    account_id: 'acc-research',
    account_label: 'research',
    workspace: 'acme-research',
    environment: 'main',
    volume_name: 'checkpoints',
    volume_id: 'vo-00000checkpoints',
    mountpoint: `${ROOT}/checkpoints`,
    mode: 'auto',
    active_mode: 'nfs',
    filter_junk: true,
    on_conflict: 'keep-both',
    state: 'mounted',
    started_at: now() - 1800,
    status: status({ pending_files: 2, pending_bytes: 1.2 * GiB, bytes_down: 812 * MiB, bytes_up: 1.9 * GiB, commits: 6, conflicts: 1, last_commit_at: now() - 30 }),
    open_conflicts: 1,
    size_bytes: 158 * GiB,
    size_at: now() - 26 * 3600,
  },
  {
    id: 'm-notebooks',
    account_id: 'acc-personal',
    account_label: 'personal',
    workspace: 'sam-dev',
    volume_name: 'notebooks',
    volume_id: 'vo-000000notebooks',
    mountpoint: `${ROOT}/notebooks`,
    mode: 'auto',
    filter_junk: true,
    auto_mount: true,
    state: 'stopped',
    last_used: now() - 3 * 86400,
    size_bytes: 9.2 * GiB,
    size_at: now() - 5 * 86400,
  },
  {
    id: 'm-models',
    account_id: 'acc-research',
    account_label: 'research',
    workspace: 'acme-research',
    environment: 'main',
    volume_name: 'models',
    volume_id: 'vo-000000000models',
    mountpoint: `${ROOT}/models`,
    mode: 'auto',
    active_mode: 'nfs',
    filter_junk: true,
    on_conflict: 'keep-both',
    state: 'mounted',
    started_at: now() - 7200,
    status: status({ bytes_down: 22.1 * GiB, bytes_up: 0, commits: 0 }),
    unmount_error: 'unmount: Resource busy',
    in_use_by: ['Terminal', 'Preview'],
    size_bytes: 31 * GiB,
    size_at: now() - 3600,
  },
  {
    id: 'm-evals',
    account_id: 'acc-research',
    account_label: 'research',
    workspace: 'acme-research',
    environment: 'main',
    volume_name: 'eval-runs',
    volume_id: 'vo-0000000evalruns',
    mountpoint: `${ROOT}/eval-runs`,
    mode: 'auto',
    filter_junk: true,
    auto_mount: true,
    state: 'error',
    error: "couldn't reach Modal: connection refused. Is this Mac online?",
    retry_at: now() + 95,
  },
]

const volumes: Record<string, Volume[]> = {
  'acc-research': [
    { id: 'vo-0000000000datasets', name: 'datasets', format: 'v2', mounted: true, mount_id: 'm-datasets', created_at: now() - 86400 * 90, created_by: 'sam' },
    { id: 'vo-00000checkpoints', name: 'checkpoints', format: 'v2', mounted: true, mount_id: 'm-checkpoints', created_at: now() - 86400 * 31, created_by: 'sam' },
    { id: 'vo-0000000evalruns', name: 'eval-runs', format: 'v2', mounted: false, created_at: now() - 86400 * 9, created_by: 'alex' },
    { id: 'vo-00000000hfcache', name: 'hf-cache', format: 'v2', mounted: false, created_at: now() - 86400 * 120, created_by: 'alex' },
    { id: 'vo-000legacyweights', name: 'legacy-weights', format: 'v1', mounted: false, created_at: now() - 86400 * 400, created_by: 'sam' },
  ],
  'acc-personal': [
    { id: 'vo-000000notebooks', name: 'notebooks', format: 'v2', mounted: false, mount_id: 'm-notebooks', created_at: now() - 86400 * 20, created_by: 'sam' },
    { id: 'vo-00000000scratch', name: 'scratch', format: 'v2', mounted: false, created_at: now() - 86400 * 2, created_by: 'sam' },
  ],
}

// A workspace shared by many projects: the add dialog has to find one among them.
{
  const words = ['bert', 'clip', 'diffusion', 'eval', 'features', 'gpt', 'index', 'logs', 'lora', 'mmlu', 'panels', 'rl', 'sae', 'shards', 'speech', 'tokens', 'video', 'whisper']
  const people = ['alex', 'jo', 'kim', 'priya', 'sam']
  for (let i = 0; volumes['acc-research'].length < 150; i++) {
    const name = `${words[i % words.length]}-${['cache', 'runs', 'data', 'ckpt', 'tmp'][i % 5]}-${String(i).padStart(3, '0')}`
    volumes['acc-research'].push({ id: `vo-gen${String(i).padStart(12, '0')}`, name, format: i % 37 === 5 ? 'v1' : 'v2', mounted: false, created_at: now() - 86400 * (2 + ((i * 7) % 300)), created_by: people[i % people.length] })
  }
  volumes['acc-research'].push({ id: 'vo-000000000models', name: 'models', format: 'v2', mounted: true, created_at: now() - 86400 * 61, created_by: 'jo' })
}

const environments: Record<string, Environment[]> = {
  'acc-research': [
    { name: 'main', default: true },
    { name: 'staging', default: false },
  ],
  'acc-personal': [{ name: 'main', default: true }],
}

const lang = params.get('lang')
const state: State = {
  version: '0.1.0',
  platform: 'darwin/arm64',
  accounts,
  mounts,
  settings: {
    cache_size_gb: 20,
    read_ahead: 8,
    language: lang === 'en' || lang === 'zh-Hant' || lang === 'zh-Hans' ? lang : 'system',
    appearance: params.get('theme') === 'dark' ? 'dark' : params.get('theme') === 'light' ? 'light' : 'system',
    mount_root: ROOT,
    remote_downloads: params.get('downloads') !== 'off',
  },
  cache: { blocks: 612, bytes: 4.6 * GiB, max: 20 * GiB },
  service: { supported: true, installed: true },
  last_seq: 0,
  boot_id: 'mock',
  config_dir: `${HOME}/Library/Application Support/modalfs`,
}

const events: DaemonEvent[] = []
function emit(kind: DaemonEvent['kind'], volume: string, code: string, args: string[] = [], at = now()) {
  state.last_seq++
  events.push({ seq: state.last_seq, at, kind, volume, message: code, code, args })
}
emit('info', '', 'started', [], now() - 3600 * 5)
emit('mounted', 'datasets', 'mounted', [`${ROOT}/datasets`], now() - 3600 * 5)
emit('recovered', 'datasets', 'recovered', ['train/shard-0007.parquet'], now() - 3600 * 5 + 2)
emit('commit', 'datasets', 'commit', ['train/shard-0007.parquet'], now() - 3600 * 5 + 9)
emit('mounted', 'checkpoints', 'mounted', [`${ROOT}/checkpoints`], now() - 1800)
emit('commit', 'checkpoints', 'commit', ['run-42/step-1000.safetensors'], now() - 1500)
emit('conflict', 'checkpoints', 'conflict', ['run-42/config.yaml', 'run-42/config.conflict-20261012-101533.yaml'], now() - 900)
emit('commit', 'datasets', 'commit', ['eval/notes.md'], now() - 140)
emit('commit', 'checkpoints', 'commit', ['run-42/step-2000.safetensors'], now() - 30)

const profiles: Profile[] = [
  { name: 'default', token_id: 'ak-p3rs0nal000000000', active: true, imported: true },
  { name: 'work', token_id: 'ak-w0rk0000000000000', active: false, imported: false },
]

class MockError extends Error {
  status = 400
}
const fail = (msg: string): never => {
  throw new MockError(msg)
}
const ok = { ok: true }
const wait = (ms: number) => new Promise((r) => setTimeout(r, ms))
const clone = <T>(v: T): T => JSON.parse(JSON.stringify(v))

function findMount(id: string) {
  return mounts.find((m) => m.id === id) ?? fail('mount not found')
}

function setState(m: Mount, next: Mount['state'], after: number) {
  setTimeout(() => {
    m.state = next
    if (next === 'mounted') {
      m.active_mode = m.mode === 'fuse' ? 'fuse' : 'nfs'
      m.started_at = now()
      m.status = status()
      emit('mounted', m.volume_name, 'mounted', [m.mountpoint])
    } else if (next === 'stopped') {
      m.status = undefined
      m.active_mode = undefined
      emit('unmounted', m.volume_name, 'unmounted', [m.mountpoint])
    }
  }, after)
}

/** Unmounting waits for the pending uploads: about 24 MB/s, so the progress shows for a while. */
function uploadThenStop(m: Mount) {
  const s = m.status!
  m.stopping_since = now()
  m.stop_bytes_up = s.bytes_up
  m.stop_pending_bytes = s.pending_bytes
  const timer = setInterval(() => {
    if (m.state !== 'stopping') return clearInterval(timer)
    const step = Math.min(s.pending_bytes, 24 * MiB * (0.8 + Math.random() * 0.4))
    s.bytes_up += step
    s.pending_bytes -= step
    if (s.pending_bytes <= 0) {
      clearInterval(timer)
      s.commits += s.pending_files
      s.pending_files = 0
      setState(m, 'stopped', 600)
    }
  }, 1000)
}

function syncVolumeFlags() {
  for (const [acct, vs] of Object.entries(volumes)) {
    for (const v of vs) {
      const m = mounts.find((x) => x.account_id === acct && x.volume_id === v.id)
      v.mount_id = m?.id
      v.mounted = m?.state === 'mounted'
    }
  }
}

type Body = Record<string, unknown>

// ─── Remote downloads ───
//
// A few downloads in every state the page shows, with speed histories that
// look like real ones (a ramp up, waves, dips) and the events that go with
// them. The running ones keep going: four samples a second, as the daemon
// relays them from the container.

/** A seeded random number generator (mulberry32), so the histories look the same in every screenshot. */
function rng(seed: number) {
  return () => {
    seed |= 0
    seed = (seed + 0x6d2b79f5) | 0
    let x = Math.imul(seed ^ (seed >>> 15), 1 | seed)
    x = (x + Math.imul(x ^ (x >>> 7), 61 | x)) ^ x
    return ((x ^ (x >>> 14)) >>> 0) / 4294967296
  }
}

/** Bytes per second at `s` seconds into a run averaging `mean`: a ramp up, slow waves, quick wobble and noise. */
function profile(mean: number, seed: number) {
  const r = rng(seed)
  const phase = r() * 100
  return (s: number) => {
    const ramp = Math.min(1, s / 4)
    const wave = 1 + 0.16 * Math.sin((s + phase) / 23) + 0.06 * Math.sin((s + phase) / 3.7)
    return Math.max(0, mean * ramp * wave * (0.95 + r() * 0.1))
  }
}

interface MockDownload {
  d: Download
  events: DownloadEvent[]
  /** The durable history, one sample a second (or coarser for long runs). */
  history: Sample[]
  /** While running: this run's samples, then four a second. */
  live?: Sample[]
  speedAt?: (s: number) => number
  runStart?: number
  log: string
}

const mockDownloads: MockDownload[] = []

/** Samples of a run from `from` to `to`, every `step` seconds, starting at `done` bytes; returns the bytes done at the end. */
function run(rows: Sample[], from: number, to: number, done: number, speedAt: (s: number) => number, step = 1) {
  for (let t = from; t <= to; t += step) {
    const v = speedAt(t - from)
    done += v * step
    rows.push([t, Math.round(done), Math.round(v)])
  }
  return done
}

function addMockDownload(o: { id: string; url: string; name: string; tool: Download['tool']; folder: string; volume: string; created: number; state: Download['state'] }) {
  const d: Download = {
    id: o.id,
    account_id: 'acc-research',
    account_label: 'research',
    environment: 'main',
    volume_id: volumes['acc-research'].find((v) => v.name === o.volume)!.id,
    volume_name: o.volume,
    folder: o.folder,
    url: o.url,
    name: o.name,
    tool: o.tool,
    size: 'small',
    created: o.created,
    state: o.state,
  }
  const m: MockDownload = { d, events: [], history: [], log: '' }
  mockDownloads.push(m)
  return m
}

{
  const t0 = now()
  const GB = 1e9
  const status = (m: MockDownload, o: Partial<NonNullable<Download['status']>>) => {
    m.d.status = { state: m.d.state, tool: m.d.tool, url: m.d.url, name: m.d.name, done: 0, total: -1, speed: 0, t: now(), ...o }
  }

  // Running, in its second attempt: Modal stopped the first container (a preemption) 34 s before it went on.
  {
    const m = addMockDownload({ id: 'dl-train', url: 'https://data.example.org/vision/train-2024.tar', name: 'train-2024.tar', tool: 'gopeed', folder: 'raw', volume: 'datasets', created: t0 - 905, state: 'running' })
    const a = profile(40e6, 1)
    const b = profile(42e6, 2)
    const s1 = t0 - 900
    const e1 = t0 - 560
    let done = run(m.history, s1, e1, 0, a)
    const saved = done
    const s2 = e1 + 34
    m.events.push({ ev: 'start', t: s1, attempt: 1 }, { ev: 'signal', t: e1 + 0.4, done: saved }, { ev: 'start', t: s2, attempt: 2 }, { ev: 'resumed', t: s2 + 0.1, gap: 34, done: saved })
    m.live = []
    done = run(m.live, s2, t0, done, b)
    m.speedAt = b
    m.runStart = s2
    status(m, { state: 'running', done, total: 48 * GB, speed: m.live[m.live.length - 1][2], attempt: 2 })
  }

  // Running, just started, with aria2.
  {
    const m = addMockDownload({ id: 'dl-coco', url: 'https://datasets.example.com/coco/train2017.zip', name: 'train2017.zip', tool: 'aria2', folder: 'coco', volume: 'datasets', created: t0 - 97, state: 'running' })
    const a = profile(68e6, 3)
    m.events.push({ ev: 'start', t: t0 - 95, attempt: 1 })
    m.live = []
    const done = run(m.live, t0 - 95, t0, 0, a)
    m.speedAt = a
    m.runStart = t0 - 95
    status(m, { state: 'running', done, total: 18.1 * GB, speed: m.live[m.live.length - 1][2], attempt: 1 })
  }

  // Running through Cloudflare WARP: a YouTube video (Blender's Big Buck Bunny), slower than direct.
  {
    const m = addMockDownload({ id: 'dl-bbb', url: 'https://www.youtube.com/watch?v=aqz-KE-bpKQ', name: 'Big Buck Bunny 60fps 4K - Official Blender Foundation Short Film [aqz-KE-bpKQ].webm', tool: 'yt-dlp', folder: 'video', volume: 'datasets', created: t0 - 64, state: 'running' })
    m.d.warp = true
    const a = profile(16e6, 11)
    m.events.push({ ev: 'start', t: t0 - 62, attempt: 1 }, { ev: 'warp', t: t0 - 57, colo: 'IAD', ip: '2a09:bac1:76a0::3e:12', leak_check: { tcp: 'blocked', udp: 'blocked', dns: 'blocked' } })
    m.live = []
    const done = run(m.live, t0 - 55, t0, 0, a)
    m.speedAt = a
    m.runStart = t0 - 55
    status(m, { state: 'running', done, total: 2.1 * GB, speed: m.live[m.live.length - 1][2], attempt: 1, warp: { colo: 'IAD', ip: '2a09:bac1:76a0::3e:12' } })
  }

  // Paused by the user.
  {
    const m = addMockDownload({ id: 'dl-weights', url: 'https://files.example.net/models/weights-7b.safetensors', name: 'weights-7b.safetensors', tool: 'gopeed', folder: 'models', volume: 'checkpoints', created: t0 - 7300, state: 'paused' })
    const s1 = t0 - 7200
    const done = run(m.history, s1, s1 + 180, 0, profile(31e6, 4))
    m.events.push({ ev: 'start', t: s1, attempt: 1 }, { ev: 'paused', t: s1 + 181, done })
    status(m, { state: 'paused', done, total: 13.5 * GB, attempt: 1, t: s1 + 182 })
  }

  // Done after more than a day: a slow server, and the Function's 24-hour limit in the middle.
  {
    const m = addMockDownload({ id: 'dl-crawl', url: 'https://archive.example.org/crawl/segment-0042.warc.gz', name: 'segment-0042.warc.gz', tool: 'gopeed', folder: 'crawl', volume: 'datasets', created: t0 - 30 * 3600 - 60, state: 'done' })
    const s1 = t0 - 30 * 3600
    const e1 = s1 + 24 * 3600 - 600
    const slow = profile(1.05e6, 5)
    let done = run(m.history, s1, e1, 0, slow, 2)
    m.events.push({ ev: 'start', t: s1, attempt: 1 }, { ev: 'continued', t: e1 + 1, call: 'fc-0000next' }, { ev: 'start', t: e1 + 9, attempt: 2 })
    done = run(m.history, e1 + 9, t0 - 3 * 3600, done, profile(1.1e6, 6), 2)
    const path = 'crawl/segment-0042.warc.gz'
    m.events.push({ ev: 'finished', t: t0 - 3 * 3600 + 2, files: [{ path, size: Math.round(done) }] })
    status(m, { state: 'done', done, total: done, attempt: 2, t: t0 - 3 * 3600 + 3, files: [{ path, size: Math.round(done), sha256: '9f1c2e4b7a0d33c58e6f1a2b4c6d8e0f1a3b5c7d9e1f2a4b6c8d0e2f4a6b8c0d' }] })
  }

  // Done: gopeed kept failing on this server, so aria2 took over and started the file over.
  {
    const m = addMockDownload({ id: 'dl-bin', url: 'https://cdn.example.com/test/10GB.bin', name: '10GB.bin', tool: 'gopeed', folder: 'scratch', volume: 'eval-runs', created: t0 - 5 * 3600, state: 'done' })
    const s1 = t0 - 5 * 3600 + 5
    run(m.history, s1, s1 + 14, 0, profile(55e6, 7))
    for (let i = 0; i < 26; i++) m.history.push([s1 + 15 + i, m.history[m.history.length - 1][1], 0])
    const msg = 'connection 3 failed: server ignored HTTP range request: expected 206 Partial Content, got 200 OK'
    m.events.push({ ev: 'start', t: s1, attempt: 1 })
    for (let i = 1; i <= 6; i++) m.events.push({ ev: 'error', t: s1 + 14 + i * 4, tool: 'gopeed', message: msg, retry: i })
    m.events.push({ ev: 'fallback', t: s1 + 41, tool: 'aria2', reason: msg })
    const done = run(m.history, s1 + 42, s1 + 42 + 125, 0, profile(80e6, 8))
    const path = 'scratch/10GB.bin'
    m.events.push({ ev: 'finished', t: s1 + 170, files: [{ path, size: 10e9 }] })
    status(m, { state: 'done', tool: 'aria2', done: Math.min(done, 10e9), total: 10e9, attempt: 1, t: s1 + 171, files: [{ path, size: 10e9, sha256: 'e5b844cc57f57094ea4585e235f36c78c1cd222262bb89d53c94dcb4d6b3e55d' }] })
    m.log = `--- tools.log\n[gopeed] task created\n[gopeed] ${msg}\n[agent] continuing (1 of 5)\n[gopeed] ${msg}\n[agent] continuing (2 of 5)\n[gopeed] ${msg}\n[agent] falling back to aria2\n[aria2] Download complete: /vol/.modalfs/downloads/dl-bin/work/10GB.bin\n`
  }

  // Failed: the link is gone.
  {
    const m = addMockDownload({ id: 'dl-tool', url: 'https://downloads.example.org/releases/tool-1.4.2.tar.zst', name: 'tool-1.4.2.tar.zst', tool: 'gopeed', folder: '', volume: 'hf-cache', created: t0 - 26 * 3600, state: 'failed' })
    const s1 = t0 - 26 * 3600 + 4
    m.events.push({ ev: 'start', t: s1, attempt: 1 }, { ev: 'failed', t: s1 + 2, message: 'HTTP 404 Not Found' })
    m.d.error = 'HTTP 404 Not Found'
    status(m, { state: 'failed', error: 'HTTP 404 Not Found', attempt: 1, t: s1 + 2, log_tail: ['resolve https://downloads.example.org/releases/tool-1.4.2.tar.zst', 'error="HTTP 404 Not Found"'] })
  }
}

// Modal wouldn't start these. The first is what a workspace over its spend
// limit showed (Modal gave no reason); the second's message is made up, as
// Modal's own wording for a spend limit hasn't been seen yet.
{
  const t0 = now()
  const iso = addMockDownload({ id: 'dl-iso', url: 'https://releases.example.com/26.04/desktop-amd64.iso', name: 'desktop-amd64.iso', tool: 'gopeed', folder: 'scratch', volume: 'eval-runs', created: t0 - 1800, state: 'failed' })
  iso.d.error = 'preparing the downloader: starting the deploy Sandbox: RemoteError: Image build for im-AmHP9P7tCZr2GkFtM90o5A terminated due to external shut-down. Please try again.'
  iso.d.error_kind = 'unexplained'
  const ck = addMockDownload({ id: 'dl-ckpt', url: 'https://files.example.net/models/ckpt-step-90000.pt', name: 'ckpt-step-90000.pt', tool: 'aria2', folder: 'models', volume: 'checkpoints', created: t0 - 4000, state: 'failed' })
  ck.d.error = 'RemoteError: the workspace has reached its spend limit'
  ck.d.error_kind = 'billing'
  const s1 = t0 - 3950
  const done = run(ck.history, s1, s1 + 60, 0, profile(50e6, 9))
  ck.events.push({ ev: 'start', t: s1, attempt: 1 })
  ck.d.status = { state: 'running', tool: 'aria2', url: ck.d.url, name: ck.d.name, done, total: 14.2e9, speed: 0, attempt: 1, t: s1 + 60 }
  // To go through Cloudflare WARP, which couldn't be reached: nothing was downloaded directly instead.
  const w = addMockDownload({ id: 'dl-warpfail', url: 'https://www.youtube.com/watch?v=eRsGyueVLvQ', name: 'Sintel [eRsGyueVLvQ]', tool: 'yt-dlp', folder: 'video', volume: 'datasets', created: t0 - 2400, state: 'failed' })
  w.d.warp = true
  w.d.error = "Cloudflare WARP: couldn't connect (nothing came back through the tunnel in 40 s (last: context deadline exceeded))"
  w.d.error_kind = 'warp'
  w.events.push({ ev: 'start', t: t0 - 2390, attempt: 1 }, { ev: 'failed', t: t0 - 2340, message: w.d.error })
  // Running as far as the daemon knows, but nothing heard from its container for a while.
  const q = addMockDownload({ id: 'dl-quiet', url: 'https://data.example.org/audio/librispeech-960h.tar', name: 'librispeech-960h.tar', tool: 'gopeed', folder: 'audio', volume: 'datasets', created: t0 - 500, state: 'starting' })
  q.d.silent = 480
}

const billing = (account: string): Billing => {
  const start = new Date()
  start.setUTCDate(1)
  start.setUTCHours(0, 0, 0, 0)
  const end = new Date(start)
  end.setUTCMonth(end.getUTCMonth() + 1)
  return {
    period_start: start.getTime() / 1000,
    period_end: end.getTime() / 1000,
    metered: account === 'acc-research' ? 41.27 : 3.1,
    billed: account === 'acc-research' ? 11.27 : 0,
    adjustments: { Credits: account === 'acc-research' ? -30 : -3.1 },
    downloads: account === 'acc-research' ? 0.3812 : 0,
    hourly: { small: 0.0099, medium: 0.0198, large: 0.063 },
    usage_url: `https://modal.com/settings/${account === 'acc-research' ? 'research-lab' : 'sam'}/usage`,
    at: now(),
  }
}

/** The running downloads go on: a sample every 250 ms, as the agent takes them. */
let ticking = false
function tickDownloads() {
  if (ticking) return
  ticking = true
  setInterval(() => {
    const t = Date.now() / 1000
    for (const m of mockDownloads) {
      const s = m.d.status
      if (m.d.state !== 'running' || !s || !m.live || !m.speedAt) continue
      const last = m.live[m.live.length - 1]
      const v = m.speedAt(t - m.runStart!)
      const done = Math.min(s.total, last[1] + v * (t - last[0]))
      m.live.push([t, Math.round(done), Math.round(v)])
      Object.assign(s, { done, speed: Math.round(v), eta: v > 0 ? Math.round((s.total - done) / v) : undefined, t })
      if (done >= s.total) finishMock(m)
    }
  }, 250)
}

function finishMock(m: MockDownload) {
  const s = m.d.status!
  m.history.push(...(m.live ?? []).filter((r) => r[0] > (m.history[m.history.length - 1]?.[0] ?? 0)))
  m.live = undefined
  m.d.state = 'done'
  const path = `${m.d.folder ? m.d.folder + '/' : ''}${m.d.name}`
  Object.assign(s, { state: 'done', speed: 0, eta: undefined, files: [{ path, size: s.total }] })
  m.events.push({ ev: 'finished', t: now(), files: [{ path, size: s.total }] })
  emit('info', m.d.volume_name, 'dl_done', [m.d.name, `${m.d.volume_name}:/${m.d.folder}`])
}

/** Oldest first, as the daemon lists them. */
const listed = () => mockDownloads.map((x) => x.d).sort((a, b) => a.created - b.created)

function findDownload(id: string) {
  return mockDownloads.find((m) => m.d.id === id) ?? fail(`no download ${id}`)
}

const ENDED_MOCK = new Set(['done', 'paused', 'stalled', 'failed', 'time limit', 'cancelled'])

async function downloadsRoute(method: string, p: string, url: URL, body: Body): Promise<unknown> {
  tickDownloads()
  let m: RegExpMatchArray | null
  if (method === 'GET' && p === '/downloads') return clone(listed())
  if (method === 'POST' && p === '/downloads') {
    if (!state.settings.remote_downloads) fail('remote downloads are off; turn them on in Settings first')
    const urls = (body.urls as string[]) || []
    const added = urls.map((u, i) => {
      const name = u.replace(/[?#].*$/, '').split('/').filter(Boolean).pop() || 'download'
      const warp = body.warp === undefined ? warpByDefault(u) : !!body.warp
      if (warp && isTorrent(u)) fail(`${u} is a torrent: Cloudflare WARP can't carry BitTorrent (its peers are reached directly), so add it without WARP`)
      const x = addMockDownload({ id: `dl-new${mockDownloads.length}${i}`, url: u, name, tool: (body.tool as Download['tool']) || 'gopeed', folder: String(body.folder || ''), volume: String(body.volume), created: now(), state: 'preparing' })
      if (warp) x.d.warp = true
      setTimeout(() => startMock(x, 1), 1500)
      return clone(x.d)
    })
    return added
  }
  if ((m = p.match(/^\/downloads\/([^/]+)$/))) {
    const x = findDownload(m[1])
    if (method === 'DELETE') {
      if (!ENDED_MOCK.has(x.d.state)) fail(`download ${x.d.id} is ${x.d.state}; pause or cancel it first`)
      mockDownloads.splice(mockDownloads.indexOf(x), 1)
      return ok
    }
    const after = Number(url.searchParams.get('after') || 0)
    return clone({ download: x.d, events: x.events, history: x.history.filter((r) => r[0] > after) })
  }
  if ((m = p.match(/^\/downloads\/([^/]+)\/live$/))) {
    const x = findDownload(m[1])
    const after = Number(url.searchParams.get('after') || 0)
    if (!x.live) return { live: false, status: clone(x.d.status), samples: [] }
    return { live: true, status: clone(x.d.status), samples: x.live.filter((r) => r[0] > after) }
  }
  if ((m = p.match(/^\/downloads\/([^/]+)\/log$/))) {
    const x = findDownload(m[1])
    if (!x.log) fail("there's no log yet: it's saved when a run ends (the status shows the latest errors meanwhile)")
    return x.log
  }
  if ((m = p.match(/^\/downloads\/([^/]+)\/(pause|resume|cancel)$/))) {
    const x = findDownload(m[1])
    const s = x.d.status
    if (m[2] === 'resume') {
      if (!['paused', 'stalled', 'failed', 'time limit'].includes(x.d.state)) fail(`download ${x.d.id} is ${x.d.state} already`)
      x.d.state = 'preparing'
      x.d.error = undefined
      x.d.error_kind = undefined
      if (body.warp !== undefined) x.d.warp = !!body.warp
      setTimeout(() => startMock(x, (s?.attempt ?? 0) + 1), 1200)
    } else {
      if (ENDED_MOCK.has(x.d.state)) {
        if (m[2] === 'cancel') x.d.state = 'cancelled'
        else fail(`download ${x.d.id} isn't running (it's ${x.d.state})`)
        return clone(x.d)
      }
      x.d.state = m[2] === 'pause' ? 'pausing' : 'cancelling'
      setTimeout(() => {
        x.history.push(...(x.live ?? []).filter((r) => r[0] > (x.history[x.history.length - 1]?.[0] ?? 0)))
        x.live = undefined
        x.events.push({ ev: 'paused', t: now(), done: s?.done ?? 0 })
        x.d.state = m![2] === 'pause' ? 'paused' : 'cancelled'
        if (s) Object.assign(s, { state: x.d.state, speed: 0, eta: undefined })
        emit('info', x.d.volume_name, m![2] === 'pause' ? 'dl_paused' : 'dl_cancelled', [x.d.name])
      }, 1500)
    }
    return clone(x.d)
  }
  if ((m = p.match(/^\/accounts\/([^/]+)\/billing$/))) return billing(m[1])
  if ((m = p.match(/^\/accounts\/([^/]+)\/volumes\/([^/]+)\/folders$/))) {
    const path = url.searchParams.get('path') || ''
    const tree: Record<string, string[]> = { '': ['datasets', 'models', 'scratch'], datasets: ['coco', 'raw'], models: [] }
    if (!(path in tree)) fail('not found')
    return tree[path]
  }
  return undefined
}

function startMock(x: MockDownload, attempt: number) {
  const t = now()
  x.events.push({ ev: 'start', t, attempt })
  x.speedAt = profile(45e6, attempt * 13 + x.d.id.length)
  x.runStart = t
  const done = x.d.status?.done ?? 0
  x.live = [[t, done, 0]]
  x.d.state = 'running'
  x.d.status = { state: 'running', tool: x.d.tool, url: x.d.url, name: x.d.name, done, total: x.d.status?.total ?? 6.4e9, speed: 0, attempt, t }
  if (x.d.warp) {
    x.events.push({ ev: 'warp', t: t + 3, colo: 'IAD', ip: '2a09:bac1:76a0::3e:12' })
    x.d.status.warp = { colo: 'IAD', ip: '2a09:bac1:76a0::3e:12' }
  }
  emit('info', x.d.volume_name, 'dl_started', [x.d.name, `${x.d.volume_name}:/${x.d.folder}`])
}


// ─── What a volume's page shows ───

const usage: Record<string, Usage> = {}
const measuring: Record<string, { counted: number; files: number; started: number }> = {}
function folderSplit(total: number, seed: number): Usage['folders'] {
  const names = ['raw', 'imagenet', 'shards', 'eval', 'meta', 'tmp']
  const r = rng(seed)
  let left = total * 0.93
  return names.slice(0, 5).map((name, i) => {
    const bytes = i === 4 ? left * 0.4 : left * (0.35 + r() * 0.25)
    left -= bytes
    return { name, bytes: Math.round(bytes), files: Math.round(bytes / (40 * MiB)) + 3 }
  })
}
for (const mt of mounts) {
  if (mt.size_bytes && mt.volume_id) usage[mt.volume_id] = { total: mt.size_bytes, files: Math.round(mt.size_bytes / (31 * MiB)), folders: folderSplit(mt.size_bytes, mt.volume_id.length), at: mt.size_at ?? now() }
}
const conflictsOf: Record<string, Conflict[]> = {
  'm-checkpoints': [{ path: 'run-42/config.yaml', copy: 'run-42/config.conflict-20261012-101533.yaml', at: now() - 900 }],
}

// ?scenario=demo, for the README's pictures: an ordinary good day. Everything
// mounted is on Modal or on its way, and no download needs a decision. The
// trouble states above stay in `bun run dev:mock`, where they're needed.
if (params.get('scenario') === 'demo') {
  const drop = <T>(list: T[], gone: (x: T) => boolean) => {
    for (let i = list.length - 1; i >= 0; i--) if (gone(list[i])) list.splice(i, 1)
  }
  drop(mounts, (m) => m.state === 'error')
  for (const m of mounts) {
    delete m.unmount_error
    delete m.in_use_by
    m.open_conflicts = 0
    if (m.status) m.status.conflicts = 0
  }
  delete conflictsOf['m-checkpoints']
  drop(events, (e) => e.kind === 'conflict')
  drop(mockDownloads, (x) => x.d.state === 'failed' || x.d.state === 'starting')
}

function insight(method: string, mt: Mount, what: string, body: Body): unknown {
  const vid = mt.volume_id ?? ''
  if (what === 'usage') {
    if (method === 'POST' && !measuring[vid]) {
      measuring[vid] = { counted: 0, files: 0, started: now() }
      const total = usage[vid]?.total ?? 48 * GiB
      const timer = setInterval(() => {
        const x = measuring[vid]
        x.counted = Math.min(total, x.counted + total / 6)
        x.files = Math.round(x.counted / (31 * MiB))
        if (x.counted >= total) {
          clearInterval(timer)
          delete measuring[vid]
          usage[vid] = { total, files: x.files, folders: folderSplit(total, vid.length), at: now() }
          mt.size_bytes = total
          mt.size_at = now()
        }
      }, 700)
    }
    if (method === 'DELETE') delete measuring[vid]
    const job = measuring[vid]
    return clone({ last: usage[vid], running: !!job, counted: job?.counted, counted_files: job?.files, started: job?.started })
  }
  if (what === 'traffic') {
    if (mt.state !== 'mounted' && mt.state !== 'stopping') return []
    const out: TrafficSample[] = []
    const r = rng(mt.id.length * 31)
    const t0 = now() - 3600
    let up = 0
    let down = 0
    for (let t = t0; t <= now(); t += 10) {
      const k = (t - t0) / 3600
      up += Math.max(0, (k > 0.7 ? 9e6 : 0.6e6) * (0.4 + r()) * (Math.sin(k * 9) > 0.2 ? 1 : 0.15))
      down += Math.max(0, 4e6 * (0.3 + r()) * (Math.sin(k * 5 + 1) > -0.3 ? 1 : 0.1))
      out.push([t, Math.round(up), Math.round(down)])
    }
    return out
  }
  if (what === 'pending') {
    const n = mt.status?.pending_files ?? 0
    const total = mt.status?.pending_bytes ?? 0
    const files: PendingFile[] = []
    for (let i = 0; i < n; i++) files.push({ path: `run-42/step-${3000 + i * 1000}.safetensors`, bytes: Math.round(total / n), size: Math.round(total / n), since: now() - 60 * (n - i) })
    return files
  }
  if (what === 'conflicts') return clone(conflictsOf[mt.id] ?? [])
  if (what === 'conflicts/clear') {
    const copy = String(body.copy || '')
    conflictsOf[mt.id] = copy ? (conflictsOf[mt.id] ?? []).filter((c) => c.copy !== copy) : []
    mt.open_conflicts = conflictsOf[mt.id].length
    return ok
  }
  return undefined
}

// After a shutdown the mock stops answering, like the daemon does once it has quit.
let gone = false

export async function mockRequest(method: string, path: string, raw?: unknown): Promise<unknown> {
  await wait(method === 'GET' ? 60 : 280)
  if (gone) throw new TypeError('mock: modalfs has quit')
  const body = (raw ?? {}) as Body
  const url = new URL(path, 'http://mock')
  const p = url.pathname
  const route = `${method} ${p}`
  let m: RegExpMatchArray | null

  if (route === 'GET /ping') return ok
  if (route === 'POST /shutdown') {
    state.quitting = true
    for (const mt of mounts) if (mt.state === 'mounted' || mt.state === 'stopping') setState(mt, 'stopped', 300)
    setTimeout(() => (gone = true), 900)
    return ok
  }

  if (route === 'GET /state') {
    syncVolumeFlags()
    tickDownloads()
    return clone({ ...state, downloads: listed() })
  }
  if (p.startsWith('/downloads') || p.endsWith('/folders') || p.endsWith('/billing')) {
    const r = await downloadsRoute(method, p, url, body)
    if (r !== undefined) return r
  }
  if (route === 'GET /events') {
    const after = Number(url.searchParams.get('after') || 0)
    return clone(events.filter((e) => e.seq > after))
  }
  if (route === 'POST /session') return ok
  if (route === 'PATCH /settings') {
    const s = body as State['settings']
    for (const k of Object.keys(s) as (keyof State['settings'])[]) {
      if (s[k] !== undefined && s[k] !== '' && s[k] !== 0) (state.settings as Record<string, unknown>)[k] = s[k]
    }
    if (s.cache_size_gb) {
      // The daemon evicts down to a new, smaller limit right away.
      state.cache.max = s.cache_size_gb * GiB
      state.cache.bytes = Math.min(state.cache.bytes, state.cache.max)
    }
    if (s.transport === 'auto') state.settings.transport = ''
    return ok
  }
  if (route === 'POST /cache/clear') {
    state.cache.bytes = 0
    state.cache.blocks = 0
    return ok
  }
  if ((m = route.match(/^POST \/service\/(install|uninstall)$/))) {
    state.service.installed = m[1] === 'install'
    return clone(state.service)
  }
  if (route === 'POST /open') return ok

  // accounts
  if (route === 'POST /accounts') {
    const tokenId = String(body.tokenId || '').trim()
    if (!/^ak-/.test(tokenId) || !String(body.tokenSecret || '').startsWith('as-')) fail('invalid token: check the ID and secret')
    const a: Account = {
      id: `acc-${state.accounts.length + 1}`,
      label: String(body.label || '').trim() || 'new-workspace',
      token_id: tokenId,
      workspace: 'new-workspace',
      secret_store: 'keychain',
      added_at: now(),
    }
    accounts.push(a)
    volumes[a.id] = []
    environments[a.id] = [{ name: 'main', default: true }]
    emit('info', '', 'acct_added', [a.label, a.workspace!])
    return clone(a)
  }
  if (route === 'GET /import/profiles') return clone(profiles)
  if (route === 'POST /import/profiles') {
    const names = (body.names as string[]) || []
    const added: Account[] = []
    for (const n of names) {
      const pr = profiles.find((x) => x.name === n && !x.imported)
      if (!pr) continue
      pr.imported = true
      const a: Account = { id: `acc-${n}`, label: n, token_id: pr.token_id, workspace: `${n}-workspace`, imported_from: n, secret_store: 'keychain', added_at: now() }
      accounts.push(a)
      volumes[a.id] = []
      environments[a.id] = [{ name: 'main', default: true }]
      added.push(a)
    }
    return { added, errors: [] }
  }
  if ((m = p.match(/^\/accounts\/([^/]+)$/)) && method === 'DELETE') {
    const i = accounts.findIndex((a) => a.id === m![1])
    if (i < 0) fail('account not found')
    // Like the daemon: an account whose volume is mounted, or whose download runs, isn't removed.
    const busy = mounts.find((x) => x.account_id === m![1] && (x.state === 'mounted' || x.state === 'starting' || x.state === 'stopping'))
    if (busy) fail(`${busy.volume_name} is mounted; unmount it first (it belongs to this account)`)
    if (mockDownloads.some((x) => x.d.account_id === m![1] && !ENDED_MOCK.has(x.d.state))) fail('a remote download of this account is running; pause or cancel it first')
    accounts.splice(i, 1)
    for (let j = mounts.length - 1; j >= 0; j--) if (mounts[j].account_id === m[1]) mounts.splice(j, 1)
    return ok
  }
  if ((m = route.match(/^POST \/accounts\/([^/]+)\/verify$/))) {
    const a = accounts.find((x) => x.id === m![1]) ?? fail('account not found')
    const id: Identity = { token_id: a.token_id, workspace_id: 'ac-0000', workspace_name: a.workspace || a.label, username: a.username }
    return id
  }
  if ((m = route.match(/^GET \/accounts\/([^/]+)\/avatar$/))) return { data_url: identicon(m[1]) }
  if ((m = route.match(/^GET \/accounts\/([^/]+)\/environments$/))) return clone(environments[m[1]] || [])
  if ((m = route.match(/^GET \/accounts\/([^/]+)\/volumes$/))) {
    await wait(250)
    syncVolumeFlags()
    return clone(volumes[m[1]] || [])
  }
  if ((m = route.match(/^POST \/accounts\/([^/]+)\/volumes$/))) {
    const name = String(body.name || '')
    if (!/^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$/.test(name)) fail("volume names use letters, digits, '.', '_' and '-' (max 64)")
    const list = (volumes[m[1]] ||= [])
    if (list.some((v) => v.name === name)) fail(`a volume named ${name} already exists`)
    const v: Volume = { id: `vo-${name}`, name, format: 'v2', mounted: false, created_at: now(), created_by: 'sam' }
    list.push(v)
    emit('info', name, 'vol_created')
    return clone(v)
  }
  if ((m = route.match(/^POST \/accounts\/([^/]+)\/volumes\/([^/]+)\/rename$/))) {
    const v = (volumes[m[1]] || []).find((x) => x.id === m![2]) ?? fail('volume not found')
    const name = String(body.name || '')
    if (!/^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$/.test(name)) fail('invalid volume name')
    const mt = mounts.find((x) => x.volume_id === v.id)
    if (mt) mt.volume_name = name
    emit('info', name, 'vol_renamed')
    v.name = name
    return ok
  }
  if ((m = route.match(/^POST \/accounts\/([^/]+)\/volumes\/([^/]+)\/delete$/))) {
    const list = volumes[m[1]] || []
    const v = list.find((x) => x.id === m![2]) ?? fail('volume not found under that name')
    if (body.confirm !== v.name) fail('type the volume name to confirm deletion')
    if (mounts.some((x) => x.volume_id === v.id && x.state === 'mounted')) fail('unmount this volume before deleting it')
    list.splice(list.indexOf(v), 1)
    emit('info', v.name, 'vol_deleted')
    return ok
  }

  // a volume's page
  if ((m = p.match(/^\/mounts\/([^/]+)\/(usage|traffic|pending|conflicts|conflicts\/clear)$/))) {
    const r = insight(method, findMount(m[1]), m[2], body)
    if (r !== undefined) return r
  }

  // accounts: a new token, a new name or default environment
  if ((m = route.match(/^POST \/accounts\/([^/]+)\/token$/))) {
    const a = accounts.find((x) => x.id === m![1]) ?? fail('account not found')
    const id = String(body.tokenId || '')
    if (!/^ak-/.test(id) || !String(body.tokenSecret || '').startsWith('as-')) fail('Modal rejected this token: check the ID and secret')
    a.token_id = id
    emit('info', '', 'acct_token', [a.label])
    return clone(a)
  }
  if ((m = p.match(/^\/accounts\/([^/]+)$/)) && method === 'PATCH') {
    const a = accounts.find((x) => x.id === m![1]) ?? fail('account not found')
    if (typeof body.label === 'string' && body.label.trim()) a.label = body.label.trim()
    if (typeof body.environment === 'string') a.environment = body.environment
    for (const mt of mounts) if (mt.account_id === a.id) mt.account_label = a.label
    return ok
  }

  // mounts
  if (route === 'POST /mounts') {
    const acct = accounts.find((a) => a.id === body.account_id) ?? fail('account not found')
    const name = String(body.volume_name)
    const v = (volumes[acct.id] || []).find((x) => x.name === name) ?? fail(`volume ${name} not found`)
    if (mounts.some((x) => x.volume_id === v.id)) fail('this volume is already mounted')
    const mt: Mount = {
      id: `m-${name}`,
      account_id: acct.id,
      account_label: acct.label,
      workspace: acct.workspace || acct.label,
      environment: String(body.environment || ''),
      volume_name: name,
      volume_id: v.id,
      mountpoint: String(body.mountpoint || '') || `${ROOT}/${name}`,
      mode: 'auto',
      filter_junk: true,
      state: body.start ? 'starting' : 'stopped',
    }
    mounts.push(mt)
    if (body.start) setState(mt, 'mounted', 900)
    return { mount: clone(mt) }
  }
  if ((m = p.match(/^\/mounts\/([^/]+)$/))) {
    const mt = findMount(m[1])
    if (method === 'DELETE') {
      mounts.splice(mounts.indexOf(mt), 1)
      return ok
    }
    if (method === 'PATCH') {
      // Like the daemon: these only apply when a volume is mounted, so they can't change while it is.
      const atMount = ['mountpoint', 'read_only', 'mode', 'filter_junk', 'on_conflict', 'write_back_ms']
      const live = mt.state === 'mounted' || mt.state === 'starting' || mt.state === 'stopping'
      if (live && atMount.some((k) => k in body && (body as Record<string, unknown>)[k] !== (mt as unknown as Record<string, unknown>)[k])) {
        fail(`${mt.volume_name} is mounted; unmount it first to change this`)
      }
      Object.assign(mt, body)
      return ok
    }
  }
  if ((m = route.match(/^POST \/mounts\/([^/]+)\/(start|stop|sync|reveal)$/))) {
    const mt = findMount(m[1])
    if (m[2] === 'start') {
      mt.state = 'starting'
      mt.error = undefined
      setState(mt, 'mounted', 900)
    } else if (m[2] === 'stop') {
      const forced = url.searchParams.get('force') === '1'
      // The volume apps still use (models): a patient unmount retries a while, then gives up.
      if (!forced && mt.in_use_by?.length && url.searchParams.get('patient') === '1') {
        mt.state = 'stopping'
        await wait(1600)
        mt.state = 'mounted'
        fail(`${mt.volume_name} is in use by ${mt.in_use_by.join(', ')}; close the files open in it and try again`)
      }
      mt.state = 'stopping'
      mt.unmount_error = undefined
      mt.in_use_by = undefined
      if (mt.status && mt.status.pending_bytes > 0 && !forced) uploadThenStop(mt)
      else setState(mt, 'stopped', 700)
      mt.last_used = now()
      // The daemon answers once it's unmounted.
      if (url.searchParams.get('patient') === '1') while (mt.state === 'stopping') await wait(150)
    } else if (m[2] === 'sync' && mt.status) {
      // Uploads what's pending at about 240 MB/s, and answers when it's done.
      const s = mt.status
      while (s.pending_bytes > 0) {
        await wait(250)
        const step = Math.min(s.pending_bytes, 60 * MiB)
        s.pending_bytes -= step
        s.bytes_up += step
      }
      s.commits += s.pending_files
      s.pending_files = 0
      s.last_commit_at = now()
    }
    return ok
  }

  throw new MockError(`mock: no route for ${route}`)
}
