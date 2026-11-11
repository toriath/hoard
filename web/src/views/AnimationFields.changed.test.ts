import { describe, expect, it } from 'vitest'
import { parseAnimationLines } from '../lib/sprite'
import { animationDraft, animationPatch } from './AnimationFields'

describe('animationPatch only judges what changed', () => {
  it('an existing fps of 7.5 does not block other edits', () => {
    const before = { frame_width: 16, frame_height: 16, fps: 7.5 }
    const draft = { ...animationDraft(before), loop: false }
    expect(animationPatch(before, draft)).toEqual({ patch: { loop: false } })
  })
  it('but a changed fps is checked, and capped at 60', () => {
    const before = { fps: 7.5 }
    expect(animationPatch(before, { ...animationDraft(before), fps: '8.5' }).error).toMatch(/fps/)
    expect(animationPatch({}, { ...animationDraft({}), fps: '120' }).patch).toEqual({ fps: 60 })
  })
  it('leaves stored animations alone until they are edited', () => {
    const before = { animations: { a: [0, 1] } }
    expect(animationPatch(before, animationDraft(before))).toEqual({ patch: {} })
  })
})

describe('parseAnimationLines with hostile names', () => {
  it('treats __proto__ as an ordinary name', () => {
    const { value } = parseAnimationLines('__proto__: 0-3\nconstructor: 4-5')
    expect(Object.keys(value)).toEqual(['__proto__', 'constructor'])
    expect(Object.getPrototypeOf(value)).toBeNull()
    expect(({} as Record<string, unknown>).polluted).toBeUndefined()
  })
  it('produces a patch value that is a plain object', () => {
    const p = animationPatch({}, { ...animationDraft({}), animations: 'idle: 0-1' }).patch.animations
    expect(Object.getPrototypeOf(p)).toBe(Object.prototype)
  })
})
