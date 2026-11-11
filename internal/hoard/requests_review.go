package hoard

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ---- submitting ----

// SubmitRequest offers existing assets as the result of a request. The
// request moves to "submitted" and the claim ends. The assets must exist, not
// be in the trash, not be superseded or placeholders, not be the asset the
// request replaces, and not already wait for review in another request.
// Only assets without a review status become drafts; rework, rejected or
// approved stay as they are. Submitting is allowed for the claimer and for
// unclaimed requests; a request someone else holds a live lease on is refused.
func (s *Service) SubmitRequest(ctx context.Context, id string, assetIDs []string, note string) (*Request, error) {
	if len(assetIDs) == 0 {
		return nil, invalid("submit at least one asset id")
	}
	if len(assetIDs) > maxSubmitAssets {
		return nil, invalid("at most %d candidates per submission", maxSubmitAssets)
	}
	note, err := cleanNoteBody(note, false)
	if err != nil {
		return nil, err
	}
	actor := claimerOf(ctx)
	seen := map[string]bool{}
	err = s.writeTx(ctx, func(tx *sql.Tx) error {
		r, err := loadRequestTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := requestFinished(r); err != nil {
			return err
		}
		if r.Status == "claimed" && r.ClaimedBy != actor {
			return fmt.Errorf("request %s is claimed by %s: %w", id, r.ClaimedBy, ErrConflict)
		}
		var have int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM request_submissions WHERE request_id = ?`, id).Scan(&have); err != nil {
			return err
		}
		ts := now()
		for _, aid := range assetIDs {
			if seen[aid] {
				continue
			}
			seen[aid] = true
			if !ValidAssetID(aid) {
				return invalid("%q is not an asset id", aid)
			}
			if aid == r.TargetAssetID || aid == r.PlaceholderAssetID {
				return invalid("asset %s is the asset this request replaces; submit the new candidate instead", aid)
			}
			var deleted sql.NullString
			var status string
			if err := tx.QueryRowContext(ctx, `SELECT deleted_at, status FROM assets WHERE id = ?`, aid).Scan(&deleted, &status); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return fmt.Errorf("asset %q: %w", aid, ErrNotFound)
				}
				return err
			}
			if deleted.Valid {
				return invalid("asset %s is in the trash; restore it or submit another", aid)
			}
			if status == "superseded" || status == "placeholder" {
				return invalid("asset %s is %s and cannot be a candidate", aid, status)
			}
			var other string
			err := tx.QueryRowContext(ctx, `SELECT s.request_id FROM request_submissions s JOIN asset_requests r ON r.id = s.request_id
				WHERE s.asset_id = ? AND s.verdict = '' AND s.request_id <> ? AND r.status IN ('open', 'claimed', 'submitted') LIMIT 1`, aid, id).Scan(&other)
			if err == nil {
				return fmt.Errorf("asset %s already waits for review in request %s: %w", aid, other, ErrConflict)
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			var exists int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM request_submissions WHERE request_id = ? AND asset_id = ?`, id, aid).Scan(&exists); err != nil {
				return err
			}
			if exists == 0 {
				have++
				if have > maxSubmissionsPerRequest {
					return capErr("candidates for one request", maxSubmissionsPerRequest)
				}
			}
			if _, err := tx.ExecContext(ctx, `UPDATE assets SET status = CASE WHEN status = '' THEN 'draft' ELSE status END, updated_at = ? WHERE id = ?`, ts, aid); err != nil {
				return err
			}
			if r.Kind == "rework" && r.TargetAssetID != "" {
				if _, err := tx.ExecContext(ctx, `UPDATE assets SET derived_from = ? WHERE id = ? AND derived_from = ''`, r.TargetAssetID, aid); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO request_submissions(request_id, asset_id, note, verdict, submitted_by, submitted_key, submitted_at)
				VALUES(?, ?, ?, '', ?, ?, ?) ON CONFLICT(request_id, asset_id) DO UPDATE SET
				note = excluded.note, verdict = '', submitted_by = excluded.submitted_by, submitted_key = excluded.submitted_key, submitted_at = excluded.submitted_at`,
				id, aid, note, actor, identityKey(ctx), ts); err != nil {
				return err
			}
			s.record(ctx, tx, "request.submit", aid, id, r.Title)
		}
		_, err = tx.ExecContext(ctx, `UPDATE asset_requests SET status = 'submitted', claimed_by = '', claim_expires_at = NULL, updated_at = ? WHERE id = ?`, ts, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	for aid := range seen {
		s.syncSidecar(ctx, aid)
	}
	return s.GetRequest(ctx, id)
}

// ---- reviewing submissions ----

// pendingSubmission loads a request for review: it must be "submitted" and
// the candidate must still wait for a verdict. A stale second review gets
// ErrConflict instead of overwriting what happened since.
func pendingSubmission(ctx context.Context, tx *sql.Tx, requestID, assetID string) (*Request, bool, error) {
	r, err := loadRequestTx(ctx, tx, requestID)
	if err != nil {
		return nil, false, err
	}
	if err := requestFinished(r); err != nil {
		return nil, false, err
	}
	if r.Status != "submitted" {
		return nil, false, fmt.Errorf("request %s is %s, not waiting for review: %w", requestID, r.Status, ErrConflict)
	}
	var verdict, by string
	var deleted sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT s.verdict, s.submitted_key, a.deleted_at FROM request_submissions s JOIN assets a ON a.id = s.asset_id
		WHERE s.request_id = ? AND s.asset_id = ?`, requestID, assetID).Scan(&verdict, &by, &deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, invalid("asset %s was not submitted for request %s", assetID, requestID)
	}
	if err != nil {
		return nil, false, err
	}
	if verdict != "" {
		return nil, false, fmt.Errorf("asset %s was already %s for request %s: %w", assetID, verdict, requestID, ErrConflict)
	}
	if err := refuseSelfReview(ctx, by); err != nil {
		return nil, false, err
	}
	return r, deleted.Valid, nil
}

// AcceptSubmission picks a candidate as the result of a request. The request
// must be waiting for review and the candidate pending; the acting agent must
// not be the one that submitted it (browser sessions always may).
//
// The candidate becomes approved (its own superseded_by is cleared), the
// request done. The other candidates still waiting are marked "not chosen"
// (verdict rejected, no note); their assets are left untouched.
// With supersede, the asset the request replaces (the rework target, or the
// placeholder), resolved to the head of its superseded_by chain, becomes
// superseded with superseded_by pointing at the candidate, its project usage
// (with notes) moves to the candidate, its collection memberships are copied
// (the old asset keeps them), and open rework notes on it are resolved. It is
// refused if that chain leads to the candidate. Without supersede the old
// asset is left as it is. The request's own project always gets the candidate.
func (s *Service) AcceptSubmission(ctx context.Context, requestID, assetID string, supersede bool) (*Request, error) {
	var old string
	err := s.writeTx(ctx, func(tx *sql.Tx) error {
		r, trashed, err := pendingSubmission(ctx, tx, requestID, assetID)
		if err != nil {
			return err
		}
		if trashed {
			return invalid("asset %s is in the trash; restore it first", assetID)
		}
		old = r.TargetAssetID
		if old == "" {
			old = r.PlaceholderAssetID
		}
		if old == assetID || !supersede {
			old = ""
		}
		if old != "" {
			head, chain, err := supersedeHead(ctx, tx, old)
			if err != nil {
				return err
			}
			for _, c := range chain {
				if c == assetID {
					return invalid("asset %s already is part of the replacement chain of %s; accepting it would create a loop", assetID, old)
				}
			}
			old = head
		}
		ts := now()
		if _, err := tx.ExecContext(ctx, `UPDATE request_submissions SET verdict = 'accepted' WHERE request_id = ? AND asset_id = ?`, requestID, assetID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE request_submissions SET verdict = 'rejected' WHERE request_id = ? AND asset_id <> ? AND verdict = ''`, requestID, assetID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE assets SET status = 'approved', superseded_by = '', updated_at = ? WHERE id = ?`, ts, assetID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE asset_requests SET status = 'done', accepted_asset_id = ?, claimed_by = '', claim_expires_at = NULL,
			updated_at = ?, closed_at = ? WHERE id = ?`, assetID, ts, ts, requestID); err != nil {
			return err
		}
		if old != "" {
			if err := supersedeAsset(ctx, tx, old, assetID, ts); err != nil {
				return err
			}
		}
		if r.ProjectID != "" {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO project_assets(project_id, asset_id, note, added_at) VALUES(?, ?, '', ?)`,
				r.ProjectID, assetID, ts); err != nil {
				return err
			}
		}
		s.record(ctx, tx, "request.accept", assetID, requestID, r.Title)
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.syncSidecar(ctx, assetID)
	if old != "" {
		s.syncSidecar(ctx, old)
	}
	return s.GetRequest(ctx, requestID)
}

// supersedeAsset marks old as replaced by repl and moves its usage over.
func supersedeAsset(ctx context.Context, tx *sql.Tx, old, repl, ts string) error {
	for _, q := range []string{
		`UPDATE assets SET status = 'superseded', superseded_by = ?1, updated_at = ?3 WHERE id = ?2`,
		// Project usage moves, notes included: reports and repository sync must see the new asset.
		`INSERT INTO project_assets(project_id, asset_id, note, added_at)
			SELECT project_id, ?1, note, added_at FROM project_assets WHERE asset_id = ?2
			ON CONFLICT(project_id, asset_id) DO UPDATE SET note = CASE WHEN excluded.note = '' THEN note ELSE excluded.note END`,
		`DELETE FROM project_assets WHERE asset_id = ?2`,
		// Collections are copied; the superseded asset stays in them as history.
		`INSERT OR IGNORE INTO collection_assets(collection_id, asset_id, added_at)
			SELECT collection_id, ?1, ?3 FROM collection_assets WHERE asset_id = ?2`,
		// The feedback has been dealt with.
		`UPDATE asset_notes SET resolved_at = ?3 WHERE asset_id = ?2 AND kind = 'rework' AND resolved_at IS NULL`,
	} {
		if _, err := tx.ExecContext(ctx, q, repl, old, ts); err != nil {
			return err
		}
	}
	return nil
}

// RejectSubmission turns a pending candidate down (same preconditions as
// AcceptSubmission). The feedback is stored as a rework note on the candidate
// and shown to the next generator that claims the request. The candidate
// becomes rejected. The request goes back to open (claim cleared) unless other
// candidates are still waiting for review.
func (s *Service) RejectSubmission(ctx context.Context, requestID, assetID, feedback string) (*Request, error) {
	feedback, err := cleanNoteBody(feedback, true)
	if err != nil {
		return nil, invalid("say what is wrong: feedback must not be empty")
	}
	err = s.writeTx(ctx, func(tx *sql.Tx) error {
		r, _, err := pendingSubmission(ctx, tx, requestID, assetID)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE request_submissions SET verdict = 'rejected' WHERE request_id = ? AND asset_id = ?`, requestID, assetID); err != nil {
			return err
		}
		ts := now()
		if _, err := tx.ExecContext(ctx, `UPDATE assets SET status = 'rejected', updated_at = ? WHERE id = ?`, ts, assetID); err != nil {
			return err
		}
		if _, err := insertNote(ctx, tx, assetID, "rework", feedback, ActorFrom(ctx).String()); err != nil {
			return err
		}
		var pending int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM request_submissions WHERE request_id = ? AND verdict = ''`, requestID).Scan(&pending); err != nil {
			return err
		}
		if pending == 0 {
			if _, err := tx.ExecContext(ctx, `UPDATE asset_requests SET status = 'open', claimed_by = '', claim_expires_at = NULL, updated_at = ?
				WHERE id = ? AND status = 'submitted'`, ts, requestID); err != nil {
				return err
			}
		}
		s.record(ctx, tx, "request.reject", assetID, requestID, r.Title+": "+feedback)
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.syncSidecar(ctx, assetID)
	return s.GetRequest(ctx, requestID)
}
