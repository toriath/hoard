import { useLocation } from '../../lib/router'
import { rawUrl, thumbUrl } from '../../lib/api'
import { libraryApi } from '../../lib/api-library'
import { formatBytes } from '../../lib/format'
import { useQuery } from '../../lib/store'
import type { Asset } from '../../lib/types'
import { typeColor, typeIcon } from '../../ui'
import { openAsset } from '../filters'
import type { AssetPanelProps } from '.'

/** Small preview of an asset: its thumbnail, else the picture itself, else the type icon. */
export function MiniThumb({ asset }: { asset: Asset }) {
  const primary = asset.files.find((f) => f.id === asset.primary_file_id) ?? asset.files[0]
  const Icon = typeIcon(asset.type)
  const pixel = !!primary?.width && Math.max(primary.width, primary.height ?? 0) <= 128
  return (
    <span className="mini-thumb checker">
      {asset.thumb_version > 0 ? (
        <img src={thumbUrl(asset)} alt="" loading="lazy" decoding="async" className={pixel ? 'pixel' : undefined} draggable={false} />
      ) : primary?.kind === 'image' ? (
        <img src={rawUrl(asset.id, primary.filename)} alt="" loading="lazy" decoding="async" draggable={false} />
      ) : (
        <Icon aria-hidden style={{ color: typeColor(asset.type) }} />
      )}
    </span>
  )
}

/** Asset drawer panel: duplicates and look-alikes of this asset (library area). */
export function SimilarPanel({ asset }: AssetPanelProps) {
  const loc = useLocation()
  // Re-ask when the files change: a new upload can make the asset a duplicate.
  const q = useQuery(() => libraryApi.similar(asset.id), [asset.id, asset.updated_at])
  const hits = q.data ?? []
  if (hits.length === 0) return null
  const identical = hits.filter((h) => h.reasons.includes('identical file')).length
  return (
    <section className="stack">
      <p className="eyebrow">
        Similar assets
        {identical > 0 && <span className="similar-flag"> · {identical === 1 ? '1 duplicate' : `${identical} duplicates`}</span>}
      </p>
      <ul className="similar-list">
        {hits.map((h) => (
          <li key={h.asset.id}>
            <button type="button" className="similar-item" onClick={() => openAsset(loc, h.asset.id)} aria-label={`Open ${h.asset.name}`}>
              <MiniThumb asset={h.asset} />
              <span className="similar-text">
                <span className="truncate similar-name">{h.asset.name}</span>
                <span className="faint similar-reasons">{h.reasons.join(' · ')}</span>
              </span>
              <span className="faint mono similar-size">{formatBytes(h.asset.total_size)}</span>
            </button>
          </li>
        ))}
      </ul>
    </section>
  )
}
