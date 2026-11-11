import { useState } from 'react'
import { formatAnimationLines, parseAnimationLines } from '../lib/sprite'
import { Check, Field } from '../ui'

/** The sprite-sheet attributes as the edit form holds them (text, so partial input is possible). */
export interface AnimationDraft {
  frame_width: string
  frame_height: string
  columns: string
  frames: string
  fps: string
  loop: boolean
  animations: string
}

export function animationDraft(attrs: Record<string, unknown>): AnimationDraft {
  const num = (v: unknown) => (typeof v === 'number' ? String(v) : '')
  return {
    frame_width: num(attrs.frame_width),
    frame_height: num(attrs.frame_height),
    columns: num(attrs.columns),
    frames: num(attrs.frames),
    fps: num(attrs.fps),
    loop: attrs.loop !== false,
    animations: formatAnimationLines(attrs.animations),
  }
}

/**
 * The attrs patch for the changes made in the form: changed keys only, null
 * deletes a key. Returns an error message when a field cannot be saved.
 */
export function animationPatch(before: Record<string, unknown>, draft: AnimationDraft): { patch: Record<string, unknown>; error?: string } {
  const patch: Record<string, unknown> = {}
  for (const k of ['frame_width', 'frame_height', 'columns', 'frames', 'fps'] as const) {
    const raw = draft[k].trim()
    const next = raw === '' ? null : Number(raw)
    if (next !== null && (!Number.isInteger(next) || next < 1 || next > 65536)) return { patch, error: `${k.replace('_', ' ')} must be a whole number of at least 1` }
    const prev = typeof before[k] === 'number' ? before[k] : null
    if (next !== prev) patch[k] = next
  }
  if (draft.loop !== (before.loop !== false)) patch.loop = draft.loop
  const parsed = parseAnimationLines(draft.animations)
  if (parsed.errors.length) return { patch, error: `Cannot read animation line "${parsed.errors[0]}" (write name: first-last)` }
  if (draft.animations.trim() !== formatAnimationLines(before.animations).trim()) {
    patch.animations = Object.keys(parsed.value).length ? parsed.value : null
  }
  return { patch }
}

export function AnimationFields({ value, onChange }: { value: AnimationDraft; onChange: (v: AnimationDraft) => void }) {
  const [open, setOpen] = useState(() => !!(value.frame_width || value.columns || value.animations))
  const set = <K extends keyof AnimationDraft>(k: K, v: AnimationDraft[K]) => onChange({ ...value, [k]: v })
  return (
    <details className="stack animation-fields" open={open} onToggle={(e) => setOpen(e.currentTarget.open)}>
      <summary className="eyebrow">Animation (sprite sheet)</summary>
      <p className="hint">
        Describe a sheet of equally sized frames and the preview plays it. Give the frame size, or the number of columns and frames.
      </p>
      <div className="field-row">
        <Field label="Frame width (px)">
          <input className="input" inputMode="numeric" value={value.frame_width} onChange={(e) => set('frame_width', e.target.value)} />
        </Field>
        <Field label="Frame height (px)">
          <input className="input" inputMode="numeric" value={value.frame_height} onChange={(e) => set('frame_height', e.target.value)} />
        </Field>
      </div>
      <div className="field-row">
        <Field label="Columns">
          <input className="input" inputMode="numeric" value={value.columns} onChange={(e) => set('columns', e.target.value)} />
        </Field>
        <Field label="Frames" hint="Empty: every frame that fits">
          <input className="input" inputMode="numeric" value={value.frames} onChange={(e) => set('frames', e.target.value)} />
        </Field>
        <Field label="Frames per second">
          <input className="input" inputMode="numeric" value={value.fps} placeholder="12" onChange={(e) => set('fps', e.target.value)} />
        </Field>
      </div>
      <Check checked={value.loop} onChange={(v) => set('loop', v)}>
        Loop
      </Check>
      <Field label="Named animations" hint="One per line: name: first-last (frame numbers start at 0), e.g. walk: 0-5">
        <textarea className="textarea" rows={3} value={value.animations} onChange={(e) => set('animations', e.target.value)} spellCheck={false} />
      </Field>
    </details>
  )
}
