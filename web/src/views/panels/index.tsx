import type { Asset, Project } from '../../lib/types'
import { FitPanel } from './Fit'
import { HistoryPanel } from './History'
import { LineagePanel } from './Lineage'
import { BriefPanel } from './ProjectBrief'
import { RepoPanel } from './ProjectRepo'
import { ProjectRequestsPanel } from './ProjectRequests'
import { ReviewPanel } from './Review'
import { SimilarPanel } from './Similar'

// Extension slots: each feature area owns one panel file. A panel renders
// nothing when it has nothing to say.

export interface AssetPanelProps {
  asset: Asset
  /** Call with the updated asset after a change so the drawer re-renders. */
  onChange: (a: Asset) => void
}

/** Right below the preview: review status, rework steering, fit against project briefs. */
export function AssetPanelsTop(p: AssetPanelProps) {
  return (
    <>
      <ReviewPanel {...p} />
      <FitPanel {...p} />
    </>
  )
}

/** Above the technical info: lineage, similar assets, history. */
export function AssetPanelsBottom(p: AssetPanelProps) {
  return (
    <>
      <LineagePanel {...p} />
      <SimilarPanel {...p} />
      <HistoryPanel {...p} />
    </>
  )
}

/** On a project page, between the header and the asset grid. */
export function ProjectPanels({ project }: { project: Project }) {
  return (
    <>
      <BriefPanel project={project} />
      <ProjectRequestsPanel project={project} />
      <RepoPanel project={project} />
    </>
  )
}
