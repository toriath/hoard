import { Plus } from 'lucide-react'
import { useState } from 'react'
import { api } from '../../lib/api'
import { workApi } from '../../lib/api-work'
import { link, useLocation } from '../../lib/router'
import { changed, toast, useQuery, useVersion } from '../../lib/store'
import type { Project } from '../../lib/types'
import { Button, TypeBadge } from '../../ui'
import { RequestBadge } from '../../ui/status'
import { openAsset } from '../filters'
import { NewRequestDialog } from '../work-parts'

/** Project page panel: open asset requests and placeholders of the project still in use. */
export function ProjectRequestsPanel({ project }: { project: Project }) {
  const loc = useLocation()
  const version = useVersion('assets')
  const [creating, setCreating] = useState(false)
  const requests = useQuery(() => workApi.requests({ status: ['open', 'claimed', 'submitted'], project: project.id, limit: 50 }), [project.id, version])
  const placeholders = useQuery(() => api.list({ project: project.id, statuses: ['placeholder'] }, 50, 0).then((r) => r.items), [project.id, version])
  const reqs = requests.data ?? []
  const stand = placeholders.data ?? []

  return (
    <section className="card project-requests" aria-label="Asset requests">
      <div className="card-head">
        <div className="grow">
          <h3>Asset requests</h3>
          <p className="faint">
            {requests.data ? `${reqs.length}${reqs.length >= 50 ? '+' : ''} unfinished` : 'Loading…'}
            {stand.length > 0 && ` · ${stand.length} placeholder${stand.length === 1 ? '' : 's'} still in use`}
          </p>
        </div>
        <Button size="sm" icon={Plus} onClick={() => setCreating(true)}>
          Request asset
        </Button>
      </div>
      {(reqs.length > 0 || stand.length > 0) && (
        <ul className="plain-list">
          {reqs.map((r) => (
            <li key={r.id} className="row">
              <RequestBadge status={r.status} />
              <a className="grow truncate" {...link('/requests')} title={r.title}>
                {r.title}
              </a>
              {r.asset_type && <TypeBadge type={r.asset_type} />}
            </li>
          ))}
          {stand.map((a) => (
            <li key={a.id} className="row">
              <span className="badge status-badge" data-status="placeholder">
                Placeholder
              </span>
              <button type="button" className="grow truncate linklike" onClick={() => openAsset(loc, a.id)} title={a.name}>
                {a.name}
              </button>
              <TypeBadge type={a.type} />
            </li>
          ))}
        </ul>
      )}
      {creating && (
        <NewRequestDialog
          project={project.id}
          onClose={() => setCreating(false)}
          onCreated={() => {
            toast('Request created')
            changed('assets')
          }}
        />
      )}
    </section>
  )
}
