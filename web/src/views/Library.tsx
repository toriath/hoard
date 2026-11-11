import {
  ArrowDownUp, Check, Download, FolderOpen, Gamepad2, LayoutGrid, List, Pause, Pencil, Play, RotateCcw, Search, SlidersHorizontal,
  Sparkles, Trash2, Upload, X,
} from 'lucide-react'
import { memo, useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { api, filtersToQuery, rawUrl, thumbUrl, zipUrl } from '../lib/api'
import { togglePreview, useNowPlaying } from '../lib/audio'
import { formatBytes, plural, timeAgo } from '../lib/format'
import { link, useLocation } from '../lib/router'
import { changed, emitAsset, getMeta, prefs, toast, toastError, typeLabel, useAssetUpdates, useQuery, useVersion } from '../lib/store'
import { canRenderThumb, ensureThumb } from '../lib/thumbs'
import type { Asset, AssetPatch, Filters } from '../lib/types'
import { Button, Check as CheckBox, Chip, Dialog, Empty, Field, Menu, Spinner, TagInput, TypeBadge, cx, typeColor, typeIcon, useConfirm, useDebounced } from '../ui'
import { activeFilterCount, openAsset, readFilters, setFilters, type Scope } from './filters'
import { useUpload } from './Upload'

const PAGE = 60
const MAX_PAGE = 500 // the API's upper limit per request

// ---------- data ----------

function useAssetList(filters: Filters) {
  const key = filtersToQuery(filters)
  const version = useVersion('assets')
  const [state, setState] = useState<{ items: Asset[]; total: number; loading: boolean; error?: string }>({ items: [], total: 0, loading: true })
  const keyRef = useRef('')
  const countRef = useRef(0)
  const moreRef = useRef(false)

  useEffect(() => {
    const ctrl = new AbortController()
    const sameQuery = keyRef.current === key
    keyRef.current = key
    // A data change (not a new query) reloads everything already on screen,
    // however far the user has scrolled, in pages the API allows.
    const want = sameQuery ? Math.max(PAGE, countRef.current) : PAGE
    setState((s) => (sameQuery ? { ...s, loading: true, error: undefined } : { items: [], total: 0, loading: true }))
    ;(async () => {
      const items: Asset[] = []
      let total = 0
      while (items.length < want) {
        const r = await api.list(filters, Math.min(MAX_PAGE, want - items.length), items.length, ctrl.signal)
        total = r.total
        items.push(...r.items)
        if (r.items.length === 0 || items.length >= total) break
      }
      return { items, total }
    })().then(
      (r) => {
        if (ctrl.signal.aborted) return
        countRef.current = r.items.length
        setState({ items: r.items, total: r.total, loading: false })
      },
      (e) => !ctrl.signal.aborted && setState((s) => ({ ...s, loading: false, error: e.message })),
    )
    return () => ctrl.abort()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, version])

  const loadMore = useCallback(() => {
    if (moreRef.current) return
    moreRef.current = true
    const forKey = keyRef.current
    api
      .list(filters, PAGE, countRef.current)
      .then((r) => {
        if (keyRef.current !== forKey) return
        setState((s) => {
          const seen = new Set(s.items.map((a) => a.id))
          const items = [...s.items, ...r.items.filter((a) => !seen.has(a.id))]
          countRef.current = items.length
          return { items, total: r.total, loading: false }
        })
      }, toastError)
      .finally(() => {
        moreRef.current = false
      })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key])

  useAssetUpdates((a) => setState((s) => ({ ...s, items: s.items.map((x) => (x.id === a.id ? { ...x, ...a } : x)) })))

  return { ...state, loadMore, hasMore: state.items.length < state.total }
}

// ---------- thumbnail ----------

export function Thumb({ asset }: { asset: Asset }) {
  const primary = asset.files.find((f) => f.id === asset.primary_file_id) ?? asset.files[0]
  const Icon = typeIcon(asset.type)
  if (asset.thumb_version > 0) {
    const pixel = !!primary?.width && Math.max(primary.width, primary.height ?? 0) <= 128
    return <img src={thumbUrl(asset)} alt="" loading="lazy" decoding="async" className={pixel ? 'pixel' : undefined} draggable={false} />
  }
  if (primary?.kind === 'image') {
    return <img src={rawUrl(asset.id, primary.filename)} alt="" loading="lazy" decoding="async" draggable={false} />
  }
  return (
    <span className="thumb-icon" style={{ color: typeColor(asset.type) }}>
      <Icon aria-hidden />
      {primary && <span className="ext mono">{primary.filename.split('.').pop()}</span>}
    </span>
  )
}

// ---------- card & row ----------

interface ItemProps {
  asset: Asset
  selected: boolean
  selecting: boolean
  playing: boolean
  onOpen: (a: Asset, e: React.MouseEvent) => void
  onToggle: (a: Asset, shift: boolean) => void
}

function PlayButton({ asset, playing }: { asset: Asset; playing: boolean }) {
  const primary = asset.files.find((f) => f.id === asset.primary_file_id)
  if (primary?.kind !== 'audio') return null
  return (
    <button
      className="card-play"
      aria-label={playing ? 'Pause' : 'Play'}
      onClick={(e) => {
        e.stopPropagation()
        togglePreview(asset.id, rawUrl(asset.id, primary.filename))
      }}
    >
      {playing ? <Pause aria-hidden /> : <Play aria-hidden />}
    </button>
  )
}

const AssetCard = memo(function AssetCard({ asset, selected, selecting, playing, onOpen, onToggle }: ItemProps) {
  return (
    <article className="asset-card" data-selected={selected} data-selecting={selecting} data-playing={playing}>
      <button className="thumb checker" onClick={(e) => onOpen(asset, e)} aria-label={`Open ${asset.name}`}>
        <Thumb asset={asset} />
      </button>
      <label className="card-select" onClick={(e) => e.stopPropagation()}>
        <input
          type="checkbox"
          checked={selected}
          aria-label={`Select ${asset.name}`}
          onChange={() => {}}
          onClick={(e) => onToggle(asset, e.shiftKey)}
        />
      </label>
      <PlayButton asset={asset} playing={playing} />
      {asset.ai_status === 'queued' || asset.ai_status === 'running' ? (
        <span className="card-flag" title="AI agent is analysing this asset">
          <Sparkles aria-hidden />
        </span>
      ) : null}
      <div className="card-meta" onClick={(e) => onOpen(asset, e)}>
        <span className="name truncate" title={asset.name}>
          {asset.name || 'Untitled'}
        </span>
        <span className="sub">
          <span className="dot" style={{ background: typeColor(asset.type) }} />
          <span className="truncate">{typeLabel(asset.type)}</span>
          <span className="faint">{asset.file_count > 1 ? `${asset.file_count} files` : formatBytes(asset.total_size)}</span>
        </span>
      </div>
    </article>
  )
})

const AssetRow = memo(function AssetRow({ asset, selected, playing, onOpen, onToggle }: ItemProps) {
  return (
    <div className="asset-row" data-selected={selected} onClick={(e) => onOpen(asset, e)}>
      <label className="check" onClick={(e) => e.stopPropagation()}>
        <input type="checkbox" checked={selected} aria-label={`Select ${asset.name}`} onChange={() => {}} onClick={(e) => onToggle(asset, e.shiftKey)} />
      </label>
      <span className="row-thumb checker">
        <Thumb asset={asset} />
        <PlayButton asset={asset} playing={playing} />
      </span>
      <span className="row-name">
        <button type="button" className="name row-open truncate" onClick={(e) => { e.stopPropagation(); onOpen(asset, e) }}>
          {asset.name || 'Untitled'}
        </button>
        <span className="row-tags truncate faint">{asset.tags.join(' · ')}</span>
      </span>
      <span className="col-type">
        <TypeBadge type={asset.type} />
      </span>
      <span className="col-license truncate muted">{asset.license || '–'}</span>
      <span className="col-size muted mono">{formatBytes(asset.total_size)}</span>
      <span className="col-date muted">{timeAgo(asset.updated_at)}</span>
    </div>
  )
})

// ---------- library ----------

const SORTS: { id: string; label: string }[] = [
  { id: '', label: 'Default' },
  { id: 'created', label: 'Date added' },
  { id: 'updated', label: 'Last changed' },
  { id: 'name', label: 'Name' },
  { id: 'size', label: 'Size' },
  { id: 'type', label: 'Type' },
]

export function Library({ scope = {}, header, emptyHint }: { scope?: Scope; header?: ReactNode; emptyHint?: ReactNode }) {
  const loc = useLocation()
  const filters = useMemo(() => readFilters(loc.query, scope), [loc.query.toString(), scope.collection, scope.project, scope.trashed]) // eslint-disable-line react-hooks/exhaustive-deps
  const { items, total, loading, error, loadMore, hasMore } = useAssetList(filters)
  const stats = useQuery(() => api.stats(), [useVersion('assets')])
  const upload = useUpload()
  const confirm = useConfirm()
  const playing = useNowPlaying()
  const meta = getMeta()

  const [view, setView] = useState<'grid' | 'list'>(() => (prefs.get('hoard-view') === 'list' ? 'list' : 'grid'))
  const chooseView = (v: 'grid' | 'list') => {
    setView(v)
    prefs.set('hoard-view', v)
  }
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [filterOpen, setFilterOpen] = useState(false)
  const [bulkEdit, setBulkEdit] = useState(false)
  const anchor = useRef<string | null>(null)

  // Search box: local state, debounced into the URL.
  const [search, setSearch] = useState(filters.q)
  const debounced = useDebounced(search, 220)
  const searchRef = useRef<HTMLInputElement>(null)
  useEffect(() => {
    if (debounced !== filters.q) setFilters(loc, { q: debounced }, { replace: true })
  }, [debounced]) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    setSearch(filters.q)
  }, [filters.q])

  // Selection only makes sense within one result set.
  const filterKey = filtersToQuery(filters)
  useEffect(() => setSelected(new Set()), [filterKey])

  // Infinite scroll.
  const sentinel = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const el = sentinel.current
    if (!el || !hasMore) return
    const io = new IntersectionObserver((entries) => entries[0].isIntersecting && loadMore(), { rootMargin: '900px' })
    io.observe(el)
    return () => io.disconnect()
  }, [hasMore, loadMore, items.length])

  // Render missing previews (audio waveforms, 3D renders, video frames…) in the background.
  useEffect(() => {
    if (filters.trashed) return
    for (const a of items) {
      const p = a.files.find((f) => f.id === a.primary_file_id)
      if (a.thumb_version === 0 && p && canRenderThumb(p) && p.size <= 64 << 20) void ensureThumb(a)
    }
  }, [items, filters.trashed])

  // Poll while the AI agent is working on visible assets.
  const busyIds = items.filter((a) => a.ai_status === 'queued' || a.ai_status === 'running').map((a) => a.id).join(',')
  useEffect(() => {
    if (!busyIds) return
    const t = setInterval(() => {
      // Patch just these rows in place: a full refresh every few seconds
      // would rebuild a long list under the user.
      api.byIds(busyIds.split(',').slice(0, 200), filters.trashed).then(
        (rows) => rows.forEach(emitAsset),
        () => {},
      )
    }, 3500)
    return () => clearInterval(t)
  }, [busyIds, filters.trashed])

  const toggle = useCallback(
    (a: Asset, shift: boolean) => {
      const from = anchor.current // read now: the updater below runs later
      setSelected((prev) => {
        const next = new Set(prev)
        if (shift && from) {
          const ids = items.map((x) => x.id)
          const i = ids.indexOf(from)
          const j = ids.indexOf(a.id)
          if (i >= 0 && j >= 0) for (const id of ids.slice(Math.min(i, j), Math.max(i, j) + 1)) next.add(id)
        } else if (next.has(a.id)) next.delete(a.id)
        else next.add(a.id)
        return next
      })
      anchor.current = a.id
    },
    [items],
  )

  const selecting = selected.size > 0
  // The location object is new on every render; reading it through a ref
  // keeps `open` stable, so memoised cards do not all re-render on each
  // keystroke in the search box.
  const locRef = useRef(loc)
  locRef.current = loc
  const open = useCallback(
    (a: Asset, e: React.MouseEvent) => {
      if (selecting || e.metaKey || e.ctrlKey || e.shiftKey) toggle(a, e.shiftKey)
      else openAsset(locRef.current, a.id)
    },
    [selecting, toggle],
  )

  // Keyboard: "/" focuses search, Escape clears the selection, ⌘A selects all.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const typing = e.target instanceof Element && e.target.closest('input, textarea, select, [contenteditable]')
      if (e.key === '/' && !typing) {
        e.preventDefault()
        searchRef.current?.focus()
      } else if (e.key === 'Escape' && !typing && selected.size && !document.querySelector('.overlay, .menu')) {
        setSelected(new Set())
      } else if ((e.metaKey || e.ctrlKey) && e.key === 'a' && !typing && !document.querySelector('.overlay')) {
        e.preventDefault()
        setSelected(new Set(items.map((a) => a.id)))
      }
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [items, selected.size])

  const ids = [...selected]
  const bulk = async (action: '' | 'trash' | 'restore' | 'purge', patch: AssetPatch = {}, done?: string) => {
    try {
      const r = await api.bulk(ids, action, patch)
      setSelected(new Set())
      changed('assets', 'groups')
      if (done) toast(done.replace('%n', plural(r.affected, 'asset')))
    } catch (e) {
      toastError(e)
    }
  }

  const typeCounts = stats.data?.by_type ?? {}
  const typesShown = meta.types.filter((t) => typeCounts[t.id] || filters.types.includes(t.id))
  const nFilters = activeFilterCount(filters)
  const scoped = !!(scope.collection || scope.project || scope.trashed)
  const filtered = !!(filters.q || filters.types.length || nFilters)

  return (
    <div className="page library">
      {header}
      <div className="toolbar">
        <div className="search">
          <Search aria-hidden />
          <input
            ref={searchRef}
            className="input"
            type="search"
            placeholder="Search name, tags, description, prompt…"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            aria-label="Search assets"
          />
          <span className="kbd search-hint">/</span>
        </div>
        <Button icon={SlidersHorizontal} onClick={() => setFilterOpen(true)} aria-pressed={nFilters > 0} aria-label="Filters">
          <span className="hide-sm">Filters</span>
          {nFilters > 0 && <span className="count">{nFilters}</span>}
        </Button>
        <Menu
          align="right"
          trigger={(p) => (
            <Button icon={ArrowDownUp} {...p} title="Sort" aria-label="Sort and view">
              <span className="hide-sm">{SORTS.find((s) => s.id === filters.sort)?.label ?? 'Sort'}</span>
            </Button>
          )}
          items={[
            ...SORTS.map((s) => ({ label: s.label, icon: filters.sort === s.id ? Check : undefined, onSelect: () => setFilters(loc, { sort: s.id, order: '' }) })),
            { label: '', separator: true },
            { label: filters.order === 'asc' ? 'Ascending ✓' : 'Ascending', onSelect: () => setFilters(loc, { order: filters.order === 'asc' ? '' : 'asc' }) },
            { label: filters.order === 'desc' ? 'Descending ✓' : 'Descending', onSelect: () => setFilters(loc, { order: filters.order === 'desc' ? '' : 'desc' }) },
            { label: '', separator: true },
            { label: 'Grid view', icon: view === 'grid' ? Check : LayoutGrid, onSelect: () => chooseView('grid') },
            { label: 'List view', icon: view === 'list' ? Check : List, onSelect: () => chooseView('list') },
          ]}
        />
        <div className="seg hide-sm" role="group" aria-label="View">
          {(['grid', 'list'] as const).map((v) => (
            <Button
              key={v}
              iconOnly
              icon={v === 'grid' ? LayoutGrid : List}
              aria-pressed={view === v}
              aria-label={v === 'grid' ? 'Grid view' : 'List view'}
              onClick={() => chooseView(v)}
            />
          ))}
        </div>
        {!scope.trashed && (
          <Button variant="primary" icon={Upload} aria-label="Upload" onClick={() => upload.open([], { collection: scope.collection, project: scope.project })}>
            <span className="hide-sm">Upload</span>
          </Button>
        )}
      </div>

      {(typesShown.length > 1 || nFilters > 0) && (
        <div className="filterbar">
          {!scoped && typesShown.length > 1 && (
            <>
              <Chip active={filters.types.length === 0} onClick={() => setFilters(loc, { types: [] })}>
                All <b>{stats.data?.assets ?? ''}</b>
              </Chip>
              {typesShown.map((t) => (
                <Chip
                  key={t.id}
                  active={filters.types.includes(t.id)}
                  onClick={() =>
                    setFilters(loc, { types: filters.types.includes(t.id) ? filters.types.filter((x) => x !== t.id) : [...filters.types, t.id] })
                  }
                >
                  <span className="dot" style={{ background: typeColor(t.id) }} />
                  {t.label} <b>{typeCounts[t.id] ?? 0}</b>
                </Chip>
              ))}
            </>
          )}
          {scoped && filters.types.map((t) => (
            <Chip key={t} active onRemove={() => setFilters(loc, { types: filters.types.filter((x) => x !== t) })}>{typeLabel(t)}</Chip>
          ))}
          {filters.tags.map((t) => (
            <Chip key={t} active onRemove={() => setFilters(loc, { tags: filters.tags.filter((x) => x !== t) })}>
              #{t}
            </Chip>
          ))}
          {filters.license && (
            <Chip active onRemove={() => setFilters(loc, { license: '' })}>
              {filters.license === 'none' ? 'No license' : filters.license}
            </Chip>
          )}
          {filters.untagged && (
            <Chip active onRemove={() => setFilters(loc, { untagged: false })}>
              Untagged
            </Chip>
          )}
          {filters.project === 'none' && !scope.project && (
            <Chip active onRemove={() => setFilters(loc, { project: '' })}>
              Not used in a project
            </Chip>
          )}
        </div>
      )}

      <div className="result-line muted">
        {loading && items.length === 0 ? 'Loading…' : `${plural(total, 'asset')}${filtered ? ' found' : ''}`}
        {selecting && <span> · {selected.size} selected</span>}
      </div>

      {error ? (
        <Empty icon={X} title="Could not load assets">{error}</Empty>
      ) : loading && items.length === 0 ? (
        <div className="asset-grid" aria-hidden>
          {Array.from({ length: 12 }, (_, i) => (
            <div className="asset-card" key={i}>
              <div className="thumb skeleton" />
            </div>
          ))}
        </div>
      ) : items.length === 0 ? (
        filtered ? (
          <Empty icon={Search} title="Nothing matches">
            No asset matches the current search and filters.
          </Empty>
        ) : (
          <Empty
            icon={scope.trashed ? Trash2 : Upload}
            title={scope.trashed ? 'Trash is empty' : 'Nothing here yet'}
            action={!scope.trashed && <Button variant="primary" icon={Upload} onClick={() => upload.open([], { collection: scope.collection, project: scope.project })}>Upload assets</Button>}
          >
            {emptyHint ?? (scope.trashed ? 'Deleted assets stay here until you remove them for good.' : 'Drop files anywhere on this page, or let Claude add assets through MCP.')}
          </Empty>
        )
      ) : view === 'grid' ? (
        <div className="asset-grid">
          {items.map((a) => (
            <AssetCard key={a.id} asset={a} selected={selected.has(a.id)} selecting={selecting} playing={playing === a.id} onOpen={open} onToggle={toggle} />
          ))}
        </div>
      ) : (
        <div className="asset-list">
          {items.map((a) => (
            <AssetRow key={a.id} asset={a} selected={selected.has(a.id)} selecting={selecting} playing={playing === a.id} onOpen={open} onToggle={toggle} />
          ))}
        </div>
      )}
      <div ref={sentinel} style={{ height: 1 }} />
      {hasMore && <Spinner />}

      {selecting && (
        <div className="bulkbar" role="toolbar" aria-label="Bulk actions">
          <span className="bulk-count">
            <b>{selected.size}</b> selected
          </span>
          <Button size="sm" variant="ghost" onClick={() => setSelected(new Set(items.map((a) => a.id)))}>
            All {items.length}
          </Button>
          <span className="bulk-sep" />
          {scope.trashed ? (
            <>
              <Button size="sm" icon={RotateCcw} onClick={() => bulk('restore', {}, 'Restored %n')}>
                Restore
              </Button>
              <Button
                size="sm"
                variant="danger"
                icon={Trash2}
                onClick={async () => {
                  if (await confirm({ title: 'Delete permanently?', message: `${plural(selected.size, 'asset')} and all their files will be removed from disk. This cannot be undone.`, confirmLabel: 'Delete forever', danger: true }))
                    bulk('purge', {}, 'Deleted %n permanently')
                }}
              >
                Delete forever
              </Button>
            </>
          ) : (
            <>
              <Button size="sm" icon={Pencil} onClick={() => setBulkEdit(true)}>
                Edit
              </Button>
              {meta.ai_enabled && (
                <Button
                  size="sm"
                  icon={Sparkles}
                  aria-label="AI tag"
                  title="Let the AI agent describe and tag the selection"
                  onClick={async () => {
                    try {
                      const r = await api.analyze(ids)
                      setSelected(new Set())
                      changed('assets')
                      toast(`AI agent queued ${plural(r.queued, 'asset')}`)
                    } catch (e) {
                      toastError(e)
                    }
                  }}
                >
                  <span className="hide-sm">AI tag</span>
                </Button>
              )}
              <a className="btn btn-sm" href={zipUrl(ids)} download title="Download as zip" aria-label="Download as zip">
                <Download aria-hidden />
                <span className="hide-sm">Download</span>
              </a>
              <Button size="sm" variant="danger" icon={Trash2} aria-label="Move to trash" onClick={() => bulk('trash', {}, 'Moved %n to trash')}>
                <span className="hide-sm">Trash</span>
              </Button>
            </>
          )}
          <Button size="sm" variant="ghost" iconOnly icon={X} onClick={() => setSelected(new Set())} aria-label="Clear selection" />
        </div>
      )}

      {filterOpen && <FilterDialog filters={filters} scope={scope} onClose={() => setFilterOpen(false)} />}
      {bulkEdit && (
        <BulkEditDialog
          count={selected.size}
          onClose={() => setBulkEdit(false)}
          onApply={async (patch) => {
            setBulkEdit(false)
            await bulk('', patch, 'Updated %n')
          }}
        />
      )}
    </div>
  )
}

// ---------- filter dialog ----------

function FilterDialog({ filters, scope, onClose }: { filters: Filters; scope: Scope; onClose: () => void }) {
  const loc = useLocation()
  const [q, setQ] = useState('')
  const tags = useQuery(() => api.tags('', 2000), [])
  const stats = useQuery(() => api.stats(), [])
  const shown = (tags.data ?? []).filter((t) => t.tag.includes(q.trim().toLowerCase()))
  const toggleTag = (t: string) => setFilters(loc, { tags: filters.tags.includes(t) ? filters.tags.filter((x) => x !== t) : [...filters.tags, t] })
  return (
    <Dialog
      title="Filters"
      onClose={onClose}
      width={520}
      footer={
        <>
          <Button variant="ghost" onClick={() => setFilters(loc, { tags: [], license: '', untagged: false, project: scope.project ? undefined : '' })}>
            Clear all
          </Button>
          <Button variant="primary" onClick={onClose}>
            Done
          </Button>
        </>
      }
    >
      <div className="stack">
        <Field label="License">
          <select className="select" value={filters.license} onChange={(e) => setFilters(loc, { license: e.target.value })}>
            <option value="">Any license</option>
            <option value="none">No license recorded</option>
            {(stats.data?.licenses ?? []).map((l) => (
              <option key={l.tag} value={l.tag}>
                {l.tag} ({l.count})
              </option>
            ))}
          </select>
        </Field>
        <div className="row-wrap">
          <CheckBox checked={filters.untagged} onChange={(v) => setFilters(loc, { untagged: v })}>
            Untagged only
          </CheckBox>
          {!scope.project && (
            <CheckBox checked={filters.project === 'none'} onChange={(v) => setFilters(loc, { project: v ? 'none' : '' })}>
              Not used in any project
            </CheckBox>
          )}
        </div>
        <div className="field">
          <span className="label">Tags — assets must have all selected</span>
          <input className="input" placeholder="Find a tag…" value={q} onChange={(e) => setQ(e.target.value)} />
          <div className="tagcloud">
            {tags.loading && <Spinner />}
            {!tags.loading && shown.length === 0 && <span className="faint">No tags{q ? ' match' : ' yet'}</span>}
            {shown.slice(0, 300).map((t) => (
              <Chip key={t.tag} active={filters.tags.includes(t.tag)} onClick={() => toggleTag(t.tag)}>
                {t.tag} <b>{t.count}</b>
              </Chip>
            ))}
          </div>
        </div>
      </div>
    </Dialog>
  )
}

// ---------- bulk edit dialog ----------

function BulkEditDialog({ count, onClose, onApply }: { count: number; onClose: () => void; onApply: (patch: AssetPatch) => void }) {
  const meta = getMeta()
  const collections = useQuery(() => api.collections(), [])
  const projects = useQuery(() => api.projects(), [])
  const [addTags, setAddTags] = useState<string[]>([])
  const [removeTags, setRemoveTags] = useState<string[]>([])
  const [type, setType] = useState('')
  const [license, setLicense] = useState('')
  const [source, setSource] = useState('')
  const [attribution, setAttribution] = useState('')
  const [collection, setCollection] = useState('')
  const [project, setProject] = useState('')
  const [note, setNote] = useState('')

  const patch: AssetPatch = {}
  if (addTags.length) patch.add_tags = addTags
  if (removeTags.length) patch.remove_tags = removeTags
  if (type) patch.type = type
  if (license) patch.license = license
  if (source) patch.source = source
  if (attribution) patch.attribution = attribution
  if (collection) patch.add_collections = [collection]
  if (project) {
    patch.add_projects = [project]
    patch.project_note = note
  }
  const dirty = Object.keys(patch).length > 0

  return (
    <Dialog
      title={`Edit ${plural(count, 'asset')}`}
      onClose={onClose}
      width={560}
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" onClick={() => (dirty ? onApply(patch) : toast('Nothing to change: fill in at least one field'))}>
            Apply to {count}
          </Button>
        </>
      }
    >
      <div className="stack">
        <p className="muted">Only the fields you fill in are changed.</p>
        <Field label="Add tags">
          <TagInput value={addTags} onChange={setAddTags} autoFocus />
        </Field>
        <Field label="Remove tags">
          <TagInput value={removeTags} onChange={setRemoveTags} placeholder="Tag to remove…" />
        </Field>
        <div className="field-row">
          <Field label="Add to collection">
            <select className="select" value={collection} onChange={(e) => setCollection(e.target.value)}>
              <option value="">—</option>
              {(collections.data ?? []).map((c) => (
                <option key={c.id} value={c.id}>
                  {c.name}
                </option>
              ))}
            </select>
          </Field>
          <Field label="Used in project">
            <select className="select" value={project} onChange={(e) => setProject(e.target.value)}>
              <option value="">—</option>
              {(projects.data ?? []).map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name}
                </option>
              ))}
            </select>
          </Field>
        </div>
        {project && (
          <Field label="Usage note" hint="How or where the assets are used in the project (optional)">
            <input className="input" value={note} onChange={(e) => setNote(e.target.value)} placeholder="e.g. enemy sprites in level 2" />
          </Field>
        )}
        <div className="field-row">
          <Field label="Set type">
            <select className="select" value={type} onChange={(e) => setType(e.target.value)}>
              <option value="">Keep</option>
              {meta.types.map((t) => (
                <option key={t.id} value={t.id}>
                  {t.label}
                </option>
              ))}
            </select>
          </Field>
          <Field label="Set license">
            <input className="input" list="license-list" value={license} onChange={(e) => setLicense(e.target.value)} placeholder="Keep" />
          </Field>
        </div>
        <div className="field-row">
          <Field label="Set source">
            <input className="input" value={source} onChange={(e) => setSource(e.target.value)} placeholder="Keep" />
          </Field>
          <Field label="Set attribution">
            <input className="input" value={attribution} onChange={(e) => setAttribution(e.target.value)} placeholder="Keep" />
          </Field>
        </div>
      </div>
    </Dialog>
  )
}

// ---------- scoped headers ----------

export function ScopeHeader({ kind, name, description, badge, count, backTo, actions }: { kind: 'collection' | 'project'; name: string; description?: string; badge?: string; count: number; backTo: string; actions?: ReactNode }) {
  const Icon = kind === 'collection' ? FolderOpen : Gamepad2
  return (
    <div className="scope-head">
      <a className="btn btn-ghost btn-sm" {...link(backTo)}>
        ← {kind === 'collection' ? 'Collections' : 'Projects'}
      </a>
      <div className="row">
        <span className={cx('scope-icon')}>
          <Icon aria-hidden />
        </span>
        <div className="grow">
          <div className="row-wrap">
            <h1 className="truncate">{name}</h1>
            {badge && <span className="badge badge-plain">{badge}</span>}
          </div>
          <p className="muted">
            {plural(count, 'asset')}
            {description ? ` · ${description}` : ''}
          </p>
        </div>
        {actions}
      </div>
    </div>
  )
}
