// An in-memory stand-in for the daemon, for `bun run dev:mock` and the README's
// screenshots. It answers the same routes as internal/daemon/api.go with sample
// data that belongs to no one, and actions change it the way the daemon would
// (mounting takes a moment, deleting a mounted volume is refused).
//
// URL parameters set what the screenshots need: ?lang=en|zh-Hant|zh-Hans and
// ?theme=light|dark.

import type { Account, DaemonEvent, Environment, Identity, Mount, Profile, State, Volume } from './types'

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
    status: status({ pending_files: 2, pending_bytes: 48 * MiB, bytes_down: 812 * MiB, bytes_up: 1.9 * GiB, commits: 6, conflicts: 1, last_commit_at: now() - 30 }),
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

export async function mockRequest(method: string, path: string, raw?: unknown): Promise<unknown> {
  await wait(method === 'GET' ? 60 : 280)
  const body = (raw ?? {}) as Body
  const url = new URL(path, 'http://mock')
  const p = url.pathname
  const route = `${method} ${p}`
  let m: RegExpMatchArray | null

  if (route === 'GET /state') {
    syncVolumeFlags()
    return clone(state)
  }
  if (route === 'GET /events') {
    const after = Number(url.searchParams.get('after') || 0)
    return clone(events.filter((e) => e.seq > after))
  }
  if (route === 'POST /shutdown' || route === 'POST /session') return ok
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
    accounts.splice(i, 1)
    for (let j = mounts.length - 1; j >= 0; j--) if (mounts[j].account_id === m[1]) mounts.splice(j, 1)
    return ok
  }
  if ((m = route.match(/^POST \/accounts\/([^/]+)\/verify$/))) {
    const a = accounts.find((x) => x.id === m![1]) ?? fail('account not found')
    const id: Identity = { token_id: a.token_id, workspace_id: 'ac-0000', workspace_name: a.workspace || a.label, username: a.username }
    return id
  }
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
      mt.state = 'stopping'
      mt.unmount_error = undefined
      if (mt.status && mt.status.pending_bytes > 0 && url.searchParams.get('force') !== '1') uploadThenStop(mt)
      else setState(mt, 'stopped', 700)
    } else if (m[2] === 'sync' && mt.status) {
      await wait(500)
      mt.status.commits += mt.status.pending_files
      mt.status.bytes_up += mt.status.pending_bytes
      mt.status.pending_files = 0
      mt.status.pending_bytes = 0
      mt.status.last_commit_at = now()
    }
    return ok
  }

  throw new MockError(`mock: no route for ${route}`)
}
