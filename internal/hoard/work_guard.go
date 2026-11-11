package hoard

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Caps that keep the queue and the notes bounded.
const (
	maxActiveRequestsPerProject = 2000
	maxNotesPerAsset            = 500
	maxSubmissionsPerRequest    = 50
)

type agentKey struct{}

// WithAgent labels the agent behind a request. Agents that share one API
// token are told apart by it: claimer and submitter become
// "<actor> / <label>".
func WithAgent(ctx context.Context, label string) context.Context {
	label = strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.ToValidUTF8(label, "")))
	if r := []rune(label); len(r) > 60 {
		label = string(r[:60])
	}
	if label == "" {
		return ctx
	}
	return context.WithValue(ctx, agentKey{}, label)
}

// claimerOf is the identity recorded for claims and submissions.
func claimerOf(ctx context.Context) string {
	a := ActorFrom(ctx).String()
	if l, ok := ctx.Value(agentKey{}).(string); ok && l != "" {
		return a + " / " + l
	}
	return a
}

// errSelfReview is returned when an agent judges what it submitted itself.
var errSelfReview = invalid("a submission cannot be reviewed by the agent that made it; ask the user")

// refuseSelfReview refuses a review by the identity that submitted the work.
// Browser sessions are the user and always allowed; a different agent (for
// example another token) may review.
func refuseSelfReview(ctx context.Context, submittedBy string) error {
	if ActorFrom(ctx).Via == "web" {
		return nil
	}
	if submittedBy != "" && submittedBy == claimerOf(ctx) {
		return errSelfReview
	}
	return nil
}

// supersedeHead follows superseded_by from id to the last asset of the chain.
// It reports an error for a cycle or an absurdly long chain.
func supersedeHead(ctx context.Context, q queryer, id string) (string, []string, error) {
	seen := []string{id}
	cur := id
	for range 64 {
		var next string
		err := q.QueryRowContext(ctx, `SELECT superseded_by FROM assets WHERE id = ?`, cur).Scan(&next)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && next == "") {
			return cur, seen, nil
		}
		if err != nil {
			return "", nil, err
		}
		for _, s := range seen {
			if s == next {
				return "", nil, invalid("the superseded_by chain of %s loops back on itself", id)
			}
		}
		seen = append(seen, next)
		cur = next
	}
	return "", nil, invalid("the superseded_by chain of %s is too long", id)
}

// reviewGuard refuses reviews that make no sense for the asset's state.
func reviewGuard(ctx context.Context, tx *sql.Tx, assetID, verdict string) error {
	var status, by string
	if err := tx.QueryRowContext(ctx, `SELECT status, superseded_by FROM assets WHERE id = ?`, assetID).Scan(&status, &by); err != nil {
		return err
	}
	if status == "superseded" {
		head := by
		if by != "" {
			if h, _, err := supersedeHead(ctx, tx, by); err == nil {
				head = h
			}
		}
		if head == "" {
			return invalid("asset %s is superseded and cannot be reviewed", assetID)
		}
		return invalid("asset %s is superseded; review its replacement %s instead", assetID, head)
	}
	if status == "placeholder" && (verdict == "approved" || verdict == "rework") {
		return invalid("placeholders are replaced by accepting a submission for their request; they cannot be approved or sent for rework")
	}
	// An agent may not approve or reject the work it submitted and that is still under review.
	if verdict == "approved" || verdict == "rejected" {
		if ActorFrom(ctx).Via != "web" {
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM request_submissions s JOIN asset_requests r ON r.id = s.request_id
				WHERE s.asset_id = ? AND s.verdict = '' AND s.submitted_by = ? AND r.status = 'submitted'`, assetID, claimerOf(ctx)).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				return errSelfReview
			}
		}
	}
	return nil
}

func capErr(what string, max int) error {
	return invalid("too many %s (limit %d)", what, max)
}

func countErr(err error) error { return fmt.Errorf("count: %w", err) }
