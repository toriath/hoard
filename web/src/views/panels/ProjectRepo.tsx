import { ChevronDown, ChevronRight, ClipboardCopy, Download, RefreshCw } from 'lucide-react'
import { useState, type ReactNode } from 'react'
import { deliveryApi } from '../../lib/api-delivery'
import { copyText, formatBytes, plural, timeAgo } from '../../lib/format'
import { link } from '../../lib/router'
import { toast, toastError, useQuery } from '../../lib/store'
import type { Project } from '../../lib/types'
import type { RepoAudit } from '../../lib/types-delivery'
import { Button, Spinner, cx } from '../../ui'

type ListKey = 'outdated' | 'untracked' | 'missing' | 'unlinked' | 'attention'

/** Project page panel: repository audit and sync status (delivery area). */
export function RepoPanel({ project }: { project: Project }) {
  const q = useQuery(() => deliveryApi.audit(project.id), [project.id])
  const [open, setOpen] = useState<ListKey | null>(null)
  const [root, setRoot] = useState('.')
  const [busy, setBusy] = useState(false)
  const a = q.data

  async function copyCommand() {
    setBusy(true)
    try {
      const r = await deliveryApi.auditCommand(project.id, root.trim() || '.')
      if (await copyText(r.command)) toast('Audit command copied. Run it in the game repository.')
      else toastError(new Error('Could not copy to the clipboard. Ask your agent to call get_repo_audit_command instead.'))
    } catch (e) {
      toastError(e)
    } finally {
      setBusy(false)
    }
  }

  return (
    <section className="card repo-panel" aria-label="Game repository">
      <div className="card-head">
        <h2 className="repo-title">Game repository</h2>
        <span className="muted">{!a ? '' : a.has_snapshot && a.snapshot_at ? `snapshot ${timeAgo(a.snapshot_at)}` : 'no snapshot yet'}</span>
        <span className="grow" />
        <Button size="sm" variant="ghost" iconOnly icon={RefreshCw} aria-label="Reload audit" onClick={q.reload} />
        <a className="btn btn-sm" href={deliveryApi.lockUrl(project.id)} download title="hoard.lock.json: every file the project should have, with hash and license">
          <Download aria-hidden />
          <span className="hide-sm">Lock file</span>
        </a>
      </div>
      <div className="card-pad repo-body">
        {q.loading && !a && <Spinner />}
        {q.error && !a && <p className="muted">{q.error.message}</p>}
        {a && <Summary a={a} open={open} setOpen={setOpen} />}
        {a && open && <Details a={a} which={open} />}
        <div className="repo-cmd">
          <label className="label" htmlFor={`repo-root-${project.id}`}>
            Repository directory
          </label>
          <div className="repo-cmd-row">
            <input id={`repo-root-${project.id}`} className="input" value={root} onChange={(e) => setRoot(e.target.value)} placeholder="." />
            <Button size="sm" icon={ClipboardCopy} busy={busy} onClick={copyCommand}>
              Copy audit command
            </Button>
          </div>
          <p className="muted repo-hint">
            Run the command in the game repository, or let your agent call <code>get_repo_audit_command</code>. It sends file names and hashes to hoard, never file contents. The command holds a link that is valid for two hours: do not share it.
          </p>
        </div>
      </div>
    </section>
  )
}

function Summary({ a, open, setOpen }: { a: RepoAudit; open: ListKey | null; setOpen: (k: ListKey | null) => void }) {
  if (!a.has_snapshot) return <p className="muted">{a.hint ?? 'No repository snapshot yet.'}</p>
  const c = a.counts
  const stat = (key: ListKey | null, n: number, label: string, tone?: string) => {
    const inner = (
      <>
        <b>{n}</b>
        <span>{label}</span>
      </>
    )
    if (!key || n === 0)
      return (
        <div className={cx('repo-stat', tone)} key={label}>
          {inner}
        </div>
      )
    return (
      <button type="button" key={label} className={cx('repo-stat', tone, open === key && 'repo-stat-open')} aria-expanded={open === key} onClick={() => setOpen(open === key ? null : key)}>
        {inner}
        {open === key ? <ChevronDown aria-hidden /> : <ChevronRight aria-hidden />}
      </button>
    )
  }
  return (
    <div className="repo-stats">
      {stat(null, c.in_sync, 'in sync', 'repo-ok')}
      {stat('outdated', c.outdated, 'outdated', 'repo-warn')}
      {stat('untracked', c.untracked, 'untracked')}
      {stat('missing', c.missing, 'missing', 'repo-warn')}
      {stat('unlinked', c.unlinked, 'not linked')}
      {stat('attention', c.attention, 'need attention', 'repo-warn')}
    </div>
  )
}

function Details({ a, which }: { a: RepoAudit; which: ListKey }) {
  const row = (key: string, main: ReactNode, sub?: ReactNode) => (
    <li key={key} className="repo-row">
      <span className="truncate">{main}</span>
      {sub && <span className="muted truncate">{sub}</span>}
    </li>
  )
  const asset = (id: string, name: string) => <a {...link(`/assets/${encodeURIComponent(id)}`)}>{name || id}</a>
  let rows: ReactNode[] = []
  let intro = ''
  switch (which) {
    case 'outdated':
      intro = 'Files of assets that were replaced by a newer version. Ask your agent to run sync_project.'
      rows = a.known
        .filter((k) => k.flags?.includes('superseded'))
        .map((k) => row(k.path, <code>{k.path}</code>, <>newer: {asset(k.newer_asset_id ?? k.asset_id, k.newer_name ?? '')}</>))
      break
    case 'attention':
      intro = 'Files of assets with status rework, rejected or placeholder.'
      rows = a.known
        .filter((k) => k.flags?.some((f) => f === 'rework' || f === 'rejected' || f === 'placeholder'))
        .map((k) => row(k.path, <code>{k.path}</code>, <>{asset(k.asset_id, k.name)} ({k.status})</>))
      break
    case 'untracked':
      intro = 'Files hoard does not know. Ask your agent to create assets for the ones that belong in the library.'
      rows = a.unknown.map((u) => row(u.path, <code>{u.path}</code>, `${formatBytes(u.size)} · looks like ${u.suggested_type}`))
      break
    case 'missing':
      intro = 'Project assets that are not in the repository yet.'
      rows = a.missing.map((m) => row(m.asset_id, asset(m.asset_id, m.name), `${m.type} · ${plural(m.files.length, 'file')}`))
      break
    case 'unlinked':
      intro = 'Assets found in the repository that are not linked to this project.'
      rows = a.unlinked.map((u) => row(u.asset_id, asset(u.asset_id, u.name), u.paths[0] + (u.paths.length > 1 ? ` +${u.paths.length - 1}` : '')))
      break
  }
  return (
    <div className="repo-details">
      <p className="muted">{intro}</p>
      <ul className="repo-list">{rows}</ul>
      {a.truncated && <p className="muted">Lists are cut at 500 entries; the counts above are complete.</p>}
    </div>
  )
}
