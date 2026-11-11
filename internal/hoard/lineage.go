package hoard

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const maxLineageDepth = 20

var reToolName = regexp.MustCompile(`^[A-Za-z0-9 ._()-]{1,40}$`)

// LineageNode is a compact view of an asset inside a lineage.
type LineageNode struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	Type         string         `json:"type"`
	Status       string         `json:"status,omitempty"`
	ThumbVersion int            `json:"thumb_version"`
	Prompt       string         `json:"prompt,omitempty"`
	Generator    map[string]any `json:"generator,omitempty"`
	Trashed      bool           `json:"trashed,omitempty"`
}

// Lineage describes how an asset relates to others.
type Lineage struct {
	Ancestors  []LineageNode `json:"ancestors"`           // derived_from chain, nearest parent first, at most 20 deep
	Children   []LineageNode `json:"children"`            // derived directly from this asset
	Siblings   []LineageNode `json:"siblings"`            // other children of the same parent
	Supersedes []LineageNode `json:"supersedes"`          // assets this one replaced
	Superseded []LineageNode `json:"superseded_by_chain"` // what replaced this asset, then what replaced that…
}

const nodeCols = `id, type, name, status, thumb_version, prompt, generator, derived_from, superseded_by, deleted_at IS NOT NULL`

type nodeRow struct {
	LineageNode
	derivedFrom, supersededBy string
}

func scanNode(row interface{ Scan(...any) error }) (nodeRow, error) {
	var n nodeRow
	var gen string
	if err := row.Scan(&n.ID, &n.Type, &n.Name, &n.Status, &n.ThumbVersion, &n.Prompt, &gen, &n.derivedFrom, &n.supersededBy, &n.Trashed); err != nil {
		return n, err
	}
	if gen != "" && gen != "{}" {
		_ = json.Unmarshal([]byte(gen), &n.Generator)
	}
	return n, nil
}

func (s *Service) lineageNode(ctx context.Context, id string) (*nodeRow, error) {
	if !ValidAssetID(id) {
		return nil, nil
	}
	n, err := scanNode(s.db.QueryRowContext(ctx, `SELECT `+nodeCols+` FROM assets WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &n, nil
}

func (s *Service) lineageNodes(ctx context.Context, where string, args ...any) ([]LineageNode, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+nodeCols+` FROM assets WHERE `+where+` AND deleted_at IS NULL ORDER BY created_at, id LIMIT 100`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LineageNode{}
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n.LineageNode)
	}
	return out, rows.Err()
}

// walk follows next() from start. It stops on a cycle (an id seen before,
// including skip, the asset the walk started from), on a missing asset and
// after maxLineageDepth steps.
func (s *Service) walk(ctx context.Context, start, skip string, next func(*nodeRow) string) ([]LineageNode, error) {
	out := []LineageNode{}
	seen := map[string]bool{skip: true}
	id := start
	for len(out) < maxLineageDepth && id != "" && !seen[id] {
		seen[id] = true
		n, err := s.lineageNode(ctx, id)
		if err != nil {
			return nil, err
		}
		if n == nil {
			break
		}
		out = append(out, n.LineageNode)
		id = next(n)
	}
	return out, nil
}

// Lineage returns the relatives of an asset. Cycles in the stored chains are
// tolerated: a walk stops where it would revisit an asset.
func (s *Service) Lineage(ctx context.Context, assetID string) (*Lineage, error) {
	self, err := s.lineageNode(ctx, assetID)
	if err != nil {
		return nil, err
	}
	if self == nil {
		return nil, fmt.Errorf("asset %q: %w", assetID, ErrNotFound)
	}
	l := &Lineage{Siblings: []LineageNode{}}
	if l.Ancestors, err = s.walk(ctx, self.derivedFrom, self.ID, func(n *nodeRow) string { return n.derivedFrom }); err != nil {
		return nil, err
	}
	if l.Superseded, err = s.walk(ctx, self.supersededBy, self.ID, func(n *nodeRow) string { return n.supersededBy }); err != nil {
		return nil, err
	}
	if l.Children, err = s.lineageNodes(ctx, `derived_from = ? AND id != ?`, self.ID, self.ID); err != nil {
		return nil, err
	}
	if self.derivedFrom != "" {
		if l.Siblings, err = s.lineageNodes(ctx, `derived_from = ? AND id != ?`, self.derivedFrom, self.ID); err != nil {
			return nil, err
		}
	}
	if l.Supersedes, err = s.lineageNodes(ctx, `superseded_by = ? AND id != ?`, self.ID, self.ID); err != nil {
		return nil, err
	}
	return l, nil
}

// Recipe is everything needed to understand, reproduce or vary a generated asset.
type Recipe struct {
	AssetID     string         `json:"asset_id"`
	Name        string         `json:"name"`
	Type        string         `json:"type"`
	Prompt      string         `json:"prompt"`
	Generator   map[string]any `json:"generator"`
	DerivedFrom []LineageNode  `json:"derived_from"` // ancestors, nearest first, each with prompt and generator
	Variations  []LineageNode  `json:"variations"`   // assets derived from this one
	HasRecipe   bool           `json:"has_recipe"`
	Reproduce   string         `json:"reproduce"`
}

// GetRecipe assembles the recipe of an asset.
func (s *Service) GetRecipe(ctx context.Context, assetID string) (*Recipe, error) {
	n, err := s.lineageNode(ctx, assetID)
	if err != nil {
		return nil, err
	}
	if n == nil {
		return nil, fmt.Errorf("asset %q: %w", assetID, ErrNotFound)
	}
	l, err := s.Lineage(ctx, assetID)
	if err != nil {
		return nil, err
	}
	r := &Recipe{
		AssetID: n.ID, Name: n.Name, Type: n.Type, Prompt: n.Prompt, Generator: n.Generator,
		DerivedFrom: l.Ancestors, Variations: l.Children,
	}
	if r.Generator == nil {
		r.Generator = map[string]any{}
	}
	r.HasRecipe = r.Prompt != "" || len(r.Generator) > 0
	r.Reproduce = reproduceHint(r)
	return r, nil
}

func reproduceHint(r *Recipe) string {
	if !r.HasRecipe {
		return "No prompt or generator is recorded for this asset, so it cannot be reproduced. If you know how it was made, record it with update_asset (prompt, generator)."
	}
	tool, _ := r.Generator["tool"].(string)
	var b strings.Builder
	if tool != "" {
		// The tool name is data written by someone else: only a plain name is quoted.
		if reToolName.MatchString(tool) {
			fmt.Fprintf(&b, "Call the generator tool %q with the inputs below. ", tool)
		} else {
			b.WriteString("Call the tool recorded in generator.tool with the inputs below. ")
		}
	} else {
		b.WriteString("The generator tool is not recorded; use the one that fits the asset type. ")
	}
	var inputs []string
	if r.Prompt != "" {
		inputs = append(inputs, "prompt")
	}
	for _, k := range []string{"negative_prompt", "model", "seed", "steps", "cfg", "sampler", "width", "height", "strength", "loras", "duration_s", "settings"} {
		if _, ok := r.Generator[k]; ok {
			inputs = append(inputs, k)
		}
	}
	if len(inputs) > 0 {
		fmt.Fprintf(&b, "Inputs recorded: %s. ", strings.Join(inputs, ", "))
	}
	if _, ok := r.Generator["seed"]; ok {
		b.WriteString("Keep the seed to reproduce the result as exactly as the tool allows; change it (or request several variations) for alternatives. ")
	} else {
		b.WriteString("No seed is recorded, so an exact reproduction is not possible. ")
	}
	fmt.Fprintf(&b, "To store the new result as a variation, create_asset with derived_from = %s and the prompt and generator you actually used, so its recipe is recorded too. ", r.AssetID)
	if len(r.DerivedFrom) > 0 {
		b.WriteString("Earlier steps of the chain are in derived_from, nearest parent first. ")
	}
	return strings.TrimSpace(b.String())
}
