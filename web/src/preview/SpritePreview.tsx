import { Grid2x2, Pause, Play } from 'lucide-react'
import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { MAX_FPS, advance, clampFps, frameOrigin, isPixelArt, type SpriteSheet } from '../lib/sprite'
import { Spinner } from '../ui'

/**
 * Animated preview of a sprite sheet described by the asset's attributes
 * (frame size, frames, fps, named animations). `sheetView` is the plain image
 * preview, shown when the user wants to see the whole sheet.
 */
export function SpritePreview({ url, sheet, sheetView }: { url: string; sheet: SpriteSheet; sheetView: ReactNode }) {
  const canvas = useRef<HTMLCanvasElement>(null)
  const [image, setImage] = useState<HTMLImageElement | null>(null)
  const [failed, setFailed] = useState(false)
  const [anim, setAnim] = useState('') // '' = all frames
  const range = useMemo(() => {
    const a = sheet.animations.find((x) => x.name === anim)
    return a ? { first: a.first, last: a.last } : { first: 0, last: sheet.frames - 1 }
  }, [anim, sheet])
  const [frame, setFrame] = useState(0)
  const frameRef = useRef(0)
  frameRef.current = frame
  const [fps, setFps] = useState(sheet.fps)
  const [playing, setPlaying] = useState(() => !window.matchMedia?.('(prefers-reduced-motion: reduce)').matches)
  const [showSheet, setShowSheet] = useState(false)

  useEffect(() => {
    setImage(null)
    setFailed(false)
    const img = new Image()
    img.onload = () => setImage(img)
    img.onerror = () => setFailed(true)
    img.src = url
    return () => {
      img.onload = img.onerror = null
    }
  }, [url])

  // Changing the animation restarts it.
  useEffect(() => setFrame(range.first), [range])

  // The clock. Frame changes are driven by elapsed time, not by a timer per
  // frame, so a busy tab drops frames instead of running slow.
  useEffect(() => {
    if (!playing || !image) return
    let raf = 0
    let last = performance.now()
    let acc = 0
    const step = (now: number) => {
      acc += now - last
      last = now
      const per = 1000 / fps
      if (acc >= per) {
        const n = Math.floor(acc / per)
        acc -= n * per
        let cur = frameRef.current
        for (let i = 0; i < n; i++) {
          const next = advance(cur, range.first, range.last, sheet.loop)
          cur = next.frame
          if (next.done) {
            setPlaying(false)
            break
          }
        }
        frameRef.current = cur
        setFrame(cur)
      }
      raf = requestAnimationFrame(step)
    }
    raf = requestAnimationFrame(step)
    return () => cancelAnimationFrame(raf)
  }, [playing, image, fps, range, sheet.loop])

  const pixel = isPixelArt(sheet)
  useEffect(() => {
    const c = canvas.current
    if (!c || !image) return
    const ctx = c.getContext('2d')
    if (!ctx) return
    ctx.imageSmoothingEnabled = !pixel
    ctx.clearRect(0, 0, c.width, c.height)
    const { x, y } = frameOrigin(sheet, frame)
    ctx.drawImage(image, x, y, sheet.frameWidth, sheet.frameHeight, 0, 0, sheet.frameWidth, sheet.frameHeight)
  }, [image, frame, sheet, pixel, showSheet])

  if (failed) return <>{sheetView}</>
  const scale = pixel ? Math.max(1, Math.floor(280 / Math.max(sheet.frameWidth, sheet.frameHeight))) : 1
  const total = range.last - range.first + 1
  const play = () => {
    // Replaying a finished one-shot starts it over.
    if (!playing && !sheet.loop && frame >= range.last) setFrame(range.first)
    setPlaying((p) => !p)
  }

  return (
    <div className="sprite-preview">
      {showSheet ? (
        sheetView
      ) : (
        <div className="preview-stage checker sprite-stage">
          {!image && <Spinner label="Loading frames…" />}
          <canvas
            ref={canvas}
            className={'sprite-canvas' + (pixel ? ' pixel' : '')}
            width={sheet.frameWidth}
            height={sheet.frameHeight}
            role="img"
            aria-label={`Animation preview, frame ${frame + 1} of ${sheet.frames}`}
            style={{ visibility: image ? 'visible' : 'hidden', width: Math.min(sheet.frameWidth * scale, 4096), maxWidth: '100%', aspectRatio: `${sheet.frameWidth} / ${sheet.frameHeight}` }}
          />
        </div>
      )}
      <div className="sprite-controls">
        {!showSheet && (
          <>
            <button type="button" className="btn btn-primary btn-icon play" onClick={play} aria-label={playing ? 'Pause' : 'Play'}>
              {playing ? <Pause aria-hidden /> : <Play aria-hidden />}
            </button>
            <input
              type="range"
              className="sprite-scrub"
              min={range.first}
              max={range.last}
              step={1}
              value={Math.min(Math.max(frame, range.first), range.last)}
              aria-label="Frame"
              aria-valuetext={`Frame ${frame - range.first + 1} of ${total}`}
              onChange={(e) => {
                setPlaying(false)
                setFrame(Number(e.target.value))
              }}
            />
            <span className="mono muted sprite-count" aria-hidden>
              {frame - range.first + 1}/{total}
            </span>
            {sheet.animations.length > 0 && (
              <select className="select sprite-anim" aria-label="Animation" value={anim} onChange={(e) => setAnim(e.target.value)}>
                <option value="">All frames</option>
                {sheet.animations.map((a) => (
                  <option key={a.name} value={a.name}>
                    {a.name}
                  </option>
                ))}
              </select>
            )}
            <label className="sprite-fps">
              <input
                type="number"
                className="input"
                min={1}
                max={MAX_FPS}
                value={fps}
                aria-label="Frames per second"
                onChange={(e) => setFps(clampFps(Number(e.target.value)))}
              />
              <span className="muted">fps</span>
            </label>
          </>
        )}
        <button type="button" className="btn btn-sm" aria-pressed={showSheet} onClick={() => setShowSheet((s) => !s)} title="Show the whole sprite sheet">
          <Grid2x2 aria-hidden /> Sheet
        </button>
      </div>
    </div>
  )
}
