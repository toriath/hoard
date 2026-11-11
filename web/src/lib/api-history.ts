import { get, seg } from './api'
import type { ActivityFeed, ActivityParams, Lineage } from './types-history'

export const historyApi = {
  activity: (p: ActivityParams = {}, signal?: AbortSignal) => {
    const q = new URLSearchParams()
    if (p.asset) q.set('asset', p.asset)
    if (p.after) q.set('after', String(p.after))
    if (p.action) q.set('action', p.action)
    if (p.via) q.set('via', p.via)
    if (p.limit) q.set('limit', String(p.limit))
    return get<ActivityFeed>('/activity?' + q.toString(), { signal })
  },
  lineage: (id: string) => get<Lineage>(`/assets/${seg(id)}/lineage`),
}
