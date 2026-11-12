import { useEffect, useState } from 'react'
import { thumbUrl } from '../../lib/api'
import { historyApi } from '../../lib/api-history'
import { useLocation } from '../../lib/router'
import type { Lineage, LineageNode } from '../../lib/types-history'
import { CopyButton, typeIcon } from '../../ui'
import { openAsset } from '../filters'
import type { AssetPanelProps } from '.'

const KNOWN = ['tool', 'model', 'seed', 'steps', 'cfg', 'sampler', 'width', 'height', 'negative_prompt']

const show = (v: unknown): string => (typeof v === 'string' ? v : typeof v === 'number' || typeof v === 'boolean' ? String(v) : JSON.stringify(v))
const filled = (v: unknown) => v !== undefined && v !== null && v !== ''

function NodeLinks({ title, nodes }: { title: string; nodes: LineageNode[] }) {
  const loc = useLocation()
  if (nodes.length === 0) return null
  return (
    <div className="stack" style={{ gap: 'var(--sp-1)' }}>
      <p className="faint" style={{ fontSize: 'var(--fs-sm)' }}>
        {title}
      </p>
      <ul className="lineage-nodes">
        {nodes.map((n) => {
          const Icon = typeIcon(n.type)
          return (
            <li key={n.id}>
              <button type="button" className="lineage-node" onClick={() => openAsset(loc, n.id)} title={n.name || n.id}>
                <span className="lineage-thumb checker">{n.thumb_version ? <img src={thumbUrl(n)} alt="" loading="lazy" /> : <Icon aria-hidden />}</span>
                <span className="truncate">{n.name || n.id}</span>
              </button>
            </li>
          )
        })}
      </ul>
    </div>
  )
}

/** Asset drawer panel: generation recipe and the asset's relatives (parent, variations, replacements). */
export function LineagePanel({ asset }: AssetPanelProps) {
  const [l, setLineage] = useState<Lineage | null>(null)
  useEffect(() => {
    let live = true
    historyApi
      .lineage(asset.id)
      .then((r) => live && setLineage(r))
      .catch(() => live && setLineage(null))
    return () => {
      live = false
    }
  }, [asset.id, asset.updated_at])

  const gen = asset.generator ?? {}
  const hasRecipe = Object.keys(gen).length > 0
  const hasLineage = !!l && l.ancestors.length + l.children.length + l.siblings.length + l.supersedes.length + l.superseded_by_chain.length > 0
  if (!hasRecipe && !hasLineage) return null

  const rows: [string, string][] = []
  const add = (label: string, v: unknown) => filled(v) && rows.push([label, show(v)])
  add('Tool', gen.tool)
  add('Model', gen.model)
  add('Sampler', gen.sampler)
  add('Steps', gen.steps)
  add('CFG', gen.cfg)
  if (filled(gen.width) && filled(gen.height)) add('Size', `${show(gen.width)}×${show(gen.height)}`)
  else {
    add('Width', gen.width)
    add('Height', gen.height)
  }
  for (const [k, v] of Object.entries(gen)) if (!KNOWN.includes(k) && filled(v)) rows.push([k, show(v)])

  return (
    <section className="stack">
      <p className="eyebrow">Recipe and lineage</p>
      {hasRecipe && (
        <dl className="props">
          {rows.map(([k, v]) => (
            <Row key={k} k={k} v={v} />
          ))}
          {filled(gen.seed) && (
            <>
              <dt>Seed</dt>
              <dd className="row">
                <span className="mono">{show(gen.seed)}</span>
                <CopyButton text={show(gen.seed)} label="Copy seed" />
              </dd>
            </>
          )}
          {typeof gen.negative_prompt === 'string' && gen.negative_prompt && (
            <>
              <dt>Negative prompt</dt>
              <dd className="prose">{gen.negative_prompt}</dd>
            </>
          )}
        </dl>
      )}
      {hasRecipe && asset.prompt && (
        <div>
          <CopyButton text={asset.prompt} label="Copy prompt" />
        </div>
      )}
      {l && (
        <>
          <NodeLinks title="Made from" nodes={l.ancestors} />
          <NodeLinks title="Variations of this asset" nodes={l.children} />
          <NodeLinks title="Other variations of the same parent" nodes={l.siblings} />
          <NodeLinks title="Replaced by" nodes={l.superseded_by_chain} />
          <NodeLinks title="Replaces" nodes={l.supersedes} />
        </>
      )}
    </section>
  )
}

function Row({ k, v }: { k: string; v: string }) {
  return (
    <>
      <dt>{k}</dt>
      <dd className={v.length > 80 ? 'prose' : undefined}>{v}</dd>
    </>
  )
}
