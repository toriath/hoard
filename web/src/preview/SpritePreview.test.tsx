import { act, fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { parseSprite } from '../lib/sprite'
import { SpritePreview } from './SpritePreview'

// 4 columns x 2 rows of 32px frames, with two named animations.
const sheet = parseSprite(
  { frame_width: 32, frame_height: 32, fps: 10, animations: { idle: [0, 1], run: [4, 7] } },
  { width: 128, height: 64 },
)!

beforeEach(() => {
  // Images "load" at once; jsdom has no canvas, so drawing is a no-op.
  vi.stubGlobal(
    'Image',
    class {
      onload: (() => void) | null = null
      onerror: (() => void) | null = null
      set src(_: string) {
        queueMicrotask(() => this.onload?.())
      }
    },
  )
  vi.stubGlobal('matchMedia', (q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} }))
})
afterEach(() => {
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

const renderIt = (s = sheet) => render(<SpritePreview url="/raw/x.png" sheet={s} sheetView={<div>whole sheet</div>} />)
const frameLabel = () => screen.getByRole('slider', { name: 'Frame' }).getAttribute('aria-valuetext')

describe('SpritePreview', () => {
  it('has keyboard-operable controls with names', async () => {
    renderIt()
    expect(await screen.findByRole('button', { name: 'Pause' })).toBeTruthy() // plays on its own
    expect(screen.getByRole('slider', { name: 'Frame' })).toBeTruthy()
    expect(screen.getByRole('spinbutton', { name: 'Frames per second' })).toBeTruthy()
    expect(screen.getByRole('combobox', { name: 'Animation' })).toBeTruthy()
    expect(screen.getByRole('img', { name: /frame 1 of 8/i })).toBeTruthy()
  })

  it('pauses and plays', async () => {
    renderIt()
    await userEvent.click(await screen.findByRole('button', { name: 'Pause' }))
    expect(screen.getByRole('button', { name: 'Play' })).toBeTruthy()
  })

  it('does not start moving for people who prefer reduced motion', async () => {
    vi.stubGlobal('matchMedia', (q: string) => ({ matches: true, media: q, addEventListener() {}, removeEventListener() {} }))
    renderIt()
    expect(await screen.findByRole('button', { name: 'Play' })).toBeTruthy()
  })

  it('scrubs with a native range input (arrow keys come from the browser) and stops playback', async () => {
    renderIt()
    const slider = (await screen.findByRole('slider', { name: 'Frame' })) as HTMLInputElement
    expect(slider.type).toBe('range')
    expect(frameLabel()).toBe('Frame 1 of 8')
    fireEvent.change(slider, { target: { value: '2' } })
    expect(frameLabel()).toBe('Frame 3 of 8')
    expect(screen.getByRole('button', { name: 'Play' })).toBeTruthy()
    fireEvent.change(slider, { target: { value: '7' } })
    expect(frameLabel()).toBe('Frame 8 of 8')
  })

  it('limits the scrubber to the chosen animation', async () => {
    renderIt()
    const user = userEvent.setup()
    await user.selectOptions(await screen.findByRole('combobox', { name: 'Animation' }), 'run')
    const slider = screen.getByRole('slider', { name: 'Frame' }) as HTMLInputElement
    expect([slider.min, slider.max]).toEqual(['4', '7'])
    expect(frameLabel()).toBe('Frame 1 of 4')
  })

  it('advances with time at the given fps and wraps when looping', async () => {
    vi.useFakeTimers({ toFake: ['requestAnimationFrame', 'cancelAnimationFrame', 'performance'] })
    renderIt()
    await act(async () => {
      await Promise.resolve()
    })
    expect(frameLabel()).toBe('Frame 1 of 8')
    await act(async () => {
      vi.advanceTimersByTime(350) // 10 fps: three frames
    })
    expect(frameLabel()).toMatch(/Frame [34] of 8/)
    await act(async () => {
      vi.advanceTimersByTime(1000)
    })
    expect(screen.getByRole('button', { name: 'Pause' })).toBeTruthy() // still looping
  })

  it('stops on the last frame of a one-shot animation', async () => {
    vi.useFakeTimers({ toFake: ['requestAnimationFrame', 'cancelAnimationFrame', 'performance'] })
    const once = parseSprite({ frame_width: 32, frame_height: 32, fps: 20, loop: false }, { width: 128, height: 64 })!
    renderIt(once)
    await act(async () => {
      await Promise.resolve()
    })
    await act(async () => {
      vi.advanceTimersByTime(2000)
    })
    expect(frameLabel()).toBe('Frame 8 of 8')
    expect(screen.getByRole('button', { name: 'Play' })).toBeTruthy()
  })

  it('shows the whole sheet on request', async () => {
    renderIt()
    const user = userEvent.setup()
    const toggle = await screen.findByRole('button', { name: /sheet/i })
    expect(toggle.getAttribute('aria-pressed')).toBe('false')
    await user.click(toggle)
    expect(screen.getByText('whole sheet')).toBeTruthy()
    expect(toggle.getAttribute('aria-pressed')).toBe('true')
  })

  it('falls back to the plain image when the image cannot be loaded', async () => {
    vi.stubGlobal(
      'Image',
      class {
        onload: (() => void) | null = null
        onerror: (() => void) | null = null
        set src(_: string) {
          queueMicrotask(() => this.onerror?.())
        }
      },
    )
    renderIt()
    expect(await screen.findByText('whole sheet')).toBeTruthy()
  })
})
