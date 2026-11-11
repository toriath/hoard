package hoard

import (
	"context"
	"database/sql"
	"fmt"
	"image"
	"math/bits"
	"slices"
	"sort"
	"strconv"
)

// Perceptual hashing: a 64-bit difference hash (dHash) of the picture. The
// image is reduced to 9x8 grey cells; each bit says whether a cell is brighter
// than its right neighbour. Re-encoding, rescaling and small edits change few
// bits, a different picture changes about half of them.
const (
	// NearIdenticalDistance is the largest Hamming distance still called
	// "near-identical" (same picture, re-encoded, rescaled or lightly touched).
	NearIdenticalDistance = 5
	// SimilarDistance is the largest distance reported by FindSimilar.
	SimilarDistance = 10

	// phashNone marks a file that was looked at and has no usable hash (flat
	// picture, undecodable), so the backfill does not try again.
	phashNone = "-"
)

// imageStats is what one decode of an image yields besides the thumbnail.
type imageStats struct {
	PHash  string // 16 hex characters, "" when the picture is flat
	Colors []ColorWeight
}

func statsOf(thumb *image.NRGBA) *imageStats {
	return &imageStats{PHash: dHash(thumb), Colors: dominantColors(thumb)}
}

// dHash computes the difference hash of an image as 16 hex characters.
// Transparent areas are composited over mid grey, so a light sprite on a
// transparent background and a dark one still differ. A picture without
// structure (flat colour) has no meaningful hash and yields "".
func dHash(img *image.NRGBA) string {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w == 0 || h == 0 {
		return ""
	}
	var cell [8][9]float64
	for cy := 0; cy < 8; cy++ {
		y0 := cy * h / 8
		y1 := max(y0+1, (cy+1)*h/8)
		for cx := 0; cx < 9; cx++ {
			x0 := cx * w / 9
			x1 := max(x0+1, (cx+1)*w/9)
			var sum float64
			n := 0
			for y := y0; y < y1 && y < h; y++ {
				row := img.Pix[y*img.Stride:]
				for x := x0; x < x1 && x < w; x++ {
					p := row[x*4 : x*4+4]
					a := float64(p[3]) / 255
					lum := 0.299*float64(p[0]) + 0.587*float64(p[1]) + 0.114*float64(p[2])
					sum += lum*a + 128*(1-a)
					n++
				}
			}
			if n > 0 {
				cell[cy][cx] = sum / float64(n)
			}
		}
	}
	lo, hi := cell[0][0], cell[0][0]
	var hash uint64
	for y := 0; y < 8; y++ {
		for x := 0; x < 9; x++ {
			lo, hi = min(lo, cell[y][x]), max(hi, cell[y][x])
		}
		for x := 0; x < 8; x++ {
			hash <<= 1
			if cell[y][x] > cell[y][x+1] {
				hash |= 1
			}
		}
	}
	if hi-lo < 6 {
		return ""
	}
	return fmt.Sprintf("%016x", hash)
}

func parseHash(h string) (uint64, bool) {
	if len(h) != 16 {
		return 0, false
	}
	v, err := strconv.ParseUint(h, 16, 64)
	return v, err == nil
}

// HashDistance is the Hamming distance of two hashes (64 when either is invalid).
func HashDistance(a, b string) int {
	x, ok1 := parseHash(a)
	y, ok2 := parseHash(b)
	if !ok1 || !ok2 {
		return 64
	}
	return bits.OnesCount64(x ^ y)
}

// storeImageStats records the hash of a file and the colours of its asset.
// It touches neither updated_at nor the activity feed: both are derived data.
func (s *Service) storeImageStats(ctx context.Context, assetID, fileID string, st *imageStats) error {
	if st == nil {
		return nil
	}
	hash := st.PHash
	if hash == "" {
		hash = phashNone
	}
	// TODO(lock): serialise with other mutations of this asset (lockAssets) once available.
	return s.tx(ctx, func(tx *sql.Tx) error {
		// The statistics describe the primary file; if it changed while the
		// image was decoded, they are stale and dropped.
		var primary string
		if err := tx.QueryRowContext(ctx, `SELECT primary_file_id FROM assets WHERE id = ?`, assetID).Scan(&primary); err != nil || primary != fileID {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE asset_files SET phash = ? WHERE id = ? AND asset_id = ?`, hash, fileID, assetID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM asset_colors WHERE asset_id = ?`, assetID); err != nil {
			return err
		}
		for _, c := range st.Colors {
			if _, err := tx.ExecContext(ctx, `INSERT INTO asset_colors(asset_id, color, weight) VALUES(?, ?, ?)`, assetID, c.Color, c.Weight); err != nil {
				return err
			}
		}
		return nil
	})
}

type hashRow struct {
	assetID string
	hash    uint64
}

// primaryHashes lists the hash of the primary file of every live asset.
func (s *Service) primaryHashes(ctx context.Context) ([]hashRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT a.id, f.phash FROM assets a JOIN asset_files f ON f.id = a.primary_file_id
		WHERE a.deleted_at IS NULL AND length(f.phash) = 16`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []hashRow
	for rows.Next() {
		var id, str string
		if err := rows.Scan(&id, &str); err != nil {
			return nil, err
		}
		if v, ok := parseHash(str); ok {
			out = append(out, hashRow{id, v})
		}
	}
	return out, rows.Err()
}

// nearAssets returns ids of live assets whose primary picture is within
// maxDist of hash, closest first, leaving out self and the excluded ids.
func (s *Service) nearAssets(ctx context.Context, hash, self string, maxDist, limit int, exclude []string) []string {
	h, ok := parseHash(hash)
	if !ok {
		return nil
	}
	all, err := s.primaryHashes(ctx)
	if err != nil {
		return nil
	}
	type hit struct {
		id string
		d  int
	}
	var hits []hit
	for _, r := range all {
		if r.assetID == self || slices.Contains(exclude, r.assetID) {
			continue
		}
		if d := bits.OnesCount64(r.hash ^ h); d <= maxDist {
			hits = append(hits, hit{r.assetID, d})
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].d != hits[j].d {
			return hits[i].d < hits[j].d
		}
		return hits[i].id < hits[j].id
	})
	var out []string
	for _, h := range hits {
		if len(out) == limit {
			break
		}
		out = append(out, h.id)
	}
	return out
}

// SimilarHit is one asset related to the one asked about.
type SimilarHit struct {
	Asset    *Asset   `json:"asset"`
	Score    float64  `json:"score"`              // 0..1, higher is closer
	Reasons  []string `json:"reasons"`            // why it is listed
	Distance *int     `json:"distance,omitempty"` // perceptual hash distance, images only
}

// FindSimilar lists assets that duplicate or resemble the given one, best
// first: files with identical content, pictures with a close perceptual hash
// and assets of the same type that share tags.
func (s *Service) FindSimilar(ctx context.Context, assetID string, limit int) ([]SimilarHit, error) {
	if limit <= 0 {
		limit = 20
	}
	limit = min(limit, 100)
	a, err := s.GetAsset(ctx, assetID)
	if err != nil {
		return nil, err
	}
	type cand struct {
		score   float64
		reasons []string
		dist    *int
	}
	cands := map[string]*cand{}
	add := func(id string, score float64, reason string, dist *int) {
		if id == a.ID {
			return
		}
		c := cands[id]
		if c == nil {
			c = &cand{}
			cands[id] = c
		}
		c.score = max(c.score, score)
		c.reasons = append(c.reasons, reason)
		if dist != nil {
			c.dist = dist
		}
	}

	// identical content in any file
	var shas []any
	for _, f := range a.Files {
		shas = append(shas, f.SHA256)
	}
	if len(shas) > 0 {
		rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT f.asset_id FROM asset_files f JOIN assets x ON x.id = f.asset_id
			WHERE f.sha256 IN (`+placeholders(len(shas))+`) AND f.asset_id != ? AND x.deleted_at IS NULL`, append(shas, a.ID)...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			add(id, 1, "identical file", nil)
		}
		rows.Close()
	}

	// perceptual hash of the primary picture
	hashed := false
	if p := a.Primary(); p != nil {
		var ph string
		if err := s.db.QueryRowContext(ctx, `SELECT phash FROM asset_files WHERE id = ?`, p.ID).Scan(&ph); err == nil {
			if h, ok := parseHash(ph); ok {
				hashed = true
				all, err := s.primaryHashes(ctx)
				if err != nil {
					return nil, err
				}
				for _, r := range all {
					d := bits.OnesCount64(r.hash ^ h)
					if r.assetID == a.ID || d > SimilarDistance {
						continue
					}
					dd := d
					word := "visually similar"
					if d <= NearIdenticalDistance {
						word = "visually near-identical"
					}
					add(r.assetID, 0.98-float64(d)*0.04, fmt.Sprintf("%s (distance %d)", word, d), &dd)
				}
			}
		}
	}

	// same type, shared tags
	if len(a.Tags) > 0 {
		shared := map[string]int{}
		rows, err := s.db.QueryContext(ctx, `SELECT t.asset_id, COUNT(*) FROM asset_tags t JOIN assets x ON x.id = t.asset_id
			WHERE t.tag IN (`+placeholders(len(a.Tags))+`) AND t.asset_id != ? AND x.type = ? AND x.deleted_at IS NULL
			GROUP BY t.asset_id`, append(anySlice(a.Tags), a.ID, a.Type)...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			var n int
			if err := rows.Scan(&id, &n); err != nil {
				rows.Close()
				return nil, err
			}
			shared[id] = n
		}
		rows.Close()
		if len(shared) > 0 {
			ids := make([]string, 0, len(shared))
			for id := range shared {
				ids = append(ids, id)
			}
			counts := map[string]int{}
			rows, err := s.db.QueryContext(ctx, `SELECT asset_id, COUNT(*) FROM asset_tags WHERE asset_id IN (`+placeholders(len(ids))+`) GROUP BY asset_id`, anySlice(ids)...)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var id string
				var n int
				if err := rows.Scan(&id, &n); err != nil {
					rows.Close()
					return nil, err
				}
				counts[id] = n
			}
			rows.Close()
			for id, n := range shared {
				union := len(a.Tags) + counts[id] - n
				j := float64(n) / float64(max(union, 1))
				// A tag in common is weak evidence on its own; for pictures with a
				// hash it only annotates candidates found by content.
				_, known := cands[id]
				if n < 2 || j < 0.25 || (hashed && !known) {
					continue
				}
				add(id, 0.7*j, fmt.Sprintf("shares %d tags", n), nil)
			}
		}
	}

	ids := make([]string, 0, len(cands))
	for id := range cands {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		ci, cj := cands[ids[i]], cands[ids[j]]
		if ci.score != cj.score {
			return ci.score > cj.score
		}
		return ids[i] < ids[j]
	})
	if len(ids) > limit {
		ids = ids[:limit]
	}
	if len(ids) == 0 {
		return []SimilarHit{}, nil
	}
	assets, err := s.ListAssets(ctx, ListQuery{IDs: ids, Limit: len(ids)})
	if err != nil {
		return nil, err
	}
	byID := map[string]*Asset{}
	for _, x := range assets.Items {
		byID[x.ID] = x
	}
	out := make([]SimilarHit, 0, len(ids))
	for _, id := range ids {
		x := byID[id]
		if x == nil {
			continue
		}
		c := cands[id]
		out = append(out, SimilarHit{Asset: x, Score: float64(int(c.score*1000+0.5)) / 1000, Reasons: c.reasons, Distance: c.dist})
	}
	return out, nil
}

// DuplicateGroup is a set of assets that hold the same picture or file.
type DuplicateGroup struct {
	Kind   string   `json:"kind"`   // identical | near-identical
	Reason string   `json:"reason"` // human-readable
	Assets []*Asset `json:"assets"`
}

const (
	maxDuplicateGroups  = 200
	maxGroupMembers     = 30
	duplicateSampleSize = 100000
)

// FindDuplicates lists groups of live assets with byte-identical files and
// groups of near-identical pictures, identical ones and larger groups first.
func (s *Service) FindDuplicates(ctx context.Context) ([]DuplicateGroup, error) {
	// exact: the same hash in files of different assets
	rows, err := s.db.QueryContext(ctx, `SELECT f.sha256, f.asset_id FROM asset_files f JOIN assets a ON a.id = f.asset_id
		WHERE a.deleted_at IS NULL AND f.sha256 IN (
			SELECT f2.sha256 FROM asset_files f2 JOIN assets a2 ON a2.id = f2.asset_id WHERE a2.deleted_at IS NULL
			GROUP BY f2.sha256 HAVING COUNT(DISTINCT f2.asset_id) > 1)
		ORDER BY f.sha256, a.created_at, a.id LIMIT ?`, duplicateSampleSize)
	if err != nil {
		return nil, err
	}
	exact := map[string][]string{}
	var shaOrder []string
	for rows.Next() {
		var sha, id string
		if err := rows.Scan(&sha, &id); err != nil {
			rows.Close()
			return nil, err
		}
		if _, ok := exact[sha]; !ok {
			shaOrder = append(shaOrder, sha)
		}
		if !slices.Contains(exact[sha], id) {
			exact[sha] = append(exact[sha], id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	type group struct {
		kind, reason string
		ids          []string
	}
	var groups []group
	seen := map[string]bool{}
	for _, sha := range shaOrder {
		ids := exact[sha]
		key := fmt.Sprint(ids)
		if seen[key] { // several shared files, same assets
			continue
		}
		seen[key] = true
		groups = append(groups, group{"identical", "identical file content", ids})
	}
	nExact := len(groups)

	// Near-identical pictures: connect hashes within the threshold. Two
	// hashes at distance <= 5 agree in at least one of six chunks (pigeonhole),
	// so only entries sharing a chunk value are compared.
	hashes, err := s.primaryHashes(ctx)
	if err != nil {
		return nil, err
	}
	parent := make([]int, len(hashes))
	for i := range parent {
		parent[i] = i
	}
	find := func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	chunks := [6][2]uint{{0, 11}, {11, 11}, {22, 11}, {33, 11}, {44, 10}, {54, 10}}
	for _, cb := range chunks {
		buckets := map[uint64][]int{}
		mask := uint64(1)<<cb[1] - 1
		for i, r := range hashes {
			k := r.hash >> cb[0] & mask
			buckets[k] = append(buckets[k], i)
		}
		for _, idx := range buckets {
			if len(idx) > 2000 { // pathological bucket: skip rather than stall
				s.log.Warn("duplicate scan: skipped an oversized hash bucket; some near-identical pictures may be missing", "entries", len(idx))
				continue
			}
			for x := 0; x < len(idx); x++ {
				for y := x + 1; y < len(idx); y++ {
					if bits.OnesCount64(hashes[idx[x]].hash^hashes[idx[y]].hash) <= NearIdenticalDistance {
						parent[find(idx[x])] = find(idx[y])
					}
				}
			}
		}
	}
	comps := map[int][]string{}
	var roots []int
	for i, r := range hashes {
		root := find(i)
		if _, ok := comps[root]; !ok {
			roots = append(roots, root)
		}
		comps[root] = append(comps[root], r.assetID)
	}
	sort.Ints(roots)
	for _, r := range roots {
		ids := comps[r]
		if len(ids) < 2 {
			continue
		}
		slices.Sort(ids)
		covered := false // already shown as one group of identical files
		for _, g := range groups[:nExact] {
			if containsAll(g.ids, ids) {
				covered = true
				break
			}
		}
		if !covered {
			groups = append(groups, group{"near-identical", fmt.Sprintf("visually near-identical (hash distance up to %d)", NearIdenticalDistance), ids})
		}
	}

	sort.SliceStable(groups, func(i, j int) bool {
		if (groups[i].kind == "identical") != (groups[j].kind == "identical") {
			return groups[i].kind == "identical"
		}
		return len(groups[i].ids) > len(groups[j].ids)
	})
	if len(groups) > maxDuplicateGroups {
		groups = groups[:maxDuplicateGroups]
	}

	var want []string
	for i := range groups {
		if len(groups[i].ids) > maxGroupMembers {
			groups[i].ids = groups[i].ids[:maxGroupMembers]
		}
		want = append(want, groups[i].ids...)
	}
	byID := map[string]*Asset{}
	for start := 0; start < len(want); start += 500 {
		chunk := want[start:min(start+500, len(want))]
		res, err := s.ListAssets(ctx, ListQuery{IDs: chunk, Limit: len(chunk)})
		if err != nil {
			return nil, err
		}
		for _, a := range res.Items {
			byID[a.ID] = a
		}
	}
	out := make([]DuplicateGroup, 0, len(groups))
	for _, g := range groups {
		dg := DuplicateGroup{Kind: g.kind, Reason: g.reason}
		for _, id := range g.ids {
			if a := byID[id]; a != nil {
				dg.Assets = append(dg.Assets, a)
			}
		}
		if len(dg.Assets) > 1 {
			out = append(out, dg)
		}
	}
	return out, nil
}

func containsAll(set, sub []string) bool {
	for _, x := range sub {
		if !slices.Contains(set, x) {
			return false
		}
	}
	return true
}
