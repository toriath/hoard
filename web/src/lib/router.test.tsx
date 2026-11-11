import { act, fireEvent, render, screen } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { link, navigate, useLocation } from './router'

function Where() {
  const loc = useLocation()
  return (
    <div>
      <output data-testid="path">{loc.path}</output>
      <output data-testid="q">{loc.query.get('q') ?? ''}</output>
      <a {...link('/collections')}>go</a>
    </div>
  )
}

beforeEach(() => {
  window.scrollTo = vi.fn() as typeof window.scrollTo
  window.history.replaceState(null, '', '/')
})

describe('router', () => {
  it('reads path and query from the URL', () => {
    window.history.replaceState(null, '', '/tags?q=fire%20ball')
    render(<Where />)
    expect(screen.getByTestId('path').textContent).toBe('/tags')
    expect(screen.getByTestId('q').textContent).toBe('fire ball')
  })

  it('navigate pushes history and re-renders', () => {
    render(<Where />)
    const before = window.history.length
    act(() => navigate('/projects?q=x'))
    expect(window.location.pathname + window.location.search).toBe('/projects?q=x')
    expect(window.history.length).toBe(before + 1)
    expect(screen.getByTestId('path').textContent).toBe('/projects')
    expect(screen.getByTestId('q').textContent).toBe('x')
  })

  it('replace does not add an entry, and navigating to the same URL does nothing', () => {
    render(<Where />)
    act(() => navigate('/a'))
    const n = window.history.length
    act(() => navigate('/b', { replace: true }))
    expect(window.history.length).toBe(n)
    act(() => navigate('/b'))
    expect(window.history.length).toBe(n)
  })

  it('scrolls to the top on a new page but not when only the query changes', () => {
    render(<Where />)
    act(() => navigate('/page'))
    expect(window.scrollTo).toHaveBeenCalledTimes(1)
    act(() => navigate('/page?q=1'))
    expect(window.scrollTo).toHaveBeenCalledTimes(1)
  })

  it('follows the back button', () => {
    render(<Where />)
    act(() => navigate('/one'))
    act(() => navigate('/two'))
    act(() => {
      window.history.back()
    })
    return vi.waitFor(() => expect(screen.getByTestId('path').textContent).toBe('/one'))
  })

  // What our handler decided, read at the window (after React ran), where the
  // click is then cancelled so jsdom does not try to load the page.
  function clicked(el: Element, init: MouseEventInit = {}): boolean {
    let prevented = false
    const spy = (e: Event) => {
      prevented = e.defaultPrevented
      e.preventDefault()
    }
    window.addEventListener('click', spy)
    fireEvent.click(el, init)
    window.removeEventListener('click', spy)
    return prevented
  }

  it('link() navigates client-side on a plain click', () => {
    render(<Where />)
    const a = screen.getByText('go')
    expect(a.getAttribute('href')).toBe('/collections')
    expect(clicked(a)).toBe(true)
    expect(screen.getByTestId('path').textContent).toBe('/collections')
  })

  it('link() leaves modified clicks to the browser', () => {
    render(<Where />)
    const a = screen.getByText('go')
    for (const mod of [{ ctrlKey: true }, { metaKey: true }, { shiftKey: true }, { altKey: true }, { button: 1 }]) {
      expect(clicked(a, mod)).toBe(false)
    }
    expect(screen.getByTestId('path').textContent).toBe('/')
  })
})
