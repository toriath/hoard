// Mirrors the JSON of the request queue and review API (internal/hoard/requests.go, notes.go, review.go).

import type { Asset } from './types'

export type RequestStatus = 'open' | 'claimed' | 'submitted' | 'done' | 'cancelled'
export type NoteKind = 'comment' | 'rework' | 'approval' | 'rejection'

export interface Submission {
  request_id: string
  asset_id: string
  note?: string
  /** '' = waiting for review */
  verdict?: '' | 'accepted' | 'rejected'
  submitted_by: string
  submitted_at: string
  feedback?: string
  asset_name?: string
  asset_type?: string
  asset_status?: string
  preview_kind?: string
  primary_file?: string
  thumb_version?: number
  trashed?: boolean
}

export interface Note {
  id: string
  asset_id: string
  kind: NoteKind
  body: string
  author: string
  created_at: string
  resolved_at?: string
}

export interface WorkRequest {
  id: string
  project_id?: string
  project_name?: string
  kind: 'new' | 'rework'
  title: string
  description: string
  asset_type: string
  spec: Record<string, unknown>
  target_asset_id?: string
  target_name?: string
  placeholder_asset_id?: string
  accepted_asset_id?: string
  status: RequestStatus
  priority: number
  claimed_by?: string
  claim_expires_at?: string
  created_by: string
  created_at: string
  updated_at: string
  closed_at?: string
  /** Set on creation when a placeholder was asked for but could not be made. */
  placeholder_error?: string
  submissions?: Submission[]
  notes?: Note[]
}

export interface RequestInput {
  project_id?: string
  title: string
  description?: string
  asset_type?: string
  spec?: Record<string, unknown>
  priority?: number
  placeholder?: boolean
}

export interface RequestFilter {
  status?: RequestStatus[]
  project?: string
  kind?: string
  type?: string
  sort?: 'recent'
  limit?: number
}

/** What the asset drawer needs: notes and the requests the asset takes part in. */
export interface ReviewState {
  status: string
  superseded_by?: string
  notes: Note[]
  requests: WorkRequest[]
}

export interface ReviewResult {
  asset: Asset
  note?: Note
  request?: WorkRequest
  reused_request?: boolean
}

export type Verdict = 'approve' | 'reject' | 'rework'

/** Priorities offered in the UI; the API takes any integer. */
export const PRIORITIES: { value: number; label: string }[] = [
  { value: -1, label: 'Low' },
  { value: 0, label: 'Normal' },
  { value: 1, label: 'High' },
  { value: 2, label: 'Urgent' },
]

export const priorityLabel = (p: number) => PRIORITIES.find((x) => x.value === p)?.label ?? (p > 2 ? 'Urgent' : 'Low')

/** Server limits on text, counted in UTF-8 bytes. */
export const NOTE_MAX_BYTES = 8192
export const DESCRIPTION_MAX_BYTES = 20000
export const byteLength = (s: string) => new TextEncoder().encode(s).length

/** Asset types for which hoard can generate a placeholder (PNG for the first group, WAV for the second). */
export const PLACEHOLDER_IMAGE_TYPES = ['sprite', 'ui', 'tileset', 'texture', 'vfx']
export const PLACEHOLDER_AUDIO_TYPES = ['sfx', 'music', 'voice']
export const canPlaceholder = (type: string) => PLACEHOLDER_IMAGE_TYPES.includes(type) || PLACEHOLDER_AUDIO_TYPES.includes(type)
