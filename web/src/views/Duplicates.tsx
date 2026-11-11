import { Copy, Trash2 } from 'lucide-react'
import { api } from '../lib/api'
import { libraryApi } from '../lib/api-library'
import { formatBytes, plural, timeAgo } from '../lib/format'
import { useLocation } from '../lib/router'
import { changed, toast, toastError, useQuery } from '../lib/store'
import { Button, Dialog, Empty, Spinner, useConfirm } from '../ui'
import { openAsset } from './filters'
import { MiniThumb } from './panels/Similar'

/**
 * Groups of assets that hold the same file or the same picture. Opened from
 * the Library's view menu; trashing is the only action, so nothing is lost
 * for good (the trash keeps assets for the configured retention).
 */
export function DuplicatesDialog({ onClose }: { onClose: () => void }) {
  const loc = useLocation()
  const confirm = useConfirm()
  const q = useQuery(() => libraryApi.duplicates(), [])
  const groups = q.data ?? []

  const trash = async (id: string, name: string) => {
    const ok = await confirm({
      title: `Move “${name}” to the trash?`,
      message: 'It stays recoverable from the trash until the retention period ends.',
      confirmLabel: 'Move to trash',
      danger: true,
    })
    if (!ok) return
    try {
      await api.trash(id)
      changed('assets', 'groups')
      q.reload()
      toast('Moved to the trash')
    } catch (e) {
      toastError(e)
    }
  }

  return (
    <Dialog
      title="Duplicates"
      onClose={onClose}
      width={720}
      footer={
        <Button variant="primary" onClick={onClose}>
          Done
        </Button>
      }
    >
      <div className="stack">
        {q.loading && !q.data && <Spinner label="Looking for duplicates…" />}
        {q.error && !q.data && <div className="note note-danger">Could not look for duplicates: {q.error.message}</div>}
        {q.data && groups.length === 0 && (
          <Empty icon={Copy} title="No duplicates">
            No two assets share a file or a near-identical picture. Pictures stored a while ago are checked in the background after the server starts.
          </Empty>
        )}
        {groups.length > 0 && (
          <p className="muted">
            {plural(groups.length, 'group')} of assets that look the same. Open one to compare, or move the surplus ones to the trash.
          </p>
        )}
        {groups.map((g, i) => (
          <section key={i} className="dup-group stack">
            <p className="eyebrow">
              {g.kind === 'identical' ? 'Identical files' : 'Near-identical pictures'} · {plural(g.assets.length, 'asset')}
            </p>
            <ul className="similar-list">
              {g.assets.map((a) => (
                <li key={a.id} className="dup-row">
                  <button
                    type="button"
                    className="similar-item"
                    onClick={() => {
                      onClose()
                      openAsset(loc, a.id)
                    }}
                    aria-label={`Open ${a.name}`}
                  >
                    <MiniThumb asset={a} />
                    <span className="similar-text">
                      <span className="truncate similar-name">{a.name}</span>
                      <span className="faint similar-reasons">
                        {a.type} · {a.files[0]?.width ? `${a.files[0].width}×${a.files[0].height} · ` : ''}
                        {formatBytes(a.total_size)} · added {timeAgo(a.created_at)}
                        {a.projects?.length ? ` · used in ${plural(a.projects.length, 'project')}` : ''}
                      </span>
                    </span>
                  </button>
                  <Button variant="ghost" size="sm" iconOnly icon={Trash2} aria-label={`Move ${a.name} to the trash`} onClick={() => void trash(a.id, a.name)} />
                </li>
              ))}
            </ul>
          </section>
        ))}
      </div>
    </Dialog>
  )
}
