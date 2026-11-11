import { Check, MessageSquare, RotateCcw, ThumbsUp, X } from 'lucide-react'
import { useState } from 'react'
import { timeAgo } from '../../lib/format'
import { api } from '../../lib/api'
import { link, useLocation } from '../../lib/router'
import { workApi } from '../../lib/api-work'
import { changed, toast, toastError, useQuery, useVersion } from '../../lib/store'
import type { Note, WorkRequest } from '../../lib/types-work'
import { Button, Chip } from '../../ui'
import { RequestBadge, StatusBadge } from '../../ui/status'
import { FeedbackDialog } from '../work-parts'
import { openAsset } from '../filters'
import type { AssetPanelProps } from '.'

const KIND_LABEL: Record<string, string> = { comment: 'Comment', rework: 'Rework', approval: 'Approved', rejection: 'Rejected' }

/** Asset drawer panel: review status, steering (approve / needs rework / reject) and the notes thread. */
export function ReviewPanel({ asset, onChange }: AssetPanelProps) {
  const loc = useLocation()
  const version = useVersion('assets')
  const state = useQuery(() => workApi.review(asset.id), [asset.id, asset.status, asset.updated_at, version])
  const [dialog, setDialog] = useState<'rework' | 'reject' | null>(null)
  const [busy, setBusy] = useState('')
  const [comment, setComment] = useState('')
  const trashed = !!asset.deleted_at

  const run = async (key: string, fn: () => Promise<void>) => {
    setBusy(key)
    try {
      await fn()
    } catch (e) {
      toastError(e)
    } finally {
      setBusy('')
    }
  }
  const refresh = () => {
    changed('assets')
    state.reload()
  }

  const approve = () =>
    run('approve', async () => {
      const r = await workApi.reviewAsset(asset.id, 'approve')
      onChange(r.asset)
      toast('Approved')
      refresh()
    })
  const post = () =>
    run('comment', async () => {
      await workApi.addNote(asset.id, comment.trim())
      setComment('')
      state.reload()
    })
  const resolve = (n: Note) =>
    run('resolve:' + n.id, async () => {
      await workApi.resolveNote(n.id)
      state.reload()
    })
  const decide = (req: WorkRequest, accept: boolean, feedback = '') =>
    run('sub:' + req.id, async () => {
      if (accept) await workApi.accept(req.id, asset.id)
      else await workApi.reject(req.id, asset.id, feedback)
      onChange(await api.asset(asset.id))
      toast(accept ? 'Accepted' : 'Sent back for another try')
      refresh()
    })

  const notes = state.data?.notes ?? []
  const requests = state.data?.requests ?? []
  const waiting = requests.filter((r) => r.status !== 'done' && r.status !== 'cancelled' && r.submissions?.some((s) => s.asset_id === asset.id && !s.verdict))
  const rework = requests.filter((r) => r.target_asset_id === asset.id && ['open', 'claimed', 'submitted'].includes(r.status))
  const replaces = requests.find((r) => r.accepted_asset_id === asset.id && (r.target_asset_id || r.placeholder_asset_id))
  const hasContent = !!asset.status || notes.length > 0 || requests.length > 0

  return (
    <section className="review-panel stack" aria-label="Review">
      <div className="row-wrap">
        <span className="eyebrow">Review</span>
        <StatusBadge status={asset.status} />
        {asset.status === 'superseded' && asset.superseded_by && (
          <button type="button" className="linklike" onClick={() => openAsset(loc, asset.superseded_by!)}>
            see the asset that replaced it
          </button>
        )}
      </div>

      {asset.status === 'placeholder' && (
        <div className="note">This is a stand-in. Accepting a real asset for its request replaces it in the project.</div>
      )}
      {replaces && (
        <div className="note note-ok">
          Accepted for “{replaces.title}”{replaces.target_name ? ` as the new version of ${replaces.target_name}` : ' in place of its placeholder'}.
        </div>
      )}

      {waiting.map((r) => (
        <RequestWaiting key={r.id} req={r} busy={busy === 'sub:' + r.id} onAccept={() => decide(r, true)} onReject={(f) => decide(r, false, f)} />
      ))}

      {rework.map((r) => (
        <div className="review-request row-wrap" key={r.id}>
          <RequestBadge status={r.status} />
          <span className="grow">
            Rework request{r.status === 'claimed' && r.claimed_by ? ` – ${r.claimed_by}` : ''}
            {r.status === 'submitted' ? ': candidates are waiting for review' : ''}
          </span>
          <a {...link('/requests')}>Open board</a>
        </div>
      ))}

      {!trashed && (
        <div className="row-wrap review-actions">
          <Button size="sm" icon={ThumbsUp} onClick={approve} busy={busy === 'approve'} disabled={asset.status === 'approved'}>
            Approve
          </Button>
          <Button size="sm" icon={RotateCcw} onClick={() => setDialog('rework')}>
            Needs rework
          </Button>
          <Button size="sm" icon={X} onClick={() => setDialog('reject')} disabled={asset.status === 'rejected'}>
            Reject
          </Button>
        </div>
      )}

      {hasContent && notes.length === 0 && state.loading ? null : (
        <ul className="notes" aria-label="Notes">
          {notes.map((n) => (
            <li key={n.id} className="note-item" data-kind={n.kind} data-resolved={!!n.resolved_at}>
              <div className="row-wrap note-head">
                <Chip>{KIND_LABEL[n.kind] ?? n.kind}</Chip>
                <span className="muted truncate grow" title={n.author}>
                  {n.author}
                </span>
                <span className="faint" title={n.created_at}>
                  {timeAgo(n.created_at)}
                </span>
              </div>
              <p className="note-body">{n.body}</p>
              {n.resolved_at ? (
                <span className="faint">Resolved {timeAgo(n.resolved_at)}</span>
              ) : (
                n.kind === 'rework' && (
                  <Button size="sm" variant="ghost" icon={Check} busy={busy === 'resolve:' + n.id} onClick={() => resolve(n)}>
                    Mark resolved
                  </Button>
                )
              )}
            </li>
          ))}
        </ul>
      )}

      {!trashed && (
        <div className="note-form">
          <textarea
            className="textarea"
            rows={2}
            maxLength={8000}
            aria-label="Add a comment"
            placeholder="Add a comment…"
            value={comment}
            onChange={(e) => setComment(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && (e.metaKey || e.ctrlKey) && comment.trim()) void post()
            }}
          />
          <Button size="sm" icon={MessageSquare} disabled={!comment.trim()} busy={busy === 'comment'} onClick={post}>
            Comment
          </Button>
        </div>
      )}

      {dialog === 'rework' && (
        <FeedbackDialog
          title={`Needs rework: ${asset.name}`}
          intro="The feedback goes to the generator agent that picks up the rework request. Say what to keep and what to change."
          label="What should change?"
          placeholder="The helmet is too large, make the palette warmer…"
          confirmLabel="Request rework"
          withPriority
          onClose={() => setDialog(null)}
          onSubmit={async (text, priority) => {
            const r = await workApi.reviewAsset(asset.id, 'rework', text, priority)
            onChange(r.asset)
            toast(r.reused_request ? 'Added to the open rework request' : 'Rework request created')
            refresh()
          }}
        />
      )}
      {dialog === 'reject' && (
        <FeedbackDialog
          title={`Reject: ${asset.name}`}
          label="Why?"
          placeholder="Wrong style for this game…"
          confirmLabel="Reject"
          danger
          onClose={() => setDialog(null)}
          onSubmit={async (text) => {
            const r = await workApi.reviewAsset(asset.id, 'reject', text)
            onChange(r.asset)
            toast('Rejected')
            refresh()
          }}
        />
      )}
    </section>
  )
}

/** This asset is a candidate for a request: accept it or send it back. */
function RequestWaiting({ req, busy, onAccept, onReject }: { req: WorkRequest; busy: boolean; onAccept: () => void; onReject: (feedback: string) => void }) {
  const [rejecting, setRejecting] = useState(false)
  return (
    <div className="note note-warn review-candidate">
      <div className="stack grow">
        <span>
          Candidate for the request “{req.title}”
          {req.kind === 'rework' && req.target_name ? ` (rework of ${req.target_name})` : ''}.
        </span>
        <div className="row-wrap">
          <Button size="sm" variant="primary" icon={Check} busy={busy} onClick={onAccept}>
            Accept
          </Button>
          <Button size="sm" icon={X} onClick={() => setRejecting(true)}>
            Send back…
          </Button>
        </div>
      </div>
      {rejecting && (
        <FeedbackDialog
          title="Send back with feedback"
          intro="The request returns to the queue; the next claim shows this feedback."
          label="What is wrong?"
          confirmLabel="Send back"
          onClose={() => setRejecting(false)}
          onSubmit={async (t) => onReject(t)}
        />
      )}
    </div>
  )
}
