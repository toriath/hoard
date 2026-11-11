import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { Activity, FolderOpen, Library, ClipboardList, Settings, Tags, Trash2, Gamepad2 } from 'lucide-react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { BottomNav, type NavItem } from './BottomNav'

const nav = (to: string, label: string, icon: NavItem['icon']): NavItem => ({ to, label, icon, match: (p) => p === to })
const items = [
  nav('/', 'Library', Library),
  nav('/collections', 'Collections', FolderOpen),
  nav('/projects', 'Projects', Gamepad2),
  nav('/requests', 'Requests', ClipboardList),
  nav('/tags', 'Tags', Tags),
  nav('/activity', 'Activity', Activity),
  nav('/trash', 'Trash', Trash2),
  nav('/settings', 'Settings', Settings),
]

beforeEach(() => {
  window.scrollTo = vi.fn() as typeof window.scrollTo
  window.history.replaceState(null, '', '/')
})

describe('BottomNav', () => {
  it('shows four destinations and a More button', () => {
    render(<BottomNav items={items} path="/" />)
    const nav = screen.getByRole('navigation', { name: 'Main' })
    expect([...nav.querySelectorAll('a')].map((a) => a.textContent)).toEqual(['Library', 'Collections', 'Projects', 'Requests'])
    expect(screen.getByRole('button', { name: 'More' }).getAttribute('aria-haspopup')).toBe('menu')
    expect(screen.queryByRole('menu')).toBeNull()
  })

  it('marks the current page', () => {
    render(<BottomNav items={items} path="/projects" />)
    expect(screen.getByRole('link', { name: 'Projects' }).getAttribute('aria-current')).toBe('page')
    expect(screen.getByRole('link', { name: 'Library' }).getAttribute('aria-current')).toBeNull()
    expect(screen.getByRole('button', { name: 'More' }).getAttribute('aria-current')).toBeNull()
  })

  it('lists the other pages under More and navigates', async () => {
    render(<BottomNav items={items} path="/" />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'More' }))
    expect(screen.getAllByRole('menuitem').map((m) => m.textContent)).toEqual(['Tags', 'Activity', 'Trash', 'Settings'])
    await user.click(screen.getByRole('menuitem', { name: 'Trash' }))
    expect(window.location.pathname).toBe('/trash')
    expect(screen.queryByRole('menu')).toBeNull()
  })

  it('shows More as current when one of its pages is open, and is keyboard operable', async () => {
    render(<BottomNav items={items} path="/activity" />)
    const more = screen.getByRole('button', { name: 'More' })
    expect(more.getAttribute('aria-current')).toBe('page')
    const user = userEvent.setup()
    more.focus()
    await user.keyboard('{Enter}')
    expect(document.activeElement).toBe(screen.getByRole('menuitem', { name: 'Tags' }))
    await user.keyboard('{Escape}')
    expect(document.activeElement).toBe(more)
  })
})
