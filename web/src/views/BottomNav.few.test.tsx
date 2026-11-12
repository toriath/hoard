import { render, screen } from '@testing-library/react'
import { Library, Tags } from 'lucide-react'
import { describe, expect, it } from 'vitest'
import { BottomNav } from './BottomNav'

describe('BottomNav with few destinations', () => {
  it('has no empty More menu', () => {
    const items = [
      { to: '/', label: 'Library', icon: Library, match: (p: string) => p === '/' },
      { to: '/tags', label: 'Tags', icon: Tags, match: (p: string) => p === '/tags' },
    ]
    render(<BottomNav items={items} path="/" />)
    expect(screen.queryByRole('button', { name: 'More' })).toBeNull()
    expect(screen.getAllByRole('link')).toHaveLength(2)
  })
})
