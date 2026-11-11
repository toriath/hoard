package hoard

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// ProjectBrief is what a project needs from its assets: engine, art
// direction, technical limits, accepted formats and the layout of the game
// repository. Every field is optional; an empty brief means "no constraints
// known". It is stored as JSON in projects.brief.
type ProjectBrief struct {
	Engine         string              `json:"engine,omitempty"` // see BriefEngines
	EngineVersion  string              `json:"engine_version,omitempty"`
	ArtDirection   string              `json:"art_direction,omitempty"`
	PixelArt       bool                `json:"pixel_art,omitempty"`
	Palette        []string            `json:"palette,omitempty"` // "#rrggbb", lowercase
	TileSize       *int                `json:"tile_size,omitempty"`
	BaseResolution string              `json:"base_resolution,omitempty"` // "320x180"
	MaxTextureSize *int                `json:"max_texture_size,omitempty"`
	PowerOfTwo     bool                `json:"power_of_two,omitempty"`
	Formats        map[string][]string `json:"formats,omitempty"` // asset type -> accepted extensions
	Audio          *BriefAudio         `json:"audio,omitempty"`
	Licenses       []string            `json:"licenses,omitempty"` // allowed; empty = any
	Folders        map[string]string   `json:"folders,omitempty"`  // asset type (or "default") -> repo folder
	Naming         string              `json:"naming,omitempty"`   // see BriefNamings
	SizeBudgetMB   *float64            `json:"size_budget_mb,omitempty"`
	Notes          string              `json:"notes,omitempty"`
}

// BriefAudio is the audio target of a project.
type BriefAudio struct {
	SampleRate   *int     `json:"sample_rate,omitempty"`
	Channels     *int     `json:"channels,omitempty"`
	LoudnessLUFS *float64 `json:"loudness_lufs,omitempty"`
	MaxPeakDBFS  *float64 `json:"max_peak_dbfs,omitempty"`
}

// BriefEngines and BriefNamings are the accepted values of the enum fields.
var (
	BriefEngines = []string{"godot", "unity", "unreal", "phaser", "bevy", "love2d", "gamemaker", "web", "custom"}
	BriefNamings = []string{"snake_case", "kebab-case", "camelCase", "PascalCase", "keep"}
)

// maxBriefBytes bounds the stored brief.
const maxBriefBytes = 32 << 10

var (
	reHexColour = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	reHexShort  = regexp.MustCompile(`^#[0-9a-fA-F]{3}$`)
	reResolut   = regexp.MustCompile(`^[1-9][0-9]{0,5}x[1-9][0-9]{0,5}$`)
	reExt       = regexp.MustCompile(`^[a-z0-9][a-z0-9_+-]{0,15}$`)
)

// IsEmpty reports whether the brief carries no information at all.
func (b *ProjectBrief) IsEmpty() bool {
	if b == nil {
		return true
	}
	raw, err := json.Marshal(b)
	return err != nil || string(raw) == "{}"
}

// Validate checks and normalises the brief in place (palette colours are
// lowercased and short hex forms expanded).
func (b *ProjectBrief) Validate() error {
	if b.Engine != "" && !slices.Contains(BriefEngines, b.Engine) {
		return invalid("brief.engine %q is not valid (valid: %s)", b.Engine, strings.Join(BriefEngines, ", "))
	}
	if b.Naming != "" && !slices.Contains(BriefNamings, b.Naming) {
		return invalid("brief.naming %q is not valid (valid: %s)", b.Naming, strings.Join(BriefNamings, ", "))
	}
	for field, v := range map[string]struct {
		s   string
		max int
	}{
		"engine_version": {b.EngineVersion, 40}, "art_direction": {b.ArtDirection, 8000}, "notes": {b.Notes, 8000},
	} {
		if len(v.s) > v.max {
			return invalid("brief.%s is too long (max %d bytes)", field, v.max)
		}
	}
	if len(b.Palette) > 256 {
		return invalid("brief.palette has too many colours (max 256)")
	}
	for i, c := range b.Palette {
		c = strings.TrimSpace(c)
		switch {
		case reHexShort.MatchString(c):
			c = "#" + string([]byte{c[1], c[1], c[2], c[2], c[3], c[3]})
		case !reHexColour.MatchString(c):
			return invalid("brief.palette[%d] %q is not a hex colour like #1a1c2c", i, c)
		}
		b.Palette[i] = strings.ToLower(c)
	}
	if err := intIn("tile_size", b.TileSize, 1, 4096); err != nil {
		return err
	}
	if err := intIn("max_texture_size", b.MaxTextureSize, 1, 65536); err != nil {
		return err
	}
	if b.BaseResolution != "" && !reResolut.MatchString(b.BaseResolution) {
		return invalid("brief.base_resolution %q must look like 320x180", b.BaseResolution)
	}
	if b.SizeBudgetMB != nil && (*b.SizeBudgetMB <= 0 || *b.SizeBudgetMB > 1e7) {
		return invalid("brief.size_budget_mb must be a positive number (at most 10000000)")
	}
	if len(b.Formats) > len(AssetTypes) {
		return invalid("brief.formats has too many entries")
	}
	for typ, exts := range b.Formats {
		if !ValidType(typ) {
			return invalid("brief.formats: unknown asset type %q (valid: %s)", typ, strings.Join(TypeIDs(), ", "))
		}
		if len(exts) > 32 {
			return invalid("brief.formats.%s has too many extensions", typ)
		}
		for _, e := range exts {
			if !reExt.MatchString(e) {
				return invalid("brief.formats.%s: extension %q must be lowercase without a leading dot, e.g. \"png\"", typ, e)
			}
		}
	}
	for typ, dir := range b.Folders {
		if typ != "default" && !ValidType(typ) {
			return invalid("brief.folders: unknown asset type %q (valid: default, %s)", typ, strings.Join(TypeIDs(), ", "))
		}
		if err := validRepoFolder(dir); err != nil {
			return invalid("brief.folders.%s: %v", typ, err)
		}
	}
	if len(b.Licenses) > 50 {
		return invalid("brief.licenses has too many entries (max 50)")
	}
	for i, l := range b.Licenses {
		l = strings.TrimSpace(l)
		if l == "" || len(l) > 100 {
			return invalid("brief.licenses[%d] must be 1-100 characters", i)
		}
		b.Licenses[i] = l
	}
	if a := b.Audio; a != nil {
		if err := intIn("audio.sample_rate", a.SampleRate, 8000, 384000); err != nil {
			return err
		}
		if err := intIn("audio.channels", a.Channels, 1, 8); err != nil {
			return err
		}
		if a.LoudnessLUFS != nil && (*a.LoudnessLUFS < -70 || *a.LoudnessLUFS > 0) {
			return invalid("brief.audio.loudness_lufs must be between -70 and 0")
		}
		if a.MaxPeakDBFS != nil && (*a.MaxPeakDBFS < -60 || *a.MaxPeakDBFS > 0) {
			return invalid("brief.audio.max_peak_dbfs must be between -60 and 0")
		}
	}
	return nil
}

func intIn(field string, v *int, lo, hi int) error {
	if v != nil && (*v < lo || *v > hi) {
		return invalid("brief.%s must be a whole number between %d and %d", field, lo, hi)
	}
	return nil
}

// validRepoFolder accepts a relative, forward-slash path inside the game
// repository: no leading slash, no "..", no drive letters, no control chars.
func validRepoFolder(dir string) error {
	if dir == "" || len(dir) > 200 {
		return errors.New("must be a relative path of 1-200 characters")
	}
	if strings.HasPrefix(dir, "/") || strings.Contains(dir, `\`) || strings.Contains(dir, ":") {
		return errors.New("must be a relative path with forward slashes, e.g. assets/sprites")
	}
	for _, r := range dir {
		if r < 0x20 || r == 0x7f {
			return errors.New("must not contain control characters")
		}
	}
	for _, seg := range strings.Split(dir, "/") {
		if seg == ".." {
			return errors.New("must not contain \"..\"")
		}
	}
	if path.Clean(dir) == ".." || strings.HasPrefix(path.Clean(dir), "../") {
		return errors.New("must stay inside the repository")
	}
	return nil
}

// GetProjectBrief returns the typed brief of a project (empty when none was
// saved). Unknown stored fields are ignored.
func (s *Service) GetProjectBrief(ctx context.Context, projectID string) (*ProjectBrief, error) {
	raw, err := s.ProjectBriefJSON(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return parseBrief(raw), nil
}

func parseBrief(raw []byte) *ProjectBrief {
	var b ProjectBrief
	_ = json.Unmarshal(raw, &b)
	return &b
}

// SaveProjectBrief merges patch into the stored brief and returns the result.
// Keys present in patch replace the stored value of that field as a whole (a
// list or object is replaced, not merged); a null value clears the field;
// keys that are absent stay as they are. The result is validated before it is
// stored.
func (s *Service) SaveProjectBrief(ctx context.Context, projectID string, patch map[string]json.RawMessage) (*ProjectBrief, error) {
	var out *ProjectBrief
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var cur string
		err := tx.QueryRowContext(ctx, `SELECT brief FROM projects WHERE id = ?`, projectID).Scan(&cur)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("project %q: %w", projectID, ErrNotFound)
		}
		if err != nil {
			return err
		}
		merged := map[string]json.RawMessage{}
		_ = json.Unmarshal([]byte(cur), &merged)
		var changed []string
		for k, v := range patch {
			if trimmed := bytes.TrimSpace(v); len(trimmed) == 0 || string(trimmed) == "null" {
				if _, ok := merged[k]; ok {
					delete(merged, k)
					changed = append(changed, k)
				}
				continue
			}
			merged[k] = v
			changed = append(changed, k)
		}
		blob, err := json.Marshal(merged)
		if err != nil {
			return err
		}
		var b ProjectBrief
		dec := json.NewDecoder(bytes.NewReader(blob))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&b); err != nil {
			return invalid("invalid brief: %v", err)
		}
		if err := b.Validate(); err != nil {
			return err
		}
		stored, err := json.Marshal(&b)
		if err != nil {
			return err
		}
		if len(stored) > maxBriefBytes {
			return invalid("brief is too large (max %d KiB)", maxBriefBytes>>10)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE projects SET brief = ?, updated_at = ? WHERE id = ?`, string(stored), now(), projectID); err != nil {
			return err
		}
		sort.Strings(changed)
		if len(changed) > 0 {
			s.record(ctx, tx, "project.brief", "", projectID, "brief changed: "+strings.Join(changed, ", "))
		}
		out = &b
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
