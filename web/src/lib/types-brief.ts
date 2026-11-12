// Types of the brief area: project briefs, fit checks, project reports and credits.

export interface BriefAudio {
  sample_rate?: number
  channels?: number
  loudness_lufs?: number
  max_peak_dbfs?: number
}

export interface ProjectBrief {
  engine?: string
  engine_version?: string
  art_direction?: string
  pixel_art?: boolean
  palette?: string[]
  tile_size?: number
  base_resolution?: string
  max_texture_size?: number
  power_of_two?: boolean
  formats?: Record<string, string[]>
  audio?: BriefAudio
  licenses?: string[]
  folders?: Record<string, string>
  naming?: string
  size_budget_mb?: number
  notes?: string
}

export const ENGINES = ['godot', 'unity', 'unreal', 'phaser', 'bevy', 'love2d', 'gamemaker', 'web', 'custom']
export const NAMINGS = ['snake_case', 'kebab-case', 'camelCase', 'PascalCase', 'keep']

export type Severity = 'error' | 'warning' | 'info'

export interface FitIssue {
  severity: Severity
  code: string
  message: string
}

export interface FitReport {
  asset_id: string
  project_id: string
  project_name: string
  has_brief: boolean
  issues: FitIssue[]
  errors: number
  warnings: number
}

export interface ReportAsset {
  id: string
  name: string
  type: string
  status?: string
  detail?: string
}

export interface ReportFit {
  asset: ReportAsset
  errors: number
  warnings: number
  messages: string[]
}

export interface ProjectReport {
  project_id: string
  project_name: string
  has_brief: boolean
  asset_count: number
  verdict: 'ok' | 'attention' | 'problems'
  summary: string
  license_problems: ReportAsset[]
  missing_attribution: ReportAsset[]
  placeholders: ReportAsset[]
  needs_work: ReportAsset[]
  fit_issues: ReportFit[]
  total_size: number
  size_budget_bytes?: number
  over_budget?: boolean
  requests: { open: number; claimed: number; submitted: number; total: number }
  repo_drift?: { snapshot_at: string; files: number; missing_assets: ReportAsset[] }
}

export interface Credits {
  project_id: string
  markdown: string
  asset_count: number
  needs_attention: { asset_id: string; name: string; problem: string }[]
}
