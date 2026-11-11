import { Activity as ActivityIcon, Bot, User } from 'lucide-react'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { historyApi } from '../lib/api-history'
import { thumbUrl } from '../lib/api'
import { timeAgo } from '../lib/format'
import { useLocation } from '../lib/router'
import { toastError } from '../lib/store'
import type { ActivityEntry } from '../lib/types-history'
import { Button, Chip, Empty, Spinner, typeIcon } from '../ui'
import { openAsset } from './filters'

const KINDS = [
  { id: 'all', label: 'Everything', action: '' },
  { id: 'assets', label: 'Assets', action: 'asset.' },
  { id: 'requests', label: 'Requests', action: 'request.' },
  { id: 'reviews', label: 'Reviews', action: 'asset.review' },
  { id: 'notes', label: 'Notes', action: 'note.' },
  { id: 'projects', label: 'Projects', action: 'project.' },
] as const
type Kind = (typeof KINDS)[number]['id']

const VIAS = [
  { id: 'all', label: 'People and agents', via: '' },
  { id: 'people', label: 'People', via: 'web' },
  { id: 'agents', label: 'Agents', via: 'mcp,api' },
] as const
type Via = (typeof VIAS)[number]['id']

const PAGE = 100
const MAX = 500
const REFRESH_MS = 30_000

const dayKey = (iso: string) => {
  const d = new Date(iso)
  return `${d.getFullYear()}-${d.getMonth()}-${d.getDate()}`
}

function dayLabel(iso: string): string {
  const d = new Date(iso)
  const today = new Date()
  const yesterday = new Date()
  yesterday.setDate(today.getDate() - 1)
  if (dayKey(iso) === dayKey(today.toISOString())) return 'Today'
  if (dayKey(iso) === dayKey(yesterday.toISOString())) return 'Yesterday'
  return d.toLocaleDateString(undefined, { weekday: 'long', year: 'numeric', month: 'long', day: 'numeric' })
}

/** Activity feed: who changed what, people and agents. */
export function ActivityPage() {
  const loc = useLocation()
  const [kind, setKind] = useState<Kind>('all')
  const [via, setVia] = useState<Via>('all')
  const [limit, setLimit] = useState(PAGE)
  const [entries, setEntries] = useState<ActivityEntry[]>([])
  const [loading, setLoading] = useState(true)
  const latest = useRef(0)

  const action = KINDS.find((k) => k.id === kind)!.action
  const viaFilter = VIAS.find((v) => v.id === via)!.via

  // Full load whenever filters or the page size change.
  useEffect(() => {
    const ctl = new AbortController()
    setLoading(true)
    historyApi
      .activity({ action, via: viaFilter, limit }, ctl.signal)
      .then((f) => {
        latest.current = f.latest_seq
        setEntries(f.items)
        setLoading(false)
      })
      .catch((e) => {
        if (ctl.signal.aborted) return
        setLoading(false)
        toastError(e)
      })
    return () => ctl.abort()
  }, [action, viaFilter, limit])

  // Poll for new entries with after_seq; skipped while the tab is hidden.
  const poll = useCallback(async () => {
    if (document.hidden || latest.current === 0) return
    try {
      const f = await historyApi.activity({ action, via: viaFilter, after: latest.current, limit: MAX })
      latest.current = Math.max(latest.current, f.latest_seq)
      if (f.items.length) setEntries((cur) => [...[...f.items].reverse(), ...cur])
    } catch {
      /* the next tick tries again */
    }
  }, [action, viaFilter])
  useEffect(() => {
    const t = setInterval(poll, REFRESH_MS)
    const onVisible = () => !document.hidden && poll()
    document.addEventListener('visibilitychange', onVisible)
    return () => {
      clearInterval(t)
      document.removeEventListener('visibilitychange', onVisible)
    }
  }, [poll])

  const days = useMemo(() => {
    const out: { key: string; label: string; items: ActivityEntry[] }[] = []
    for (const e of entries) {
      const k = dayKey(e.at)
      const last = out[out.length - 1]
      if (last && last.key === k) last.items.push(e)
      else out.push({ key: k, label: dayLabel(e.at), items: [e] })
    }
    return out
  }, [entries])

  return (
    <div className="page page-narrow">
      <div className="page-head">
        <div>
          <h1>Activity</h1>
          <p className="muted">What people and agents changed. Refreshes every 30 seconds; entries are kept for a year.</p>
        </div>
      </div>

      <div className="stack" style={{ marginBottom: 'var(--sp-4)' }}>
        <div className="row-wrap" role="group" aria-label="Kind">
          {KINDS.map((k) => (
            <Chip key={k.id} active={kind === k.id} onClick={() => { setKind(k.id); setLimit(PAGE) }}>
              {k.label}
            </Chip>
          ))}
        </div>
        <div className="row-wrap" role="group" aria-label="Made by">
          {VIAS.map((v) => (
            <Chip key={v.id} active={via === v.id} onClick={() => { setVia(v.id); setLimit(PAGE) }}>
              {v.label}
            </Chip>
          ))}
        </div>
      </div>

      {loading && entries.length === 0 ? (
        <Spinner label="Loading activity…" />
      ) : entries.length === 0 ? (
        <Empty icon={ActivityIcon} title="Nothing here yet">
          Changes to assets, requests, reviews and notes appear here as they happen.
        </Empty>
      ) : (
        <div className="stack" style={{ gap: 'var(--sp-5)' }}>
          {days.map((d) => (
            <section key={d.key} className="stack">
              <h2 className="eyebrow">{d.label}</h2>
              <ul className="activity-list">
                {d.items.map((e) => (
                  <ActivityRow key={e.seq} e={e} onOpen={(id) => openAsset(loc, id)} />
                ))}
              </ul>
            </section>
          ))}
          {entries.length >= limit && limit < MAX && (
            <div>
              <Button busy={loading} onClick={() => setLimit((l) => Math.min(MAX, l + PAGE))}>
                Show older
              </Button>
            </div>
          )}
        </div>
      )}
    </div>
  )
}

export function ActivityRow({ e, onOpen, compact }: { e: ActivityEntry; onOpen?: (assetId: string) => void; compact?: boolean }) {
  const Icon = e.via === 'mcp' || e.via === 'api' ? Bot : User
  const clickable = !!e.asset_id && !e.asset_gone && !!onOpen
  const Fallback = typeIcon('other')
  return (
    <li className="activity-row">
      {!compact && (
        <span className="activity-thumb checker" aria-hidden>
          {e.asset_id && !e.asset_gone && e.thumb_version ? <img src={thumbUrl({ id: e.asset_id, thumb_version: e.thumb_version })} alt="" loading="lazy" /> : <Fallback />}
        </span>
      )}
      <div className="activity-body">
        <p className="activity-line">
          <Icon className="activity-via" aria-label={e.via === 'mcp' || e.via === 'api' ? 'Agent' : 'Person'} />
          <strong>{e.actor || 'hoard'}</strong> <span className="muted">{e.action}</span>
          {e.asset_id && !compact && (
            <>
              {' '}
              {clickable ? (
                <button type="button" className="linklike" onClick={() => onOpen!(e.asset_id!)}>
                  {e.asset_name || e.asset_id}
                </button>
              ) : (
                <span className="faint">{e.asset_gone ? `${e.asset_id} (deleted)` : e.asset_id}</span>
              )}
            </>
          )}
        </p>
        {e.summary && <p className="activity-summary">{e.summary}</p>}
      </div>
      <time className="faint activity-time" dateTime={e.at} title={new Date(e.at).toLocaleString()}>
        {timeAgo(e.at)}
      </time>
    </li>
  )
}
