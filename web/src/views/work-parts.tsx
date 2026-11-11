import { Pause, Play } from 'lucide-react'
import { useState } from 'react'
import { api, rawUrl, thumbUrl } from '../lib/api'
import { workApi } from '../lib/api-work'
import { togglePreview, useNowPlaying } from '../lib/audio'
import { getMeta, toastError, typeLabel, useQuery } from '../lib/store'
import {
  PLACEHOLDER_AUDIO_TYPES, PLACEHOLDER_IMAGE_TYPES, PRIORITIES, canPlaceholder, type RequestInput, type Submission, type WorkRequest,
} from '../lib/types-work'
import { Button, Check, Dialog, Field, typeColor, typeIcon } from '../ui'

// Pieces shared by the requests board, the asset drawer and the project page.

// ---------- candidate thumbnail ----------

/** Preview of a submitted candidate: its thumbnail, or the type icon when it has none. */
export function CandidateThumb({ sub }: { sub: Pick<Submission, 'asset_id' | 'asset_type' | 'thumb_version' | 'preview_kind' | 'primary_file'> }) {
  const Icon = typeIcon(sub.asset_type ?? 'other')
  const playing = useNowPlaying() === sub.asset_id
  return (
    <span className="cand-thumb checker">
      {sub.thumb_version ? (
        <img src={thumbUrl({ id: sub.asset_id, thumb_version: sub.thumb_version })} alt="" loading="lazy" decoding="async" draggable={false} />
      ) : (
        <span className="thumb-icon" style={{ color: typeColor(sub.asset_type ?? 'other') }}>
          <Icon aria-hidden />
        </span>
      )}
      {sub.preview_kind === 'audio' && sub.primary_file && (
        <button
          type="button"
          className="card-play cand-play"
          aria-label={playing ? 'Pause' : 'Play'}
          onClick={(e) => {
            e.stopPropagation()
            togglePreview(sub.asset_id, rawUrl(sub.asset_id, sub.primary_file!))
          }}
        >
          {playing ? <Pause aria-hidden /> : <Play aria-hidden />}
        </button>
      )}
    </span>
  )
}

// ---------- feedback dialog ----------

/** Asks for a text (feedback, reason) and optionally a priority; used for rework and reject. */
export function FeedbackDialog({
  title, intro, label, placeholder, confirmLabel, danger, withPriority, onClose, onSubmit,
}: {
  title: string
  intro?: string
  label: string
  placeholder?: string
  confirmLabel: string
  danger?: boolean
  withPriority?: boolean
  onClose: () => void
  onSubmit: (text: string, priority: number) => Promise<void>
}) {
  const [text, setText] = useState('')
  const [priority, setPriority] = useState(0)
  const [busy, setBusy] = useState(false)
  const submit = async () => {
    if (!text.trim()) return
    setBusy(true)
    try {
      await onSubmit(text.trim(), priority)
      onClose()
    } catch (e) {
      toastError(e)
      setBusy(false)
    }
  }
  return (
    <Dialog
      title={title}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant={danger ? 'danger' : 'primary'} busy={busy} disabled={!text.trim()} onClick={submit}>
            {confirmLabel}
          </Button>
        </>
      }
    >
      <div className="stack">
        {intro && <p className="muted">{intro}</p>}
        <Field label={label}>
          <textarea
            className="textarea"
            rows={4}
            autoFocus
            maxLength={8000}
            value={text}
            placeholder={placeholder}
            onChange={(e) => setText(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) void submit()
            }}
          />
        </Field>
        {withPriority && (
          <Field label="Priority">
            <select className="select" value={priority} onChange={(e) => setPriority(Number(e.target.value))}>
              {PRIORITIES.map((p) => (
                <option key={p.value} value={p.value}>
                  {p.label}
                </option>
              ))}
            </select>
          </Field>
        )}
      </div>
    </Dialog>
  )
}

// ---------- new request ----------

const num = (v: string) => {
  const n = Number(v)
  return v.trim() !== '' && Number.isFinite(n) && n > 0 ? n : undefined
}

export function NewRequestDialog({
  project, onClose, onCreated,
}: {
  project?: string
  onClose: () => void
  onCreated: (r: WorkRequest) => void
}) {
  const projects = useQuery(() => api.projects(), [])
  const types = getMeta().types
  const [title, setTitle] = useState('')
  const [description, setDescription] = useState('')
  const [type, setType] = useState('sprite')
  const [projectId, setProjectId] = useState(project ?? '')
  const [priority, setPriority] = useState(0)
  const [width, setWidth] = useState('')
  const [height, setHeight] = useState('')
  const [duration, setDuration] = useState('')
  const [filename, setFilename] = useState('')
  const [placeholder, setPlaceholder] = useState(false)
  const [busy, setBusy] = useState(false)

  const image = PLACEHOLDER_IMAGE_TYPES.includes(type) || ['model', 'animation', 'video', 'font'].includes(type)
  const audio = PLACEHOLDER_AUDIO_TYPES.includes(type) || type === 'video' || type === 'animation'
  const submit = async () => {
    if (!title.trim()) return
    const spec: Record<string, unknown> = {}
    if (image && num(width)) spec.width = num(width)
    if (image && num(height)) spec.height = num(height)
    if (audio && num(duration)) spec.duration_s = num(duration)
    if (filename.trim()) spec.filename = filename.trim()
    setBusy(true)
    const body: RequestInput = {
      title: title.trim(), description: description.trim(), asset_type: type, project_id: projectId || undefined, priority, spec,
      placeholder: placeholder && canPlaceholder(type),
    }
    try {
      onCreated(await workApi.createRequest(body))
      onClose()
    } catch (e) {
      toastError(e)
      setBusy(false)
    }
  }
  return (
    <Dialog
      title="New asset request"
      onClose={onClose}
      width={560}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" busy={busy} disabled={!title.trim()} onClick={submit}>
            Create request
          </Button>
        </>
      }
    >
      <div className="stack">
        <p className="muted">Generator agents pick requests up through the hoard MCP server and submit candidates for you to review.</p>
        <Field label="Title">
          <input className="input" autoFocus maxLength={300} value={title} placeholder="Gold coin, spinning" onChange={(e) => setTitle(e.target.value)} />
        </Field>
        <Field label="Description" hint="What it shows or sounds like, mood, where the game uses it.">
          <textarea className="textarea" rows={3} value={description} onChange={(e) => setDescription(e.target.value)} />
        </Field>
        <div className="field-row">
          <Field label="Type">
            <select className="select" value={type} onChange={(e) => setType(e.target.value)}>
              {types.map((t) => (
                <option key={t.id} value={t.id}>
                  {t.label}
                </option>
              ))}
            </select>
          </Field>
          <Field label="Project" hint="The generator receives its brief.">
            <select className="select" value={projectId} onChange={(e) => setProjectId(e.target.value)}>
              <option value="">No project</option>
              {(projects.data ?? []).map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name}
                </option>
              ))}
            </select>
          </Field>
          <Field label="Priority">
            <select className="select" value={priority} onChange={(e) => setPriority(Number(e.target.value))}>
              {PRIORITIES.map((p) => (
                <option key={p.value} value={p.value}>
                  {p.label}
                </option>
              ))}
            </select>
          </Field>
        </div>
        {(image || audio) && (
          <div className="field-row">
            {image && (
              <>
                <Field label="Width (px)">
                  <input className="input" inputMode="numeric" value={width} placeholder="64" onChange={(e) => setWidth(e.target.value)} />
                </Field>
                <Field label="Height (px)">
                  <input className="input" inputMode="numeric" value={height} placeholder="64" onChange={(e) => setHeight(e.target.value)} />
                </Field>
              </>
            )}
            {audio && (
              <Field label="Duration (s)">
                <input className="input" inputMode="decimal" value={duration} placeholder="1.5" onChange={(e) => setDuration(e.target.value)} />
              </Field>
            )}
          </div>
        )}
        <Field label="File name" hint="The name the game expects, e.g. coin.png. Optional.">
          <input className="input" maxLength={180} value={filename} onChange={(e) => setFilename(e.target.value)} />
        </Field>
        {canPlaceholder(type) ? (
          <Check checked={placeholder} onChange={setPlaceholder}>
            Make a placeholder now ({PLACEHOLDER_AUDIO_TYPES.includes(type) ? 'a quiet beep' : 'flat colour with the title'}) so the game can use it meanwhile
          </Check>
        ) : (
          <p className="faint">No placeholder can be generated for {typeLabel(type)}.</p>
        )}
      </div>
    </Dialog>
  )
}
