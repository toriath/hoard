import { Ellipsis, Merge, Pencil, Search, Tag, Trash2 } from 'lucide-react'
import { useMemo, useState } from 'react'
import { api } from '../lib/api'
import { plural } from '../lib/format'
import { link } from '../lib/router'
import { changed, toast, toastError, useQuery, useVersion } from '../lib/store'
import type { TagCount } from '../lib/types'
import { Button, Check, Dialog, Empty, Field, Menu, Spinner, useConfirm } from '../ui'

type Sort = 'count' | 'name'

/** Same canonicalisation as the server (internal/hoard NormalizeTag), so the dialog can tell whether a name already exists. */
const normalize = (t: string) => {
  const clean = t
    .toLowerCase()
    .trim()
    .replace(/[^\p{L}\p{N}\-_.:+#]+/gu, '-')
    .replace(/-+/g, '-')
    .replace(/^-+|-+$/g, '')
  // The server cuts at 48 bytes of UTF-8, not 48 characters.
  let out = ''
  let bytes = 0
  for (const ch of clean) {
    bytes += new TextEncoder().encode(ch).length
    if (bytes > 48) break
    out += ch
  }
  return out.replace(/-+$/g, '')
}

/** Tag management: list, rename, merge, delete. */
export function TagsPage() {
  const assetsVersion = useVersion('assets')
  const q = useQuery<TagCount[]>(() => api.tags('', 5000), [assetsVersion])
  const [search, setSearch] = useState('')
  const [sort, setSort] = useState<Sort>('count')
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [renaming, setRenaming] = useState<string | null>(null)
  const [merging, setMerging] = useState(false)
  const confirm = useConfirm()

  const tags = q.data ?? []
  const counts = useMemo(() => new Map(tags.map((t) => [t.tag, t.count])), [tags])
  const shown = useMemo(() => {
    const needle = search.trim().toLowerCase()
    const list = tags.filter((t) => !needle || t.tag.includes(needle))
    list.sort(sort === 'name' ? (a, b) => a.tag.localeCompare(b.tag) : (a, b) => b.count - a.count || a.tag.localeCompare(b.tag))
    return list
  }, [tags, search, sort])

  const toggle = (tag: string) =>
    setSelected((s) => {
      const n = new Set(s)
      if (!n.delete(tag)) n.add(tag)
      return n
    })

  const done = (message: string) => {
    toast(message)
    changed('assets')
    q.reload()
  }

  const remove = async (tag: string) => {
    const n = counts.get(tag) ?? 0
    if (!(await confirm({ title: `Delete tag “${tag}”?`, message: `It is removed from ${plural(n, 'asset')}. The assets stay.`, confirmLabel: 'Delete tag', danger: true }))) return
    try {
      await api.renameTag(tag, '')
      setSelected((s) => {
        const x = new Set(s)
        x.delete(tag)
        return x
      })
      done(`Deleted “${tag}”`)
    } catch (e) {
      toastError(e)
    }
  }

  const chosen = tags.filter((t) => selected.has(t.tag))

  return (
    <div className="page page-narrow">
      <div className="page-head">
        <div>
          <h1>Tags</h1>
          <p className="muted">{q.data ? `${plural(tags.length, 'tag')} on active assets.` : 'Rename, merge or delete tags across the whole library.'}</p>
        </div>
      </div>

      <div className="tags-bar">
        <div className="search">
          <Search aria-hidden />
          <input className="input" type="search" placeholder="Search tags" aria-label="Search tags" value={search} onChange={(e) => setSearch(e.target.value)} />
        </div>
        <select className="select" aria-label="Sort tags" value={sort} onChange={(e) => setSort(e.target.value as Sort)} style={{ width: 'auto' }}>
          <option value="count">Most used</option>
          <option value="name">Name</option>
        </select>
      </div>

      {selected.size > 0 && (
        <div className="tags-selection" role="status">
          <span>{plural(selected.size, 'tag')} selected</span>
          <Button size="sm" icon={Merge} disabled={selected.size < 2} onClick={() => setMerging(true)}>
            Merge…
          </Button>
          <Button size="sm" variant="ghost" onClick={() => setSelected(new Set())}>
            Clear
          </Button>
        </div>
      )}

      {q.loading && !q.data ? (
        <Spinner label="Loading tags…" />
      ) : q.error && !q.data ? (
        <Empty icon={Tag} title="Tags could not be loaded">
          {q.error.message}
        </Empty>
      ) : shown.length === 0 ? (
        <Empty icon={Tag} title={tags.length === 0 ? 'No tags yet' : 'No tag matches'}>
          {tags.length === 0 ? 'Tags you add to assets show up here.' : 'Try a different search.'}
        </Empty>
      ) : (
        <ul className="tag-list">
          {shown.map((t) => (
            <li key={t.tag} className="tag-row" data-selected={selected.has(t.tag)}>
              <Check checked={selected.has(t.tag)} onChange={() => toggle(t.tag)}>
                <span className="sr-only">Select {t.tag}</span>
              </Check>
              <a className="tag-name truncate" {...link(`/?tag=${encodeURIComponent(t.tag)}`)} title="Show these assets in the library">
                {t.tag}
              </a>
              <span className="faint tag-count">{plural(t.count, 'asset')}</span>
              <Menu
                trigger={(p) => (
                  <Button {...p} size="sm" variant="ghost" iconOnly icon={Ellipsis} aria-label={`Actions for ${t.tag}`} aria-haspopup="menu" />
                )}
                items={[
                  { label: 'Rename…', icon: Pencil, onSelect: () => setRenaming(t.tag) },
                  { label: 'Delete', icon: Trash2, danger: true, onSelect: () => remove(t.tag) },
                ]}
              />
            </li>
          ))}
        </ul>
      )}

      {renaming && (
        <TagDialog
          mode="rename"
          from={[renaming]}
          counts={counts}
          onChanged={() => {
            changed('assets')
            q.reload()
          }}
          onClose={() => setRenaming(null)}
          onDone={(m) => {
            setRenaming(null)
            setSelected(new Set())
            done(m)
          }}
        />
      )}
      {merging && (
        <TagDialog
          mode="merge"
          from={chosen.sort((a, b) => b.count - a.count).map((t) => t.tag)}
          counts={counts}
          onChanged={() => {
            changed('assets')
            q.reload()
          }}
          onClose={() => setMerging(false)}
          onDone={(m) => {
            setMerging(false)
            setSelected(new Set())
            done(m)
          }}
        />
      )}
    </div>
  )
}

/** Rename one tag, or merge several into one target. Asks for confirmation when the target already exists. */
function TagDialog({
  mode, from, counts, onClose, onDone, onChanged,
}: {
  mode: 'rename' | 'merge'
  from: string[]
  counts: Map<string, number>
  onClose: () => void
  onDone: (message: string) => void
  onChanged: () => void
}) {
  const [target, setTarget] = useState(from[0] ?? '')
  const [busy, setBusy] = useState(false)
  const confirm = useConfirm()
  const to = normalize(target)
  const exists = to !== '' && counts.has(to) && !(mode === 'rename' && to === from[0])
  const sources = from.filter((f) => f !== to)

  const submit = async () => {
    if (!to || sources.length === 0) return
    if (exists && mode === 'rename') {
      const ok = await confirm({
        title: `Merge into “${to}”?`,
        message: `“${to}” already exists (${plural(counts.get(to) ?? 0, 'asset')}). Assets tagged “${from[0]}” get “${to}” and lose “${from[0]}”.`,
        confirmLabel: 'Merge',
        danger: true,
      })
      if (!ok) return
    }
    setBusy(true)
    try {
      for (const f of sources) await api.renameTag(f, to)
      onDone(mode === 'merge' ? `Merged ${plural(sources.length, 'tag')} into “${to}”` : exists ? `Merged into “${to}”` : `Renamed to “${to}”`)
    } catch (e) {
      toastError(e)
    } finally {
      setBusy(false)
      onChanged() // also after a partial failure, so the list shows what really happened
    }
  }

  return (
    <Dialog
      title={mode === 'merge' ? `Merge ${from.length} tags` : `Rename “${from[0]}”`}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" busy={busy} disabled={!to || sources.length === 0} onClick={submit}>
            {mode === 'merge' ? 'Merge' : exists ? 'Merge…' : 'Rename'}
          </Button>
        </>
      }
    >
      <form
        className="stack"
        onSubmit={(e) => {
          e.preventDefault()
          void submit()
        }}
      >
        {mode === 'merge' && <p className="muted">{from.join(', ')} become one tag on every asset that has any of them.</p>}
        <Field
          label={mode === 'merge' ? 'Merge into' : 'New name'}
          hint={to && to !== target ? `Will be saved as “${to}”.` : exists && mode === 'merge' ? 'This tag exists; the others are merged into it.' : undefined}
        >
          <input className="input" autoFocus value={target} onChange={(e) => setTarget(e.target.value)} maxLength={64} />
        </Field>
      </form>
    </Dialog>
  )
}
