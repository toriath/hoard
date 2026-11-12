import { act, render, renderHook, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { Asset } from './types'
import {
  applyTheme, changed, emitAsset, getTheme, prefs, toast, toastError, useAssetUpdates, useQuery, useToasts, useVersion,
} from './store'

describe('change notifications', () => {
  it('bumps the version of the topic only', () => {
    const { result } = renderHook(() => [useVersion('assets'), useVersion('groups')])
    const [a0, g0] = result.current
    act(() => changed('assets'))
    expect(result.current).toEqual([a0 + 1, g0])
    act(() => changed('assets', 'groups'))
    expect(result.current).toEqual([a0 + 2, g0 + 1])
  })
})

describe('useQuery', () => {
  it('loads data and reloads on demand', async () => {
    let n = 0
    const { result } = renderHook(() => useQuery(async () => ++n, []))
    expect(result.current.loading).toBe(true)
    await waitFor(() => expect(result.current.data).toBe(1))
    expect(result.current.loading).toBe(false)
    act(() => result.current.reload())
    await waitFor(() => expect(result.current.data).toBe(2))
  })

  it('refetches when a dependency changes', async () => {
    const fn = vi.fn(async (k: string) => k.toUpperCase())
    const { result, rerender } = renderHook(({ k }) => useQuery(() => fn(k), [k]), { initialProps: { k: 'a' } })
    await waitFor(() => expect(result.current.data).toBe('A'))
    rerender({ k: 'b' })
    await waitFor(() => expect(result.current.data).toBe('B'))
  })

  it('keeps the previous data when a refresh fails', async () => {
    let fail = false
    const { result } = renderHook(() =>
      useQuery(async () => {
        if (fail) throw new Error('offline')
        return 'ok'
      }, []),
    )
    await waitFor(() => expect(result.current.data).toBe('ok'))
    fail = true
    act(() => result.current.reload())
    await waitFor(() => expect(result.current.error?.message).toBe('offline'))
    expect(result.current.data).toBe('ok')
  })

  it('ignores an answer that arrives after the dependency moved on', async () => {
    let resolveFirst: (v: string) => void = () => {}
    const { result, rerender } = renderHook(
      ({ k }) => useQuery(() => (k === 'a' ? new Promise<string>((r) => (resolveFirst = r)) : Promise.resolve('second')), [k]),
      { initialProps: { k: 'a' } },
    )
    rerender({ k: 'b' })
    await waitFor(() => expect(result.current.data).toBe('second'))
    await act(async () => resolveFirst('stale'))
    expect(result.current.data).toBe('second')
  })
})

describe('toasts', () => {
  afterEach(() => vi.useRealTimers())

  function Toasts() {
    return (
      <ul>
        {useToasts().map((t) => (
          <li key={t.id}>{t.kind}:{t.message}</li>
        ))}
      </ul>
    )
  }

  it('shows a toast and removes it after its time', () => {
    vi.useFakeTimers()
    render(<Toasts />)
    act(() => toast('saved'))
    expect(screen.getByText('info:saved')).toBeTruthy()
    act(() => {
      vi.advanceTimersByTime(3300)
    })
    expect(screen.queryByText('info:saved')).toBeNull()
  })

  it('keeps errors longer and formats non-Error values', () => {
    vi.useFakeTimers()
    render(<Toasts />)
    act(() => toastError(new Error('boom')))
    act(() => toastError('plain'))
    act(() => {
      vi.advanceTimersByTime(4000)
    })
    expect(screen.getByText('error:boom')).toBeTruthy()
    expect(screen.getByText('error:plain')).toBeTruthy()
    act(() => {
      vi.advanceTimersByTime(2100)
    })
    expect(screen.queryByText('error:boom')).toBeNull()
  })
})

describe('preferences and theme', () => {
  afterEach(() => {
    localStorage.clear()
    delete document.documentElement.dataset.theme
    vi.restoreAllMocks()
  })

  it('stores and removes values', () => {
    prefs.set('k', 'v')
    expect(prefs.get('k')).toBe('v')
    prefs.remove('k')
    expect(prefs.get('k')).toBeNull()
  })

  it('survives blocked storage', () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('blocked')
    })
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('blocked')
    })
    expect(prefs.get('k')).toBeNull()
    expect(() => prefs.set('k', 'v')).not.toThrow()
  })

  it('applies, remembers and clears a theme', () => {
    expect(getTheme()).toBe('auto')
    applyTheme('dark')
    expect(document.documentElement.dataset.theme).toBe('dark')
    expect(getTheme()).toBe('dark')
    applyTheme('auto')
    expect(document.documentElement.dataset.theme).toBeUndefined()
    expect(getTheme()).toBe('auto')
  })
})

describe('asset updates', () => {
  it('delivers emitted assets to subscribers until they unmount', () => {
    const seen: string[] = []
    const { unmount } = renderHook(() => useAssetUpdates((a) => seen.push(a.id)))
    emitAsset({ id: 'ast_1' } as Asset)
    unmount()
    emitAsset({ id: 'ast_2' } as Asset)
    expect(seen).toEqual(['ast_1'])
  })
})
