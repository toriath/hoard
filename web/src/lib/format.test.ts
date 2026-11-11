import { afterEach, describe, expect, it, vi } from 'vitest'
import { copyText, formatBytes, formatDuration, plural, timeAgo } from './format'

describe('formatBytes', () => {
  it('handles zero, negative and non-finite input', () => {
    expect(formatBytes(0)).toBe('0 B')
    expect(formatBytes(-5)).toBe('0 B')
    expect(formatBytes(NaN)).toBe('0 B')
    expect(formatBytes(Infinity)).toBe('0 B')
  })
  it('scales units and keeps one decimal below 100', () => {
    expect(formatBytes(512)).toBe('512 B')
    expect(formatBytes(1024)).toBe('1.0 KB')
    expect(formatBytes(1536)).toBe('1.5 KB')
    expect(formatBytes(150 * 1024)).toBe('150 KB')
    expect(formatBytes(5 * 1024 ** 2)).toBe('5.0 MB')
    expect(formatBytes(3 * 1024 ** 3)).toBe('3.0 GB')
  })
  it('does not run past terabytes', () => {
    expect(formatBytes(2048 * 1024 ** 4)).toBe('2048 TB')
  })
})

describe('formatDuration', () => {
  it('shows seconds with decimals for short sounds', () => {
    expect(formatDuration(0.5)).toBe('0.50s')
    expect(formatDuration(9.999)).toBe('10.00s')
  })
  it('shows minutes and padded seconds from ten seconds', () => {
    expect(formatDuration(10)).toBe('0:10')
    expect(formatDuration(65)).toBe('1:05')
    expect(formatDuration(3725)).toBe('62:05')
  })
  it('shows a dash for non-finite values', () => {
    expect(formatDuration(NaN)).toBe('–')
    expect(formatDuration(Infinity)).toBe('–')
  })
})

describe('plural', () => {
  it('chooses the form by count', () => {
    expect(plural(1, 'asset')).toBe('1 asset')
    expect(plural(0, 'asset')).toBe('0 assets')
    expect(plural(2, 'person', 'people')).toBe('2 people')
  })
  it('groups thousands', () => {
    expect(plural(1234, 'file')).toBe('1,234 files')
  })
})

describe('timeAgo', () => {
  afterEach(() => vi.useRealTimers())
  const at = (offsetSeconds: number) => new Date(Date.now() + offsetSeconds * 1000).toISOString()

  it('is relative up to a month', () => {
    vi.useFakeTimers({ now: new Date('2026-06-15T12:00:00Z') })
    expect(timeAgo(at(-20))).toBe('just now')
    expect(timeAgo(at(-5 * 60))).toBe('5 minutes ago')
    expect(timeAgo(at(-3 * 3600))).toBe('3 hours ago')
    expect(timeAgo(at(-86400))).toBe('yesterday')
    expect(timeAgo(at(-3 * 86400))).toBe('3 days ago')
  })
  it('is a date after a month', () => {
    vi.useFakeTimers({ now: new Date('2026-06-15T12:00:00Z') })
    expect(timeAgo('2026-01-02T12:00:00Z')).toMatch(/2026/)
  })
})

describe('copyText', () => {
  afterEach(() => vi.unstubAllGlobals())
  it('uses the clipboard API when it is there', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    vi.stubGlobal('navigator', { clipboard: { writeText } })
    expect(await copyText('hello')).toBe(true)
    expect(writeText).toHaveBeenCalledWith('hello')
  })
  it('falls back to a temporary textarea outside secure contexts', async () => {
    vi.stubGlobal('navigator', { clipboard: { writeText: vi.fn().mockRejectedValue(new Error('denied')) } })
    const exec = vi.fn().mockReturnValue(true)
    document.execCommand = exec
    expect(await copyText('plain http')).toBe(true)
    expect(exec).toHaveBeenCalledWith('copy')
    expect(document.querySelector('textarea')).toBeNull()
  })
})
