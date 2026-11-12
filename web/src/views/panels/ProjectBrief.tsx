import { FileText, HeartPulse, Package, Pencil, Plus, ScrollText, X } from 'lucide-react'
import { useState } from 'react'
import { briefApi, bundleUrl, creditsMdUrl } from '../../lib/api-brief'
import { formatBytes, plural } from '../../lib/format'
import { link } from '../../lib/router'
import { changed, getMeta, toast, toastError, typeLabel, useQuery, useVersion } from '../../lib/store'
import type { Project } from '../../lib/types'
import { ENGINES, NAMINGS, type ProjectBrief, type ReportAsset } from '../../lib/types-brief'
import { Button, Check, Chip, CopyButton, Dialog, Field, Spinner } from '../../ui'

const isEmpty = (b: ProjectBrief) => Object.keys(b).length === 0

/** Project page panel: the project brief, the project's health and its credits. */
export function BriefPanel({ project }: { project: Project }) {
  const [rev, setRev] = useState(0)
  const [editing, setEditing] = useState(false)
  const [credits, setCredits] = useState(false)
  const assetsVersion = useVersion('assets')
  const groupsVersion = useVersion('groups')
  const brief = useQuery(() => briefApi.brief(project.id), [project.id, rev])
  const b = brief.data
  if (!b) return brief.loading ? <Spinner /> : null

  return (
    <section className="card brief-panel">
      <div className="card-head">
        <h2 className="grow">Brief</h2>
        <Button size="sm" icon={ScrollText} onClick={() => setCredits(true)}>
          Credits
        </Button>
        <Button size="sm" icon={Pencil} onClick={() => setEditing(true)}>
          {isEmpty(b) ? 'Set up' : 'Edit'}
        </Button>
      </div>
      <div className="card-pad stack">
        {isEmpty(b) ? (
          <p className="muted">
            No brief yet. A brief tells agents and the fit check what this project needs (engine, art style, palette, sizes, file formats, audio targets, allowed licenses), so that
            assets are made and picked to fit.
          </p>
        ) : (
          <BriefSummary brief={b} />
        )}
        <Health projectId={project.id} refresh={`${rev}:${assetsVersion}:${groupsVersion}`} />
      </div>
      {editing && (
        <BriefDialog
          project={project}
          brief={b}
          onClose={() => setEditing(false)}
          onSaved={() => {
            setEditing(false)
            setRev((r) => r + 1)
            changed('groups')
          }}
        />
      )}
      {credits && <CreditsDialog project={project} onClose={() => setCredits(false)} />}
    </section>
  )
}

function Swatches({ colors }: { colors: string[] }) {
  return (
    <span className="swatches">
      {colors.map((c) => (
        <span key={c} className="swatch" style={{ background: c }} title={c} role="img" aria-label={c} />
      ))}
    </span>
  )
}

function BriefSummary({ brief: b }: { brief: ProjectBrief }) {
  const rows: [string, React.ReactNode][] = []
  if (b.engine) rows.push(['Engine', [b.engine, b.engine_version].filter(Boolean).join(' ')])
  if (b.art_direction) rows.push(['Art direction', <span className="brief-text">{b.art_direction}</span>])
  const flags = [b.pixel_art && 'pixel art', b.power_of_two && 'power-of-two textures'].filter(Boolean)
  const sizes = [
    b.tile_size && `tile ${b.tile_size} px`,
    b.base_resolution && `base ${b.base_resolution}`,
    b.max_texture_size && `max texture ${b.max_texture_size} px`,
    ...flags,
  ].filter(Boolean)
  if (sizes.length) rows.push(['Graphics', sizes.join(' · ')])
  if (b.palette?.length) rows.push(['Palette', <Swatches colors={b.palette} />])
  const formats = Object.entries(b.formats ?? {}).filter(([, v]) => v.length)
  if (formats.length) rows.push(['Formats', formats.map(([t, v]) => `${typeLabel(t)}: ${v.join(', ')}`).join(' · ')])
  const a = b.audio
  if (a) {
    const parts = [
      a.sample_rate && `${a.sample_rate} Hz`,
      a.channels && (a.channels === 1 ? 'mono' : a.channels === 2 ? 'stereo' : `${a.channels} ch`),
      a.loudness_lufs != null && `${a.loudness_lufs} LUFS`,
      a.max_peak_dbfs != null && `peak ≤ ${a.max_peak_dbfs} dBFS`,
    ].filter(Boolean)
    if (parts.length) rows.push(['Audio', parts.join(' · ')])
  }
  if (b.licenses?.length) rows.push(['Licenses', b.licenses.join(', ')])
  const folders = Object.entries(b.folders ?? {})
  if (folders.length) rows.push(['Folders', folders.map(([t, f]) => `${t === 'default' ? 'default' : typeLabel(t)}: ${f}`).join(' · ')])
  if (b.naming && b.naming !== 'keep') rows.push(['Naming', b.naming])
  if (b.size_budget_mb) rows.push(['Size budget', `${b.size_budget_mb} MB`])
  if (b.notes) rows.push(['Notes', <span className="brief-text">{b.notes}</span>])
  return (
    <dl className="props brief-props">
      {rows.map(([k, v]) => (
        <div key={k} className="brief-row">
          <dt>{k}</dt>
          <dd>{v}</dd>
        </div>
      ))}
    </dl>
  )
}

// ---------- health ----------

function AssetList({ title, items }: { title: string; items: ReportAsset[] }) {
  if (items.length === 0) return null
  return (
    <details className="fit">
      <summary>
        <span className="grow">{title}</span>
        <span className="muted">{items.length}</span>
      </summary>
      <ul className="fit-list">
        {items.map((a) => (
          <li key={a.id}>
            <span>
              <a {...link(`/assets/${a.id}`)}>{a.name || a.id}</a>
              {a.detail && <span className="muted"> · {a.detail}</span>}
            </span>
          </li>
        ))}
      </ul>
    </details>
  )
}

function Health({ projectId, refresh }: { projectId: string; refresh: string }) {
  const q = useQuery(() => briefApi.report(projectId), [projectId, refresh])
  const r = q.data
  if (!r) return q.loading ? <Spinner label="Checking health" /> : null
  const cls = r.verdict === 'ok' ? 'note-ok' : r.verdict === 'attention' ? 'note-warn' : 'note-danger'
  return (
    <div className="stack">
      <div className="row">
        <HeartPulse aria-hidden width={16} height={16} />
        <p className="eyebrow grow">Health</p>
      </div>
      <div className={`note ${cls}`}>{r.summary}</div>
      <AssetList title="License problems" items={r.license_problems} />
      <AssetList title="Missing attribution" items={r.missing_attribution} />
      <AssetList title="Placeholders still in use" items={r.placeholders} />
      <AssetList title="In rework, rejected or superseded" items={r.needs_work} />
      {r.fit_issues.length > 0 && (
        <details className="fit">
          <summary>
            <span className="grow">Do not fit the brief</span>
            <span className="muted">{r.fit_issues.length}</span>
          </summary>
          <ul className="fit-list">
            {r.fit_issues.map((f) => (
              <li key={f.asset.id}>
                <span>
                  <a {...link(`/assets/${f.asset.id}`)}>{f.asset.name || f.asset.id}</a>
                  <span className="muted">
                    {' '}
                    · {[f.errors && plural(f.errors, 'error'), f.warnings && plural(f.warnings, 'warning')].filter(Boolean).join(', ')}
                  </span>
                  {f.messages.map((m, i) => (
                    <span key={i} className="fit-msg">
                      {m}
                    </span>
                  ))}
                </span>
              </li>
            ))}
          </ul>
        </details>
      )}
      {r.repo_drift && <AssetList title={`Not in the repository (snapshot ${r.repo_drift.snapshot_at.slice(0, 10)})`} items={r.repo_drift.missing_assets} />}
      <div className="stack-tight">
        <div className="row-wrap muted">
          <span>
            {plural(r.asset_count, 'asset')} · {formatBytes(r.total_size)}
            {r.size_budget_bytes ? ` of ${formatBytes(r.size_budget_bytes)} budget` : ''}
          </span>
          {r.requests.total > 0 && (
            <span>
              · {plural(r.requests.total, 'open request')}
              {r.requests.submitted > 0 && ` (${r.requests.submitted} to review)`}
            </span>
          )}
        </div>
        {r.size_budget_bytes ? (
          <div className="progress" role="img" aria-label={`${Math.round((r.total_size / r.size_budget_bytes) * 100)}% of the size budget`}>
            <i className={r.over_budget ? 'over' : undefined} style={{ width: `${Math.min(100, (r.total_size / r.size_budget_bytes) * 100)}%` }} />
          </div>
        ) : null}
      </div>
    </div>
  )
}

// ---------- credits ----------

function CreditsDialog({ project, onClose }: { project: Project; onClose: () => void }) {
  const q = useQuery(() => briefApi.credits(project.id), [project.id])
  const c = q.data
  return (
    <Dialog
      title={`Credits: ${project.name}`}
      width={680}
      onClose={onClose}
      footer={
        <>
          <a className="btn" href={creditsMdUrl(project.id)} download="CREDITS.md">
            <FileText aria-hidden /> Download CREDITS.md
          </a>
          <a className="btn" href={bundleUrl(project.id)} download>
            <Package aria-hidden /> Download bundle
          </a>
          <Button variant="primary" onClick={onClose}>
            Close
          </Button>
        </>
      }
    >
      {!c ? (
        q.error ? <div className="note note-danger">{q.error.message}</div> : <Spinner />
      ) : (
        <div className="stack">
          {c.needs_attention.length > 0 && (
            <div className="note note-warn">
              <div>
                <strong>Needs attention before you ship:</strong>
                <ul className="fit-list">
                  {c.needs_attention.map((i) => (
                    <li key={i.asset_id}>
                      <a {...link(`/assets/${i.asset_id}`)}>{i.name}</a>
                      <span className="muted"> · {i.problem}</span>
                    </li>
                  ))}
                </ul>
              </div>
            </div>
          )}
          <div className="row">
            <p className="muted grow">{plural(c.asset_count, 'asset')} credited. The bundle holds every asset of the project plus this file.</p>
            <CopyButton text={c.markdown} label="Copy Markdown" />
          </div>
          <pre className="codeblock credits-md">{c.markdown}</pre>
        </div>
      )}
    </Dialog>
  )
}

// ---------- editing ----------

const list = (s: string) => s.split(/[\s,]+/).map((x) => x.trim().replace(/^\./, '').toLowerCase()).filter(Boolean)

function BriefDialog({ project, brief, onClose, onSaved }: { project: Project; brief: ProjectBrief; onClose: () => void; onSaved: () => void }) {
  const meta = getMeta()
  const [engine, setEngine] = useState(brief.engine ?? '')
  const [engineVersion, setEngineVersion] = useState(brief.engine_version ?? '')
  const [art, setArt] = useState(brief.art_direction ?? '')
  const [pixel, setPixel] = useState(!!brief.pixel_art)
  const [palette, setPalette] = useState<string[]>(brief.palette ?? [])
  const [tile, setTile] = useState(brief.tile_size?.toString() ?? '')
  const [res, setRes] = useState(brief.base_resolution ?? '')
  const [maxTex, setMaxTex] = useState(brief.max_texture_size?.toString() ?? '')
  const [pot, setPot] = useState(!!brief.power_of_two)
  const [formats, setFormats] = useState<Record<string, string>>(Object.fromEntries(Object.entries(brief.formats ?? {}).map(([k, v]) => [k, v.join(', ')])))
  const [folders, setFolders] = useState<Record<string, string>>(brief.folders ?? {})
  const [rate, setRate] = useState(brief.audio?.sample_rate?.toString() ?? '')
  const [channels, setChannels] = useState(brief.audio?.channels?.toString() ?? '')
  const [lufs, setLufs] = useState(brief.audio?.loudness_lufs?.toString() ?? '')
  const [peak, setPeak] = useState(brief.audio?.max_peak_dbfs?.toString() ?? '')
  const [licenses, setLicenses] = useState<string[]>(brief.licenses ?? [])
  const [customLicense, setCustomLicense] = useState('')
  const [naming, setNaming] = useState(brief.naming ?? '')
  const [budget, setBudget] = useState(brief.size_budget_mb?.toString() ?? '')
  const [notes, setNotes] = useState(brief.notes ?? '')
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  const licenseChoices = [...new Set([...meta.licenses, ...licenses])]
  const toggleLicense = (l: string) => setLicenses((cur) => (cur.includes(l) ? cur.filter((x) => x !== l) : [...cur, l]))

  const save = async () => {
    // An empty field clears the value; text that is not a number is an error and nothing is sent.
    const bad: string[] = []
    const numOrNull = (label: string, s: string) => {
      if (s.trim() === '') return null
      const n = Number(s)
      if (!Number.isFinite(n)) bad.push(label)
      return n
    }
    const fmt = Object.fromEntries(Object.entries(formats).map(([k, v]) => [k, list(v)]).filter(([, v]) => v.length))
    const fol = Object.fromEntries(Object.entries(folders).map(([k, v]) => [k, v.trim()]).filter(([, v]) => v))
    const audio = {
      sample_rate: numOrNull('Sample rate', rate), channels: numOrNull('Channels', channels), loudness_lufs: numOrNull('Loudness', lufs), max_peak_dbfs: numOrNull('Max peak', peak),
    }
    const audioSet = Object.fromEntries(Object.entries(audio).filter(([, v]) => v !== null))
    // Every field is sent: an empty one is null, which clears it on the server.
    const patch = {
      engine: engine || null,
      engine_version: engineVersion.trim() || null,
      art_direction: art.trim() || null,
      pixel_art: pixel || null,
      palette: palette.length ? palette : null,
      tile_size: numOrNull('Tile size', tile),
      base_resolution: res.trim() || null,
      max_texture_size: numOrNull('Max texture', maxTex),
      power_of_two: pot || null,
      formats: Object.keys(fmt).length ? fmt : null,
      audio: Object.keys(audioSet).length ? audioSet : null,
      licenses: licenses.length ? licenses : null,
      folders: Object.keys(fol).length ? fol : null,
      naming: naming || null,
      size_budget_mb: numOrNull('Size budget', budget),
      notes: notes.trim() || null,
    }
    if (bad.length) {
      setError(`Not a number: ${bad.join(', ')}.`)
      return
    }
    setSaving(true)
    setError('')
    try {
      await briefApi.saveBrief(project.id, patch)
      toast('Brief saved')
      onSaved()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
      toastError(e)
    } finally {
      setSaving(false)
    }
  }

  return (
    <Dialog
      title={`Brief: ${project.name}`}
      width={680}
      onClose={onClose}
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" busy={saving} onClick={save}>
            Save
          </Button>
        </>
      }
    >
      <div className="stack">
        {error && <div className="note note-danger">{error}</div>}
        <p className="eyebrow">Engine and style</p>
        <div className="field-row">
          <Field label="Engine">
            <select className="select" value={engine} onChange={(e) => setEngine(e.target.value)}>
              <option value="">Not set</option>
              {ENGINES.map((x) => (
                <option key={x} value={x}>
                  {x}
                </option>
              ))}
            </select>
          </Field>
          <Field label="Engine version">
            <input className="input" value={engineVersion} onChange={(e) => setEngineVersion(e.target.value)} placeholder="e.g. 4.4" />
          </Field>
        </div>
        <Field label="Art direction" hint="Style, mood, references. Agents read this before they generate.">
          <textarea className="textarea" rows={3} value={art} onChange={(e) => setArt(e.target.value)} />
        </Field>

        <p className="eyebrow">Graphics</p>
        <div className="row-wrap">
          <Check checked={pixel} onChange={setPixel}>
            Pixel art
          </Check>
          <Check checked={pot} onChange={setPot}>
            Power-of-two textures
          </Check>
        </div>
        <div className="brief-grid">
          <Field label="Tile size (px)">
            <input className="input" inputMode="numeric" value={tile} onChange={(e) => setTile(e.target.value)} placeholder="16" />
          </Field>
          <Field label="Base resolution">
            <input className="input" value={res} onChange={(e) => setRes(e.target.value)} placeholder="320x180" />
          </Field>
          <Field label="Max texture (px)">
            <input className="input" inputMode="numeric" value={maxTex} onChange={(e) => setMaxTex(e.target.value)} placeholder="2048" />
          </Field>
        </div>
        <Field label="Palette" hint="Opaque pixels more than a few units away from every colour are reported by the fit check.">
          <div className="stack-tight">
            <div className="palette-edit">
              {palette.map((c, i) => (
                <span key={i} className="palette-item">
                  <input
                    type="color"
                    className="swatch-input"
                    aria-label={`Palette colour ${i + 1}`}
                    value={/^#[0-9a-f]{6}$/i.test(c) ? c : '#000000'}
                    onChange={(e) => setPalette((p) => p.map((x, j) => (j === i ? e.target.value : x)))}
                  />
                  <input
                    className="input mono"
                    aria-label={`Palette colour ${i + 1} hex`}
                    value={c}
                    maxLength={7}
                    onChange={(e) => setPalette((p) => p.map((x, j) => (j === i ? e.target.value : x)))}
                  />
                  <Button size="sm" variant="ghost" iconOnly icon={X} aria-label={`Remove colour ${c}`} onClick={() => setPalette((p) => p.filter((_, j) => j !== i))} />
                </span>
              ))}
            </div>
            <div>
              <Button size="sm" icon={Plus} disabled={palette.length >= 256} onClick={() => setPalette((p) => [...p, '#000000'])}>
                Add colour
              </Button>
            </div>
          </div>
        </Field>

        <p className="eyebrow">Accepted formats per type</p>
        <div className="brief-grid brief-grid-2">
          {meta.types.map((t) => (
            <Field key={t.id} label={t.label}>
              <input
                className="input mono"
                value={formats[t.id] ?? ''}
                onChange={(e) => setFormats((f) => ({ ...f, [t.id]: e.target.value }))}
                placeholder={t.id === 'sprite' ? 'png' : t.id === 'sfx' ? 'wav, ogg' : 'any'}
              />
            </Field>
          ))}
        </div>

        <p className="eyebrow">Audio</p>
        <div className="brief-grid">
          <Field label="Sample rate (Hz)">
            <input className="input" inputMode="numeric" value={rate} onChange={(e) => setRate(e.target.value)} placeholder="48000" />
          </Field>
          <Field label="Channels">
            <input className="input" inputMode="numeric" value={channels} onChange={(e) => setChannels(e.target.value)} placeholder="2" />
          </Field>
          <Field label="Loudness (LUFS)">
            <input className="input" inputMode="decimal" value={lufs} onChange={(e) => setLufs(e.target.value)} placeholder="-16" />
          </Field>
          <Field label="Max peak (dBFS)">
            <input className="input" inputMode="decimal" value={peak} onChange={(e) => setPeak(e.target.value)} placeholder="-1" />
          </Field>
        </div>

        <p className="eyebrow">Licenses</p>
        <Field label="Allowed licenses" hint="None selected means any license is fine.">
          <div className="stack-tight">
            <div className="row-wrap">
              {licenseChoices.map((l) => (
                <Chip key={l} active={licenses.includes(l)} onClick={() => toggleLicense(l)}>
                  {l}
                </Chip>
              ))}
            </div>
            <div className="row">
              <input
                className="input"
                aria-label="Add another license"
                placeholder="Another license…"
                value={customLicense}
                onChange={(e) => setCustomLicense(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' && customLicense.trim()) {
                    e.preventDefault()
                    setLicenses((cur) => (cur.includes(customLicense.trim()) ? cur : [...cur, customLicense.trim()]))
                    setCustomLicense('')
                  }
                }}
              />
            </div>
          </div>
        </Field>

        <p className="eyebrow">Repository</p>
        <div className="brief-grid brief-grid-2">
          <Field label="Default folder">
            <input className="input mono" value={folders.default ?? ''} onChange={(e) => setFolders((f) => ({ ...f, default: e.target.value }))} placeholder="assets" />
          </Field>
          <Field label="File naming">
            <select className="select" value={naming} onChange={(e) => setNaming(e.target.value)}>
              <option value="">Not set</option>
              {NAMINGS.map((x) => (
                <option key={x} value={x}>
                  {x}
                </option>
              ))}
            </select>
          </Field>
          {meta.types.map((t) => (
            <Field key={t.id} label={`${t.label} folder`}>
              <input className="input mono" value={folders[t.id] ?? ''} onChange={(e) => setFolders((f) => ({ ...f, [t.id]: e.target.value }))} placeholder="relative path" />
            </Field>
          ))}
        </div>

        <div className="field-row">
          <Field label="Size budget (MB)">
            <input className="input" inputMode="decimal" value={budget} onChange={(e) => setBudget(e.target.value)} placeholder="200" />
          </Field>
        </div>
        <Field label="Notes for agents">
          <textarea className="textarea" rows={3} value={notes} onChange={(e) => setNotes(e.target.value)} />
        </Field>
      </div>
    </Dialog>
  )
}
