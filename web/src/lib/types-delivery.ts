/** Types of the repository delivery area (audit, sync, atlas). */

export interface AuditCounts {
  files: number
  in_sync: number
  outdated: number
  attention: number
  untracked: number
  missing: number
  unlinked: number
}

export interface AuditKnown {
  path: string
  asset_id: string
  name: string
  status?: string
  license?: string
  flags?: string[]
  newer_asset_id?: string
  newer_name?: string
}

export interface AuditUnknown {
  path: string
  size: number
  suggested_type: string
  suggested_name: string
}

export interface AuditMissing {
  asset_id: string
  name: string
  type: string
  status?: string
  files: string[]
}

export interface AuditUnlinked {
  asset_id: string
  name: string
  paths: string[]
}

export interface RepoAudit {
  project_id: string
  has_snapshot: boolean
  snapshot_at?: string
  counts: AuditCounts
  known: AuditKnown[]
  unknown: AuditUnknown[]
  missing: AuditMissing[]
  unlinked: AuditUnlinked[]
  truncated?: boolean
  hint?: string
}

export interface AtlasOptions {
  padding: number
  max_size: number
  power_of_two: boolean
  extrude: number
  format: 'json' | 'phaser' | 'godot'
  res_path: string
}
