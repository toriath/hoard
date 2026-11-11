// Mirrors the JSON produced by the Go API (internal/hoard).

export interface AssetFile {
  id: string
  filename: string
  mime: string
  size: number
  sha256: string
  width?: number
  height?: number
  kind: PreviewKind
  created_at: string
}

export type PreviewKind = 'image' | 'audio' | 'video' | 'model' | 'font' | 'text' | 'none'

export interface Ref {
  id: string
  name: string
  kind?: string
  note?: string
}

export interface Asset {
  id: string
  type: string
  name: string
  description: string
  tags: string[]
  prompt: string
  source: string
  source_url: string
  license: string
  license_url: string
  attribution: string
  attrs: Record<string, unknown>
  files: AssetFile[]
  primary_file_id: string
  preview_kind: PreviewKind
  thumb_version: number
  total_size: number
  file_count: number
  collections?: Ref[]
  projects?: Ref[]
  ai_status?: '' | 'queued' | 'running' | 'done' | 'error'
  ai_error?: string
  /** Review status; '' = not reviewed. */
  status: AssetStatus
  superseded_by?: string
  derived_from?: string
  /** Structured generation recipe: tool, model, seed, settings… */
  generator?: Record<string, unknown>
  created_at: string
  updated_at: string
  deleted_at?: string
}

export type AssetStatus = '' | 'draft' | 'approved' | 'final' | 'rework' | 'rejected' | 'placeholder' | 'superseded'

export interface AssetPatch {
  status?: AssetStatus
  derived_from?: string
  generator?: Record<string, unknown>
  type?: string
  name?: string
  description?: string
  prompt?: string
  source?: string
  source_url?: string
  license?: string
  license_url?: string
  attribution?: string
  tags?: string[]
  add_tags?: string[]
  remove_tags?: string[]
  attrs?: Record<string, unknown>
  add_collections?: string[]
  remove_collections?: string[]
  add_projects?: string[]
  remove_projects?: string[]
  project_note?: string
}

export interface ListResult {
  items: Asset[]
  total: number
  limit: number
  offset: number
}

export interface Collection {
  id: string
  name: string
  description: string
  kind: string
  asset_count: number
  covers: string[]
  created_at: string
  updated_at: string
}

export interface Project {
  id: string
  name: string
  description: string
  url: string
  status: string
  asset_count: number
  covers: string[]
  created_at: string
  updated_at: string
}

export interface TagCount {
  tag: string
  count: number
}

export interface Stats {
  assets: number
  files: number
  total_size: number
  trashed: number
  untagged: number
  by_type: Record<string, number>
  licenses: TagCount[]
  tags: number
  collections: number
  projects: number
}

export interface AssetType {
  id: string
  label: string
  description: string
}

export interface User {
  id: string
  username: string
  created_at: string
}

export interface Meta {
  version: string
  types: AssetType[]
  licenses: string[]
  collection_kinds: string[]
  project_statuses: string[]
  max_upload_bytes: number
  trash_days: number
  ai_enabled: boolean
  user: User
}

export interface ApiToken {
  id: string
  name: string
  prefix: string
  created_at: string
  last_used_at: string | null
}

export interface AISettings {
  provider: '' | 'anthropic' | 'openai'
  base_url: string
  model: string
  api_key?: string
  has_api_key: boolean
  auto_analyze: boolean
  vision: boolean
  instructions: string
}

export interface Suggestion {
  name: string
  description: string
  tags: string[]
  type: string
}

export interface Filters {
  q: string
  types: string[]
  tags: string[]
  collection: string
  project: string
  license: string
  /** Review statuses, any of; 'none' = not reviewed. */
  statuses: string[]
  untagged: boolean
  trashed: boolean
  sort: string
  order: '' | 'asc' | 'desc'
}
