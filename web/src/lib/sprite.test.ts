import { describe, expect, it } from 'vitest'
import { advance, clampFps, formatAnimationLines, frameOrigin, isPixelArt, parseAnimationLines, parseSprite } from './sprite'

const img = { width: 128, height: 64 }

describe('parseSprite', () => {
  it('reads an explicit frame size and derives the grid', () => {
    const s = parseSprite({ frame_width: 32, frame_height: 32 }, img)!
    expect(s).toMatchObject({ frameWidth: 32, frameHeight: 32, columns: 4, rows: 2, frames: 8, fps: 12, loop: true })
  })

  it('respects frames, fps and loop', () => {
    const s = parseSprite({ frame_width: 32, frame_height: 32, frames: 6, fps: 8, loop: false }, img)!
    expect(s).toMatchObject({ frames: 6, rows: 2, fps: 8, loop: false })
  })

  it('derives the frame size from columns and frames', () => {
    const s = parseSprite({ columns: 4, frames: 8 }, img)!
    expect(s).toMatchObject({ frameWidth: 32, frameHeight: 32, columns: 4, rows: 2, frames: 8 })
  })

  it('never claims more frames than fit in the image', () => {
    expect(parseSprite({ frame_width: 32, frame_height: 32, frames: 99 }, img)!.frames).toBe(8)
    expect(parseSprite({ frame_width: 32, frame_height: 32, columns: 9 }, img)!.columns).toBe(4)
  })

  it('refuses attributes that do not describe a sheet', () => {
    expect(parseSprite(undefined, img)).toBeNull()
    expect(parseSprite({}, img)).toBeNull()
    expect(parseSprite({ frame_width: 32 }, img)).toBeNull()
    expect(parseSprite({ frame_width: 0, frame_height: 32 }, img)).toBeNull()
    expect(parseSprite({ frame_width: '32', frame_height: 32 }, img)).toBeNull()
    expect(parseSprite({ frame_width: 500, frame_height: 32 }, img)).toBeNull()
    expect(parseSprite({ columns: 4, frames: 8 }, {})).toBeNull()
    expect(parseSprite({ frame_width: 128, frame_height: 64 }, img)).toBeNull() // one frame
  })

  it('clamps fps', () => {
    expect(parseSprite({ frame_width: 32, frame_height: 32, fps: 500 }, img)!.fps).toBe(60)
    expect(parseSprite({ frame_width: 32, frame_height: 32, fps: 0 }, img)!.fps).toBe(1)
    expect(clampFps(NaN)).toBe(12)
  })

  it('reads named animations and clamps them to the sheet', () => {
    const s = parseSprite({ frame_width: 32, frame_height: 32, animations: { idle: [0, 1], run: [2, 99], bad: 'x', backwards: [5, 3], gone: [20, 30], one: [4] } }, img)!
    expect(s.animations).toEqual([
      { name: 'idle', first: 0, last: 1 },
      { name: 'run', first: 2, last: 7 },
      { name: 'backwards', first: 3, last: 5 },
      { name: 'one', first: 4, last: 4 },
    ])
  })
})

describe('frameOrigin', () => {
  const s = parseSprite({ frame_width: 32, frame_height: 16, frames: 6, columns: 4 }, { width: 128, height: 32 })!
  it('walks the grid row by row', () => {
    expect(frameOrigin(s, 0)).toEqual({ x: 0, y: 0 })
    expect(frameOrigin(s, 3)).toEqual({ x: 96, y: 0 })
    expect(frameOrigin(s, 4)).toEqual({ x: 0, y: 16 })
  })
  it('clamps out-of-range frames', () => {
    expect(frameOrigin(s, -3)).toEqual({ x: 0, y: 0 })
    expect(frameOrigin(s, 100)).toEqual(frameOrigin(s, 5))
  })
})

describe('advance', () => {
  it('steps and wraps when looping', () => {
    expect(advance(1, 0, 3, true)).toEqual({ frame: 2, done: false })
    expect(advance(3, 0, 3, true)).toEqual({ frame: 0, done: false })
  })
  it('stops on the last frame when not looping', () => {
    expect(advance(2, 0, 3, false)).toEqual({ frame: 3, done: false })
    expect(advance(3, 0, 3, false)).toEqual({ frame: 3, done: true })
  })
  it('moves into the range from outside it', () => {
    expect(advance(0, 4, 7, true)).toEqual({ frame: 4, done: false })
  })
})

describe('isPixelArt', () => {
  it('is for small frames', () => {
    expect(isPixelArt(parseSprite({ frame_width: 16, frame_height: 16 }, { width: 64, height: 16 })!)).toBe(true)
    expect(isPixelArt(parseSprite({ frame_width: 256, frame_height: 256 }, { width: 1024, height: 256 })!)).toBe(false)
  })
})

describe('animation lines', () => {
  it('parses the accepted notations', () => {
    const { value, errors } = parseAnimationLines('walk: 0-5\nidle:6\njump 7 9\nrun: 12 to 10\n\nfire2: 1,2')
    expect(errors).toEqual([])
    expect(value).toEqual({ walk: [0, 5], idle: [6, 6], jump: [7, 9], run: [10, 12], fire2: [1, 2] })
  })
  it('reports lines it cannot read', () => {
    expect(parseAnimationLines('walk\nrun: x-y').errors).toEqual(['walk', 'run: x-y'])
  })
  it('round-trips', () => {
    const text = 'idle: 0-1\nwalk: 2-5'
    expect(formatAnimationLines(parseAnimationLines(text).value)).toBe(text)
    expect(formatAnimationLines(undefined)).toBe('')
    expect(formatAnimationLines([1, 2])).toBe('')
  })
})
