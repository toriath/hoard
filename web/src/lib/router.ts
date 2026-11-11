import { useSyncExternalStore } from 'react'

// A tiny history-API router: the URL is the single source of truth for
// navigation and library filters.

const listeners = new Set<() => void>()
const notify = () => listeners.forEach((l) => l())
window.addEventListener('popstate', notify)

function subscribe(cb: () => void) {
  listeners.add(cb)
  return () => listeners.delete(cb)
}

const snapshot = () => window.location.pathname + window.location.search

export interface Location {
  path: string
  query: URLSearchParams
}

export function useLocation(): Location {
  const url = useSyncExternalStore(subscribe, snapshot)
  const i = url.indexOf('?')
  return { path: i < 0 ? url : url.slice(0, i), query: new URLSearchParams(i < 0 ? '' : url.slice(i)) }
}

export function navigate(to: string, opts: { replace?: boolean } = {}) {
  if (to === snapshot()) return
  const before = window.location.pathname
  if (opts.replace) window.history.replaceState(null, '', to)
  else window.history.pushState(null, '', to)
  // A different page starts at its top; filters and the drawer keep the scroll.
  if (window.location.pathname !== before) window.scrollTo(0, 0)
  notify()
}

/** onClick handler for <a>: client-side navigation unless modified click. */
export function link(to: string) {
  return {
    href: to,
    onClick: (e: React.MouseEvent) => {
      if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return
      e.preventDefault()
      navigate(to)
    },
  }
}
