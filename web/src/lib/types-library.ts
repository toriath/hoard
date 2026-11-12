import type { Asset } from './types'

/** One asset related to another (GET /assets/{id}/similar). */
export interface SimilarHit {
  asset: Asset
  /** 0..1, higher is closer. */
  score: number
  /** Why it is listed: "identical file", "visually near-identical (distance 3)", "shares 4 tags". */
  reasons: string[]
  /** Perceptual hash distance (0-64), images only. */
  distance?: number
}

export interface DuplicateGroup {
  kind: 'identical' | 'near-identical'
  reason: string
  assets: Asset[]
}

export type ImportMode = 'per_file' | 'per_folder' | 'single'

export interface ImportOptions {
  mode?: ImportMode
  collection_name?: string
  license?: string
  tags?: string[]
  project_id?: string
}

export interface ImportResult {
  summary: string
  mode: ImportMode
  collection_id?: string
  collection_name?: string
  license?: string
  license_source?: string
  attribution?: string
  assets: { id: string; name: string; files: number }[]
  files: number
  skipped: number
  skipped_sample?: string[]
  failed?: { name: string; error: string }[]
}
