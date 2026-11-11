import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, expect, it } from 'vitest'
import { AnimationFields, animationDraft, animationPatch } from './AnimationFields'

describe('animationPatch', () => {
  it('sends nothing when nothing changed', () => {
    const attrs = { frame_width: 32, frame_height: 32, frames: 8, fps: 10, loop: false, animations: { walk: [0, 3] } }
    expect(animationPatch(attrs, animationDraft(attrs))).toEqual({ patch: {} })
    expect(animationPatch({}, animationDraft({}))).toEqual({ patch: {} })
  })

  it('sends changed numbers and null for cleared ones', () => {
    const before = { frame_width: 32, frame_height: 32, columns: 4 }
    const draft = { ...animationDraft(before), frame_width: '16', columns: '', fps: '8' }
    expect(animationPatch(before, draft).patch).toEqual({ frame_width: 16, columns: null, fps: 8 })
  })

  it('writes loop only when it differs from the stored value', () => {
    expect(animationPatch({}, { ...animationDraft({}), loop: false }).patch).toEqual({ loop: false })
    expect(animationPatch({ loop: false }, { ...animationDraft({ loop: false }), loop: true }).patch).toEqual({ loop: true })
  })

  it('turns the animation text into ranges, and an empty text into a deletion', () => {
    const d = animationDraft({})
    expect(animationPatch({}, { ...d, animations: 'idle: 0-1\nwalk: 2-5' }).patch).toEqual({ animations: { idle: [0, 1], walk: [2, 5] } })
    const before = { animations: { idle: [0, 1] } }
    expect(animationPatch(before, { ...animationDraft(before), animations: '' }).patch).toEqual({ animations: null })
  })

  it('rejects values that cannot be saved', () => {
    const d = animationDraft({})
    expect(animationPatch({}, { ...d, frame_width: '0' }).error).toMatch(/frame width/)
    expect(animationPatch({}, { ...d, fps: 'fast' }).error).toMatch(/fps/)
    expect(animationPatch({}, { ...d, frames: '2.5' }).error).toMatch(/frames/)
    expect(animationPatch({}, { ...d, animations: 'walk' }).error).toMatch(/walk/)
  })
})

describe('AnimationFields', () => {
  function Host({ attrs = {} }: { attrs?: Record<string, unknown> }) {
    const [v, setV] = useState(animationDraft(attrs))
    return (
      <>
        <AnimationFields value={v} onChange={setV} />
        <output data-testid="patch">{JSON.stringify(animationPatch(attrs, v).patch)}</output>
      </>
    )
  }

  it('is collapsed for assets without animation attributes and open for sheets', () => {
    const { unmount } = render(<Host />)
    expect((document.querySelector('details') as HTMLDetailsElement).open).toBe(false)
    unmount()
    render(<Host attrs={{ frame_width: 16, frame_height: 16 }} />)
    expect((document.querySelector('details') as HTMLDetailsElement).open).toBe(true)
  })

  it('has labelled fields that feed the patch', async () => {
    render(<Host attrs={{ frame_width: 16, frame_height: 16 }} />)
    await userEvent.type(screen.getByLabelText('Frames per second'), '24')
    await userEvent.type(screen.getByLabelText('Named animations'), 'run: 1-4')
    await userEvent.click(screen.getByLabelText('Loop'))
    expect(JSON.parse(screen.getByTestId('patch').textContent!)).toEqual({ fps: 24, loop: false, animations: { run: [1, 4] } })
  })
})
