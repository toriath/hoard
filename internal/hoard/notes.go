package hoard

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// maxNoteBody bounds one note.
const maxNoteBody = 8 << 10

// Note is a comment, a piece of rework feedback or a review verdict on an
// asset. Bodies are written by people and agents: untrusted everywhere.
type Note struct {
	ID         string  `json:"id"`
	AssetID    string  `json:"asset_id"`
	Kind       string  `json:"kind"` // see NoteKinds
	Body       string  `json:"body"`
	Author     string  `json:"author"`
	CreatedAt  string  `json:"created_at"`
	ResolvedAt *string `json:"resolved_at,omitempty"`
}

func scanNote(row scanner) (*Note, error) {
	var n Note
	var res sql.NullString
	if err := row.Scan(&n.ID, &n.AssetID, &n.Kind, &n.Body, &n.Author, &n.CreatedAt, &res); err != nil {
		return nil, err
	}
	if res.Valid {
		n.ResolvedAt = &res.String
	}
	return &n, nil
}

const noteCols = `id, asset_id, kind, body, author, created_at, resolved_at`

func insertNote(ctx context.Context, tx *sql.Tx, assetID, kind, body, author string) (*Note, error) {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM asset_notes WHERE asset_id = ?`, assetID).Scan(&count); err != nil {
		return nil, err
	}
	if count >= maxNotesPerAsset {
		return nil, capErr("notes on one asset", maxNotesPerAsset)
	}
	n := &Note{ID: newID("not"), AssetID: assetID, Kind: kind, Body: body, Author: author, CreatedAt: now()}
	_, err := tx.ExecContext(ctx, `INSERT INTO asset_notes(id, asset_id, kind, body, author, created_at) VALUES(?, ?, ?, ?, ?, ?)`,
		n.ID, n.AssetID, n.Kind, n.Body, n.Author, n.CreatedAt)
	return n, err
}

func cleanNoteBody(body string, required bool) (string, error) {
	body = strings.TrimSpace(strings.ToValidUTF8(body, ""))
	if body == "" && required {
		return "", invalid("the note text must not be empty")
	}
	if len(body) > maxNoteBody {
		return "", invalid("the note is too long (max %d KiB)", maxNoteBody>>10)
	}
	return body, nil
}

// AddNote attaches a note to an asset. kind is one of NoteKinds (default
// comment). A note does not change the asset's status: use ApproveAsset,
// RejectAsset or MarkForRework for that.
func (s *Service) AddNote(ctx context.Context, assetID, kind, body string) (*Note, error) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind == "" {
		kind = "comment"
	}
	if !slices.Contains(NoteKinds, kind) {
		return nil, invalid("unknown note kind %q (valid: %s)", kind, strings.Join(NoteKinds, ", "))
	}
	body, err := cleanNoteBody(body, true)
	if err != nil {
		return nil, err
	}
	var n *Note
	err = s.tx(ctx, func(tx *sql.Tx) error {
		if err := assetExists(ctx, tx, assetID); err != nil {
			return err
		}
		var err error
		if n, err = insertNote(ctx, tx, assetID, kind, body, ActorFrom(ctx).String()); err != nil {
			return err
		}
		s.record(ctx, tx, "note.add", assetID, n.ID, kind+": "+body)
		return nil
	})
	return n, err
}

// ListNotes returns the notes of an asset, oldest first. Resolved notes are
// included only when includeResolved is set.
func (s *Service) ListNotes(ctx context.Context, assetID string, includeResolved bool) ([]Note, error) {
	if !ValidAssetID(assetID) {
		return nil, fmt.Errorf("asset %q: %w", assetID, ErrNotFound)
	}
	q := `SELECT ` + noteCols + ` FROM asset_notes WHERE asset_id = ?`
	if !includeResolved {
		q += ` AND resolved_at IS NULL`
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY created_at, rowid`, assetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Note{}
	for rows.Next() {
		n, err := scanNote(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *n)
	}
	return out, rows.Err()
}

// ResolveNote marks a note as dealt with. Resolving twice is not an error.
func (s *Service) ResolveNote(ctx context.Context, noteID string) (*Note, error) {
	var n *Note
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var err error
		n, err = scanNote(tx.QueryRowContext(ctx, `SELECT `+noteCols+` FROM asset_notes WHERE id = ?`, noteID))
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("note %q: %w", noteID, ErrNotFound)
		}
		if err != nil || n.ResolvedAt != nil {
			return err
		}
		ts := now()
		if _, err := tx.ExecContext(ctx, `UPDATE asset_notes SET resolved_at = ? WHERE id = ?`, ts, noteID); err != nil {
			return err
		}
		n.ResolvedAt = &ts
		s.record(ctx, tx, "note.resolve", n.AssetID, n.ID, "")
		return nil
	})
	return n, err
}
