import { fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { Asset, AssetFile } from '../lib/types'
import { Preview } from './Preview'

vi.mock('../lib/thumbs', () => ({
  MAX_DECODE_SECONDS: 480,
  computePeaks: () => new Float32Array(),
  decodeAudio: () => new Promise(() => {}), // never resolves: no waveform is drawn
  drawWaveform: () => {},
  loadFont: () => new Promise(() => {}),
}))

const file = { id: 'fil_1', filename: 'boom.wav', mime: 'audio/wav', size: 10, sha256: 'x', kind: 'audio', created_at: '' } as AssetFile
const asset = { id: 'ast_1', type: 'sfx', attrs: {}, files: [file] } as unknown as Asset

beforeEach(() => {
  window.scrollTo = vi.fn() as typeof window.scrollTo
})

function setup(duration: number) {
  render(<Preview asset={asset} file={file} />)
  const audio = document.querySelector('audio') as HTMLAudioElement
  let time = 0
  Object.defineProperty(audio, 'currentTime', { configurable: true, get: () => time, set: (v: number) => (time = v) })
  Object.defineProperty(audio, 'duration', { configurable: true, get: () => duration })
  audio.play = vi.fn().mockResolvedValue(undefined)
  audio.pause = vi.fn()
  fireEvent.loadedMetadata(audio)
  return { audio, now: () => time }
}

describe('audio waveform as a slider', () => {
  it('exposes the playback position to assistive technology', () => {
    setup(12)
    const slider = screen.getByRole('slider', { name: 'Seek' })
    expect(slider.getAttribute('aria-valuemin')).toBe('0')
    expect(slider.getAttribute('aria-valuemax')).toBe('12')
    expect(slider.getAttribute('aria-valuenow')).toBe('0')
    expect(slider.getAttribute('aria-valuetext')).toBe('0.00s of 0:12')
    expect(slider.tabIndex).toBe(0)
  })

  it('is not focusable until the duration is known', () => {
    render(<Preview asset={asset} file={file} />)
    const slider = screen.getByRole('slider', { name: 'Seek' })
    expect(slider.tabIndex).toBe(-1)
    expect(slider.getAttribute('aria-disabled')).toBe('true')
  })

  it('seeks with the arrow, Page, Home and End keys', async () => {
    const { now } = setup(100)
    const user = userEvent.setup()
    screen.getByRole('slider', { name: 'Seek' }).focus()
    await user.keyboard('{ArrowRight}')
    expect(now()).toBe(5)
    await user.keyboard('{PageUp}')
    expect(now()).toBe(15)
    await user.keyboard('{ArrowLeft}{ArrowLeft}')
    expect(now()).toBe(5)
    await user.keyboard('{End}')
    expect(now()).toBe(100)
    await user.keyboard('{Home}')
    expect(now()).toBe(0)
    expect(screen.getByRole('slider', { name: 'Seek' }).getAttribute('aria-valuenow')).toBe('0')
  })

  it('reports the new position', async () => {
    setup(100)
    const slider = screen.getByRole('slider', { name: 'Seek' })
    slider.focus()
    await userEvent.keyboard('{PageUp}')
    expect(slider.getAttribute('aria-valuenow')).toBe('10')
    expect(slider.getAttribute('aria-valuetext')).toBe('0:10 of 1:40')
  })

  it('Space plays and other keys keep their default', async () => {
    const { audio } = setup(100)
    Object.defineProperty(audio, 'paused', { configurable: true, get: () => true })
    const slider = screen.getByRole('slider', { name: 'Seek' })
    slider.focus()
    await userEvent.keyboard(' ')
    expect(audio.play).toHaveBeenCalled()
    const tab = fireEvent.keyDown(slider, { key: 'Tab' })
    expect(tab).toBe(true) // not prevented
  })
})
