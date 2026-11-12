import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { Location } from '../lib/router'
import type { Filters } from '../lib/types'
import { activeFilterCount, currentAssetId, openAsset, readFilters, setFilters } from './filters'

const loc = (path: string, query = ''): Location => ({ path, query: new URLSearchParams(query) })
const here = () => window.location.pathname + window.location.search

beforeEach(() => {
  window.scrollTo = vi.fn() as typeof window.scrollTo
  window.history.replaceState(null, '', '/')
})

describe('readFilters', () => {
  it('parses lists and flags from the query', () => {
    const f = readFilters(new URLSearchParams('q=sword&type=sprite,sfx&tag=a,,b&license=CC0-1.0&status=draft&untagged=1&sort=name&order=desc'), {})
    expect(f).toMatchObject({
      q: 'sword', types: ['sprite', 'sfx'], tags: ['a', 'b'], license: 'CC0-1.0', statuses: ['draft'],
      untagged: true, sort: 'name', order: 'desc', collection: '', project: '', trashed: false,
    })
  })
  it('defaults to empty filters', () => {
    const f = readFilters(new URLSearchParams(), {})
    expect(f.types).toEqual([])
    expect(f.q).toBe('')
    expect(f.untagged).toBe(false)
  })
  it('lets the page scope win over the URL', () => {
    const f = readFilters(new URLSearchParams('project=none'), { project: 'prj_1', collection: 'col_1', trashed: true })
    expect(f).toMatchObject({ project: 'prj_1', collection: 'col_1', trashed: true })
    expect(readFilters(new URLSearchParams('project=none'), {}).project).toBe('none')
  })
})

describe('setFilters', () => {
  it('writes values and drops empty ones', () => {
    setFilters(loc('/', 'q=old&tag=x'), { q: '', types: ['sprite', 'ui'], untagged: true })
    expect(here()).toBe('/?tag=x&type=sprite%2Cui&untagged=1')
  })
  it('keeps unrelated parameters such as the open asset', () => {
    setFilters(loc('/', 'asset=ast_abc'), { sort: 'size', order: 'asc' })
    expect(new URLSearchParams(window.location.search).get('asset')).toBe('ast_abc')
    expect(new URLSearchParams(window.location.search).get('sort')).toBe('size')
  })
  it('clears an empty list', () => {
    setFilters(loc('/', 'tag=a,b'), { tags: [] })
    expect(here()).toBe('/')
  })
})

describe('activeFilterCount', () => {
  const base: Filters = readFilters(new URLSearchParams(), {})
  it('counts what the filter panel can clear', () => {
    expect(activeFilterCount(base)).toBe(0)
    expect(activeFilterCount({ ...base, tags: ['a', 'b'], statuses: ['draft'], license: 'MIT', untagged: true, project: 'none' })).toBe(6)
  })
  it('ignores search text, type and a concrete project', () => {
    expect(activeFilterCount({ ...base, q: 'x', types: ['sprite'], project: 'prj_1' })).toBe(0)
  })
})

describe('currentAssetId / openAsset', () => {
  it('reads the id from the asset page or the drawer parameter', () => {
    expect(currentAssetId(loc('/assets/ast_abc123'))).toBe('ast_abc123')
    expect(currentAssetId(loc('/', 'asset=ast_zz9'))).toBe('ast_zz9')
    expect(currentAssetId(loc('/'))).toBeNull()
  })
  it('refuses ids that are not shaped like one', () => {
    expect(currentAssetId(loc('/', 'asset=../../etc'))).toBeNull()
    expect(currentAssetId(loc('/', 'asset=ast_A!'))).toBeNull()
    expect(currentAssetId(loc('/assets/..%2fx'))).toBeNull()
  })
  it('opening pushes an entry, closing replaces it', () => {
    const n = window.history.length
    openAsset(loc('/', 'tag=x'), 'ast_one')
    expect(here()).toBe('/?tag=x&asset=ast_one')
    expect(window.history.length).toBe(n + 1)
    openAsset(loc('/', 'tag=x&asset=ast_one'), null)
    expect(here()).toBe('/?tag=x')
    expect(window.history.length).toBe(n + 1)
  })
  it('opening from an asset page returns to the library', () => {
    openAsset(loc('/assets/ast_one'), 'ast_two')
    expect(here()).toBe('/?asset=ast_two')
  })
})
