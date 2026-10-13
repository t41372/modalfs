import { QueryClient, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { toast } from 'sonner'
import { api } from './client'
import type { Billing, Conflict, Download, DownloadDetail, Environment, LiveView, Mount, PendingFile, Sample, Settings, State, TrafficSample, UsageView, Volume } from './types'

export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // The daemon is local; a failed call is either "it's down" (shown as a banner) or a real error.
      retry: 1,
      retryDelay: 500,
      refetchOnWindowFocus: true,
      staleTime: 1000,
    },
  },
})

/** The daemon's whole state, polled while the window is visible. Keeps the last good state while the daemon is down. */
export function useAppState() {
  return useQuery({
    queryKey: ['state'],
    queryFn: () => api<State>('GET', '/state'),
    refetchInterval: 1500,
    // React Query keeps the previous data when a refetch fails, so a dropped
    // daemon doesn't blank the page; the shell shows a banner instead.
    retry: false,
  })
}

/** The state, for components that only render once it's loaded. */
export function useLoadedState() {
  const { data } = useAppState()
  if (!data) throw new Error('state not loaded')
  return data
}

export function useEnvironments(accountId: string | undefined) {
  return useQuery({
    queryKey: ['envs', accountId],
    queryFn: () => api<Environment[]>('GET', `/accounts/${accountId}/environments`),
    enabled: !!accountId,
    staleTime: 60_000,
  })
}

export function useVolumes(accountId: string | undefined, env: string) {
  return useQuery({
    queryKey: ['volumes', accountId, env],
    queryFn: () => api<Volume[]>('GET', `/accounts/${accountId}/volumes?env=${encodeURIComponent(env)}`),
    enabled: !!accountId,
    staleTime: 10_000,
  })
}

/** Change the cached state right away, so a switch moves under the finger; the next poll corrects it. */
export function patchState(fn: (s: State) => void) {
  queryClient.setQueryData<State>(['state'], (old) => {
    if (!old) return old
    const next = structuredClone(old)
    fn(next)
    return next
  })
}

export const refreshState = () => queryClient.invalidateQueries({ queryKey: ['state'] })
export const refreshVolumes = () => queryClient.invalidateQueries({ queryKey: ['volumes'] })

/**
 * A daemon call from a button or switch. Errors become a toast; the state is
 * refreshed either way. `optimistic` applies the expected result first and is
 * undone by the refresh if the call fails.
 */
export function useAction<V = void>(
  fn: (v: V) => Promise<unknown>,
  opts: {
    optimistic?: (s: State, v: V) => void
    success?: (v: V) => string | void
    volumes?: boolean
    /** Don't toast errors: the daemon reports them as events, which are toasted anyway. */
    quiet?: boolean
  } = {},
) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: fn,
    onMutate: async (v) => {
      if (!opts.optimistic) return
      await qc.cancelQueries({ queryKey: ['state'] })
      patchState((s) => opts.optimistic!(s, v))
    },
    onError: (e) => {
      if (!opts.quiet) toast.error(e.message)
    },
    onSuccess: (_d, v) => {
      const msg = opts.success?.(v)
      if (msg) toast.success(msg)
    },
    onSettled: () => {
      refreshState()
      if (opts.volumes) refreshVolumes()
    },
  })
}

export const patchMount = (id: string, body: Partial<Mount>) => api('PATCH', `/mounts/${id}`, body)
export const patchSettings = (body: Settings | { transport: string }) => api('PATCH', '/settings', body)

/** An account's spending this month, which the daemon reads from Modal every few minutes at most. */
export function useBilling(accountId: string | undefined, env = '', enabled = true) {
  return useQuery({
    queryKey: ['billing', accountId, env],
    queryFn: () => api<Billing>('GET', `/accounts/${accountId}/billing?env=${encodeURIComponent(env)}`),
    enabled: enabled && !!accountId,
    staleTime: 5 * 60_000,
    refetchInterval: 5 * 60_000,
    retry: false,
  })
}

/** The downloads, with live numbers for the running ones, while the Downloads page shows them. */
export function useDownloads(enabled: boolean) {
  return useQuery({
    queryKey: ['downloads'],
    queryFn: () => api<Download[]>('GET', '/downloads?live=1'),
    enabled,
    refetchInterval: 1000,
  })
}

/**
 * A download's events and its durable speed history (one sample a second,
 * from the volume). The history is read whole once, then only what's new.
 */
export function useDownloadRecords(id: string, running: boolean) {
  const [records, setRecords] = useState<{ detail?: DownloadDetail; history: Sample[]; error?: string }>({ history: [] })
  useEffect(() => {
    let stop = false
    let timer: ReturnType<typeof setTimeout>
    let history: Sample[] = []
    const load = async () => {
      const after = history.length ? history[history.length - 1][0] : 0
      try {
        const d = await api<DownloadDetail>('GET', `/downloads/${id}${after ? `?after=${after}` : ''}`)
        if (stop) return
        history = after ? history.concat(d.history) : d.history
        setRecords({ detail: d, history })
      } catch (e) {
        if (!stop) setRecords((r) => ({ ...r, error: e instanceof Error ? e.message : String(e) }))
      }
      if (!stop && running) timer = setTimeout(load, 10_000)
    }
    load()
    return () => {
      stop = true
      clearTimeout(timer)
    }
  }, [id, running])
  return records
}

/** Most live samples kept: a day at four a second. */
const LIVE_MAX = 4 * 86400

/**
 * What a running download's container sends, four times a second, relayed by
 * the daemon: this run's samples so far, then each new one.
 */
export function useLive(id: string, active: boolean) {
  const [live, setLive] = useState<{ samples: Sample[]; view?: LiveView }>({ samples: [] })
  useEffect(() => {
    if (!active) return
    let stop = false
    let timer: ReturnType<typeof setTimeout>
    let samples: Sample[] = []
    const poll = async () => {
      const after = samples.length ? samples[samples.length - 1][0] : 0
      const started = Date.now()
      try {
        const v = await api<LiveView>('GET', `/downloads/${id}/live?after=${after}`)
        if (stop) return
        if (v.samples.length) {
          samples = samples.concat(v.samples)
          if (samples.length > LIVE_MAX) samples = samples.slice(-LIVE_MAX)
        }
        setLive({ samples, view: v })
      } catch {
        // the daemon is down for a moment; the shell says so
      }
      if (!stop) timer = setTimeout(poll, Math.max(0, 250 - (Date.now() - started)))
    }
    poll()
    return () => {
      stop = true
      clearTimeout(timer)
    }
  }, [id, active])
  return live
}

export const refreshDownloads = () => queryClient.invalidateQueries({ queryKey: ['downloads'] })

// ─── A volume's page ───

/** The volume's size from its last listing, and a listing in progress (polled while one runs). */
export function useUsage(mountId: string) {
  return useQuery({
    queryKey: ['usage', mountId],
    queryFn: () => api<UsageView>('GET', `/mounts/${mountId}/usage`),
    refetchInterval: (q) => (q.state.data?.running ? 1000 : false),
    retry: false,
  })
}
export const refreshUsage = (mountId: string) => queryClient.invalidateQueries({ queryKey: ['usage', mountId] })

/** The mount's upload and read totals every 10 s for the last hour. */
export function useTraffic(mountId: string, enabled: boolean) {
  return useQuery({
    queryKey: ['traffic', mountId],
    queryFn: () => api<TrafficSample[]>('GET', `/mounts/${mountId}/traffic`),
    enabled,
    refetchInterval: 10_000,
    retry: false,
  })
}

/** The files waiting to upload, while there are any. */
export function usePending(mountId: string, enabled: boolean) {
  return useQuery({
    queryKey: ['pending', mountId],
    queryFn: () => api<PendingFile[]>('GET', `/mounts/${mountId}/pending`),
    enabled,
    refetchInterval: 2000,
    retry: false,
  })
}

/** Conflicts not marked handled yet. */
export function useConflicts(mountId: string, count: number) {
  return useQuery({
    queryKey: ['conflicts', mountId, count],
    queryFn: () => api<Conflict[]>('GET', `/mounts/${mountId}/conflicts`),
    enabled: count > 0,
    retry: false,
  })
}
