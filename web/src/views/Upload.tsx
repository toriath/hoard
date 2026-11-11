import { CircleAlert, CircleCheck, FilePlus2, Upload as UploadIcon, X } from 'lucide-react'
import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { api, uploadAsset } from '../lib/api'
import { importZip } from '../lib/api-library'
import type { ImportMode } from '../lib/types-library'
import { formatBytes, plural } from '../lib/format'
import { changed, getMeta, toast, useQuery } from '../lib/store'
import { useConfirm } from '../ui'
import { ensureThumb } from '../lib/thumbs'
import type { AssetPatch } from '../lib/types'
import { Button, Dialog, Field, TagInput } from '../ui'

interface Defaults {
  collection?: string
  project?: string
}
interface UploadApi {
  open: (files?: File[], defaults?: Defaults) => void
}
const Ctx = createContext<UploadApi>({ open: () => {} })
export const useUpload = () => useContext(Ctx)

const MAX_DROP = 2000

/** Reads dropped items, descending into folders. */
async function filesFromDrop(dt: DataTransfer): Promise<File[]> {
  const entries = [...dt.items].map((i) => i.webkitGetAsEntry?.()).filter((e): e is FileSystemEntry => !!e)
  if (entries.length === 0) return [...dt.files]
  const out: File[] = []
  const walk = async (entry: FileSystemEntry): Promise<void> => {
    if (out.length >= MAX_DROP) return
    if (entry.isFile) {
      const f = await new Promise<File>((res, rej) => (entry as FileSystemFileEntry).file(res, rej))
      if (!f.name.startsWith('.')) out.push(f)
    } else if (entry.isDirectory) {
      const reader = (entry as FileSystemDirectoryEntry).createReader()
      for (;;) {
        const batch = await new Promise<FileSystemEntry[]>((res, rej) => reader.readEntries(res, rej))
        if (batch.length === 0) break
        for (const e of batch) await walk(e)
      }
    }
  }
  for (const e of entries) await walk(e)
  return out
}

export function UploadProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState<{ files: File[]; defaults: Defaults } | null>(null)
  const [dragging, setDragging] = useState(false)
  const depth = useRef(0)
  const open = useCallback((files: File[] = [], defaults: Defaults = {}) => setState({ files, defaults }), [])
  const value = useMemo(() => ({ open }), [open])

  // Drop files anywhere to start an upload.
  useEffect(() => {
    const hasFiles = (e: DragEvent) => !!e.dataTransfer?.types.includes('Files')
    const enter = (e: DragEvent) => {
      if (!hasFiles(e)) return
      depth.current++
      setDragging(true)
    }
    const leave = (e: DragEvent) => {
      if (!hasFiles(e)) return
      depth.current = Math.max(0, depth.current - 1)
      if (depth.current === 0) setDragging(false)
    }
    const over = (e: DragEvent) => hasFiles(e) && e.preventDefault()
    const drop = async (e: DragEvent) => {
      if (!hasFiles(e) || !e.dataTransfer) return
      e.preventDefault()
      depth.current = 0
      setDragging(false)
      try {
        const files = await filesFromDrop(e.dataTransfer)
        if (files.length >= MAX_DROP) toast(`Only the first ${MAX_DROP} files were taken; add the rest in a second upload`, 'error')
        if (files.length) setState((s) => (s ? { ...s, files: [...s.files, ...files] } : { files, defaults: {} }))
      } catch {
        toast('Those files could not be read', 'error')
      }
    }
    window.addEventListener('dragenter', enter)
    window.addEventListener('dragleave', leave)
    window.addEventListener('dragover', over)
    window.addEventListener('drop', drop)
    return () => {
      window.removeEventListener('dragenter', enter)
      window.removeEventListener('dragleave', leave)
      window.removeEventListener('dragover', over)
      window.removeEventListener('drop', drop)
    }
  }, [])

  return (
    <Ctx.Provider value={value}>
      {children}
      {dragging && !state && (
        <div className="dropzone-overlay">
          <div>
            <UploadIcon aria-hidden />
            <h2>Drop to add to your hoard</h2>
          </div>
        </div>
      )}
      {state && <UploadDialog key="upload" initial={state.files} defaults={state.defaults} externalFiles={state.files} onClose={() => setState(null)} />}
    </Ctx.Provider>
  )
}

type ItemStatus = 'waiting' | 'uploading' | 'done' | 'error'
interface Item {
  files: File[]
  label: string
  size: number
  status: ItemStatus
  progress: number
  error?: string
  duplicate?: boolean
  similar?: boolean
  /** Result of a pack import. */
  summary?: string
  notes?: string[]
}

function UploadDialog({ initial, externalFiles, defaults, onClose }: { initial: File[]; externalFiles: File[]; defaults: Defaults; onClose: () => void }) {
  const meta = getMeta()
  const collections = useQuery(() => api.collections(), [])
  const projects = useQuery(() => api.projects(), [])
  const [files, setFiles] = useState<File[]>(initial)
  const [mode, setMode] = useState<'separate' | 'single'>('separate')
  const [type, setType] = useState('')
  const [name, setName] = useState('')
  const [tags, setTags] = useState<string[]>([])
  const [license, setLicense] = useState('')
  const [source, setSource] = useState('')
  const [prompt, setPrompt] = useState('')
  const [collection, setCollection] = useState(defaults.collection ?? '')
  const [project, setProject] = useState(defaults.project ?? '')
  const [items, setItems] = useState<Item[] | null>(null)
  // A single zip archive can be imported as a pack instead of stored as one file.
  const [zipChoice, setZipChoice] = useState<'import' | 'store'>('import')
  const [importMode, setImportMode] = useState<ImportMode>('per_file')
  const [packName, setPackName] = useState('')
  const [running, setRunning] = useState(false)
  const picker = useRef<HTMLInputElement>(null)
  const abort = useRef<(() => void) | null>(null)

  // Files dropped while the dialog is open are appended by the provider.
  const seen = useRef(initial.length)
  const started = useRef(false)
  useEffect(() => {
    const fresh = externalFiles.slice(seen.current)
    seen.current = externalFiles.length
    if (fresh.length === 0) return
    if (started.current) toast('This upload has already started; add those files afterwards', 'error')
    else setFiles((cur) => [...cur, ...fresh])
  }, [externalFiles])
  const confirm = useConfirm()

  const isZip = files.length === 1 && /\.zip$/i.test(files[0].name)
  const importing = isZip && zipChoice === 'import'
  const tooBig = files.filter((f) => f.size > meta.max_upload_bytes)
  const totalSize = files.reduce((n, f) => n + f.size, 0)

  const startImport = async () => {
    const file = files[0]
    started.current = true
    setItems([{ files, label: file.name, size: file.size, status: 'uploading', progress: 0 }])
    setRunning(true)
    const patch = (p: Partial<Item>) => setItems((cur) => cur && cur.map((it) => ({ ...it, ...p })))
    const h = importZip(
      file,
      { mode: importMode, collection_name: packName || undefined, license: license || undefined, tags: tags.length ? tags : undefined, project_id: project || undefined },
      (f) => patch({ progress: f }),
    )
    abort.current = h.abort
    try {
      const res = await h.promise
      const notes: string[] = []
      if (res.attribution) notes.push(`Attribution found: ${res.attribution}`)
      if (res.license_source && res.license_source !== 'options') notes.push(`License ${res.license} read from ${res.license_source}`)
      for (const f of res.failed ?? []) notes.push(`${f.name}: ${f.error}`)
      patch({ status: 'done', progress: 1, summary: res.summary, notes })
      changed('assets', 'groups')
    } catch (e) {
      patch({ status: 'error', error: e instanceof Error ? e.message : 'failed' })
    }
    abort.current = null
    setRunning(false)
  }

  const start = async () => {
    if (importing) return startImport()
    const shared: AssetPatch = {}
    if (type) shared.type = type
    if (tags.length) shared.add_tags = tags
    if (license) shared.license = license
    if (source) shared.source = source
    if (prompt) shared.prompt = prompt
    if (collection) shared.add_collections = [collection]
    if (project) shared.add_projects = [project]

    const queue: Item[] =
      mode === 'single'
        ? [{ files, label: name || files[0].name, size: totalSize, status: 'waiting', progress: 0 }]
        : files.map((f) => ({ files: [f], label: f.name, size: f.size, status: 'waiting', progress: 0 }))
    started.current = true
    setItems(queue)
    setRunning(true)
    const patch = (i: number, p: Partial<Item>) => setItems((cur) => cur && cur.map((it, j) => (j === i ? { ...it, ...p } : it)))

    let ok = 0
    let duplicates = 0
    let cancelled = false
    for (let i = 0; i < queue.length && !cancelled; i++) {
      patch(i, { status: 'uploading' })
      const itemMeta = mode === 'single' && name ? { ...shared, name } : shared
      const h = uploadAsset(queue[i].files, itemMeta, (f) => patch(i, { progress: f }))
      abort.current = () => {
        cancelled = true
        h.abort()
      }
      try {
        const res = await h.promise
        ok++
        if (res.duplicate_of?.length || res.similar_to?.length) duplicates++
        patch(i, { status: 'done', progress: 1, duplicate: !!res.duplicate_of?.length, similar: !res.duplicate_of?.length && !!res.similar_to?.length })
        void ensureThumb(res.asset)
      } catch (e) {
        patch(i, { status: 'error', error: e instanceof Error ? e.message : 'failed' })
      }
    }
    abort.current = null
    setRunning(false)
    changed('assets', 'groups')
    // Close on a clean run; stay open when there is something to read.
    if (ok === queue.length && duplicates === 0) {
      toast(`Added ${plural(ok, 'asset')}`)
      onClose()
    }
  }

  const close = async () => {
    if (abort.current) {
      const stop = await confirm({
        title: 'Cancel the upload?',
        message: 'Files that are already uploaded stay in the library; the rest is not uploaded.',
        confirmLabel: 'Cancel upload',
        danger: true,
      })
      if (!stop) return
      abort.current?.()
    }
    onClose()
  }

  // In one asset, files with the same name replace each other on the server.
  const names = files.map((f) => f.name.toLowerCase())
  const clashing = mode === 'single' ? new Set(names.filter((n, i) => names.indexOf(n) !== i)).size : 0

  if (items) {
    const done = items.filter((i) => i.status === 'done').length
    const failed = items.filter((i) => i.status === 'error').length
    return (
      <Dialog
        title={running ? `Uploading ${done + 1 > items.length ? items.length : done + 1} of ${items.length}…` : `Uploaded ${done} of ${items.length}`}
        onClose={close}
        width={560}
        footer={<Button onClick={close}>{running ? 'Cancel' : 'Close'}</Button>}
      >
        <div className="stack">
          {failed > 0 && !running && <div className="note note-danger">{plural(failed, 'upload')} failed. The rest were added.</div>}
          {!running && failed === 0 && items.some((i) => i.duplicate) && (
            <div className="note note-warn">Everything was added, but some files are identical or near-identical to ones already in the library.</div>
          )}
          <ul className="upload-list">
            {items.map((it, i) => (
              <li key={i}>
                <div className="row">
                  <span className="grow truncate">{it.label}</span>
                  <span className="faint mono">{formatBytes(it.size)}</span>
                  {it.status === 'done' && <CircleCheck className="ok" aria-label="Done" />}
                  {it.status === 'error' && <CircleAlert className="bad" aria-label="Failed" />}
                </div>
                {it.status === 'uploading' && (
                  <div className="progress">
                    <i style={{ width: `${Math.round(it.progress * 100)}%` }} />
                  </div>
                )}
                {it.error && <div className="error">{it.error}</div>}
                {it.duplicate && <div className="hint">Identical file already exists in the library</div>}
                {it.similar && <div className="hint">A near-identical picture already exists in the library</div>}
                {it.summary && <div className="hint">{it.summary}</div>}
                {it.notes?.map((n, k) => (
                  <div key={k} className="hint faint">
                    {n}
                  </div>
                ))}
              </li>
            ))}
          </ul>
        </div>
      </Dialog>
    )
  }

  return (
    <Dialog
      title="Add assets"
      onClose={close}
      width={620}
      footer={
        <>
          <Button onClick={close}>Cancel</Button>
          <Button variant="primary" icon={UploadIcon} disabled={files.length === 0 || tooBig.length > 0} onClick={start}>
            {importing ? 'Import pack' : `Upload ${files.length > 0 ? (mode === 'single' ? '1 asset' : plural(files.length, 'asset')) : ''}`}
          </Button>
        </>
      }
    >
      <div className="stack">
        <input ref={picker} type="file" multiple hidden onChange={(e) => {
          setFiles((cur) => [...cur, ...(e.target.files ?? [])])
          e.target.value = ''
        }} />
        <button type="button" className="dropzone" onClick={() => picker.current?.click()}>
          <FilePlus2 aria-hidden />
          <span>
            <b>Choose files</b> or drop them anywhere
          </span>
          <span className="faint">Any format, up to {formatBytes(meta.max_upload_bytes)} per file</span>
        </button>

        {files.length > 0 && (
          <>
            <ul className="upload-list compact">
              {files.slice(0, 200).map((f, i) => (
                <li key={i} className="row">
                  <span className="grow truncate">{f.name}</span>
                  <span className={f.size > meta.max_upload_bytes ? 'error mono' : 'faint mono'}>{formatBytes(f.size)}</span>
                  <Button variant="ghost" size="sm" iconOnly icon={X} aria-label={`Remove ${f.name}`} onClick={() => setFiles(files.filter((_, j) => j !== i))} />
                </li>
              ))}
              {files.length > 200 && <li className="faint">… and {files.length - 200} more</li>}
            </ul>
            <div className="muted">
              {plural(files.length, 'file')} · {formatBytes(totalSize)}
            </div>
            {tooBig.length > 0 && <div className="note note-danger">{plural(tooBig.length, 'file')} exceed the upload limit. Remove them to continue.</div>}

            {isZip && (
              <div className="seg-choice" role="radiogroup" aria-label="What to do with the zip archive">
                <button role="radio" aria-checked={zipChoice === 'import'} onClick={() => setZipChoice('import')}>
                  <b>Import as pack</b>
                  <span>Unpack it into assets in a new collection; the licence is read from the pack’s files</span>
                </button>
                <button role="radio" aria-checked={zipChoice === 'store'} onClick={() => setZipChoice('store')}>
                  <b>Store the zip as one file</b>
                  <span>Keep the archive itself as a single asset</span>
                </button>
              </div>
            )}
            {importing && (
              <>
                <Field label="Assets from the pack">
                  <select className="select" value={importMode} onChange={(e) => setImportMode(e.target.value as ImportMode)}>
                    <option value="per_file">One asset per file</option>
                    <option value="per_folder">One asset per top-level folder</option>
                    <option value="single">One asset for the whole pack</option>
                  </select>
                </Field>
                <Field label="Collection name" hint="Default: the name of the zip file">
                  <input className="input" value={packName} onChange={(e) => setPackName(e.target.value)} placeholder={files[0].name.replace(/\.zip$/i, '')} />
                </Field>
              </>
            )}
            {files.length > 1 && (
              <div className="seg-choice" role="radiogroup" aria-label="How to group the files">
                <button role="radio" aria-checked={mode === 'separate'} onClick={() => setMode('separate')}>
                  <b>One asset per file</b>
                  <span>Independent sprites, sounds, models…</span>
                </button>
                <button role="radio" aria-checked={mode === 'single'} onClick={() => setMode('single')}>
                  <b>One asset with {files.length} files</b>
                  <span>A model with textures, a sprite sheet with its atlas…</span>
                </button>
              </div>
            )}
            {clashing > 0 && (
              <div className="note note-warn">
                {plural(clashing, 'file name')} occur more than once. Within one asset a later file replaces an earlier one of the same name, and folder structure is not kept.
              </div>
            )}
            {mode === 'single' && files.length > 1 && (
              <Field label="Asset name">
                <input className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder={files[0].name.replace(/\.[^.]+$/, '')} />
              </Field>
            )}

            <hr className="divider" style={{ margin: 0 }} />
            <p className="eyebrow">Applies to {importing ? 'all imported assets' : mode === 'single' ? 'the asset' : 'all uploaded assets'} — optional</p>
            <Field label="Tags">
              <TagInput value={tags} onChange={setTags} />
            </Field>
            <div className="field-row">
              {!importing && <Field label="Type">
                <select className="select" value={type} onChange={(e) => setType(e.target.value)}>
                  <option value="">Detect automatically</option>
                  {meta.types.map((t) => (
                    <option key={t.id} value={t.id}>
                      {t.label}
                    </option>
                  ))}
                </select>
              </Field>}
              <Field label={importing ? 'License (overrides the one found in the pack)' : 'License'}>
                <input className="input" list="license-list" value={license} onChange={(e) => setLicense(e.target.value)} placeholder={importing ? 'detected from the pack' : 'e.g. CC0-1.0'} />
              </Field>
            </div>
            <div className="field-row">
              {!importing && <Field label="Collection">
                <select className="select" value={collection} onChange={(e) => setCollection(e.target.value)}>
                  <option value="">None</option>
                  {(collections.data ?? []).map((c) => (
                    <option key={c.id} value={c.id}>
                      {c.name}
                    </option>
                  ))}
                </select>
              </Field>}
              <Field label="Used in project">
                <select className="select" value={project} onChange={(e) => setProject(e.target.value)}>
                  <option value="">None</option>
                  {(projects.data ?? []).map((p) => (
                    <option key={p.id} value={p.id}>
                      {p.name}
                    </option>
                  ))}
                </select>
              </Field>
            </div>
            {!importing && <><Field label="Source" hint="Generator, marketplace, artist…">
              <input className="input" value={source} onChange={(e) => setSource(e.target.value)} placeholder="e.g. Midjourney v7, itch.io, self-made" />
            </Field>
            <Field label="Generation prompt">
              <textarea className="textarea" rows={2} value={prompt} onChange={(e) => setPrompt(e.target.value)} placeholder="The prompt these assets were generated with" />
            </Field></>}
            {meta.ai_enabled && <p className="hint faint">Leave name, description and tags to the AI agent: select assets afterwards and choose “AI tag”.</p>}
          </>
        )}
      </div>
    </Dialog>
  )
}
