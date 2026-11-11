import { get, post, request, seg } from './api'
import type { Credits, FitReport, ProjectBrief, ProjectReport } from './types-brief'

export const briefApi = {
  brief: (projectId: string) => get<ProjectBrief>(`/projects/${seg(projectId)}/brief`),
  /** Merge: a field present replaces the stored value, null clears it, absent fields stay. */
  saveBrief: (projectId: string, patch: Record<string, unknown>) => request<ProjectBrief>('PATCH', `/projects/${seg(projectId)}/brief`, patch),
  report: (projectId: string) => get<ProjectReport>(`/projects/${seg(projectId)}/report`),
  credits: (projectId: string) => get<Credits>(`/projects/${seg(projectId)}/credits`),
  /** Checks against one project, or against every project the asset is used in. A POST: it stores measured audio values. */
  fit: (assetId: string, projectId?: string) =>
    post<{ reports: FitReport[] }>(`/assets/${seg(assetId)}/fit`, projectId ? { project_id: projectId } : {}),
}

export const creditsMdUrl = (projectId: string) => `/api/v1/projects/${seg(projectId)}/credits.md`
export const bundleUrl = (projectId: string) => `/api/v1/projects/${seg(projectId)}/bundle`
