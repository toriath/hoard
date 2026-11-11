// Activity feed, lineage and recipes (history area). Mirrors internal/hoard/activity_feed.go and lineage.go.

export interface ActivityEntry {
  seq: number
  at: string
  actor: string
  /** web | api | mcp | system */
  via: string
  /** <object>.<verb>, e.g. asset.update, request.claim */
  action: string
  asset_id?: string
  ref_id?: string
  summary?: string
  asset_name?: string
  thumb_version?: number
  /** The asset was purged; the entry outlives it. */
  asset_gone?: boolean
}

export interface ActivityFeed {
  items: ActivityEntry[]
  latest_seq: number
}

export interface ActivityParams {
  asset?: string
  /** Only entries with a larger seq; the oldest new entries come first. */
  after?: number
  /** Comma-separated action prefixes. */
  action?: string
  /** Comma-separated channels. */
  via?: string
  limit?: number
}

export interface LineageNode {
  id: string
  name: string
  type: string
  status?: string
  thumb_version: number
  prompt?: string
  generator?: Record<string, unknown>
  trashed?: boolean
}

export interface Lineage {
  ancestors: LineageNode[]
  children: LineageNode[]
  siblings: LineageNode[]
  supersedes: LineageNode[]
  superseded_by_chain: LineageNode[]
}
