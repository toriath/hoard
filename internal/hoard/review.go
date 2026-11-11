package hoard

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Steering: a person (or a reviewing agent) judges an asset. Approve and
// reject set the status and leave a note; mark-for-rework also puts a rework
// request into the queue so that a generator agent picks it up.

// ReviewResult is what a review action changed.
type ReviewResult struct {
	Asset   *Asset   `json:"asset"`
	Note    *Note    `json:"note,omitempty"`
	Request *Request `json:"request,omitempty"` // rework: the request a generator will work on
	Reused  bool     `json:"reused_request,omitempty"`
}

// ReviewAsset applies a verdict to an asset: "approve", "reject" or "rework".
// feedback is optional for approve and required for reject and rework.
// priority and projectID only matter for rework (projectID defaults to the
// asset's only project).
func (s *Service) ReviewAsset(ctx context.Context, assetID, verdict, feedback string, priority int, projectID string) (*ReviewResult, error) {
	switch strings.ToLower(strings.TrimSpace(verdict)) {
	case "approve", "approved":
		return s.setVerdict(ctx, assetID, "approved", "approval", feedback, false)
	case "reject", "rejected":
		return s.setVerdict(ctx, assetID, "rejected", "rejection", feedback, true)
	case "rework", "needs_rework", "needs-rework":
		return s.MarkForRework(ctx, assetID, feedback, priority, projectID)
	}
	return nil, invalid("unknown verdict %q (valid: approve, reject, rework)", verdict)
}

// ApproveAsset sets the status to approved. An optional note is kept.
func (s *Service) ApproveAsset(ctx context.Context, assetID, note string) (*ReviewResult, error) {
	return s.setVerdict(ctx, assetID, "approved", "approval", note, false)
}

// RejectAsset sets the status to rejected and records why. The reason is
// required.
func (s *Service) RejectAsset(ctx context.Context, assetID, reason string) (*ReviewResult, error) {
	return s.setVerdict(ctx, assetID, "rejected", "rejection", reason, true)
}

func (s *Service) setVerdict(ctx context.Context, assetID, status, noteKind, text string, required bool) (*ReviewResult, error) {
	text, err := cleanNoteBody(text, required)
	if err != nil {
		return nil, err
	}
	res := &ReviewResult{}
	err = s.tx(ctx, func(tx *sql.Tx) error {
		if err := reviewable(ctx, tx, assetID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE assets SET status = ?, updated_at = ? WHERE id = ?`, status, now(), assetID); err != nil {
			return err
		}
		if text != "" {
			if res.Note, err = insertNote(ctx, tx, assetID, noteKind, text, ActorFrom(ctx).String()); err != nil {
				return err
			}
		}
		s.record(ctx, tx, "asset.review", assetID, "", status+": "+text)
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.syncSidecar(ctx, assetID)
	res.Asset, err = s.GetAsset(ctx, assetID)
	return res, err
}

// reviewable checks that the asset exists and is not in the trash.
func reviewable(ctx context.Context, tx *sql.Tx, assetID string) error {
	var deleted sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT deleted_at FROM assets WHERE id = ?`, assetID).Scan(&deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("asset %q: %w", assetID, ErrNotFound)
	}
	if err != nil {
		return err
	}
	if deleted.Valid {
		return invalid("asset %s is in the trash", assetID)
	}
	return nil
}

// reworkSpecKeys are the technical attributes copied from an asset into the
// spec of its rework request.
var reworkSpecKeys = []string{"frame_width", "frame_height", "frames", "columns", "fps", "loop", "animations",
	"duration_s", "sample_rate", "channels", "bit_depth", "bpm"}

// reworkSpec describes the asset to redo, so that a generator can work from
// the request alone: type, size, duration, prompt and recipe.
func reworkSpec(a *Asset) map[string]any {
	spec := map[string]any{"type": a.Type, "rework_of": a.ID}
	if p := a.Primary(); p != nil {
		spec["filename"] = p.Filename
		if p.Width > 0 {
			spec["width"], spec["height"] = p.Width, p.Height
		}
	}
	for _, k := range reworkSpecKeys {
		if v, ok := a.Attrs[k]; ok {
			spec[k] = v
		}
	}
	if a.Prompt != "" {
		spec["prompt"] = wkTruncate(a.Prompt, 4000)
	}
	if len(a.Generator) > 0 {
		if b, err := json.Marshal(a.Generator); err == nil && len(b) <= 6<<10 {
			spec["generator"] = a.Generator
		}
	}
	return spec
}

func wkTruncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// MarkForRework flags an asset as needing another pass. The feedback becomes
// a rework note on the asset, the status becomes rework, and a rework request
// is put in the queue: title and description derive from the asset and the
// feedback, the spec is copied from the asset (type, size, duration, prompt,
// generator recipe). If the asset already has an unfinished rework request,
// that one is reused (its description follows the newest feedback, its
// priority is raised if needed) instead of creating a second.
func (s *Service) MarkForRework(ctx context.Context, assetID, feedback string, priority int, projectID string) (*ReviewResult, error) {
	feedback, err := cleanNoteBody(feedback, true)
	if err != nil {
		return nil, invalid("say what should change: feedback must not be empty")
	}
	a, err := s.GetAsset(ctx, assetID)
	if err != nil {
		return nil, err
	}
	if projectID == "" && len(a.Projects) == 1 {
		projectID = a.Projects[0].ID
	}
	res := &ReviewResult{}
	var reqID string
	err = s.tx(ctx, func(tx *sql.Tx) error {
		if err := reviewable(ctx, tx, assetID); err != nil {
			return err
		}
		ts := now()
		if _, err := tx.ExecContext(ctx, `UPDATE assets SET status = 'rework', updated_at = ? WHERE id = ?`, ts, assetID); err != nil {
			return err
		}
		var err error
		if res.Note, err = insertNote(ctx, tx, assetID, "rework", feedback, ActorFrom(ctx).String()); err != nil {
			return err
		}
		var curPriority int
		err = tx.QueryRowContext(ctx, `SELECT id, priority FROM asset_requests
			WHERE target_asset_id = ? AND kind = 'rework' AND status IN ('open', 'claimed', 'submitted')
			ORDER BY created_at LIMIT 1`, assetID).Scan(&reqID, &curPriority)
		switch {
		case err == nil:
			res.Reused = true
			if _, err := tx.ExecContext(ctx, `UPDATE asset_requests SET description = ?, priority = ?, updated_at = ? WHERE id = ?`,
				feedback, wkClampPriority(max(priority, curPriority)), ts, reqID); err != nil {
				return err
			}
		case errors.Is(err, sql.ErrNoRows):
			reqID, err = s.insertRequest(ctx, tx, RequestInput{
				ProjectID: projectID, Title: "Rework: " + wkTruncate(a.Name, 200), Description: feedback,
				AssetType: a.Type, Spec: reworkSpec(a), Priority: priority,
			}, "rework", assetID)
			if err != nil {
				return err
			}
		default:
			return err
		}
		s.record(ctx, tx, "asset.review", assetID, reqID, "rework: "+feedback)
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.syncSidecar(ctx, assetID)
	if res.Asset, err = s.GetAsset(ctx, assetID); err != nil {
		return nil, err
	}
	res.Request, err = s.GetRequest(ctx, reqID)
	return res, err
}
