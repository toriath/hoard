// Browser-side preview rendering. The server makes thumbnails for ordinary
// images; everything it cannot decode (audio, video, fonts, 3D models, and
// image formats such as SVG or AVIF) is rendered here once and uploaded as
// the asset's thumbnail, which also gives the built-in AI agent something to
// look at.

import { ApiError, api, rawUrl } from './api'
import { emitAsset, prefs } from './store'
import type { Asset, AssetFile } from './types'

const SIZE = 512
const GOLD = '#e9b44c'

function canvas(w = SIZE, h = SIZE): [HTMLCanvasElement, CanvasRenderingContext2D] {
  const c = document.createElement('canvas')
  c.width = w
  c.height = h
  return [c, c.getContext('2d')!]
}

const toBlob = (c: HTMLCanvasElement) => new Promise<Blob | null>((res) => c.toBlob((b) => res(b), 'image/png'))

// ---- audio ----

let audioCtx: AudioContext | undefined

/** Decoding yields 4 bytes per sample and channel; beyond this the tab could run out of memory. */
export const MAX_DECODE_SECONDS = 8 * 60

/** Reads the duration from the file's header without decoding it. */
export function audioDuration(url: string): Promise<number> {
  return new Promise((resolve, reject) => {
    const a = new Audio()
    const done = (fn: () => void) => {
      a.removeAttribute('src')
      a.load()
      fn()
    }
    a.preload = 'metadata'
    a.onloadedmetadata = () => done(() => resolve(a.duration))
    a.onerror = () => done(() => reject(new Error('cannot read audio')))
    a.src = url
  })
}

export async function decodeAudio(url: string): Promise<AudioBuffer> {
  audioCtx ??= new AudioContext()
  const data = await (await fetch(url, { credentials: 'same-origin' })).arrayBuffer()
  return audioCtx.decodeAudioData(data)
}

/** Peak amplitude (0..1) per bucket across all channels. */
export function computePeaks(buf: AudioBuffer, buckets: number): Float32Array {
  const peaks = new Float32Array(buckets)
  const step = Math.max(1, Math.floor(buf.length / buckets))
  const stride = Math.max(1, Math.floor(step / 64)) // sample sparsely: plenty for a plot
  for (let ch = 0; ch < buf.numberOfChannels; ch++) {
    const d = buf.getChannelData(ch)
    for (let i = 0; i < buckets; i++) {
      let max = 0
      const end = Math.min(d.length, (i + 1) * step)
      for (let j = i * step; j < end; j += stride) {
        const v = Math.abs(d[j])
        if (v > max) max = v
      }
      if (max > peaks[i]) peaks[i] = max
    }
  }
  return peaks
}

export function drawWaveform(ctx: CanvasRenderingContext2D, peaks: Float32Array, w: number, h: number, color: string, played = 0, playedColor = color) {
  ctx.clearRect(0, 0, w, h)
  const bar = w / peaks.length
  const mid = h / 2
  for (let i = 0; i < peaks.length; i++) {
    const amp = Math.max(0.02, peaks[i]) * (h * 0.46)
    ctx.fillStyle = i / peaks.length < played ? playedColor : color
    ctx.fillRect(i * bar + bar * 0.15, mid - amp, Math.max(1, bar * 0.7), amp * 2)
  }
}

async function audioThumb(url: string) {
  const seconds = await audioDuration(url)
  if (!(seconds <= MAX_DECODE_SECONDS)) throw new Error('audio too long to plot')
  const buf = await decodeAudio(url)
  const [c, ctx] = canvas(SIZE, SIZE)
  ctx.translate(0, SIZE * 0.2)
  drawWaveform(ctx, computePeaks(buf, 72), SIZE, SIZE * 0.6, GOLD)
  return {
    blob: await toBlob(c),
    attrs: { duration_s: Math.round(buf.duration * 100) / 100, sample_rate: buf.sampleRate, channels: buf.numberOfChannels },
  }
}

// ---- video ----

function videoThumb(url: string) {
  return new Promise<{ blob: Blob | null; attrs: Record<string, unknown> }>((resolve, reject) => {
    const v = document.createElement('video')
    v.muted = true
    v.playsInline = true
    v.preload = 'auto'
    const release = () => {
      v.removeAttribute('src')
      v.load() // stops the download
    }
    v.onerror = () => {
      release()
      reject(new Error('cannot decode video'))
    }
    v.onloadedmetadata = () => {
      v.currentTime = Math.min(v.duration * 0.15, 3)
    }
    v.onseeked = async () => {
      try {
        const scale = Math.min(1, SIZE / Math.max(v.videoWidth, v.videoHeight))
        const [c, ctx] = canvas(Math.round(v.videoWidth * scale), Math.round(v.videoHeight * scale))
        ctx.drawImage(v, 0, 0, c.width, c.height)
        const attrs = { duration_s: Math.round(v.duration * 100) / 100, width: v.videoWidth, height: v.videoHeight }
        resolve({ blob: await toBlob(c), attrs })
      } catch (e) {
        reject(e)
      } finally {
        release()
      }
    }
    v.src = url
  })
}

// ---- fonts ----

let fontSeq = 0

/** Loads a font file. Call `release()` when done, or it stays in memory until reload. */
export async function loadFont(url: string): Promise<{ family: string; release: () => void }> {
  const family = `hoard-font-${++fontSeq}`
  const face = new FontFace(family, `url("${url}")`)
  await face.load()
  document.fonts.add(face)
  return { family, release: () => document.fonts.delete(face) }
}

async function fontThumb(url: string) {
  const { family, release } = await loadFont(url)
  try {
    const [c, ctx] = canvas()
    ctx.fillStyle = '#1e1c18'
    ctx.fillRect(0, 0, SIZE, SIZE)
    ctx.fillStyle = '#f1ede4'
    ctx.textAlign = 'center'
    ctx.textBaseline = 'middle'
    ctx.font = `220px "${family}"`
    ctx.fillText('Aa', SIZE / 2, SIZE * 0.4)
    ctx.fillStyle = '#a9a294'
    ctx.font = `44px "${family}"`
    ctx.fillText('ABCDEFGHIJKLM', SIZE / 2, SIZE * 0.72)
    ctx.fillText('abcdefg 0123456789', SIZE / 2, SIZE * 0.84)
    return { blob: await toBlob(c), attrs: {} }
  } finally {
    release()
  }
}

// ---- images the server cannot decode (SVG, AVIF, animated WebP) ----

function imageThumb(url: string) {
  return new Promise<{ blob: Blob | null; attrs: Record<string, unknown> }>((resolve, reject) => {
    const img = new Image()
    img.onerror = () => reject(new Error('cannot decode image'))
    img.onload = async () => {
      try {
        const iw = img.naturalWidth || SIZE
        const ih = img.naturalHeight || SIZE
        const scale = Math.min(1, SIZE / Math.max(iw, ih))
        const [c, ctx] = canvas(Math.max(1, Math.round(iw * scale)), Math.max(1, Math.round(ih * scale)))
        ctx.drawImage(img, 0, 0, c.width, c.height)
        resolve({ blob: await toBlob(c), attrs: {} })
      } catch (e) {
        reject(e)
      }
    }
    img.src = url
  })
}

// ---- orchestration ----

const MAX_DECODE_BYTES = 200 << 20

export function canRenderThumb(file: AssetFile | undefined): boolean {
  return !!file && ['audio', 'video', 'font', 'model', 'image'].includes(file.kind) && file.size <= MAX_DECODE_BYTES
}

async function render(asset: Asset, file: AssetFile) {
  const url = rawUrl(asset.id, file.filename)
  switch (file.kind) {
    case 'audio':
      return audioThumb(url)
    case 'video':
      return videoThumb(url)
    case 'font':
      return fontThumb(url)
    case 'image':
      return imageThumb(url)
    case 'model': {
      const { renderModelThumb } = await import('../preview/three')
      const r = await renderModelThumb(url)
      return { blob: r.blob, attrs: { triangles: r.triangles } }
    }
  }
  return null
}

const inFlight = new Set<string>()
let chain: Promise<unknown> = Promise.resolve()

// Files this browser could not render, by content hash, so that a broken or
// unsupported file is not downloaded and retried on every visit. Entries
// expire, because a browser update or a fixed bug can make a file renderable.
const FAILED_KEY = 'hoard-thumb-failed'
const FAILED_TTL = 14 * 24 * 3600 * 1000
function loadFailures(): Map<string, number> {
  try {
    const entries = JSON.parse(prefs.get(FAILED_KEY) ?? '[]') as [string, number][]
    return new Map(entries.filter(([sha, at]) => typeof sha === 'string' && Date.now() - at < FAILED_TTL))
  } catch {
    return new Map() // unreadable preference: start over
  }
}
const failed = loadFailures()
function rememberFailure(sha: string) {
  failed.set(sha, Date.now())
  prefs.set(FAILED_KEY, JSON.stringify([...failed].slice(-500)))
}

/** Rejects if a job hangs (an undecodable file can leave a decoder waiting forever). */
function withTimeout<T>(job: Promise<T>, ms: number): Promise<T> {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error('preview rendering timed out')), ms)
    job.then(
      (v) => {
        clearTimeout(timer)
        resolve(v)
      },
      (e) => {
        clearTimeout(timer)
        reject(e)
      },
    )
  })
}

/**
 * Renders and uploads a thumbnail for an asset that has none. Jobs run one at
 * a time. Resolves with the updated asset, or null when nothing was rendered
 * (already has one, not renderable, known to fail, or failed now).
 */
export function ensureThumb(asset: Asset, force = false): Promise<Asset | null> {
  const file = asset.files.find((f) => f.id === asset.primary_file_id) ?? asset.files[0]
  if (!canRenderThumb(file) || inFlight.has(asset.id)) return Promise.resolve(null)
  if (!force && (asset.thumb_version > 0 || failed.has(file.sha256))) return Promise.resolve(null)
  inFlight.add(asset.id)
  const job = chain.then(async () => {
    try {
      const out = await withTimeout(render(asset, file), 45_000)
      if (!out?.blob) throw new Error('no preview produced')
      let updated = await api.putThumbnail(asset.id, out.blob)
      if (out.attrs && Object.keys(out.attrs).length) updated = await api.putTechnical(asset.id, out.attrs)
      emitAsset(updated)
      return updated
    } catch (e) {
      // Remember only failures of the file itself. Being offline or signed
      // out says nothing about whether the file can be rendered.
      const transient = e instanceof ApiError || !navigator.onLine
      if (!transient) rememberFailure(file.sha256)
      console.warn('preview rendering failed for', asset.id, e)
      return null
    } finally {
      inFlight.delete(asset.id)
    }
  })
  chain = job
  return job
}
