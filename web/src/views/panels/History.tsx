import { ChevronRight } from 'lucide-react'
import { useEffect, useState } from 'react'
import { historyApi } from '../../lib/api-history'
import type { ActivityEntry } from '../../lib/types-history'
import { ActivityRow } from '../Activity'
import type { AssetPanelProps } from '.'

/** Asset drawer panel: the last changes of this asset, collapsed by default. */
export function HistoryPanel({ asset }: AssetPanelProps) {
  const [open, setOpen] = useState(false)
  const [items, setItems] = useState<ActivityEntry[] | null>(null)
  const [failed, setFailed] = useState(false)

  // Loaded when opened, and again when the asset changed while it is open.
  useEffect(() => {
    if (!open) return
    let live = true
    setFailed(false)
    historyApi
      .activity({ asset: asset.id, limit: 10 })
      .then((f) => live && setItems(f.items))
      .catch(() => live && setFailed(true))
    return () => {
      live = false
    }
  }, [open, asset.id, asset.updated_at])

  return (
    <section className="stack">
      <button type="button" className="disclosure" aria-expanded={open} onClick={() => setOpen((o) => !o)}>
        <ChevronRight aria-hidden />
        <span className="eyebrow">History</span>
      </button>
      {open &&
        (failed ? (
          <p className="muted">History could not be loaded.</p>
        ) : items === null ? (
          <p className="muted">Loading…</p>
        ) : items.length === 0 ? (
          <p className="muted">No recorded changes.</p>
        ) : (
          <ul className="activity-list activity-compact">
            {items.map((e) => (
              <ActivityRow key={e.seq} e={e} compact />
            ))}
          </ul>
        ))}
    </section>
  )
}
