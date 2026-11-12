import { CircleAlert, CircleCheck, Info, TriangleAlert } from 'lucide-react'
import type { AssetPanelProps } from '.'
import { briefApi } from '../../lib/api-brief'
import { useQuery } from '../../lib/store'
import type { FitReport, Severity } from '../../lib/types-brief'

const sevIcon = { error: CircleAlert, warning: TriangleAlert, info: Info } as const
const sevLabel: Record<Severity, string> = { error: 'Error', warning: 'Warning', info: 'Info' }

export function SeverityIcon({ severity }: { severity: Severity }) {
  const Icon = sevIcon[severity]
  return <Icon className={`sev sev-${severity}`} aria-label={sevLabel[severity]} role="img" />
}

function ProjectFit({ report }: { report: FitReport }) {
  const clean = report.errors === 0 && report.warnings === 0
  return (
    // Open when something needs attention, collapsed when the asset fits.
    <details className="fit" open={!clean}>
      <summary>
        {clean ? <CircleCheck className="sev sev-ok" aria-hidden /> : <SeverityIcon severity={report.errors ? 'error' : 'warning'} />}
        <span className="grow truncate">{report.project_name}</span>
        <span className="muted">
          {clean
            ? 'fits'
            : [report.errors && `${report.errors} error${report.errors > 1 ? 's' : ''}`, report.warnings && `${report.warnings} warning${report.warnings > 1 ? 's' : ''}`].filter(Boolean).join(', ')}
        </span>
      </summary>
      <ul className="fit-list">
        {report.issues.map((i, n) => (
          <li key={n}>
            <SeverityIcon severity={i.severity} />
            <span>{i.message}</span>
          </li>
        ))}
      </ul>
    </details>
  )
}

/** Asset drawer panel: fit of the asset against the briefs of the projects it is used in. Nothing when no project has a brief. */
export function FitPanel({ asset }: AssetPanelProps) {
  const projectKey = (asset.projects ?? []).map((p) => p.id).join(',')
  const { data } = useQuery(
    () => (projectKey ? briefApi.fit(asset.id) : Promise.resolve({ reports: [] as FitReport[] })),
    [asset.id, asset.updated_at, projectKey],
  )
  const reports = (data?.reports ?? []).filter((r) => r.has_brief)
  if (reports.length === 0) return null
  return (
    <section className="stack">
      <p className="eyebrow">Fit</p>
      {reports.map((r) => (
        <ProjectFit key={r.project_id} report={r} />
      ))}
    </section>
  )
}
