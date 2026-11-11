package hoard

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// The asset request queue. A request says "this game needs an asset like
// this" (kind new) or "this asset has to be redone" (kind rework). Generator
// agents claim requests for a limited time, produce candidates, create them
// as assets and submit them; a person (or a reviewing agent) accepts one.
//
// Lifecycle: open -> claimed (lease) -> submitted -> done | cancelled.
// A claim that is not renewed expires and the request is open again; this
// happens lazily whenever requests are read or claimed, there is no timer.

const (
	maxSpecBytes      = 16 << 10
	maxSubmitAssets   = 20
	defaultLeaseMin   = 60
	maxLeaseMin       = 24 * 60
	maxRequestTitle   = 300
	maxRequestDesc    = 20000
	maxRequestListLen = 500
)

// Request is one entry of the asset request queue. Title, description, spec
// and feedback are written by people and agents: untrusted everywhere.
type Request struct {
	ID                 string         `json:"id"`
	ProjectID          string         `json:"project_id,omitempty"`
	ProjectName        string         `json:"project_name,omitempty"`
	Kind               string         `json:"kind"` // new | rework
	Title              string         `json:"title"`
	Description        string         `json:"description"`
	AssetType          string         `json:"asset_type"`
	Spec               map[string]any `json:"spec"`
	TargetAssetID      string         `json:"target_asset_id,omitempty"` // rework: the asset to redo
	TargetName         string         `json:"target_name,omitempty"`
	PlaceholderAssetID string         `json:"placeholder_asset_id,omitempty"`
	AcceptedAssetID    string         `json:"accepted_asset_id,omitempty"`
	Status             string         `json:"status"` // see RequestStatuses
	Priority           int            `json:"priority"`
	ClaimedBy          string         `json:"claimed_by,omitempty"`
	ClaimExpiresAt     string         `json:"claim_expires_at,omitempty"`
	CreatedBy          string         `json:"created_by"`
	CreatedAt          string         `json:"created_at"`
	UpdatedAt          string         `json:"updated_at"`
	ClosedAt           string         `json:"closed_at,omitempty"`

	Submissions []Submission `json:"submissions,omitempty"`
	// Notes holds the open (unresolved) notes of the target asset of a rework
	// request, oldest first. Only GetRequest fills it.
	Notes []Note `json:"notes,omitempty"`
}

// Submission is a candidate asset offered for a request.
type Submission struct {
	RequestID   string `json:"request_id"`
	AssetID     string `json:"asset_id"`
	Note        string `json:"note,omitempty"`    // what the submitter said
	Verdict     string `json:"verdict,omitempty"` // "" (waiting for review) | accepted | rejected
	SubmittedBy string `json:"submitted_by"`
	SubmittedAt string `json:"submitted_at"`
	// Feedback is the reviewer's reason when the candidate was rejected.
	Feedback     string `json:"feedback,omitempty"`
	AssetName    string `json:"asset_name,omitempty"`
	AssetType    string `json:"asset_type,omitempty"`
	AssetStatus  string `json:"asset_status,omitempty"`
	PreviewKind  string `json:"preview_kind,omitempty"`
	PrimaryFile  string `json:"primary_file,omitempty"` // file name of the main file, for previews
	ThumbVersion int    `json:"thumb_version,omitempty"`
	Trashed      bool   `json:"trashed,omitempty"`
}

// RequestInput creates a request.
type RequestInput struct {
	ProjectID   string         `json:"project_id,omitempty"`
	Title       string         `json:"title"`
	Description string         `json:"description,omitempty"`
	AssetType   string         `json:"asset_type,omitempty"`
	Spec        map[string]any `json:"spec,omitempty"`
	Priority    int            `json:"priority,omitempty"`
	Placeholder bool           `json:"placeholder,omitempty"` // also generate a placeholder asset (see MakePlaceholder)
}

// RequestPatch changes an open request. Nil fields stay as they are.
type RequestPatch struct {
	ProjectID   *string        `json:"project_id,omitempty"` // "" detaches the project
	Title       *string        `json:"title,omitempty"`
	Description *string        `json:"description,omitempty"`
	AssetType   *string        `json:"asset_type,omitempty"`
	Spec        map[string]any `json:"spec,omitempty"` // replaces the spec
	Priority    *int           `json:"priority,omitempty"`
}

// RequestQuery filters the queue.
type RequestQuery struct {
	Statuses    []string // any of RequestStatuses
	Project     string   // project id, or "none"
	Kind        string   // new | rework
	AssetTypes  []string // any of
	ClaimedByMe bool     // only requests claimed by the acting identity
	Target      string   // rework requests for this asset
	Sort        string   // queue (default: priority, then oldest) | recent (last change first)
	Limit       int      // default 100, max 500
}

const requestCols = `r.id, COALESCE(r.project_id, ''), COALESCE(p.name, ''), r.kind, r.title, r.description, r.asset_type, r.spec,
	COALESCE(r.target_asset_id, ''), COALESCE(t.name, ''), COALESCE(r.placeholder_asset_id, ''), COALESCE(r.accepted_asset_id, ''),
	r.status, r.priority, r.claimed_by, r.claim_expires_at, r.created_by, r.created_at, r.updated_at, r.closed_at`

const requestFrom = ` FROM asset_requests r LEFT JOIN projects p ON p.id = r.project_id LEFT JOIN assets t ON t.id = r.target_asset_id`

func scanRequest(row scanner) (*Request, error) {
	var r Request
	var spec string
	var exp, closed sql.NullString
	if err := row.Scan(&r.ID, &r.ProjectID, &r.ProjectName, &r.Kind, &r.Title, &r.Description, &r.AssetType, &spec,
		&r.TargetAssetID, &r.TargetName, &r.PlaceholderAssetID, &r.AcceptedAssetID,
		&r.Status, &r.Priority, &r.ClaimedBy, &exp, &r.CreatedBy, &r.CreatedAt, &r.UpdatedAt, &closed); err != nil {
		return nil, err
	}
	r.ClaimExpiresAt, r.ClosedAt = exp.String, closed.String
	r.Spec = map[string]any{}
	_ = json.Unmarshal([]byte(spec), &r.Spec)
	return &r, nil
}

func cleanSpec(spec map[string]any) (string, error) {
	if spec == nil {
		spec = map[string]any{}
	}
	b, err := json.Marshal(spec)
	if err != nil {
		return "", invalid("spec: %v", err)
	}
	if len(b) > maxSpecBytes {
		return "", invalid("spec is too large (max %d KiB)", maxSpecBytes>>10)
	}
	return string(b), nil
}

func wkClampPriority(p int) int { return max(-100, min(100, p)) }

// expireClaims returns requests whose lease ran out to the queue.
func (s *Service) expireClaims(ctx context.Context) {
	ts := now()
	if _, err := s.db.ExecContext(ctx, `UPDATE asset_requests SET status = 'open', claimed_by = '', claim_expires_at = NULL, updated_at = ?
		WHERE status = 'claimed' AND claim_expires_at < ?`, ts, ts); err != nil {
		s.log.Warn("expire claims", "err", err)
	}
}

// insertRequest validates and stores a new request inside tx.
func (s *Service) insertRequest(ctx context.Context, tx *sql.Tx, in RequestInput, kind, target string) (string, error) {
	title, err := limitLen("title", in.Title, maxRequestTitle)
	if err != nil {
		return "", err
	}
	if title == "" {
		return "", invalid("a request needs a title")
	}
	desc, err := limitLen("description", in.Description, maxRequestDesc)
	if err != nil {
		return "", err
	}
	typ := strings.ToLower(strings.TrimSpace(in.AssetType))
	if typ != "" && !ValidType(typ) {
		return "", invalid("unknown asset type %q (valid: %s)", in.AssetType, strings.Join(TypeIDs(), ", "))
	}
	spec, err := cleanSpec(in.Spec)
	if err != nil {
		return "", err
	}
	var project any
	if in.ProjectID != "" {
		if err := mustExist(ctx, tx, "projects", in.ProjectID); err != nil {
			return "", err
		}
		project = in.ProjectID
	}
	var tgt any
	if target != "" {
		tgt = target
	}
	id := newID("req")
	ts := now()
	if _, err := tx.ExecContext(ctx, `INSERT INTO asset_requests(id, project_id, kind, title, description, asset_type, spec, target_asset_id,
		status, priority, created_by, created_at, updated_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?, 'open', ?, ?, ?, ?)`,
		id, project, kind, title, desc, typ, spec, tgt, wkClampPriority(in.Priority), ActorFrom(ctx).String(), ts, ts); err != nil {
		return "", err
	}
	assetID := target
	s.record(ctx, tx, "request.create", assetID, id, kind+": "+title)
	return id, nil
}

// CreateRequest adds a request to the queue. With in.Placeholder a
// placeholder asset is generated right away (only for image and audio types).
func (s *Service) CreateRequest(ctx context.Context, in RequestInput) (*Request, error) {
	if in.Placeholder {
		if err := placeholderSupported(in.AssetType); err != nil {
			return nil, err
		}
	}
	var id string
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var err error
		id, err = s.insertRequest(ctx, tx, in, "new", "")
		return err
	})
	if err != nil {
		return nil, err
	}
	if in.Placeholder {
		if _, err := s.MakePlaceholder(ctx, id); err != nil {
			return nil, fmt.Errorf("request %s was created but its placeholder failed: %w", id, err)
		}
	}
	return s.GetRequest(ctx, id)
}

// GetRequest returns one request with its submissions and, for rework, the
// open notes of the target asset.
func (s *Service) GetRequest(ctx context.Context, id string) (*Request, error) {
	s.expireClaims(ctx)
	r, err := scanRequest(s.db.QueryRowContext(ctx, `SELECT `+requestCols+requestFrom+` WHERE r.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("request %q: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	if err := s.loadSubmissions(ctx, []*Request{r}); err != nil {
		return nil, err
	}
	if r.TargetAssetID != "" {
		if r.Notes, err = s.ListNotes(ctx, r.TargetAssetID, false); err != nil {
			return nil, err
		}
	}
	return r, nil
}

func (s *Service) loadSubmissions(ctx context.Context, reqs []*Request) error {
	if len(reqs) == 0 {
		return nil
	}
	byID := make(map[string]*Request, len(reqs))
	ids := make([]string, len(reqs))
	for i, r := range reqs {
		byID[r.ID] = r
		ids[i] = r.ID
	}
	rows, err := s.db.QueryContext(ctx, `SELECT s.request_id, s.asset_id, s.note, s.verdict, s.submitted_by, s.submitted_at,
			COALESCE((SELECT n.body FROM asset_notes n WHERE n.asset_id = s.asset_id AND n.kind IN ('rework', 'rejection')
				ORDER BY n.created_at DESC, n.rowid DESC LIMIT 1), ''),
			a.name, a.type, a.status, a.thumb_version, a.deleted_at IS NOT NULL, COALESCE(f.filename, '')
		FROM request_submissions s JOIN assets a ON a.id = s.asset_id LEFT JOIN asset_files f ON f.id = a.primary_file_id
		WHERE s.request_id IN (`+placeholders(len(ids))+`) ORDER BY s.submitted_at, s.asset_id`, anySlice(ids)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var sub Submission
		var filename string
		if err := rows.Scan(&sub.RequestID, &sub.AssetID, &sub.Note, &sub.Verdict, &sub.SubmittedBy, &sub.SubmittedAt, &sub.Feedback,
			&sub.AssetName, &sub.AssetType, &sub.AssetStatus, &sub.ThumbVersion, &sub.Trashed, &filename); err != nil {
			return err
		}
		if filename != "" {
			sub.PrimaryFile = filename
			sub.PreviewKind = PreviewKind(filename)
		}
		if sub.Verdict != "rejected" {
			sub.Feedback = ""
		}
		byID[sub.RequestID].Submissions = append(byID[sub.RequestID].Submissions, sub)
	}
	return rows.Err()
}

func requestWhere(ctx context.Context, q RequestQuery) (string, []any) {
	where := []string{"1 = 1"}
	var args []any
	if len(q.Statuses) > 0 {
		where = append(where, `r.status IN (`+placeholders(len(q.Statuses))+`)`)
		args = append(args, anySlice(q.Statuses)...)
	}
	switch q.Project {
	case "":
	case "none":
		where = append(where, `r.project_id IS NULL`)
	default:
		where = append(where, `r.project_id = ?`)
		args = append(args, q.Project)
	}
	if q.Kind != "" {
		where = append(where, `r.kind = ?`)
		args = append(args, q.Kind)
	}
	if len(q.AssetTypes) > 0 {
		where = append(where, `r.asset_type IN (`+placeholders(len(q.AssetTypes))+`)`)
		args = append(args, anySlice(q.AssetTypes)...)
	}
	if q.ClaimedByMe {
		where = append(where, `r.status = 'claimed' AND r.claimed_by = ?`)
		args = append(args, ActorFrom(ctx).String())
	}
	if q.Target != "" {
		where = append(where, `r.target_asset_id = ?`)
		args = append(args, q.Target)
	}
	return strings.Join(where, " AND "), args
}

func validateRequestQuery(q RequestQuery) error {
	for _, st := range q.Statuses {
		if !slices.Contains(RequestStatuses, st) {
			return invalid("unknown request status %q (valid: %s)", st, strings.Join(RequestStatuses, ", "))
		}
	}
	if q.Kind != "" && q.Kind != "new" && q.Kind != "rework" {
		return invalid("unknown request kind %q (valid: new, rework)", q.Kind)
	}
	for _, t := range q.AssetTypes {
		if !ValidType(t) {
			return invalid("unknown asset type %q (valid: %s)", t, strings.Join(TypeIDs(), ", "))
		}
	}
	return nil
}

// ListRequests returns requests, highest priority and oldest first (or most
// recently changed first with Sort "recent"), with their submissions.
func (s *Service) ListRequests(ctx context.Context, q RequestQuery) ([]*Request, error) {
	if err := validateRequestQuery(q); err != nil {
		return nil, err
	}
	s.expireClaims(ctx)
	if q.Limit <= 0 {
		q.Limit = 100
	}
	q.Limit = min(q.Limit, maxRequestListLen)
	where, args := requestWhere(ctx, q)
	order := `r.priority DESC, r.created_at, r.rowid`
	if q.Sort == "recent" {
		order = `r.updated_at DESC, r.rowid DESC`
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+requestCols+requestFrom+` WHERE `+where+` ORDER BY `+order+` LIMIT ?`, append(args, q.Limit)...)
	if err != nil {
		return nil, err
	}
	out := []*Request{}
	for rows.Next() {
		r, err := scanRequest(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, s.loadSubmissions(ctx, out)
}

// RequestsForAsset lists the requests an asset takes part in: as rework
// target, placeholder, accepted result or candidate. Newest first.
func (s *Service) RequestsForAsset(ctx context.Context, assetID string) ([]*Request, error) {
	if !ValidAssetID(assetID) {
		return nil, fmt.Errorf("asset %q: %w", assetID, ErrNotFound)
	}
	s.expireClaims(ctx)
	rows, err := s.db.QueryContext(ctx, `SELECT `+requestCols+requestFrom+`
		WHERE r.target_asset_id = ?1 OR r.placeholder_asset_id = ?1 OR r.accepted_asset_id = ?1
			OR r.id IN (SELECT request_id FROM request_submissions WHERE asset_id = ?1)
		ORDER BY r.updated_at DESC, r.rowid DESC LIMIT 50`, assetID)
	if err != nil {
		return nil, err
	}
	out := []*Request{}
	for rows.Next() {
		r, err := scanRequest(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, s.loadSubmissions(ctx, out)
}

// loadOpenRequest reads a request inside tx and refuses finished ones.
func loadRequestTx(ctx context.Context, tx *sql.Tx, id string) (*Request, error) {
	r, err := scanRequest(tx.QueryRowContext(ctx, `SELECT `+requestCols+requestFrom+` WHERE r.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("request %q: %w", id, ErrNotFound)
	}
	return r, err
}

func requestFinished(r *Request) error {
	if r.Status == "done" || r.Status == "cancelled" {
		return fmt.Errorf("request %s is already %s: %w", r.ID, r.Status, ErrConflict)
	}
	return nil
}

// UpdateRequest changes title, description, type, spec, priority or project
// of a request that is not finished.
func (s *Service) UpdateRequest(ctx context.Context, id string, p RequestPatch) (*Request, error) {
	err := s.tx(ctx, func(tx *sql.Tx) error {
		r, err := loadRequestTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := requestFinished(r); err != nil {
			return err
		}
		var sets []string
		var args []any
		set := func(col string, v any) {
			sets = append(sets, col+" = ?")
			args = append(args, v)
		}
		if p.Title != nil {
			v, err := limitLen("title", *p.Title, maxRequestTitle)
			if err != nil {
				return err
			}
			if v == "" {
				return invalid("a request needs a title")
			}
			set("title", v)
		}
		if p.Description != nil {
			v, err := limitLen("description", *p.Description, maxRequestDesc)
			if err != nil {
				return err
			}
			set("description", v)
		}
		if p.AssetType != nil {
			t := strings.ToLower(strings.TrimSpace(*p.AssetType))
			if t != "" && !ValidType(t) {
				return invalid("unknown asset type %q (valid: %s)", *p.AssetType, strings.Join(TypeIDs(), ", "))
			}
			set("asset_type", t)
		}
		if p.Spec != nil {
			v, err := cleanSpec(p.Spec)
			if err != nil {
				return err
			}
			set("spec", v)
		}
		if p.Priority != nil {
			set("priority", wkClampPriority(*p.Priority))
		}
		if p.ProjectID != nil {
			if *p.ProjectID == "" {
				set("project_id", nil)
			} else {
				if err := mustExist(ctx, tx, "projects", *p.ProjectID); err != nil {
					return err
				}
				set("project_id", *p.ProjectID)
			}
		}
		if len(sets) == 0 {
			return nil
		}
		set("updated_at", now())
		if _, err := tx.ExecContext(ctx, `UPDATE asset_requests SET `+strings.Join(sets, ", ")+` WHERE id = ?`, append(args, id)...); err != nil {
			return err
		}
		s.record(ctx, tx, "request.update", r.TargetAssetID, id, r.Title)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.GetRequest(ctx, id)
}

// CancelRequest withdraws a request. Candidates already submitted are kept
// as ordinary draft assets.
func (s *Service) CancelRequest(ctx context.Context, id string) (*Request, error) {
	err := s.tx(ctx, func(tx *sql.Tx) error {
		r, err := loadRequestTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := requestFinished(r); err != nil {
			return err
		}
		ts := now()
		if _, err := tx.ExecContext(ctx, `UPDATE asset_requests SET status = 'cancelled', claimed_by = '', claim_expires_at = NULL,
			updated_at = ?, closed_at = ? WHERE id = ?`, ts, ts, id); err != nil {
			return err
		}
		s.record(ctx, tx, "request.cancel", r.TargetAssetID, id, r.Title)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.GetRequest(ctx, id)
}

// ---- claiming ----

func leaseFor(minutes int) (time.Duration, string) {
	if minutes <= 0 {
		minutes = defaultLeaseMin
	}
	minutes = min(minutes, maxLeaseMin)
	d := time.Duration(minutes) * time.Minute
	return d, time.Now().UTC().Add(d).Format(time.RFC3339)
}

// ClaimRequest takes one request for the acting identity for leaseMinutes
// (default 60, at most 24 h). It succeeds only if the request is open or its
// previous lease has expired; the check and the update are one statement, so
// of two concurrent claims exactly one wins and the other gets ErrConflict.
func (s *Service) ClaimRequest(ctx context.Context, id string, leaseMinutes int) (*Request, error) {
	_, exp := leaseFor(leaseMinutes)
	actor := ActorFrom(ctx).String()
	err := s.tx(ctx, func(tx *sql.Tx) error {
		ts := now()
		res, err := tx.ExecContext(ctx, `UPDATE asset_requests SET status = 'claimed', claimed_by = ?, claim_expires_at = ?, updated_at = ?
			WHERE id = ? AND (status = 'open' OR (status = 'claimed' AND claim_expires_at < ?))`, actor, exp, ts, id, ts)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			var title, target string
			_ = tx.QueryRowContext(ctx, `SELECT title, COALESCE(target_asset_id, '') FROM asset_requests WHERE id = ?`, id).Scan(&title, &target)
			s.record(ctx, tx, "request.claim", target, id, title)
			return nil
		}
		r, err := loadRequestTx(ctx, tx, id)
		if err != nil {
			return err
		}
		switch r.Status {
		case "claimed":
			return fmt.Errorf("request %s is claimed by %s until %s: %w", id, r.ClaimedBy, r.ClaimExpiresAt, ErrConflict)
		default:
			return fmt.Errorf("request %s is %s and cannot be claimed: %w", id, r.Status, ErrConflict)
		}
	})
	if err != nil {
		return nil, err
	}
	return s.GetRequest(ctx, id)
}

// ClaimNext claims the best open request matching the filters (Kind,
// AssetTypes, Project; other fields are ignored): highest priority first,
// then the oldest. It returns nil, nil when nothing matches.
func (s *Service) ClaimNext(ctx context.Context, q RequestQuery, leaseMinutes int) (*Request, error) {
	if err := validateRequestQuery(q); err != nil {
		return nil, err
	}
	_, exp := leaseFor(leaseMinutes)
	actor := ActorFrom(ctx).String()
	filter := RequestQuery{Project: q.Project, Kind: q.Kind, AssetTypes: q.AssetTypes}
	where, args := requestWhere(ctx, filter)
	var id string
	err := s.tx(ctx, func(tx *sql.Tx) error {
		ts := now()
		a := []any{actor, exp, ts}
		a = append(a, args...)
		a = append(a, ts)
		err := tx.QueryRowContext(ctx, `UPDATE asset_requests SET status = 'claimed', claimed_by = ?, claim_expires_at = ?, updated_at = ?
			WHERE id = (SELECT r.id FROM asset_requests r WHERE `+where+`
				AND (r.status = 'open' OR (r.status = 'claimed' AND r.claim_expires_at < ?))
				ORDER BY r.priority DESC, r.created_at, r.rowid LIMIT 1)
			RETURNING id`, a...).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		var title, target string
		_ = tx.QueryRowContext(ctx, `SELECT title, COALESCE(target_asset_id, '') FROM asset_requests WHERE id = ?`, id).Scan(&title, &target)
		s.record(ctx, tx, "request.claim", target, id, title)
		return nil
	})
	if err != nil || id == "" {
		return nil, err
	}
	return s.GetRequest(ctx, id)
}

// RenewClaim extends the lease of a request the acting identity holds.
func (s *Service) RenewClaim(ctx context.Context, id string, leaseMinutes int) (*Request, error) {
	_, exp := leaseFor(leaseMinutes)
	err := s.tx(ctx, func(tx *sql.Tx) error {
		r, err := loadRequestTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if r.Status != "claimed" || r.ClaimedBy != ActorFrom(ctx).String() {
			return fmt.Errorf("request %s is not claimed by you (status %s): %w", id, r.Status, ErrConflict)
		}
		_, err = tx.ExecContext(ctx, `UPDATE asset_requests SET claim_expires_at = ?, updated_at = ? WHERE id = ?`, exp, now(), id)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.GetRequest(ctx, id)
}

// ReleaseClaim gives a claimed request back to the queue. The claimer can do
// it, and so can anyone in the web UI (to free a request whose agent died).
func (s *Service) ReleaseClaim(ctx context.Context, id string) (*Request, error) {
	a := ActorFrom(ctx)
	err := s.tx(ctx, func(tx *sql.Tx) error {
		r, err := loadRequestTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if r.Status != "claimed" {
			return fmt.Errorf("request %s is %s, not claimed: %w", id, r.Status, ErrConflict)
		}
		if r.ClaimedBy != a.String() && a.Via != "web" {
			return fmt.Errorf("request %s is claimed by %s: %w", id, r.ClaimedBy, ErrConflict)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE asset_requests SET status = 'open', claimed_by = '', claim_expires_at = NULL, updated_at = ?
			WHERE id = ?`, now(), id); err != nil {
			return err
		}
		s.record(ctx, tx, "request.release", r.TargetAssetID, id, r.Title)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.GetRequest(ctx, id)
}

// ---- submitting ----

// SubmitRequest offers existing assets as the result of a request. The
// request moves to "submitted" and the claim ends. The assets must exist and
// not be in the trash; those without a review status become drafts, and for
// rework requests derived_from is set to the asset being reworked.
// Submitting is allowed for the claimer and for unclaimed requests; a request
// someone else holds a live lease on is refused.
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
	actor := ActorFrom(ctx).String()
	seen := map[string]bool{}
	err = s.tx(ctx, func(tx *sql.Tx) error {
		r, err := loadRequestTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := requestFinished(r); err != nil {
			return err
		}
		if r.Status == "claimed" && r.ClaimedBy != actor && r.ClaimExpiresAt >= now() {
			return fmt.Errorf("request %s is claimed by %s: %w", id, r.ClaimedBy, ErrConflict)
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
			if err := tx.QueryRowContext(ctx, `SELECT deleted_at FROM assets WHERE id = ?`, aid).Scan(&deleted); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return fmt.Errorf("asset %q: %w", aid, ErrNotFound)
				}
				return err
			}
			if deleted.Valid {
				return invalid("asset %s is in the trash; restore it or submit another", aid)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE assets SET status = CASE WHEN status IN ('', 'rejected', 'rework') THEN 'draft' ELSE status END, updated_at = ? WHERE id = ?`, ts, aid); err != nil {
				return err
			}
			if r.Kind == "rework" && r.TargetAssetID != "" {
				if _, err := tx.ExecContext(ctx, `UPDATE assets SET derived_from = ? WHERE id = ? AND derived_from = ''`, r.TargetAssetID, aid); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO request_submissions(request_id, asset_id, note, verdict, submitted_by, submitted_at)
				VALUES(?, ?, ?, '', ?, ?) ON CONFLICT(request_id, asset_id) DO UPDATE SET
				note = excluded.note, verdict = '', submitted_by = excluded.submitted_by, submitted_at = excluded.submitted_at`,
				id, aid, note, actor, ts); err != nil {
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

// AcceptSubmission picks a candidate as the result of a request.
//
// The candidate becomes approved, the request done. The other candidates
// still waiting are marked "not chosen" (verdict rejected, no note); their
// assets are left untouched so nothing is lost, and can be trashed by hand.
// With supersede, the asset the request replaces (the rework target, or the
// placeholder) becomes superseded with superseded_by pointing at the
// candidate, its project usage (with notes) moves to the candidate, its
// collection memberships are copied (the old asset keeps them), and open
// rework notes on the old asset are resolved. Without supersede the old asset
// is left as it is. The request's own project always gets the candidate.
func (s *Service) AcceptSubmission(ctx context.Context, requestID, assetID string, supersede bool) (*Request, error) {
	var old string
	err := s.tx(ctx, func(tx *sql.Tx) error {
		r, err := loadRequestTx(ctx, tx, requestID)
		if err != nil {
			return err
		}
		if err := requestFinished(r); err != nil {
			return err
		}
		var verdict string
		var deleted sql.NullString
		err = tx.QueryRowContext(ctx, `SELECT s.verdict, a.deleted_at FROM request_submissions s JOIN assets a ON a.id = s.asset_id
			WHERE s.request_id = ? AND s.asset_id = ?`, requestID, assetID).Scan(&verdict, &deleted)
		if errors.Is(err, sql.ErrNoRows) {
			return invalid("asset %s was not submitted for request %s", assetID, requestID)
		}
		if err != nil {
			return err
		}
		if deleted.Valid {
			return invalid("asset %s is in the trash; restore it first", assetID)
		}
		ts := now()
		if _, err := tx.ExecContext(ctx, `UPDATE request_submissions SET verdict = 'accepted' WHERE request_id = ? AND asset_id = ?`, requestID, assetID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE request_submissions SET verdict = 'rejected' WHERE request_id = ? AND asset_id <> ? AND verdict = ''`, requestID, assetID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE assets SET status = 'approved', updated_at = ? WHERE id = ?`, ts, assetID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE asset_requests SET status = 'done', accepted_asset_id = ?, claimed_by = '', claim_expires_at = NULL,
			updated_at = ?, closed_at = ? WHERE id = ?`, assetID, ts, ts, requestID); err != nil {
			return err
		}
		old = r.TargetAssetID
		if old == "" {
			old = r.PlaceholderAssetID
		}
		if old == assetID {
			old = ""
		}
		if supersede && old != "" {
			if err := supersedeAsset(ctx, tx, old, assetID, ts); err != nil {
				return err
			}
		} else {
			old = ""
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

// RejectSubmission turns a candidate down. The feedback is stored as a rework
// note on the candidate and shown to the next generator that claims the
// request. The candidate becomes rejected. The request goes back to open
// (claim cleared) unless other candidates are still waiting for review.
func (s *Service) RejectSubmission(ctx context.Context, requestID, assetID, feedback string) (*Request, error) {
	feedback, err := cleanNoteBody(feedback, true)
	if err != nil {
		return nil, invalid("say what is wrong: feedback must not be empty")
	}
	err = s.tx(ctx, func(tx *sql.Tx) error {
		r, err := loadRequestTx(ctx, tx, requestID)
		if err != nil {
			return err
		}
		if err := requestFinished(r); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `UPDATE request_submissions SET verdict = 'rejected' WHERE request_id = ? AND asset_id = ?`, requestID, assetID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return invalid("asset %s was not submitted for request %s", assetID, requestID)
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
			if _, err := tx.ExecContext(ctx, `UPDATE asset_requests SET status = 'open', claimed_by = '', claim_expires_at = NULL, updated_at = ? WHERE id = ?`, ts, requestID); err != nil {
				return err
			}
		}
		s.record(ctx, tx, "request.reject", assetID, requestID, feedback)
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.syncSidecar(ctx, assetID)
	return s.GetRequest(ctx, requestID)
}
