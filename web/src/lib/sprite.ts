// Sprite-sheet attributes (assets.attrs), shared with the delivery and
// placeholder features: frame_width, frame_height (px), frames, columns, fps,
// loop, animations (name -> [first, last] frame, inclusive).

export interface SpriteSheet {
  frameWidth: number
  frameHeight: number
  columns: number
  rows: number
  frames: number
  fps: number
  loop: boolean
  /** Named frame ranges, clamped to the sheet; empty when none are defined. */
  animations: { name: string; first: number; last: number }[]
}

const positiveInt = (v: unknown): number | undefined =>
  typeof v === 'number' && Number.isFinite(v) && v >= 1 && v <= 65536 ? Math.floor(v) : undefined

export const DEFAULT_FPS = 12
export const MAX_FPS = 60

export function clampFps(v: number): number {
  return Number.isFinite(v) ? Math.min(MAX_FPS, Math.max(1, Math.round(v))) : DEFAULT_FPS
}

/**
 * Reads the sprite-sheet attributes of an asset. Returns null when they do not
 * describe a sheet that fits the image (so the plain image preview is shown).
 * Either frame_width/frame_height are given, or columns plus frames, from
 * which the frame size follows from the image size.
 */
export function parseSprite(attrs: Record<string, unknown> | undefined, image: { width?: number; height?: number }): SpriteSheet | null {
  const a = attrs ?? {}
  const imgW = image.width ?? 0
  const imgH = image.height ?? 0
  if (imgW < 1 || imgH < 1) return null

  let fw = positiveInt(a.frame_width)
  let fh = positiveInt(a.frame_height)
  const cols = positiveInt(a.columns)
  const frames = positiveInt(a.frames)
  if (!fw || !fh) {
    if (!cols || !frames) return null
    fw = Math.floor(imgW / cols)
    fh = Math.floor(imgH / Math.ceil(frames / cols))
  }
  if (!fw || !fh || fw > imgW || fh > imgH) return null

  const fitCols = Math.floor(imgW / fw)
  const rows = Math.floor(imgH / fh)
  const columns = Math.min(cols ?? fitCols, fitCols)
  const capacity = columns * rows
  if (capacity < 1) return null
  const count = Math.min(frames ?? capacity, capacity)
  if (count < 2) return null // a single frame is no animation

  const fpsRaw = typeof a.fps === 'number' ? a.fps : DEFAULT_FPS
  return {
    frameWidth: fw,
    frameHeight: fh,
    columns,
    rows: Math.ceil(count / columns),
    frames: count,
    fps: clampFps(fpsRaw),
    loop: a.loop !== false,
    animations: parseAnimationRanges(a.animations, count),
  }
}

function parseAnimationRanges(v: unknown, frames: number): SpriteSheet['animations'] {
  if (!v || typeof v !== 'object' || Array.isArray(v)) return []
  const out: SpriteSheet['animations'] = []
  for (const [name, range] of Object.entries(v as Record<string, unknown>)) {
    if (!Array.isArray(range) || range.length < 1) continue
    const first = Number(range[0])
    const last = Number(range[range.length > 1 ? 1 : 0])
    if (!Number.isInteger(first) || !Number.isInteger(last)) continue
    const lo = Math.max(0, Math.min(first, last))
    const hi = Math.min(frames - 1, Math.max(first, last))
    if (lo > hi) continue
    out.push({ name, first: lo, last: hi })
  }
  return out
}

/** Top-left corner of a frame in the sheet. */
export function frameOrigin(s: SpriteSheet, frame: number): { x: number; y: number } {
  const i = Math.max(0, Math.min(s.frames - 1, Math.floor(frame)))
  return { x: (i % s.columns) * s.frameWidth, y: Math.floor(i / s.columns) * s.frameHeight }
}

/**
 * The frame after `frame` within [first, last]. At the end it wraps when
 * looping and otherwise stays on the last frame and reports done.
 */
export function advance(frame: number, first: number, last: number, loop: boolean): { frame: number; done: boolean } {
  if (frame < first) return { frame: first, done: false }
  if (frame < last) return { frame: frame + 1, done: false }
  return loop ? { frame: first, done: false } : { frame: last, done: true }
}

/** Pixel art is shown with hard edges; large frames are smoothed. */
export function isPixelArt(s: SpriteSheet): boolean {
  return Math.max(s.frameWidth, s.frameHeight) <= 128
}

// ---- editing ----

/** Reads "walk: 0-5" (or "walk: 3", or "walk 0 5") per line; unreadable lines are returned in errors. */
export function parseAnimationLines(text: string): { value: Record<string, [number, number]>; errors: string[] } {
  // No prototype: a line named "__proto__" or "constructor" is just a name.
  const value: Record<string, [number, number]> = Object.create(null)
  const errors: string[] = []
  for (const raw of text.split('\n')) {
    const line = raw.trim()
    if (!line) continue
    const m =
      line.match(/^(.+?)\s*:\s*(\d+)(?:\s*(?:-|–|to|,)\s*(\d+))?$/i) ?? line.match(/^(.+?)\s+(\d+)\s+(\d+)$/)
    if (!m || m[1].length > 60) {
      errors.push(line)
      continue
    }
    const first = Number(m[2])
    const last = Number(m[3] ?? m[2])
    value[m[1].trim()] = [Math.min(first, last), Math.max(first, last)]
  }
  return { value, errors }
}

export function formatAnimationLines(v: unknown): string {
  if (!v || typeof v !== 'object' || Array.isArray(v)) return ''
  return Object.entries(v as Record<string, unknown>)
    .filter(([, r]) => Array.isArray(r) && r.length >= 1)
    .map(([name, r]) => {
      const range = r as unknown[]
      return `${name}: ${range[0]}-${range[range.length > 1 ? 1 : 0]}`
    })
    .join('\n')
}
