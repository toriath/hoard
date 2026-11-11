import { useEffect, useRef, useState, useSyncExternalStore } from 'react'
import { api } from './api'
import type { Meta } from './types'

// ---- change notifications: views refetch when data they show was mutated ----

type Topic = 'assets' | 'groups' | 'tokens'
const versions: Record<Topic, number> = { assets: 0, groups: 0, tokens: 0 }
const subs = new Set<() => void>()

export function changed(...topics: Topic[]) {
  for (const t of topics) versions[t]++
  subs.forEach((s) => s())
}

export function useVersion(topic: Topic): number {
  return useSyncExternalStore(
    (cb) => {
      subs.add(cb)
      return () => subs.delete(cb)
    },
    () => versions[topic],
  )
}

// ---- async data hook ----

export interface Query<T> {
  data: T | undefined
  error: Error | undefined
  loading: boolean
  reload: () => void
}

export function useQuery<T>(fn: () => Promise<T>, deps: unknown[]): Query<T> {
  const [state, setState] = useState<{ data?: T; error?: Error; loading: boolean }>({ loading: true })
  const [tick, setTick] = useState(0)
  const fnRef = useRef(fn)
  fnRef.current = fn
  useEffect(() => {
    let live = true
    setState((s) => ({ ...s, loading: true }))
    fnRef.current().then(
      (data) => live && setState({ data, loading: false }),
      // Keep what is already on screen: one failed refresh must not turn a
      // loaded page into an empty or "not found" one.
      (error) => live && setState((s) => ({ data: s.data, error, loading: false })),
    )
    return () => {
      live = false
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, tick])
  return { data: state.data, error: state.error, loading: state.loading, reload: () => setTick((t) => t + 1) }
}

// ---- server metadata (types, licenses, limits) ----

let meta: Meta | undefined
export function getMeta(): Meta {
  if (!meta) throw new Error('meta not loaded')
  return meta
}
export async function refreshMeta(): Promise<Meta> {
  meta = await api.meta()
  return meta
}
export function typeLabel(id: string): string {
  return meta?.types.find((t) => t.id === id)?.label ?? id
}

// ---- toasts ----

export interface Toast {
  id: number
  message: string
  kind: 'info' | 'error'
}
let toasts: Toast[] = []
let toastId = 0
const toastSubs = new Set<() => void>()

export function toast(message: string, kind: Toast['kind'] = 'info') {
  const t = { id: ++toastId, message, kind }
  toasts = [...toasts, t]
  toastSubs.forEach((s) => s())
  setTimeout(
    () => {
      toasts = toasts.filter((x) => x.id !== t.id)
      toastSubs.forEach((s) => s())
    },
    kind === 'error' ? 6000 : 3200,
  )
}
export const toastError = (e: unknown) => toast(e instanceof Error ? e.message : String(e), 'error')

export function useToasts(): Toast[] {
  return useSyncExternalStore(
    (cb) => {
      toastSubs.add(cb)
      return () => toastSubs.delete(cb)
    },
    () => toasts,
  )
}

// ---- small preferences ----
// localStorage can be unavailable (blocked storage, private modes); the app
// must still start, just without remembering.
export const prefs = {
  get(key: string): string | null {
    try {
      return localStorage.getItem(key)
    } catch {
      return null
    }
  },
  set(key: string, value: string) {
    try {
      localStorage.setItem(key, value)
    } catch {
      /* not persisted */
    }
  },
  remove(key: string) {
    try {
      localStorage.removeItem(key)
    } catch {
      /* nothing to remove */
    }
  },
}

// ---- theme ----

export type Theme = 'auto' | 'dark' | 'light'
export function getTheme(): Theme {
  return (prefs.get('hoard-theme') as Theme) || 'auto'
}
export function applyTheme(t: Theme) {
  if (t === 'auto') {
    prefs.remove('hoard-theme')
    delete document.documentElement.dataset.theme
  } else {
    prefs.set('hoard-theme', t)
    document.documentElement.dataset.theme = t
  }
}

// ---- single-asset updates: lists patch the row in place, no refetch ----

import type { Asset } from './types'

const assetSubs = new Set<(a: Asset) => void>()
export function emitAsset(a: Asset) {
  assetSubs.forEach((s) => s(a))
}
export function useAssetUpdates(cb: (a: Asset) => void) {
  const ref = useRef(cb)
  ref.current = cb
  useEffect(() => {
    const fn = (a: Asset) => ref.current(a)
    assetSubs.add(fn)
    return () => {
      assetSubs.delete(fn)
    }
  }, [])
}
