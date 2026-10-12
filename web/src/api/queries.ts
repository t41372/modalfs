import { QueryClient, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { api } from './client'
import type { Environment, Mount, Settings, State, Volume } from './types'

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
