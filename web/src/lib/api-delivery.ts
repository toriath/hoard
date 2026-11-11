import { ApiError, get, seg } from './api'
import type { AtlasOptions, RepoAudit } from './types-delivery'

export const deliveryApi = {
  audit: (projectId: string) => get<RepoAudit>(`/projects/${seg(projectId)}/repo/audit`),
  /** The command contains a signed upload link: treat it like a credential. */
  auditCommand: (projectId: string, root: string) =>
    get<{ command: string; expires_in_seconds: number }>(`/projects/${seg(projectId)}/repo/audit-command?root=${encodeURIComponent(root)}`),
  lockUrl: (projectId: string) => `/api/v1/projects/${seg(projectId)}/repo/lock`,
  atlasUrl: (ids: string[], o: AtlasOptions) => {
    const q = new URLSearchParams({
      ids: ids.join(','),
      padding: String(o.padding),
      max_size: String(o.max_size),
      power_of_two: String(o.power_of_two),
      extrude: String(o.extrude),
      format: o.format,
      res_path: o.res_path,
    })
    return `/api/v1/atlas/pack?${q}`
  },
}

/** Fetches the atlas zip; errors carry the server's message. */
export async function fetchAtlas(ids: string[], o: AtlasOptions): Promise<Blob> {
  let res: Response
  try {
    res = await fetch(deliveryApi.atlasUrl(ids, o), { credentials: 'same-origin' })
  } catch {
    throw new ApiError(0, 'network', 'Cannot reach the hoard server')
  }
  if (!res.ok) {
    let message = res.statusText || 'Packing failed'
    try {
      message = (await res.json()).error?.message ?? message
    } catch {
      /* non-JSON error body */
    }
    throw new ApiError(res.status, 'error', message)
  }
  return res.blob()
}
