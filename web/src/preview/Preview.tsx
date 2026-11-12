import { Download, FileQuestion, Pause, Play } from 'lucide-react'
import { useEffect, useMemo, useRef, useState } from 'react'
import { fileDownloadUrl, rawUrl } from '../lib/api'
import { stopPreview } from '../lib/audio'
import { formatBytes, formatDuration } from '../lib/format'
import { seekTarget } from '../lib/seek'
import { parseSprite } from '../lib/sprite'
import { MAX_DECODE_SECONDS, computePeaks, decodeAudio, drawWaveform, loadFont } from '../lib/thumbs'
import type { Asset, AssetFile } from '../lib/types'
import { Spinner, typeColor } from '../ui'
import { SpritePreview } from './SpritePreview'
import type { Viewer } from './three'

/** Shows the best available preview for one file of an asset. */
export function Preview({ asset, file }: { asset: Asset; file: AssetFile | undefined }) {
  if (!file) {
    return (
      <div className="preview-stage">
        <div className="preview-none">
          <FileQuestion aria-hidden />
          <span>This asset has no files yet</span>
        </div>
      </div>
    )
  }
  const url = rawUrl(asset.id, file.filename)
  switch (file.kind) {
    case 'image':
      return <ImageOrSheet key={url} url={url} file={file} attrs={asset.attrs} />
    case 'audio':
      return <AudioPlayer key={url} url={url} file={file} color={typeColor(asset.type)} />
    case 'video':
      return (
        <div className="preview-stage">
          <video key={url} src={url} controls playsInline preload="metadata" />
        </div>
      )
    case 'model':
      return <ModelPreview key={url} url={url} />
    case 'font':
      return <FontPreview key={url} url={url} />
    case 'text':
      return <TextPreview key={url} url={url} file={file} />
    default:
      return (
        <div className="preview-stage">
          <div className="preview-none">
            <FileQuestion aria-hidden />
            <span>
              No in-browser preview for <code>{file.filename.split('.').pop()}</code> files
            </span>
            <a className="btn btn-sm" href={fileDownloadUrl(asset.id, file.filename)}>
              <Download aria-hidden /> Download ({formatBytes(file.size)})
            </a>
          </div>
        </div>
      )
  }
}

/** A sprite sheet plays; any other image is shown as is. */
function ImageOrSheet({ url, file, attrs }: { url: string; file: AssetFile; attrs: Asset['attrs'] }) {
  // Keyed on the content, not the object: a refetched asset with the same
  // attributes must not restart a paused or scrubbed animation.
  const key = JSON.stringify(attrs)
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const sheet = useMemo(() => parseSprite(attrs, file), [key, file.width, file.height])
  const still = <ImagePreview url={url} file={file} />
  return sheet ? <SpritePreview url={url} sheet={sheet} sheetView={still} /> : still
}

function ImagePreview({ url, file }: { url: string; file: AssetFile }) {
  const [actual, setActual] = useState(false)
  const small = !!file.width && Math.max(file.width, file.height ?? 0) <= 160
  return (
    <div className={'preview-stage checker' + (actual ? ' preview-scroll' : '')} onClick={() => setActual((a) => !a)} title={actual ? 'Click to fit' : 'Click for actual size'}>
      <img
        src={url}
        alt={file.filename}
        className={actual ? 'actual' : small ? 'pixel upscale' : ''}
        style={small || actual ? { imageRendering: small ? 'pixelated' : undefined } : undefined}
        draggable={false}
      />
    </div>
  )
}

function AudioPlayer({ url, color }: { url: string; file: AssetFile; color: string }) {
  const audio = useRef<HTMLAudioElement>(null)
  const canvas = useRef<HTMLCanvasElement>(null)
  const [peaks, setPeaks] = useState<Float32Array | null>(null)
  const [waveNote, setWaveNote] = useState('Analysing audio…')
  const [playing, setPlaying] = useState(false)
  const [time, setTime] = useState(0)
  const [duration, setDuration] = useState(0)

  useEffect(() => stopPreview(), [url])

  // The waveform needs the whole file decoded into memory, so it is only
  // drawn once the duration is known to be short enough.
  useEffect(() => {
    if (!duration) return
    if (duration > MAX_DECODE_SECONDS) {
      setWaveNote('Too long for a waveform; playback works as usual')
      return
    }
    let live = true
    decodeAudio(url).then(
      (buf) => live && setPeaks(computePeaks(buf, 160)),
      () => live && setWaveNote('This browser cannot draw a waveform for this file'),
    )
    return () => {
      live = false
    }
  }, [url, duration])

  useEffect(() => {
    const c = canvas.current
    if (!c || !peaks) return
    const dpr = Math.min(window.devicePixelRatio, 2)
    const w = c.clientWidth
    const h = c.clientHeight
    c.width = w * dpr
    c.height = h * dpr
    const ctx = c.getContext('2d')!
    ctx.scale(dpr, dpr)
    const styles = getComputedStyle(c)
    drawWaveform(ctx, peaks, w, h, styles.getPropertyValue('--border-strong'), duration ? time / duration : 0, styles.getPropertyValue('--wave'))
  }, [peaks, time, duration])

  useEffect(() => {
    if (!playing) return
    let raf = 0
    const tick = () => {
      setTime(audio.current?.currentTime ?? 0)
      raf = requestAnimationFrame(tick)
    }
    raf = requestAnimationFrame(tick)
    return () => cancelAnimationFrame(raf)
  }, [playing])

  const toggle = () => (audio.current?.paused ? audio.current.play() : audio.current?.pause())
  const seekTo = (t: number) => {
    const a = audio.current
    if (!a || !duration) return
    a.currentTime = t
    setTime(a.currentTime)
  }
  const seek = (e: React.MouseEvent<HTMLCanvasElement>) => {
    const r = e.currentTarget.getBoundingClientRect()
    seekTo(((e.clientX - r.left) / r.width) * duration)
  }
  const seekKey = (e: React.KeyboardEvent<HTMLCanvasElement>) => {
    if (e.key === ' ') {
      e.preventDefault()
      toggle()
      return
    }
    const t = seekTarget(e.key, audio.current?.currentTime ?? time, duration)
    if (t === null) return
    e.preventDefault()
    seekTo(t)
  }

  return (
    <div className="preview-stage preview-audio" style={{ '--wave': color } as React.CSSProperties}>
      <audio
        ref={audio}
        src={url}
        preload="metadata"
        onPlay={() => setPlaying(true)}
        onPause={() => setPlaying(false)}
        onLoadedMetadata={(e) => setDuration(e.currentTarget.duration)}
        onError={() => setWaveNote('This browser cannot play this file')}
        onEnded={() => setTime(0)}
      />
      <canvas
        ref={canvas}
        className="wave"
        onClick={seek}
        onKeyDown={seekKey}
        tabIndex={duration ? 0 : -1}
        role="slider"
        aria-label="Seek"
        aria-orientation="horizontal"
        aria-disabled={!duration}
        aria-valuemin={0}
        aria-valuemax={duration ? Math.round(duration * 10) / 10 : 0}
        aria-valuenow={Math.round(time * 10) / 10}
        aria-valuetext={`${formatDuration(time)} of ${formatDuration(duration)}`}
      />
      {!peaks && <div className="wave-placeholder muted">{waveNote}</div>}
      <div className="row audio-controls">
        <button className="btn btn-primary btn-icon play" onClick={toggle} aria-label={playing ? 'Pause' : 'Play'}>
          {playing ? <Pause aria-hidden /> : <Play aria-hidden />}
        </button>
        <span className="mono muted">
          {formatDuration(time)} / {formatDuration(duration)}
        </span>
      </div>
    </div>
  )
}

function ModelPreview({ url }: { url: string }) {
  const host = useRef<HTMLDivElement>(null)
  const [state, setState] = useState<{ loading: boolean; error?: string; triangles?: number }>({ loading: true })
  useEffect(() => {
    let viewer: Viewer | undefined
    let live = true
    ;(async () => {
      try {
        const three = await import('./three')
        const model = await three.loadModel(url)
        if (!live || !host.current) return
        viewer = three.createViewer(host.current, model)
        setState({ loading: false, triangles: model.triangles })
      } catch (e) {
        if (live) setState({ loading: false, error: e instanceof Error ? e.message : 'Could not load model' })
      }
    })()
    return () => {
      live = false
      viewer?.dispose()
    }
  }, [url])
  return (
    <div className="preview-stage preview-model">
      <div ref={host} className="model-host" />
      {state.loading && (
        <div className="preview-overlay">
          <Spinner label="Loading model…" />
        </div>
      )}
      {state.error && (
        <div className="preview-none">
          <FileQuestion aria-hidden />
          <span>{state.error}</span>
        </div>
      )}
      {state.triangles !== undefined && <span className="preview-meta mono">{state.triangles.toLocaleString()} tris · drag to orbit</span>}
    </div>
  )
}

const SPECIMEN = 'The quick brown fox jumps over the lazy dog'

function FontPreview({ url }: { url: string }) {
  const [family, setFamily] = useState<string>()
  const [error, setError] = useState(false)
  const [text, setText] = useState(SPECIMEN)
  useEffect(() => {
    let live = true
    let release = () => {}
    loadFont(url).then(
      (f) => {
        if (!live) return f.release() // unmounted while loading
        release = f.release
        setFamily(f.family)
      },
      () => live && setError(true),
    )
    return () => {
      live = false
      release()
    }
  }, [url])
  if (error) {
    return (
      <div className="preview-stage">
        <div className="preview-none">
          <FileQuestion aria-hidden />
          <span>This font could not be loaded by the browser</span>
        </div>
      </div>
    )
  }
  return (
    <div className="preview-stage preview-font">
      {!family ? (
        <Spinner />
      ) : (
        <div style={{ fontFamily: `"${family}"` }} className="font-specimen">
          <input className="font-input" value={text} onChange={(e) => setText(e.target.value)} aria-label="Sample text" style={{ fontFamily: 'inherit' }} />
          {[48, 28, 18].map((s) => (
            <p key={s} style={{ fontSize: s }}>
              {text || SPECIMEN}
            </p>
          ))}
          <p style={{ fontSize: 15 }} className="muted">
            ABCDEFGHIJKLMNOPQRSTUVWXYZ abcdefghijklmnopqrstuvwxyz 0123456789 !?&amp;@#%
          </p>
        </div>
      )}
    </div>
  )
}

function TextPreview({ url, file }: { url: string; file: AssetFile }) {
  const [text, setText] = useState<string>()
  const LIMIT = 300_000
  useEffect(() => {
    let live = true
    fetch(url, { credentials: 'same-origin', headers: file.size > LIMIT ? { Range: `bytes=0-${LIMIT - 1}` } : {} })
      .then((r) => {
        if (!r.ok) throw new Error(`HTTP ${r.status}`)
        return r.text()
      })
      .then(
        (t) => live && setText(t),
        () => live && setText('Could not load this file.'),
      )
    return () => {
      live = false
    }
  }, [url, file.size])
  if (text === undefined) return <Spinner />
  const lines = text.split('\n')
  return (
    <div className="preview-code">
      <pre>
        <code>
          {lines.map((l, i) => (
            <span className="line" key={i}>
              {l + '\n'}
            </span>
          ))}
        </code>
      </pre>
      {file.size > LIMIT && <div className="preview-meta mono">showing the first {formatBytes(LIMIT)} of {formatBytes(file.size)}</div>}
    </div>
  )
}
