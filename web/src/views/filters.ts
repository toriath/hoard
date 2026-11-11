import { navigate, type Location } from '../lib/router'
import type { Filters } from '../lib/types'

/** Filters that a page fixes (collection, project, trash) and the URL cannot change. */
export type Scope = Partial<Pick<Filters, 'collection' | 'project' | 'trashed'>>

const list = (v: string | null) => (v ? v.split(',').filter(Boolean) : [])

export function readFilters(q: URLSearchParams, scope: Scope): Filters {
  return {
    q: q.get('q') ?? '',
    types: list(q.get('type')),
    tags: list(q.get('tag')),
    license: q.get('license') ?? '',
    untagged: q.get('untagged') === '1',
    sort: q.get('sort') ?? '',
    order: (q.get('order') as Filters['order']) ?? '',
    collection: scope.collection ?? '',
    project: scope.project ?? q.get('project') ?? '',
    trashed: scope.trashed ?? false,
  }
}

/** Writes the URL-owned part of the filters back to the address bar. */
export function setFilters(loc: Location, next: Partial<Filters>, opts: { replace?: boolean } = {}) {
  const q = new URLSearchParams(loc.query)
  const set = (k: string, v: string) => (v ? q.set(k, v) : q.delete(k))
  if (next.q !== undefined) set('q', next.q)
  if (next.types) set('type', next.types.join(','))
  if (next.tags) set('tag', next.tags.join(','))
  if (next.license !== undefined) set('license', next.license)
  if (next.untagged !== undefined) set('untagged', next.untagged ? '1' : '')
  if (next.project !== undefined) set('project', next.project)
  if (next.sort !== undefined) set('sort', next.sort)
  if (next.order !== undefined) set('order', next.order)
  const s = q.toString()
  navigate(loc.path + (s ? '?' + s : ''), opts)
}

export function activeFilterCount(f: Filters): number {
  return f.tags.length + (f.license ? 1 : 0) + (f.untagged ? 1 : 0) + (f.project === 'none' ? 1 : 0)
}

/** Opens / closes the asset drawer while keeping the current page and filters. */
export function openAsset(loc: Location, id: string | null) {
  const q = new URLSearchParams(loc.query)
  if (id) q.set('asset', id)
  else q.delete('asset')
  const path = loc.path.startsWith('/assets/') ? '/' : loc.path
  const s = q.toString()
  // Opening adds a history entry; closing replaces it, so Back never
  // re-opens an asset that was just closed.
  navigate(path + (s ? '?' + s : ''), { replace: id === null })
}

export function currentAssetId(loc: Location): string | null {
  const m = loc.path.match(/^\/assets\/(ast_[0-9a-z]+)$/)
  if (m) return m[1]
  // The id goes into API paths: accept nothing that is not shaped like one.
  const q = loc.query.get('asset')
  return q && /^ast_[0-9a-z]+$/.test(q) ? q : null
}
