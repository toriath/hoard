import type {
  AISettings, ApiToken, Asset, AssetPatch, Collection, Filters, ListResult, Meta, Project, Stats, Suggestion, TagCount, User,
} from './types'

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
  ) {
    super(message)
  }
}

/** Called when the server says the session is gone. */
let onUnauthorized: () => void = () => {}
export function setUnauthorizedHandler(fn: () => void) {
  onUnauthorized = fn
}

export async function request<T>(method: string, path: string, body?: unknown, init?: RequestInit): Promise<T> {
  const headers: Record<string, string> = {}
  let payload: BodyInit | undefined
  if (body instanceof Blob || body instanceof FormData || body instanceof ArrayBuffer) {
    payload = body
  } else if (body !== undefined) {
    headers['Content-Type'] = 'application/json'
    payload = JSON.stringify(body)
  }
  let res: Response
  try {
    res = await fetch('/api/v1' + path, { method, headers, body: payload, credentials: 'same-origin', ...init })
  } catch {
    throw new ApiError(0, 'network', 'Cannot reach the hoard server')
  }
  if (!res.ok) {
    let code = 'error'
    let message = res.statusText || 'Request failed'
    try {
      const j = await res.json()
      code = j.error?.code ?? code
      message = j.error?.message ?? message
    } catch {
      /* non-JSON error body */
    }
    if (res.status === 401 && code === 'unauthorized') onUnauthorized()
    throw new ApiError(res.status, code, message)
  }
  return (await res.json()) as T
}

export const get = <T>(path: string, init?: RequestInit) => request<T>('GET', path, undefined, init)
export const post = <T>(path: string, body?: unknown) => request<T>('POST', path, body ?? {})

export function filtersToQuery(f: Partial<Filters>, extra: Record<string, string | number> = {}): string {
  const p = new URLSearchParams()
  if (f.q) p.set('q', f.q)
  if (f.types?.length) p.set('type', f.types.join(','))
  if (f.tags?.length) p.set('tag', f.tags.join(','))
  if (f.collection) p.set('collection', f.collection)
  if (f.project) p.set('project', f.project)
  if (f.license) p.set('license', f.license)
  if (f.statuses?.length) p.set('status', f.statuses.join(','))
  if (f.untagged) p.set('untagged', '1')
  if (f.trashed) p.set('trashed', '1')
  if (f.sort) p.set('sort', f.sort)
  if (f.order) p.set('order', f.order)
  for (const [k, v] of Object.entries(extra)) p.set(k, String(v))
  return p.toString()
}

/** Path segment for an id that may have come from the address bar. */
export const seg = (id: string) => encodeURIComponent(id)

export const api = {
  // auth
  status: () => get<{ setup_required: boolean; user: User | null }>('/auth/status'),
  setup: (username: string, password: string) => post<{ user: User }>('/auth/setup', { username, password }),
  login: (username: string, password: string) => post<{ user: User }>('/auth/login', { username, password }),
  logout: () => post('/auth/logout'),
  changePassword: (current: string, next: string) => post('/auth/password', { current, new: next }),
  users: () => get<{ items: User[] }>('/users').then((r) => r.items),
  createUser: (username: string, password: string) => post<User>('/users', { username, password }),
  deleteUser: (id: string) => request('DELETE', `/users/${id}`),
  tokens: () => get<{ items: ApiToken[] }>('/tokens').then((r) => r.items),
  createToken: (name: string) => post<{ token: ApiToken; secret: string }>('/tokens', { name }),
  deleteToken: (id: string) => request('DELETE', `/tokens/${id}`),

  // library
  meta: () => get<Meta>('/meta'),
  stats: () => get<Stats>('/stats'),
  tags: (q = '', limit = 500) => get<{ items: TagCount[] }>(`/tags?q=${encodeURIComponent(q)}&limit=${limit}`).then((r) => r.items),
  renameTag: (from: string, to: string) => post<{ affected: number }>('/tags/rename', { from, to }),
  list: (f: Partial<Filters>, limit: number, offset: number, signal?: AbortSignal) =>
    get<ListResult>('/assets?' + filtersToQuery(f, { limit, offset }), { signal }),
  byIds: (ids: string[], trashed = false) =>
    get<ListResult>(`/assets?ids=${ids.map(seg).join(',')}&limit=${ids.length}${trashed ? '&trashed=1' : ''}`).then((r) => r.items),
  asset: (id: string) => get<Asset>(`/assets/${seg(id)}`),
  update: (id: string, patch: AssetPatch) => request<Asset>('PATCH', `/assets/${seg(id)}`, patch),
  trash: (id: string) => request('DELETE', `/assets/${seg(id)}`),
  bulk: (ids: string[], action: '' | 'trash' | 'restore' | 'purge', patch: AssetPatch = {}) =>
    post<{ affected: number }>('/assets/bulk', { ids, action, patch }),
  deleteFile: (id: string, fileId: string) => request<Asset>('DELETE', `/assets/${seg(id)}/files/${seg(fileId)}`),
  setPrimary: (id: string, fileId: string) => post<Asset>(`/assets/${seg(id)}/files/${seg(fileId)}/primary`),
  putThumbnail: (id: string, blob: Blob) => request<Asset>('PUT', `/assets/${seg(id)}/thumbnail`, blob),

  // groups
  collections: () => get<{ items: Collection[] }>('/collections').then((r) => r.items),
  saveCollection: (id: string | null, body: Partial<Collection>) =>
    id ? request<Collection>('PATCH', `/collections/${id}`, body) : post<Collection>('/collections', body),
  deleteCollection: (id: string) => request('DELETE', `/collections/${id}`),
  projects: () => get<{ items: Project[] }>('/projects').then((r) => r.items),
  saveProject: (id: string | null, body: Partial<Project>) =>
    id ? request<Project>('PATCH', `/projects/${id}`, body) : post<Project>('/projects', body),
  deleteProject: (id: string) => request('DELETE', `/projects/${id}`),

  // ai
  aiSettings: () => get<AISettings>('/settings/ai'),
  saveAI: (s: Partial<AISettings> & { clear_api_key?: boolean }) => request<AISettings>('PUT', '/settings/ai', s),
  testAI: () => post<{ ok: boolean; message: string }>('/settings/ai/test'),
  aiModels: (baseUrl: string, apiKey?: string) =>
    post<{ items: string[]; error?: string }>('/settings/ai/models', { base_url: baseUrl, api_key: apiKey || undefined }),
  suggest: (id: string) => post<Suggestion>(`/assets/${seg(id)}/suggest`),
  analyze: (ids: string[], overwrite = false) => post<{ queued: number }>('/assets/analyze', { ids, overwrite }),
}

// ---- URLs ----

export const thumbUrl = (a: Pick<Asset, 'id' | 'thumb_version'>) => `/api/v1/assets/${seg(a.id)}/thumbnail?v=${a.thumb_version}`
export const rawUrl = (id: string, filename: string) => `/api/v1/assets/${seg(id)}/raw/${encodeURIComponent(filename)}`
export const downloadUrl = (id: string) => `/api/v1/assets/${seg(id)}/download`
export const fileDownloadUrl = (id: string, filename: string) => rawUrl(id, filename) + '?download=1'
export const zipUrl = (ids: string[]) => `/api/v1/download?ids=${ids.join(',')}`

// ---- uploads (XHR for progress events) ----

export interface UploadHandle {
  promise: Promise<{ asset: Asset; duplicate_of?: string[] }>
  abort: () => void
}

function xhrUpload(path: string, form: FormData, onProgress?: (fraction: number) => void): UploadHandle {
  const xhr = new XMLHttpRequest()
  const promise = new Promise<{ asset: Asset; duplicate_of?: string[] }>((resolve, reject) => {
    xhr.open('POST', '/api/v1' + path)
    xhr.responseType = 'json'
    xhr.upload.onprogress = (e) => {
      if (e.lengthComputable && onProgress) onProgress(e.loaded / e.total)
    }
    xhr.onload = () => {
      if (xhr.status >= 200 && xhr.status < 300) resolve(xhr.response)
      else {
        if (xhr.status === 401) onUnauthorized()
        reject(new ApiError(xhr.status, xhr.response?.error?.code ?? 'error', xhr.response?.error?.message ?? 'Upload failed'))
      }
    }
    xhr.onerror = () => reject(new ApiError(0, 'network', 'Upload failed: connection lost'))
    xhr.onabort = () => reject(new ApiError(0, 'aborted', 'Upload cancelled'))
    xhr.send(form)
  })
  return { promise, abort: () => xhr.abort() }
}

/** Creates one asset from the given files; `meta` must precede the files. */
export function uploadAsset(files: File[], meta: AssetPatch, onProgress?: (fraction: number) => void): UploadHandle {
  const form = new FormData()
  form.append('meta', new Blob([JSON.stringify(meta)], { type: 'application/json' }))
  for (const f of files) form.append('file', f, f.name)
  return xhrUpload('/assets', form, onProgress)
}

/** Adds files to an existing asset. */
export function uploadFiles(id: string, files: File[], onProgress?: (fraction: number) => void): UploadHandle {
  const form = new FormData()
  for (const f of files) form.append('file', f, f.name)
  return xhrUpload(`/assets/${seg(id)}/files`, form, onProgress)
}
