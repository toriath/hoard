package hoard

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// ReportAsset points at an asset in a report, with what is wrong with it.
type ReportAsset struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	Status string `json:"status,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// ReportFit counts the fit findings of one asset.
type ReportFit struct {
	Asset    ReportAsset `json:"asset"`
	Errors   int         `json:"errors"`
	Warnings int         `json:"warnings"`
	Messages []string    `json:"messages"` // the first few errors and warnings
}

// ReportRequests counts the open work of a project's asset request queue.
type ReportRequests struct {
	Open      int `json:"open"`
	Claimed   int `json:"claimed"`
	Submitted int `json:"submitted"` // waiting for review
	Total     int `json:"total"`
}

// RepoDrift lists project assets that the last repository snapshot does not
// contain. It is only present when a snapshot exists.
type RepoDrift struct {
	SnapshotAt    string        `json:"snapshot_at"`
	Files         int           `json:"files"`
	MissingAssets []ReportAsset `json:"missing_assets"`
}

// ProjectReport is the health check of a project.
type ProjectReport struct {
	ProjectID   string `json:"project_id"`
	ProjectName string `json:"project_name"`
	HasBrief    bool   `json:"has_brief"`
	AssetCount  int    `json:"asset_count"`

	// Verdict is ok, attention (things to look at) or problems (something is
	// wrong: a disallowed license, assets that do not fit, a blown budget).
	Verdict string `json:"verdict"`
	Summary string `json:"summary"`

	LicenseProblems    []ReportAsset `json:"license_problems"`
	MissingAttribution []ReportAsset `json:"missing_attribution"`
	Placeholders       []ReportAsset `json:"placeholders"`
	NeedsWork          []ReportAsset `json:"needs_work"` // rework, rejected or superseded assets still in the project
	FitIssues          []ReportFit   `json:"fit_issues"`

	TotalSize       int64          `json:"total_size"`
	SizeBudgetBytes int64          `json:"size_budget_bytes,omitempty"`
	OverBudget      bool           `json:"over_budget,omitempty"`
	Requests        ReportRequests `json:"requests"`
	RepoDrift       *RepoDrift     `json:"repo_drift,omitempty"`
}

// fit codes that the report lists in sections of their own.
var reportOwnCodes = map[string]bool{
	"license_missing": true, "license_not_allowed": true, "attribution_missing": true,
	"status_rejected": true, "status_superseded": true, "status_placeholder": true, "status_rework": true,
	"no_brief": true,
}

// ProjectReport checks the health of a project: licence problems, missing
// attribution, placeholders still in use, assets that need work, assets that
// do not fit the brief, size against budget, open requests and drift between
// the library and the game repository. It only reads: audio measurements are
// not stored.
func (s *Service) ProjectReport(ctx context.Context, projectID string) (*ProjectReport, error) {
	p, err := s.GetProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	brief, err := s.GetProjectBrief(ctx, projectID)
	if err != nil {
		return nil, err
	}
	assets, err := s.projectAssets(ctx, projectID)
	if err != nil {
		return nil, err
	}
	r := &ProjectReport{
		ProjectID: p.ID, ProjectName: p.Name, HasBrief: !brief.IsEmpty(), AssetCount: len(assets),
		LicenseProblems: []ReportAsset{}, MissingAttribution: []ReportAsset{}, Placeholders: []ReportAsset{},
		NeedsWork: []ReportAsset{}, FitIssues: []ReportFit{},
	}
	ref := func(a *Asset, detail string) ReportAsset {
		return ReportAsset{ID: a.ID, Name: a.Name, Type: a.Type, Status: a.Status, Detail: detail}
	}
	for _, a := range assets {
		if a.Status != "superseded" && a.Status != "rejected" {
			r.TotalSize += a.TotalSize
		}
		lic := strings.TrimSpace(a.License)
		switch {
		case a.Status == "superseded": // not shipped any more
		case noLicense(lic):
			r.LicenseProblems = append(r.LicenseProblems, ref(a, "no license recorded"))
		case len(brief.Licenses) > 0 && !slices.ContainsFunc(brief.Licenses, func(l string) bool { return strings.EqualFold(l, lic) }):
			r.LicenseProblems = append(r.LicenseProblems, ref(a, fmt.Sprintf("license %s is not allowed by the brief (allowed: %s)", lic, strings.Join(brief.Licenses, ", "))))
		}
		if a.Status != "superseded" && needsAttribution(lic) && strings.TrimSpace(a.Attribution) == "" {
			r.MissingAttribution = append(r.MissingAttribution, ref(a, "license "+lic+" requires attribution"))
		}
		switch a.Status {
		case "placeholder":
			r.Placeholders = append(r.Placeholders, ref(a, "placeholder still in use"))
		case "rework", "rejected", "superseded":
			detail := "marked " + a.Status
			if a.Status == "superseded" && a.SupersededBy != "" {
				detail += " by " + a.SupersededBy
			}
			r.NeedsWork = append(r.NeedsWork, ref(a, detail))
		}
		if r.HasBrief {
			issues, err := s.checkFit(ctx, a, brief, false)
			if err != nil {
				return nil, err
			}
			rf := ReportFit{Asset: ref(a, ""), Messages: []string{}}
			for _, is := range issues {
				if reportOwnCodes[is.Code] || (is.Severity != sevError && is.Severity != sevWarning) {
					continue
				}
				if is.Severity == sevError {
					rf.Errors++
				} else {
					rf.Warnings++
				}
				if len(rf.Messages) < 3 {
					rf.Messages = append(rf.Messages, is.Message)
				}
			}
			if rf.Errors+rf.Warnings > 0 {
				r.FitIssues = append(r.FitIssues, rf)
			}
		}
	}
	slices.SortStableFunc(r.FitIssues, func(a, b ReportFit) int { return b.Errors - a.Errors })

	if brief.SizeBudgetMB != nil {
		r.SizeBudgetBytes = int64(*brief.SizeBudgetMB * 1e6)
		r.OverBudget = r.TotalSize > r.SizeBudgetBytes
	}

	// Open requests (read-only; the request queue belongs to the work area).
	rows, err := s.db.QueryContext(ctx, `SELECT status, COUNT(*) FROM asset_requests
		WHERE project_id = ? AND status IN ('open', 'claimed', 'submitted') GROUP BY status`, projectID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			rows.Close()
			return nil, err
		}
		switch st {
		case "open":
			r.Requests.Open = n
		case "claimed":
			r.Requests.Claimed = n
		case "submitted":
			r.Requests.Submitted = n
		}
		r.Requests.Total += n
	}
	rows.Close()

	if r.RepoDrift, err = s.repoDrift(ctx, projectID, assets, ref); err != nil {
		return nil, err
	}
	r.verdict()
	return r, nil
}

// repoDrift compares the project's assets with the latest repository
// snapshot (table project_files, written by the delivery area). An asset is
// missing when none of its files is in the snapshot, by hash or by link.
func (s *Service) repoDrift(ctx context.Context, projectID string, assets []*Asset, ref func(*Asset, string) ReportAsset) (*RepoDrift, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT sha256, COALESCE(asset_id, ''), seen_at FROM project_files WHERE project_id = ?`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	shas, ids := map[string]bool{}, map[string]bool{}
	d := &RepoDrift{MissingAssets: []ReportAsset{}}
	for rows.Next() {
		var sha, id, seen string
		if err := rows.Scan(&sha, &id, &seen); err != nil {
			return nil, err
		}
		shas[sha] = true
		if id != "" {
			ids[id] = true
		}
		d.Files++
		if seen > d.SnapshotAt {
			d.SnapshotAt = seen
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if d.Files == 0 {
		return nil, nil
	}
	for _, a := range assets {
		if a.Status == "superseded" || a.Status == "rejected" || len(a.Files) == 0 || ids[a.ID] {
			continue
		}
		if !slices.ContainsFunc(a.Files, func(f File) bool { return shas[f.SHA256] }) {
			d.MissingAssets = append(d.MissingAssets, ref(a, "none of its files is in the repository snapshot of "+d.SnapshotAt))
		}
	}
	return d, nil
}

func (r *ProjectReport) verdict() {
	var problems, attention []string
	add := func(list *[]string, n int, what string) {
		if n > 0 {
			*list = append(*list, fmt.Sprintf("%d %s", n, what))
		}
	}
	fitErr, fitWarn := 0, 0
	for _, f := range r.FitIssues {
		if f.Errors > 0 {
			fitErr++
		} else {
			fitWarn++
		}
	}
	add(&problems, len(r.LicenseProblems), "asset(s) with license problems")
	add(&problems, fitErr, "asset(s) that do not fit the brief")
	if r.OverBudget {
		problems = append(problems, fmt.Sprintf("size %s exceeds the budget of %s", humanBytes(r.TotalSize), humanBytes(r.SizeBudgetBytes)))
	}
	add(&attention, len(r.MissingAttribution), "asset(s) missing attribution")
	add(&attention, len(r.Placeholders), "placeholder(s) still in use")
	add(&attention, len(r.NeedsWork), "asset(s) in rework, rejected or superseded")
	add(&attention, fitWarn, "asset(s) with fit warnings")
	add(&attention, r.Requests.Total, "open request(s)")
	if r.RepoDrift != nil {
		add(&attention, len(r.RepoDrift.MissingAssets), "asset(s) missing from the repository")
	}
	switch {
	case len(problems) > 0:
		r.Verdict = "problems"
		r.Summary = "Problems: " + strings.Join(problems, "; ") + "."
		if len(attention) > 0 {
			r.Summary += " Also: " + strings.Join(attention, "; ") + "."
		}
	case len(attention) > 0:
		r.Verdict = "attention"
		r.Summary = "Needs attention: " + strings.Join(attention, "; ") + "."
	default:
		r.Verdict = "ok"
		r.Summary = fmt.Sprintf("No problems found in %d asset(s).", r.AssetCount)
		if !r.HasBrief {
			r.Summary += " The project has no brief, so fit was not checked."
		}
	}
}

// projectAssets loads the live (not trashed) assets used in a project.
func (s *Service) projectAssets(ctx context.Context, projectID string) ([]*Asset, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT pa.asset_id FROM project_assets pa JOIN assets a ON a.id = pa.asset_id
		WHERE pa.project_id = ? AND a.deleted_at IS NULL ORDER BY a.name COLLATE NOCASE, a.id`, projectID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	out := make([]*Asset, 0, len(ids))
	for _, id := range ids {
		a, err := s.GetAsset(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

// ProjectAssets returns the live (not trashed) assets used in a project, with
// all their details, ordered by name.
func (s *Service) ProjectAssets(ctx context.Context, projectID string) ([]*Asset, error) {
	if _, err := s.GetProject(ctx, projectID); err != nil {
		return nil, err
	}
	return s.projectAssets(ctx, projectID)
}
