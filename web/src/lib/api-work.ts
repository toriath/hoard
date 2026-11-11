import { get, post, request, seg } from './api'
import type { Asset } from './types'
import type { Note, NoteKind, RequestFilter, RequestInput, ReviewResult, ReviewState, Verdict, WorkRequest } from './types-work'

function requestQuery(f: RequestFilter): string {
  const p = new URLSearchParams()
  if (f.status?.length) p.set('status', f.status.join(','))
  if (f.project) p.set('project', f.project)
  if (f.kind) p.set('kind', f.kind)
  if (f.type) p.set('type', f.type)
  if (f.sort) p.set('sort', f.sort)
  if (f.limit) p.set('limit', String(f.limit))
  return p.toString()
}

export const workApi = {
  requests: (f: RequestFilter = {}) => get<{ items: WorkRequest[] }>('/requests?' + requestQuery(f)).then((r) => r.items),
  request: (id: string) => get<WorkRequest>(`/requests/${seg(id)}`),
  createRequest: (body: RequestInput) => post<WorkRequest>('/requests', body),
  patchRequest: (id: string, body: Partial<Omit<RequestInput, 'placeholder'>>) => request<WorkRequest>('PATCH', `/requests/${seg(id)}`, body),
  cancelRequest: (id: string) => post<WorkRequest>(`/requests/${seg(id)}/cancel`),
  releaseRequest: (id: string) => post<WorkRequest>(`/requests/${seg(id)}/release`),
  makePlaceholder: (id: string) => post<Asset>(`/requests/${seg(id)}/placeholder`),
  accept: (requestId: string, assetId: string, supersede = true) =>
    post<WorkRequest>(`/requests/${seg(requestId)}/submissions/${seg(assetId)}/accept`, { supersede }),
  reject: (requestId: string, assetId: string, feedback: string) =>
    post<WorkRequest>(`/requests/${seg(requestId)}/submissions/${seg(assetId)}/reject`, { feedback }),

  review: (assetId: string) => get<ReviewState>(`/assets/${seg(assetId)}/review`),
  reviewAsset: (assetId: string, verdict: Verdict, feedback = '', priority = 0, projectId = '') =>
    post<ReviewResult>(`/assets/${seg(assetId)}/review`, { verdict, feedback, priority, project_id: projectId }),
  addNote: (assetId: string, body: string, kind: NoteKind = 'comment') => post<Note>(`/assets/${seg(assetId)}/notes`, { body, kind }),
  resolveNote: (noteId: string) => post<Note>(`/notes/${seg(noteId)}/resolve`),
}
