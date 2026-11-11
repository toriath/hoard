import { Check, ClipboardList, Ellipsis, Plus, RotateCcw, Wand2, X } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { api } from '../lib/api'
import { workApi } from '../lib/api-work'
import { timeAgo } from '../lib/format'
import { useLocation } from '../lib/router'
import { changed, getMeta, toast, toastError, typeLabel, useQuery, useVersion } from '../lib/store'
import { canPlaceholder, priorityLabel, type Submission, type WorkRequest } from '../lib/types-work'
import { Button, Dialog, Empty, Menu, Spinner, TypeBadge, useConfirm } from '../ui'
import { RequestBadge, StatusBadge } from '../ui/status'
import { openAsset } from './filters'
import { CandidateThumb, FeedbackDialog, NewRequestDialog } from './work-parts'

const POLL_MS = 20_000
const ACTIVE = ['open', 'claimed', 'submitted'] as const

/** Asset requests board: the work queue for generator agents and the review of their submissions. */
export function RequestsPage() {
  const loc = useLocation()
  const version = useVersion('assets')
  const [project, setProject] = useState('')
  const [type, setType] = useState('')
  const [kind, setKind] = useState('')
  const [creating, setCreating] = useState(false)
  const [detail, setDetail] = useState<string | null>(null)
  const projects = useQuery(() => api.projects(), [useVersion('groups')])

  const active = useQuery(
    () => workApi.requests({ status: [...ACTIVE], project: project || undefined, type: type || undefined, kind: kind || undefined, limit: 200 }),
    [project, type, kind, version],
  )
  const done = useQuery(
    () => workApi.requests({ status: ['done'], project: project || undefined, type: type || undefined, kind: kind || undefined, sort: 'recent', limit: 12 }),
    [project, type, kind, version],
  )
  // Agents work in the background: look again now and then, and when the tab returns.
  const { reload: reloadActive } = active
  const { reload: reloadDone } = done
  useEffect(() => {
    const again = () => {
      if (document.visibilityState === 'visible') {
        reloadActive()
        reloadDone()
      }
    }
    const t = setInterval(again, POLL_MS)
    document.addEventListener('visibilitychange', again)
    return () => {
      clearInterval(t)
      document.removeEventListener('visibilitychange', again)
    }
  }, [reloadActive, reloadDone])

  const reload = () => {
    changed('assets')
    active.reload()
    done.reload()
  }
  const cols = useMemo(() => {
    const by = (s: string) => (active.data ?? []).filter((r) => r.status === s)
    return { open: by('open'), claimed: by('claimed'), submitted: by('submitted') }
  }, [active.data])
  const all = [...(active.data ?? []), ...(done.data ?? [])]
  const detailReq = detail ? all.find((r) => r.id === detail) : undefined
  const filtered = !!(project || type || kind)

  return (
    <div className="page">
      <div className="page-head">
        <div className="grow">
          <h1>Requests</h1>
          <p className="muted">
            What the games still need. Generator agents claim open requests over MCP and submit candidates; you accept or send them back.
          </p>
        </div>
        <Button variant="primary" icon={Plus} onClick={() => setCreating(true)}>
          New request
        </Button>
      </div>

      <div className="row-wrap board-filters">
        <select className="select" aria-label="Project" value={project} onChange={(e) => setProject(e.target.value)}>
          <option value="">All projects</option>
          <option value="none">No project</option>
          {(projects.data ?? []).map((p) => (
            <option key={p.id} value={p.id}>
              {p.name}
            </option>
          ))}
        </select>
        <select className="select" aria-label="Asset type" value={type} onChange={(e) => setType(e.target.value)}>
          <option value="">All types</option>
          {getMeta().types.map((t) => (
            <option key={t.id} value={t.id}>
              {t.label}
            </option>
          ))}
        </select>
        <select className="select" aria-label="Kind" value={kind} onChange={(e) => setKind(e.target.value)}>
          <option value="">New and rework</option>
          <option value="new">New assets</option>
          <option value="rework">Rework</option>
        </select>
        {filtered && (
          <Button
            variant="ghost"
            onClick={() => {
              setProject('')
              setType('')
              setKind('')
            }}
          >
            Clear filters
          </Button>
        )}
      </div>

      {active.error && !active.data ? (
        <div className="note note-danger">{active.error.message}</div>
      ) : !active.data ? (
        <Spinner label="Loading requests" />
      ) : (
        <div className="board">
          <Column title="Open" hint="Waiting for a generator" items={cols.open} empty="Nothing waiting.">
            {(r) => <RequestCard key={r.id} req={r} onDetail={setDetail} onChanged={reload} />}
          </Column>
          <Column title="In progress" hint="A generator holds a lease" items={cols.claimed} empty="No generator is working right now.">
            {(r) => <RequestCard key={r.id} req={r} onDetail={setDetail} onChanged={reload} />}
          </Column>
          <Column title="Review" hint="Candidates wait for you" items={cols.submitted} empty="Nothing to review." review>
            {(r) => <ReviewCard key={r.id} req={r} loc={loc} onDetail={setDetail} onChanged={reload} />}
          </Column>
          <Column title="Done" hint="Recently accepted" items={done.data ?? []} empty="Nothing finished yet.">
            {(r) => <RequestCard key={r.id} req={r} onDetail={setDetail} onChanged={reload} />}
          </Column>
        </div>
      )}

      {active.data && all.length === 0 && !filtered && (
        <div className="card board-empty">
          <Empty icon={ClipboardList} title="No requests yet" action={<Button variant="primary" onClick={() => setCreating(true)}>New request</Button>}>
            Coding agents add requests with <code>request_asset</code> when the game lacks an asset; you can add one by hand. A second agent with an image
            or audio tool works through the queue with the <code>work-request-queue</code> prompt.
          </Empty>
        </div>
      )}

      {creating && (
        <NewRequestDialog
          onClose={() => setCreating(false)}
          onCreated={(r) => {
            toast(r.placeholder_asset_id ? 'Request created with a placeholder' : 'Request created')
            reload()
          }}
        />
      )}
      {detailReq && <RequestDetail req={detailReq} onClose={() => setDetail(null)} onChanged={reload} />}
    </div>
  )
}

function Column({
  title, hint, items, empty, review, children,
}: {
  title: string
  hint: string
  items: WorkRequest[]
  empty: string
  review?: boolean
  children: (r: WorkRequest) => React.ReactNode
}) {
  return (
    <section className="board-col" data-review={review} aria-label={title}>
      <header>
        <h2>{title}</h2>
        <span className="count">{items.length}</span>
        <span className="faint hint truncate">{hint}</span>
      </header>
      <div className="board-list">
        {items.length === 0 ? <p className="faint board-none">{empty}</p> : items.map((r) => children(r))}
      </div>
    </section>
  )
}

// ---------- cards ----------

function RequestMeta({ req }: { req: WorkRequest }) {
  return (
    <div className="row-wrap req-meta">
      {req.kind === 'rework' && (
        <span className="badge status-badge" data-status="rework">
          Rework
        </span>
      )}
      {req.asset_type && <TypeBadge type={req.asset_type} />}
      {req.priority > 0 && (
        <span className="badge status-badge" data-status={req.priority > 1 ? 'request-urgent' : 'request-high'}>
          {priorityLabel(req.priority)}
        </span>
      )}
      {req.priority < 0 && <span className="faint">Low priority</span>}
      {req.project_name && <span className="muted truncate">{req.project_name}</span>}
    </div>
  )
}

function useRequestActions(req: WorkRequest, onChanged: () => void) {
  const confirm = useConfirm()
  const [busy, setBusy] = useState('')
  const run = async (key: string, fn: () => Promise<unknown>, ok: string) => {
    setBusy(key)
    try {
      await fn()
      toast(ok)
      onChanged()
    } catch (e) {
      toastError(e)
    } finally {
      setBusy('')
    }
  }
  return {
    busy,
    run,
    cancel: async () => {
      if (await confirm({ title: 'Cancel request?', message: `“${req.title}” leaves the queue. Candidates already submitted are kept as drafts.`, confirmLabel: 'Cancel request', danger: true }))
        void run('cancel', () => workApi.cancelRequest(req.id), 'Request cancelled')
    },
    release: () => run('release', () => workApi.releaseRequest(req.id), 'Released to the queue'),
    placeholder: () => run('placeholder', () => workApi.makePlaceholder(req.id), 'Placeholder created'),
  }
}

function RequestCard({ req, onDetail, onChanged }: { req: WorkRequest; onDetail: (id: string) => void; onChanged: () => void }) {
  const loc = useLocation()
  const a = useRequestActions(req, onChanged)
  const open = req.status === 'open' || req.status === 'claimed'
  return (
    <article className="req-card" data-status={req.status}>
      <div className="row req-head">
        <button type="button" className="req-title truncate linklike" onClick={() => onDetail(req.id)} title={req.title}>
          {req.title}
        </button>
        {open && (
          <Menu
            trigger={(p) => <Button variant="ghost" size="sm" iconOnly icon={Ellipsis} aria-label={`Actions for ${req.title}`} {...p} />}
            items={[
              ...(req.status === 'claimed' ? [{ label: 'Release claim', icon: RotateCcw, onSelect: a.release }] : []),
              ...(req.kind === 'new' && !req.placeholder_asset_id && canPlaceholder(req.asset_type)
                ? [{ label: 'Make placeholder', icon: Wand2, onSelect: a.placeholder }]
                : []),
              { label: 'Cancel request', icon: X, danger: true, onSelect: a.cancel },
            ]}
          />
        )}
      </div>
      <RequestMeta req={req} />
      {req.description && <p className="req-desc muted">{req.description}</p>}
      {req.status === 'claimed' && (
        <p className="req-claim">
          <span className="faint">By</span> <b className="truncate">{req.claimed_by}</b>
          {req.claim_expires_at && <span className="faint"> · lease ends {timeAgo(req.claim_expires_at)}</span>}
        </p>
      )}
      {req.status === 'done' && req.accepted_asset_id && (
        <button type="button" className="linklike" onClick={() => openAsset(loc, req.accepted_asset_id!)}>
          Open accepted asset
        </button>
      )}
      <div className="row-wrap req-foot">
        {req.placeholder_asset_id && req.status !== 'done' && (
          <button type="button" className="linklike" onClick={() => openAsset(loc, req.placeholder_asset_id!)}>
            Placeholder
          </button>
        )}
        {req.target_asset_id && (
          <button type="button" className="linklike truncate" onClick={() => openAsset(loc, req.target_asset_id!)}>
            Reworks {req.target_name || 'asset'}
          </button>
        )}
        <span className="faint grow" style={{ textAlign: 'right' }}>
          {timeAgo(req.status === 'done' ? (req.closed_at ?? req.updated_at) : req.created_at)}
        </span>
      </div>
    </article>
  )
}

function ReviewCard({ req, loc, onDetail, onChanged }: { req: WorkRequest; loc: ReturnType<typeof useLocation>; onDetail: (id: string) => void; onChanged: () => void }) {
  const a = useRequestActions(req, onChanged)
  const [rejecting, setRejecting] = useState<Submission | null>(null)
  const waiting = (req.submissions ?? []).filter((s) => !s.verdict)
  return (
    <article className="req-card" data-status="submitted">
      <div className="row req-head">
        <button type="button" className="req-title truncate linklike" onClick={() => onDetail(req.id)} title={req.title}>
          {req.title}
        </button>
      </div>
      <RequestMeta req={req} />
      {req.target_asset_id && (
        <button type="button" className="linklike" onClick={() => openAsset(loc, req.target_asset_id!)}>
          Compare with {req.target_name || 'the current asset'}
        </button>
      )}
      <ul className="cands">
        {waiting.map((s) => (
          <li key={s.asset_id} className="cand">
            <button type="button" className="cand-open" onClick={() => openAsset(loc, s.asset_id)} aria-label={`Open ${s.asset_name || 'candidate'}`}>
              <CandidateThumb sub={s} />
            </button>
            <span className="cand-name truncate" title={s.asset_name}>
              {s.asset_name || 'Untitled'}
            </span>
            {s.note && <span className="cand-note faint">{s.note}</span>}
            <span className="cand-actions row">
              <Button
                size="sm"
                variant="primary"
                icon={Check}
                busy={a.busy === 'accept:' + s.asset_id}
                onClick={() => a.run('accept:' + s.asset_id, () => workApi.accept(req.id, s.asset_id), 'Accepted')}
              >
                Accept
              </Button>
              <Button size="sm" icon={X} onClick={() => setRejecting(s)} aria-label={`Reject ${s.asset_name || 'candidate'}`}>
                Reject
              </Button>
            </span>
          </li>
        ))}
      </ul>
      {rejecting && (
        <FeedbackDialog
          title={`Reject “${rejecting.asset_name || 'candidate'}”`}
          intro="The request goes back to the queue (once no other candidate is waiting); the next generator sees your feedback."
          label="What is wrong?"
          confirmLabel="Reject with feedback"
          onClose={() => setRejecting(null)}
          onSubmit={async (text) => {
            await workApi.reject(req.id, rejecting.asset_id, text)
            toast('Sent back for another try')
            onChanged()
          }}
        />
      )}
    </article>
  )
}

// ---------- details ----------

function RequestDetail({ req, onClose, onChanged }: { req: WorkRequest; onClose: () => void; onChanged: () => void }) {
  const loc = useLocation()
  const a = useRequestActions(req, () => {
    onChanged()
    onClose()
  })
  const spec = Object.entries(req.spec ?? {}).filter(([k]) => k !== 'generator' && k !== 'prompt')
  const history = (req.submissions ?? []).filter((s) => s.verdict)
  return (
    <Dialog
      title={req.title}
      onClose={onClose}
      width={560}
      footer={
        (req.status === 'open' || req.status === 'claimed') && (
          <>
            {req.status === 'claimed' && (
              <Button variant="ghost" busy={a.busy === 'release'} onClick={a.release}>
                Release claim
              </Button>
            )}
            <Button variant="danger" busy={a.busy === 'cancel'} onClick={a.cancel}>
              Cancel request
            </Button>
          </>
        )
      }
    >
      <div className="stack">
        <div className="row-wrap">
          <RequestBadge status={req.status} />
          <RequestMeta req={req} />
        </div>
        {req.description && <p className="req-desc">{req.description}</p>}
        {spec.length > 0 && (
          <dl className="req-spec">
            {spec.map(([k, v]) => (
              <div key={k}>
                <dt>{k}</dt>
                <dd className="mono">{typeof v === 'object' ? JSON.stringify(v) : String(v)}</dd>
              </div>
            ))}
          </dl>
        )}
        <p className="faint">
          {typeLabel(req.asset_type) || 'Any type'} · created {timeAgo(req.created_at)} by {req.created_by}
          {req.status === 'claimed' && ` · claimed by ${req.claimed_by}`}
        </p>
        {req.placeholder_asset_id && (
          <button type="button" className="linklike" onClick={() => { onClose(); openAsset(loc, req.placeholder_asset_id!) }}>
            Open placeholder <StatusBadge status="placeholder" />
          </button>
        )}
        {history.length > 0 && (
          <>
            <h3>Earlier candidates</h3>
            <ul className="cands">
              {history.map((s) => (
                <li key={s.asset_id} className="cand">
                  <button type="button" className="cand-open" onClick={() => { onClose(); openAsset(loc, s.asset_id) }}>
                    <CandidateThumb sub={s} />
                  </button>
                  <span className="cand-name truncate">{s.asset_name}</span>
                  <span className="faint">{s.verdict === 'accepted' ? 'Accepted' : 'Rejected'}</span>
                  {s.feedback && <span className="cand-note muted">{s.feedback}</span>}
                </li>
              ))}
            </ul>
          </>
        )}
      </div>
    </Dialog>
  )
}
