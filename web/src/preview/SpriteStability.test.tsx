import { act, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { parseSprite } from '../lib/sprite'
import type { Asset, AssetFile } from '../lib/types'
import { Preview } from './Preview'
import { SpritePreview } from './SpritePreview'

const make = () => parseSprite({ frame_width: 32, frame_height: 32, fps: 10 }, { width: 128, height: 64 })!
const whole = <div>whole sheet</div>
const label = () => screen.getByRole('slider', { name: 'Frame' }).getAttribute('aria-valuetext')

beforeEach(() => {
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

describe('SpritePreview stays put across re-renders', () => {
  it('keeps a scrubbed frame when re-rendered with an equal but new sheet', async () => {
    const { rerender } = render(<SpritePreview url="/x.png" sheet={make()} sheetView={whole} />)
    fireEvent.change(await screen.findByRole('slider', { name: 'Frame' }), { target: { value: '5' } })
    expect(label()).toBe('Frame 6 of 8')
    rerender(<SpritePreview url="/x.png" sheet={make()} sheetView={whole} />)
    expect(label()).toBe('Frame 6 of 8')
    expect(screen.getByRole('button', { name: 'Play' })).toBeTruthy()
  })

  it('follows a changed fps attribute', async () => {
    const { rerender } = render(<SpritePreview url="/x.png" sheet={make()} sheetView={whole} />)
    const input = () => screen.getByRole('spinbutton', { name: 'Frames per second' }) as HTMLInputElement
    await screen.findByRole('slider', { name: 'Frame' })
    expect(input().value).toBe('10')
    rerender(<SpritePreview url="/x.png" sheet={{ ...make(), fps: 24 }} sheetView={whole} />)
    expect(input().value).toBe('24')
  })

  it('does not run the clock while the whole sheet is shown', async () => {
    vi.useFakeTimers({ toFake: ['requestAnimationFrame', 'cancelAnimationFrame', 'performance'] })
    render(<SpritePreview url="/x.png" sheet={make()} sheetView={whole} />)
    await act(async () => {
      await Promise.resolve()
    })
    fireEvent.click(screen.getByRole('button', { name: /sheet/i }))
    const spy = vi.spyOn(window, 'requestAnimationFrame')
    await act(async () => {
      vi.advanceTimersByTime(1000)
    })
    expect(spy).not.toHaveBeenCalled()
  })
})

describe('Preview of a sprite sheet', () => {
  const file = { id: 'fil_1', filename: 'walk.png', mime: 'image/png', size: 10, sha256: 'x', kind: 'image', width: 128, height: 64, created_at: '' } as AssetFile
  const asset = (attrs: Record<string, unknown>) => ({ id: 'ast_1', type: 'sprite', attrs, files: [file] }) as unknown as Asset

  it('keeps the scrubbed frame when the asset is refetched with identical attributes', async () => {
    const attrs = { frame_width: 32, frame_height: 32 }
    const { rerender } = render(<Preview asset={asset({ ...attrs })} file={file} />)
    fireEvent.change(await screen.findByRole('slider', { name: 'Frame' }), { target: { value: '4' } })
    expect(label()).toBe('Frame 5 of 8')
    rerender(<Preview asset={asset({ ...attrs })} file={file} />)
    expect(label()).toBe('Frame 5 of 8')
  })

  it('shows a plain image without animation attributes', () => {
    render(<Preview asset={asset({})} file={file} />)
    expect(screen.getByRole('img', { name: 'walk.png' })).toBeTruthy()
    expect(screen.queryByRole('slider')).toBeNull()
  })
})
