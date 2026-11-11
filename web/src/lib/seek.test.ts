import { describe, expect, it } from 'vitest'
import { seekStep, seekTarget } from './seek'

describe('seekTarget', () => {
  it('steps finely on short sounds and coarsely on long tracks', () => {
    expect(seekStep(3)).toBe(0.25)
    expect(seekStep(180)).toBe(5)
    expect(seekTarget('ArrowRight', 1, 3)).toBe(1.25)
    expect(seekTarget('ArrowLeft', 1, 3)).toBe(0.75)
    expect(seekTarget('ArrowRight', 10, 180)).toBe(15)
  })
  it('treats Up/Down like Right/Left', () => {
    expect(seekTarget('ArrowUp', 1, 3)).toBe(1.25)
    expect(seekTarget('ArrowDown', 1, 3)).toBe(0.75)
  })
  it('jumps by a tenth with Page keys (at least a second)', () => {
    expect(seekTarget('PageUp', 0, 100)).toBe(10)
    expect(seekTarget('PageDown', 50, 100)).toBe(40)
    expect(seekTarget('PageUp', 0, 4)).toBe(1)
  })
  it('goes to the ends with Home and End', () => {
    expect(seekTarget('Home', 7, 30)).toBe(0)
    expect(seekTarget('End', 7, 30)).toBe(30)
  })
  it('stays within the file', () => {
    expect(seekTarget('ArrowLeft', 0.1, 3)).toBe(0)
    expect(seekTarget('ArrowRight', 2.9, 3)).toBe(3)
    expect(seekTarget('PageUp', 95, 100)).toBe(100)
  })
  it('ignores other keys and unknown durations', () => {
    expect(seekTarget('a', 1, 3)).toBeNull()
    expect(seekTarget('Tab', 1, 3)).toBeNull()
    expect(seekTarget('ArrowRight', 0, 0)).toBeNull()
    expect(seekTarget('ArrowRight', 0, NaN)).toBeNull()
  })
})
