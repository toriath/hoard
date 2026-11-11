import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { api } from '../lib/api'
import { Button, ConfirmProvider, Dialog, Field, Menu, TagInput, Tabs, useConfirm } from './index'

vi.mock('../lib/api', () => ({ api: { tags: vi.fn() } }))

beforeEach(() => {
  vi.mocked(api.tags).mockResolvedValue([])
  // jsdom has no layout: every element would count as hidden for the dialog's
  // "first visible control" search.
  Object.defineProperty(HTMLElement.prototype, 'offsetParent', { configurable: true, get: () => document.body })
})

describe('Field', () => {
  it('ties a lone input to its label', async () => {
    render(
      <Field label="Name">
        <input />
      </Field>,
    )
    const input = screen.getByLabelText('Name')
    await userEvent.type(input, 'x')
    expect((input as HTMLInputElement).value).toBe('x')
  })

  it('labels a group of controls instead of wrapping them in a <label>', () => {
    render(
      <Field label="Tags">
        <button>one</button>
        <button>two</button>
      </Field>,
    )
    const group = screen.getByRole('group', { name: 'Tags' })
    expect(group.querySelectorAll('button')).toHaveLength(2)
    expect(document.querySelector('label')).toBeNull()
  })

  it('shows an error in place of the hint', () => {
    render(
      <Field label="Name" hint="a hint" error="required">
        <input />
      </Field>,
    )
    expect(screen.getByText('required')).toBeTruthy()
    expect(screen.queryByText('a hint')).toBeNull()
  })
})

describe('Button', () => {
  it('gives an icon-only button an accessible name', () => {
    render(<Button iconOnly aria-label="Delete" />)
    expect(screen.getByRole('button', { name: 'Delete' })).toBeTruthy()
  })
  it('is disabled while busy', () => {
    render(<Button busy>Save</Button>)
    expect((screen.getByRole('button') as HTMLButtonElement).disabled).toBe(true)
  })
})

describe('Dialog', () => {
  function Host({ onClose = () => {} }: { onClose?: () => void }) {
    return (
      <>
        <button>outside</button>
        <Dialog title="Edit" onClose={onClose} footer={<button>Save</button>}>
          <input aria-label="first" />
          <input aria-label="second" />
        </Dialog>
      </>
    )
  }

  it('is a labelled modal dialog and focuses the first control', () => {
    render(<Host />)
    expect(screen.getByRole('dialog', { name: 'Edit' }).getAttribute('aria-modal')).toBe('true')
    expect(document.activeElement).toBe(screen.getByLabelText('first'))
  })

  it('closes on Escape and on the close button', async () => {
    const onClose = vi.fn()
    render(<Host onClose={onClose} />)
    await userEvent.keyboard('{Escape}')
    expect(onClose).toHaveBeenCalledTimes(1)
    await userEvent.click(screen.getByRole('button', { name: 'Close' }))
    expect(onClose).toHaveBeenCalledTimes(2)
  })

  it('keeps Tab inside the dialog in both directions', async () => {
    render(<Host />)
    const user = userEvent.setup()
    const close = screen.getByRole('button', { name: 'Close' })
    const save = screen.getByRole('button', { name: 'Save' })
    // forward from the last control wraps to the first (the close button)
    save.focus()
    await user.tab()
    expect(document.activeElement).toBe(close)
    // backward from the first wraps to the last
    await user.tab({ shift: true })
    expect(document.activeElement).toBe(save)
    // focus that escaped is pulled back in
    screen.getByText('outside').focus()
    await user.tab()
    expect(screen.getByRole('dialog').contains(document.activeElement)).toBe(true)
  })

  it('returns focus to the opener when it closes', async () => {
    function Opener() {
      const [open, setOpen] = useState(false)
      return (
        <>
          <button onClick={() => setOpen(true)}>open</button>
          {open && (
            <Dialog title="T" onClose={() => setOpen(false)}>
              <input aria-label="inside" />
            </Dialog>
          )}
        </>
      )
    }
    render(<Opener />)
    const opener = screen.getByText('open')
    opener.focus()
    await userEvent.click(opener)
    await waitFor(() => expect(screen.getByLabelText('inside')).toBeTruthy())
    await userEvent.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(document.activeElement).toBe(opener)
  })

  it('only the top dialog reacts to Escape', async () => {
    const bottom = vi.fn()
    const top = vi.fn()
    render(
      <>
        <Dialog title="Bottom" onClose={bottom}>
          <input aria-label="b" />
        </Dialog>
        <Dialog title="Top" onClose={top}>
          <input aria-label="t" />
        </Dialog>
      </>,
    )
    await userEvent.keyboard('{Escape}')
    expect(top).toHaveBeenCalledTimes(1)
    expect(bottom).not.toHaveBeenCalled()
  })
})

describe('useConfirm', () => {
  function Ask({ onAnswer }: { onAnswer: (v: boolean) => void }) {
    const confirm = useConfirm()
    return <button onClick={async () => onAnswer(await confirm({ title: 'Delete?', message: 'Really', confirmLabel: 'Delete', danger: true }))}>ask</button>
  }
  it('resolves true on confirm and false on cancel', async () => {
    const answers: boolean[] = []
    render(
      <ConfirmProvider>
        <Ask onAnswer={(v) => answers.push(v)} />
      </ConfirmProvider>,
    )
    await userEvent.click(screen.getByText('ask'))
    await userEvent.click(await screen.findByRole('button', { name: 'Delete' }))
    await userEvent.click(screen.getByText('ask'))
    await userEvent.click(await screen.findByRole('button', { name: 'Cancel' }))
    await waitFor(() => expect(answers).toEqual([true, false]))
  })
})

describe('Menu', () => {
  const onA = vi.fn()
  const onC = vi.fn()
  function Host() {
    return (
      <Menu
        trigger={(p) => <button {...p}>actions</button>}
        items={[
          { label: 'A', onSelect: onA },
          { label: 'B', disabled: true },
          { separator: true, label: '' },
          { label: 'C', onSelect: onC },
        ]}
      />
    )
  }

  it('opens from the trigger and focuses the first enabled item', async () => {
    render(<Host />)
    expect(screen.queryByRole('menu')).toBeNull()
    await userEvent.click(screen.getByText('actions'))
    expect(screen.getByRole('menu')).toBeTruthy()
    expect(document.activeElement).toBe(screen.getByRole('menuitem', { name: 'A' }))
  })

  it('moves with arrows (skipping disabled items, wrapping) and Home/End', async () => {
    render(<Host />)
    const user = userEvent.setup()
    await user.click(screen.getByText('actions'))
    await user.keyboard('{ArrowDown}')
    expect(document.activeElement).toBe(screen.getByRole('menuitem', { name: 'C' }))
    await user.keyboard('{ArrowDown}')
    expect(document.activeElement).toBe(screen.getByRole('menuitem', { name: 'A' }))
    await user.keyboard('{ArrowUp}')
    expect(document.activeElement).toBe(screen.getByRole('menuitem', { name: 'C' }))
    await user.keyboard('{Home}')
    expect(document.activeElement).toBe(screen.getByRole('menuitem', { name: 'A' }))
    await user.keyboard('{End}')
    expect(document.activeElement).toBe(screen.getByRole('menuitem', { name: 'C' }))
  })

  it('Escape closes and returns focus to the trigger', async () => {
    render(<Host />)
    const user = userEvent.setup()
    await user.click(screen.getByText('actions'))
    await user.keyboard('{Escape}')
    expect(screen.queryByRole('menu')).toBeNull()
    expect(document.activeElement).toBe(screen.getByText('actions'))
  })

  it('selecting an item runs it and closes the menu; outside clicks close without running', async () => {
    render(<Host />)
    const user = userEvent.setup()
    await user.click(screen.getByText('actions'))
    await user.click(screen.getByRole('menuitem', { name: 'C' }))
    expect(onC).toHaveBeenCalledTimes(1)
    expect(screen.queryByRole('menu')).toBeNull()
    await user.click(screen.getByText('actions'))
    await user.click(document.body)
    expect(screen.queryByRole('menu')).toBeNull()
    expect(onA).not.toHaveBeenCalled()
  })
})

describe('TagInput', () => {
  function Host({ initial = [] as string[] }) {
    const [tags, setTags] = useState(initial)
    return (
      <>
        <TagInput value={tags} onChange={setTags} />
        <output data-testid="tags">{tags.join('|')}</output>
      </>
    )
  }
  const tags = () => screen.getByTestId('tags').textContent

  it('commits a tag on Enter, normalised to kebab-case', async () => {
    render(<Host />)
    await userEvent.type(screen.getByRole('textbox'), 'Fire Ball!{Enter}')
    expect(tags()).toBe('fire-ball')
  })

  it('commits several tags at a comma and ignores duplicates', async () => {
    render(<Host initial={['a']} />)
    await userEvent.type(screen.getByRole('textbox'), 'a, b, c,')
    expect(tags()).toBe('a|b|c')
  })

  it('removes the last tag on Backspace in an empty field and a tag by its button', async () => {
    render(<Host initial={['one', 'two', 'three']} />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('textbox'))
    await user.keyboard('{Backspace}')
    expect(tags()).toBe('one|two')
    await user.click(screen.getAllByRole('button', { name: 'Remove' })[0])
    expect(tags()).toBe('two')
  })

  it('commits pending text on blur', async () => {
    render(<Host />)
    const user = userEvent.setup()
    await user.type(screen.getByRole('textbox'), 'pending')
    await user.tab()
    expect(tags()).toBe('pending')
  })

  it('offers suggestions that are not yet chosen and picks one with the keyboard', async () => {
    vi.mocked(api.tags).mockResolvedValue([
      { tag: 'sword', count: 3 },
      { tag: 'shield', count: 2 },
    ])
    render(<Host initial={['sword']} />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('textbox'))
    const option = await screen.findByRole('option', { name: 'shield' })
    expect(screen.queryByRole('option', { name: 'sword' })).toBeNull()
    expect(option.getAttribute('aria-selected')).toBe('false')
    await user.keyboard('{ArrowDown}')
    expect(option.getAttribute('aria-selected')).toBe('true')
    await user.keyboard('{Enter}')
    expect(tags()).toBe('sword|shield')
  })
})

describe('Tabs', () => {
  it('marks the selected tab and reports changes', async () => {
    const onChange = vi.fn()
    render(<Tabs tabs={[{ id: 'a', label: 'A' }, { id: 'b', label: 'B' }]} value="a" onChange={onChange} />)
    expect(screen.getByRole('tab', { name: 'A' }).getAttribute('aria-selected')).toBe('true')
    await userEvent.click(screen.getByRole('tab', { name: 'B' }))
    expect(onChange).toHaveBeenCalledWith('b')
  })
})
