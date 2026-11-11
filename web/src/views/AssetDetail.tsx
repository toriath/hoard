import {
  Copy, Download, Ellipsis, ExternalLink, FilePlus2, FolderOpen, Gamepad2, ImageUp, Pencil, Plus, RefreshCw, RotateCcw, Sparkles, Star,
  Trash2, X,
} from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { api, downloadUrl, fileDownloadUrl, uploadFiles } from '../lib/api'
import { copyText, formatBytes, formatDate, formatDuration } from '../lib/format'
import { useLocation } from '../lib/router'
import { changed, emitAsset, getMeta, toast, toastError, useAssetUpdates, useQuery } from '../lib/store'
import { canRenderThumb, ensureThumb } from '../lib/thumbs'
import type { Asset, AssetPatch } from '../lib/types'
import { Preview } from '../preview/Preview'
import { Button, Chip, Dialog, Field, Menu, Spinner, TagInput, TypeBadge, useConfirm } from '../ui'
import { openAsset, setFilters } from './filters'
import { AssetPanelsBottom, AssetPanelsTop } from './panels'

export function AssetDrawer({ id }: { id: string }) {
  const loc = useLocation()
  const confirm = useConfirm()
  const meta = getMeta()
  const [asset, setAsset] = useState<Asset | null>(null)
  const [error, setError] = useState('')
  const [editing, setEditing] = useState(false)
  const [fileId, setFileId] = useState<string | null>(null)
  const [busy, setBusy] = useState('')
  const [noteFor, setNoteFor] = useState<{ id: string; name: string; note: string } | null>(null)
  const filePicker = useRef<HTMLInputElement>(null)
  const thumbPicker = useRef<HTMLInputElement>(null)

  // Keyboard users land in the drawer when it opens and return to where they
  // came from when it closes, instead of tabbing through the whole library.
  const [opener] = useState(() => document.activeElement as HTMLElement | null)
  const loaded = asset !== null
  useEffect(() => {
    document.querySelector<HTMLElement>('.drawer')?.focus()
  }, [loaded, error])
  useEffect(() => () => opener?.focus?.(), [opener])

  const close = () => openAsset(loc, null)
  const apply = (a: Asset) => {
    setAsset(a)
    emitAsset(a)
  }

  useEffect(() => {
    let live = true
    setAsset(null)
    setError('')
    setEditing(false)
    setFileId(null)
    api.asset(id).then(
      (a) => {
        if (!live) return
        setAsset(a)
        void ensureThumb(a)
      },
      (e) => live && setError(e.message),
    )
    return () => {
      live = false
    }
  }, [id])

  // Background updates (rendered preview, AI agent) for the open asset.
  useAssetUpdates((a) => a.id === id && setAsset((cur) => (cur ? { ...cur, ...a, collections: a.collections ?? cur.collections, projects: a.projects ?? cur.projects } : cur)))
  const aiBusy = asset?.ai_status === 'queued' || asset?.ai_status === 'running'
  useEffect(() => {
    if (!aiBusy) return
    const t = setInterval(() => api.asset(id).then(setAsset, () => {}), 3000)
    return () => clearInterval(t)
  }, [aiBusy, id])

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && !document.querySelector('.overlay, .menu') && !(e.target instanceof Element && e.target.closest('input, textarea, select'))) close()
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }) // eslint-disable-line react-hooks/exhaustive-deps

  const run = async (label: string, fn: () => Promise<Asset | void>, done?: string) => {
    setBusy(label)
    try {
      const a = await fn()
      if (a) apply(a)
      if (done) toast(done)
    } catch (e) {
      toastError(e)
    } finally {
      setBusy('')
    }
  }
  const patch = (p: AssetPatch, done?: string) =>
    run('save', async () => {
      const a = await api.update(id, p)
      if (p.add_collections || p.remove_collections || p.add_projects || p.remove_projects) changed('groups')
      return a
    }, done)

  if (error) {
    return (
      <aside className="drawer" tabIndex={-1}>
        <div className="drawer-head">
          <h3 className="grow">Asset not found</h3>
          <Button variant="ghost" iconOnly icon={X} onClick={close} aria-label="Close" />
        </div>
        <div className="drawer-body muted">{error}</div>
      </aside>
    )
  }
  if (!asset) {
    return (
      <aside className="drawer" tabIndex={-1}>
        <div className="drawer-head">
          <span className="grow" />
          <Button variant="ghost" iconOnly icon={X} onClick={close} aria-label="Close" />
        </div>
        <Spinner />
      </aside>
    )
  }

  const file = asset.files.find((f) => f.id === (fileId ?? asset.primary_file_id)) ?? asset.files[0]
  const primary = asset.files.find((f) => f.id === asset.primary_file_id)
  const trashed = !!asset.deleted_at
  const attrs = Object.entries(asset.attrs ?? {})

  return (
    <aside className="drawer" tabIndex={-1} aria-label={`Asset ${asset.name}`}>
      <div className="drawer-head">
        <div className="grow">
          <h3 className="truncate" title={asset.name}>
            {asset.name || 'Untitled'}
          </h3>
          <div className="row-wrap" style={{ marginTop: 4 }}>
            <TypeBadge type={asset.type} />
            <span className="faint">
              {formatBytes(asset.total_size)}
              {asset.file_count > 1 ? ` · ${asset.file_count} files` : ''}
            </span>
            {aiBusy && (
              <span className="badge" style={{ '--badge': 'var(--accent)' } as React.CSSProperties}>
                AI working…
              </span>
            )}
          </div>
        </div>
        {asset.files.length > 0 && (
          <a className="btn btn-primary" href={downloadUrl(asset.id)} download title={asset.files.length > 1 ? 'Download all files as zip' : 'Download'} aria-label="Download">
            <Download aria-hidden />
            <span className="hide-sm">Download</span>
          </a>
        )}
        <Menu
          trigger={(p) => <Button iconOnly icon={Ellipsis} aria-label="More actions" {...p} />}
          items={[
            { label: 'Copy ID', icon: Copy, onSelect: async () => (await copyText(asset.id)) && toast('ID copied') },
            { label: 'Copy link', icon: ExternalLink, onSelect: async () => (await copyText(`${location.origin}/assets/${asset.id}`)) && toast('Link copied') },
            { label: 'Add files…', icon: FilePlus2, onSelect: () => filePicker.current?.click(), disabled: trashed },
            { label: 'Set custom thumbnail…', icon: ImageUp, onSelect: () => thumbPicker.current?.click(), disabled: trashed },
            {
              label: 'Re-render preview', icon: RefreshCw, disabled: trashed || !canRenderThumb(primary),
              onSelect: () =>
                run('thumb', async () => {
                  const updated = await ensureThumb(asset, true)
                  if (!updated) throw new Error('The preview could not be rendered in this browser')
                  return updated
                }, 'Preview updated'),
            },
            { label: '', separator: true },
            trashed
              ? { label: 'Restore from trash', icon: RotateCcw, onSelect: () => run('restore', async () => { await api.bulk([id], 'restore'); changed('assets'); close() }, 'Restored') }
              : { label: 'Move to trash', icon: Trash2, danger: true, onSelect: () => run('trash', async () => { await api.trash(id); changed('assets'); close() }, 'Moved to trash') },
            ...(trashed
              ? [{
                  label: 'Delete permanently', icon: Trash2, danger: true,
                  onSelect: async () => {
                    if (await confirm({ title: 'Delete permanently?', message: `“${asset.name}” and its files will be removed from disk. This cannot be undone.`, confirmLabel: 'Delete forever', danger: true }))
                      run('purge', async () => { await api.bulk([id], 'purge'); changed('assets'); close() }, 'Deleted')
                  },
                }]
              : []),
          ]}
        />
        <Button variant="ghost" iconOnly icon={X} onClick={close} aria-label="Close" />
      </div>

      <div className="drawer-body">
        <input ref={filePicker} type="file" multiple hidden onChange={(e) => {
          const files = [...(e.target.files ?? [])]
          e.target.value = ''
          if (files.length) run('files', async () => (await uploadFiles(id, files).promise).asset, 'Files added')
        }} />
        <input ref={thumbPicker} type="file" accept="image/png,image/jpeg,image/webp,image/gif" hidden onChange={(e) => {
          const f = e.target.files?.[0]
          e.target.value = ''
          if (f) run('thumb', () => api.putThumbnail(id, f), 'Thumbnail updated')
        }} />

        {trashed && (
          <div className="note note-warn">
            In the trash since {formatDate(asset.deleted_at!)}.
          </div>
        )}
        {asset.ai_status === 'error' && <div className="note note-danger">AI agent failed: {asset.ai_error}</div>}

        <Preview asset={asset} file={file} />

        <AssetPanelsTop asset={asset} onChange={apply} />

        {editing ? (
          <EditForm asset={asset} onCancel={() => setEditing(false)} onSaved={(a) => { apply(a); setEditing(false) }} />
        ) : (
          <section className="stack">
            <div className="row">
              <p className="eyebrow grow">Details</p>
              {!trashed && meta.ai_enabled && (
                <Button
                  size="sm"
                  icon={Sparkles}
                  busy={busy === 'ai'}
                  title="Let the AI agent fill in missing name, description and tags"
                  onClick={() => run('ai', async () => { await api.analyze([id]); return api.asset(id) }, 'AI agent started')}
                >
                  AI fill
                </Button>
              )}
              {!trashed && (
                <Button size="sm" icon={Pencil} onClick={() => setEditing(true)}>
                  Edit
                </Button>
              )}
            </div>
            {asset.description ? <p className="prose">{asset.description}</p> : <p className="faint">No description</p>}
            <div className="row-wrap">
              {asset.tags.length === 0 && <span className="faint">No tags</span>}
              {asset.tags.map((t) => (
                <Chip key={t} onClick={() => setFilters({ ...loc, path: loc.path.startsWith('/assets/') ? '/' : loc.path }, { tags: [t] })} title={`Show all tagged ${t}`}>
                  #{t}
                </Chip>
              ))}
            </div>
            <dl className="props">
              {asset.prompt && (
                <>
                  <dt>Prompt</dt>
                  <dd>
                    <div className="prompt-box">
                      {asset.prompt}
                      <Button size="sm" variant="ghost" iconOnly icon={Copy} aria-label="Copy prompt" onClick={async () => (await copyText(asset.prompt)) && toast('Prompt copied')} />
                    </div>
                  </dd>
                </>
              )}
              <dt>Source</dt>
              <dd>
                {asset.source || <span className="faint">–</span>}{' '}
                {asset.source_url && (
                  <a href={asset.source_url} target="_blank" rel="noreferrer noopener">
                    link ↗
                  </a>
                )}
              </dd>
              <dt>License</dt>
              <dd>
                {asset.license || <span className="faint">not recorded</span>}{' '}
                {asset.license_url && (
                  <a href={asset.license_url} target="_blank" rel="noreferrer noopener">
                    terms ↗
                  </a>
                )}
              </dd>
              {asset.attribution && (
                <>
                  <dt>Attribution</dt>
                  <dd>{asset.attribution}</dd>
                </>
              )}
            </dl>
          </section>
        )}

        <section className="stack">
          <p className="eyebrow">Collections</p>
          <div className="row-wrap">
            {(asset.collections ?? []).map((c) => (
              <Chip key={c.id} onRemove={trashed ? undefined : () => patch({ remove_collections: [c.id] })}>
                <FolderOpen aria-hidden /> {c.name}
              </Chip>
            ))}
            {!trashed && <GroupAdder kind="collection" exclude={(asset.collections ?? []).map((c) => c.id)} onAdd={(gid) => patch({ add_collections: [gid] })} />}
          </div>
          <p className="eyebrow">Used in projects</p>
          <div className="row-wrap">
            {(asset.projects ?? []).map((p) => (
              <Chip key={p.id} onRemove={trashed ? undefined : () => patch({ remove_projects: [p.id] })} onClick={trashed ? undefined : () => setNoteFor({ id: p.id, name: p.name, note: p.note ?? '' })} title="Edit usage note">
                <Gamepad2 aria-hidden /> {p.name}
                {p.note ? <span className="faint"> — {p.note}</span> : null}
              </Chip>
            ))}
            {!trashed && <GroupAdder kind="project" exclude={(asset.projects ?? []).map((p) => p.id)} onAdd={(gid) => patch({ add_projects: [gid] })} />}
          </div>
        </section>

        <section className="stack">
          <p className="eyebrow">Files</p>
          <ul className="file-list">
            {asset.files.map((f) => (
              <li key={f.id} data-active={f.id === file?.id}>
                <button className="file-main" onClick={() => setFileId(f.id)} title="Preview this file">
                  <span className="truncate">{f.filename}</span>
                  <span className="faint mono">
                    {formatBytes(f.size)}
                    {f.width ? ` · ${f.width}×${f.height}` : ''}
                  </span>
                </button>
                {f.id === asset.primary_file_id ? (
                  <span className="badge badge-plain" title="Drives preview and thumbnail">
                    primary
                  </span>
                ) : (
                  !trashed && <Button size="sm" variant="ghost" iconOnly icon={Star} aria-label="Make primary" title="Make primary" onClick={() => run('primary', () => api.setPrimary(id, f.id))} />
                )}
                <a className="btn btn-ghost btn-sm btn-icon" href={fileDownloadUrl(asset.id, f.filename)} download aria-label={`Download ${f.filename}`} title="Download">
                  <Download aria-hidden />
                </a>
                {!trashed && (
                  <Button
                    size="sm"
                    variant="ghost"
                    iconOnly
                    icon={Trash2}
                    aria-label={`Delete ${f.filename}`}
                    title="Delete file"
                    onClick={async () => {
                      if (await confirm({ title: 'Delete file?', message: `${f.filename} will be removed from this asset and from disk.`, confirmLabel: 'Delete file', danger: true }))
                        run('file', () => api.deleteFile(id, f.id))
                    }}
                  />
                )}
              </li>
            ))}
          </ul>
          {!trashed && (
            <div>
              <Button size="sm" icon={Plus} busy={busy === 'files'} onClick={() => filePicker.current?.click()}>
                Add files
              </Button>
            </div>
          )}
        </section>

        <AssetPanelsBottom asset={asset} onChange={apply} />

        <section className="stack">
          <p className="eyebrow">Info</p>
          <dl className="props">
            <dt>ID</dt>
            <dd className="mono">{asset.id}</dd>
            <dt>Added</dt>
            <dd>{formatDate(asset.created_at)}</dd>
            <dt>Changed</dt>
            <dd>{formatDate(asset.updated_at)}</dd>
            {attrs.map(([k, v]) => (
              <FragmentRow key={k} k={k} v={v} />
            ))}
            {file && (
              <>
                <dt>SHA-256</dt>
                <dd className="mono truncate" title={file.sha256}>
                  {file.sha256.slice(0, 20)}…
                </dd>
              </>
            )}
          </dl>
        </section>
      </div>

      {noteFor && (
        <NoteDialog
          project={noteFor}
          onClose={() => setNoteFor(null)}
          onSave={(note) => {
            setNoteFor(null)
            // Re-adding with a note updates it; an empty note is cleared by re-linking.
            run('note', async () => {
              if (note === '') await api.update(id, { remove_projects: [noteFor.id] })
              return api.update(id, { add_projects: [noteFor.id], project_note: note })
            })
          }}
        />
      )}
    </aside>
  )
}

function FragmentRow({ k, v }: { k: string; v: unknown }) {
  const label = k.replace(/_/g, ' ')
  const value = k === 'duration_s' && typeof v === 'number' ? `${formatDuration(v)} (${v}s)` : typeof v === 'object' ? JSON.stringify(v) : String(v)
  return (
    <>
      <dt style={{ textTransform: 'capitalize' }}>{label}</dt>
      <dd className="truncate" title={value}>
        {value}
      </dd>
    </>
  )
}

function NoteDialog({ project, onClose, onSave }: { project: { name: string; note: string }; onClose: () => void; onSave: (note: string) => void }) {
  const [note, setNote] = useState(project.note)
  return (
    <Dialog
      title={`Usage in ${project.name}`}
      onClose={onClose}
      width={440}
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" onClick={() => onSave(note.trim())}>
            Save
          </Button>
        </>
      }
    >
      <Field label="Usage note" hint="How or where this asset is used in the project">
        <input className="input" value={note} onChange={(e) => setNote(e.target.value)} placeholder="e.g. main menu background" autoFocus onKeyDown={(e) => e.key === 'Enter' && onSave(note.trim())} />
      </Field>
    </Dialog>
  )
}

function GroupAdder({ kind, exclude, onAdd }: { kind: 'collection' | 'project'; exclude: string[]; onAdd: (id: string) => void }) {
  const groups = useQuery<{ id: string; name: string }[]>(() => (kind === 'collection' ? api.collections() : api.projects()), [kind])
  const [creating, setCreating] = useState(false)
  const [name, setName] = useState('')
  const available = (groups.data ?? []).filter((g) => !exclude.includes(g.id))
  const create = async () => {
    if (!name.trim()) return
    try {
      const g = kind === 'collection' ? await api.saveCollection(null, { name }) : await api.saveProject(null, { name })
      changed('groups')
      groups.reload()
      onAdd(g.id)
      setCreating(false)
      setName('')
    } catch (e) {
      toastError(e)
    }
  }
  return (
    <>
      <Menu
        align="left"
        trigger={(p) => (
          <button type="button" className="chip" {...p}>
            <Plus aria-hidden /> Add
          </button>
        )}
        items={[
          ...available.map((g) => ({ label: g.name, icon: kind === 'collection' ? FolderOpen : Gamepad2, onSelect: () => onAdd(g.id) })),
          ...(available.length ? [{ label: '', separator: true }] : []),
          { label: `New ${kind}…`, icon: Plus, onSelect: () => setCreating(true) },
        ]}
      />
      {creating && (
        <Dialog
          title={`New ${kind}`}
          onClose={() => setCreating(false)}
          width={420}
          footer={
            <>
              <Button onClick={() => setCreating(false)}>Cancel</Button>
              <Button variant="primary" onClick={create} disabled={!name.trim()}>
                Create and add
              </Button>
            </>
          }
        >
          <Field label="Name">
            <input className="input" value={name} onChange={(e) => setName(e.target.value)} autoFocus onKeyDown={(e) => e.key === 'Enter' && create()} />
          </Field>
        </Dialog>
      )}
    </>
  )
}

function EditForm({ asset, onCancel, onSaved }: { asset: Asset; onCancel: () => void; onSaved: (a: Asset) => void }) {
  const meta = getMeta()
  const [f, setF] = useState({
    name: asset.name, type: asset.type, description: asset.description, tags: asset.tags, prompt: asset.prompt, source: asset.source,
    source_url: asset.source_url, license: asset.license, license_url: asset.license_url, attribution: asset.attribution,
  })
  const [saving, setSaving] = useState(false)
  const [suggesting, setSuggesting] = useState(false)
  const set = <K extends keyof typeof f>(k: K, v: (typeof f)[K]) => setF((cur) => ({ ...cur, [k]: v }))

  const save = async () => {
    setSaving(true)
    try {
      // Send only what was changed in this form. Writing every field back
      // would overwrite edits made meanwhile by the AI agent or over MCP.
      const patch: AssetPatch = {}
      for (const k of ['name', 'type', 'description', 'prompt', 'source', 'source_url', 'license', 'license_url', 'attribution'] as const) {
        if (f[k] !== asset[k]) patch[k] = f[k]
      }
      const added = f.tags.filter((t) => !asset.tags.includes(t))
      const removed = asset.tags.filter((t) => !f.tags.includes(t))
      if (added.length) patch.add_tags = added
      if (removed.length) patch.remove_tags = removed
      onSaved(await api.update(asset.id, patch))
      toast('Saved')
    } catch (e) {
      toastError(e)
    } finally {
      setSaving(false)
    }
  }
  const suggest = async () => {
    setSuggesting(true)
    try {
      const s = await api.suggest(asset.id)
      setF((cur) => ({
        ...cur,
        name: s.name || cur.name,
        description: s.description || cur.description,
        type: s.type || cur.type,
        tags: [...new Set([...cur.tags, ...s.tags])],
      }))
      toast('Suggestions filled in — review and save')
    } catch (e) {
      toastError(e)
    } finally {
      setSuggesting(false)
    }
  }

  return (
    <form
      className="stack"
      onSubmit={(e) => {
        e.preventDefault()
        save()
      }}
    >
      <div className="row">
        <p className="eyebrow grow">Edit details</p>
        {meta.ai_enabled && (
          <Button size="sm" icon={Sparkles} busy={suggesting} onClick={suggest} title="Ask the AI agent for name, description and tags">
            Suggest with AI
          </Button>
        )}
      </div>
      <Field label="Name">
        <input className="input" value={f.name} onChange={(e) => set('name', e.target.value)} required />
      </Field>
      <Field label="Type">
        <select className="select" value={f.type} onChange={(e) => set('type', e.target.value)}>
          {meta.types.map((t) => (
            <option key={t.id} value={t.id}>
              {t.label}
            </option>
          ))}
        </select>
      </Field>
      <Field label="Description">
        <textarea className="textarea" rows={3} value={f.description} onChange={(e) => set('description', e.target.value)} />
      </Field>
      <Field label="Tags">
        <TagInput value={f.tags} onChange={(t) => set('tags', t)} />
      </Field>
      <Field label="Generation prompt">
        <textarea className="textarea" rows={3} value={f.prompt} onChange={(e) => set('prompt', e.target.value)} placeholder="The prompt this asset was generated with" />
      </Field>
      <div className="field-row">
        <Field label="Source">
          <input className="input" value={f.source} onChange={(e) => set('source', e.target.value)} placeholder="Generator, store, artist…" />
        </Field>
        <Field label="Source URL">
          <input className="input" type="url" value={f.source_url} onChange={(e) => set('source_url', e.target.value)} placeholder="https://…" />
        </Field>
      </div>
      <div className="field-row">
        <Field label="License">
          <input className="input" list="license-list" value={f.license} onChange={(e) => set('license', e.target.value)} placeholder="e.g. CC0-1.0" />
        </Field>
        <Field label="License URL">
          <input className="input" type="url" value={f.license_url} onChange={(e) => set('license_url', e.target.value)} placeholder="https://…" />
        </Field>
      </div>
      <Field label="Attribution" hint="Credit line to show when the asset is used">
        <input className="input" value={f.attribution} onChange={(e) => set('attribution', e.target.value)} />
      </Field>
      <div className="row" style={{ justifyContent: 'flex-end' }}>
        <Button onClick={onCancel}>Cancel</Button>
        <Button variant="primary" type="submit" busy={saving}>
          Save
        </Button>
      </div>
    </form>
  )
}
