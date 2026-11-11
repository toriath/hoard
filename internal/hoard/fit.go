package hoard

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"image"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
)

// FitIssue is one finding of a fit check.
type FitIssue struct {
	Severity string `json:"severity"` // error | warning | info
	Code     string `json:"code"`
	Message  string `json:"message"`
}

// FitReport is the outcome of checking one asset against one project's brief.
type FitReport struct {
	AssetID     string     `json:"asset_id"`
	ProjectID   string     `json:"project_id"`
	ProjectName string     `json:"project_name"`
	HasBrief    bool       `json:"has_brief"`
	Issues      []FitIssue `json:"issues"`
	Errors      int        `json:"errors"`
	Warnings    int        `json:"warnings"`
}

const (
	sevError   = "error"
	sevWarning = "warning"
	sevInfo    = "info"

	// paletteTolerance is the largest RGB distance (0-441) at which a pixel
	// still counts as being on the palette.
	paletteTolerance = 16
	// Share of opaque pixels off the palette above which the finding is an
	// error / a warning. Below the warning share it is not reported.
	paletteErrorShare   = 0.20
	paletteWarningShare = 0.005
	// loudnessTolerance is how far the approximate loudness may be from the
	// target before it is reported.
	loudnessTolerance = 3.0
)

func issue(sev, code, format string, a ...any) FitIssue {
	return FitIssue{Severity: sev, Code: code, Message: fmt.Sprintf(format, a...)}
}

func sortIssues(in []FitIssue) {
	rank := map[string]int{sevError: 0, sevWarning: 1, sevInfo: 2}
	slices.SortStableFunc(in, func(a, b FitIssue) int { return rank[a.Severity] - rank[b.Severity] })
}

func countIssues(in []FitIssue) (errs, warns int) {
	for _, i := range in {
		switch i.Severity {
		case sevError:
			errs++
		case sevWarning:
			warns++
		}
	}
	return
}

// CheckFit checks one asset against the brief of a project and returns what
// does not fit, errors first. The asset need not be used in the project yet:
// that is how a candidate is vetted before it is adopted. Measurements taken
// from audio files (duration, sample rate, channels, bit depth, peak) are
// stored in the asset's attrs when absent; this changes neither updated_at
// nor the activity feed.
func (s *Service) CheckFit(ctx context.Context, assetID, projectID string) ([]FitIssue, error) {
	a, err := s.GetAsset(ctx, assetID)
	if err != nil {
		return nil, err
	}
	brief, err := s.GetProjectBrief(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return s.checkFit(ctx, a, brief, true)
}

// FitReports checks an asset against one project (projectID given) or against
// every project it is used in (projectID empty).
func (s *Service) FitReports(ctx context.Context, assetID, projectID string) ([]FitReport, error) {
	a, err := s.GetAsset(ctx, assetID)
	if err != nil {
		return nil, err
	}
	type target struct{ id, name string }
	var targets []target
	if projectID != "" {
		p, err := s.GetProject(ctx, projectID)
		if err != nil {
			return nil, err
		}
		targets = append(targets, target{p.ID, p.Name})
	} else {
		for _, r := range a.Projects {
			targets = append(targets, target{r.ID, r.Name})
		}
	}
	out := []FitReport{}
	for _, t := range targets {
		brief, err := s.GetProjectBrief(ctx, t.id)
		if err != nil {
			return nil, err
		}
		issues, err := s.checkFit(ctx, a, brief, true)
		if err != nil {
			return nil, err
		}
		e, w := countIssues(issues)
		out = append(out, FitReport{AssetID: a.ID, ProjectID: t.id, ProjectName: t.name, HasBrief: !brief.IsEmpty(),
			Issues: issues, Errors: e, Warnings: w})
	}
	return out, nil
}

// checkFit runs every check. With measure set, image and audio files are read
// (palette, sample data) and measured audio attributes are stored on the
// asset. Without it nothing is decoded and nothing is written: audio is judged
// from the attributes stored earlier, which is what page views use.
func (s *Service) checkFit(ctx context.Context, a *Asset, b *ProjectBrief, measure bool) ([]FitIssue, error) {
	if b.IsEmpty() {
		return []FitIssue{issue(sevInfo, "no_brief", "The project has no brief, so there is nothing to check this asset against.")}, nil
	}
	var out []FitIssue
	add := func(i ...FitIssue) { out = append(out, i...) }

	// Review state.
	switch a.Status {
	case "rejected":
		add(issue(sevWarning, "status_rejected", "Asset is marked rejected and should not be used."))
	case "superseded":
		if a.SupersededBy != "" {
			add(issue(sevWarning, "status_superseded", "Asset was superseded by %s; use that one.", a.SupersededBy))
		} else {
			add(issue(sevWarning, "status_superseded", "Asset is marked superseded and should not be used."))
		}
	case "placeholder":
		add(issue(sevWarning, "status_placeholder", "Asset is a placeholder and still has to be replaced by the real one."))
	case "rework":
		add(issue(sevWarning, "status_rework", "Asset is marked for rework; the final version is not ready yet."))
	}

	// Licence.
	lic := strings.TrimSpace(a.License)
	if len(b.Licenses) > 0 {
		allowed := slices.ContainsFunc(b.Licenses, func(l string) bool { return strings.EqualFold(l, lic) })
		switch {
		case lic == "":
			add(issue(sevWarning, "license_missing", "No license recorded; the project only allows %s.", strings.Join(b.Licenses, ", ")))
		case !allowed:
			add(issue(sevError, "license_not_allowed", "License %q is not allowed by the project (allowed: %s).", lic, strings.Join(b.Licenses, ", ")))
		}
	}
	if needsAttribution(lic) && strings.TrimSpace(a.Attribution) == "" {
		add(issue(sevWarning, "attribution_missing", "License %s requires attribution but none is recorded.", lic))
	}

	p := a.Primary()
	if p == nil {
		add(issue(sevWarning, "no_files", "Asset has no files."))
		sortIssues(out)
		return out, nil
	}

	// Accepted formats.
	fext := strings.TrimPrefix(ext(p.Filename), ".")
	if accepted := b.Formats[a.Type]; len(accepted) > 0 && !slices.Contains(accepted, fext) {
		add(issue(sevError, "format", "File %s is .%s, the project accepts %s for %s assets.", p.Filename, fext, strings.Join(accepted, ", "), a.Type))
	}

	// Naming convention.
	if re := namingRegexp(b.Naming); re != nil {
		for _, f := range a.Files {
			stem := strings.TrimSuffix(f.Filename, filepath.Ext(f.Filename))
			if !re.MatchString(stem) {
				add(issue(sevInfo, "naming", "File name %q does not follow %s.", f.Filename, b.Naming))
				break
			}
		}
	}

	// Images.
	if p.Kind == KindImage && p.Width > 0 && p.Height > 0 {
		add(s.fitImageSize(a, p, b)...)
	}
	if len(b.Palette) > 0 && decodableImage[ext(p.Filename)] && p.Width > 0 {
		if measure {
			add(s.fitPalette(ctx, a, p, b)...)
		}
	} else if len(b.Palette) > 0 && p.Kind == KindImage {
		add(issue(sevInfo, "palette_unchecked", "Palette not checked: %s cannot be decoded by the server.", p.Filename))
	}

	// Audio.
	if isAudioExt(ext(p.Filename)) {
		add(s.fitAudio(ctx, a, p, b, measure)...)
	}

	// Size.
	size := humanBytes(a.TotalSize)
	if b.SizeBudgetMB != nil && *b.SizeBudgetMB > 0 {
		share := float64(a.TotalSize) / (*b.SizeBudgetMB * 1e6)
		if share > 0.25 {
			add(issue(sevWarning, "size_budget", "Total size %s is %.0f%% of the project's %.0f MB budget.", size, share*100, *b.SizeBudgetMB))
		} else {
			add(issue(sevInfo, "size", "Total size %s (%.1f%% of the %.0f MB budget).", size, share*100, *b.SizeBudgetMB))
		}
	} else {
		add(issue(sevInfo, "size", "Total size %s.", size))
	}

	sortIssues(out)
	return out, nil
}

func needsAttribution(lic string) bool {
	return strings.HasPrefix(strings.ToUpper(strings.TrimSpace(lic)), "CC-BY")
}

func isAudioExt(e string) bool {
	switch e {
	case ".wav", ".ogg", ".oga", ".opus", ".flac", ".mp3", ".m4a", ".aac", ".aiff", ".aif":
		return true
	}
	return false
}

var namingRes = map[string]*regexp.Regexp{
	"snake_case": regexp.MustCompile(`^[a-z0-9]+(_[a-z0-9]+)*$`),
	"kebab-case": regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`),
	"camelCase":  regexp.MustCompile(`^[a-z][a-zA-Z0-9]*$`),
	"PascalCase": regexp.MustCompile(`^[A-Z][a-zA-Z0-9]*$`),
}

func namingRegexp(n string) *regexp.Regexp { return namingRes[n] }

func humanBytes(n int64) string {
	switch {
	case n >= 1e9:
		return fmt.Sprintf("%.1f GB", float64(n)/1e9)
	case n >= 1e6:
		return fmt.Sprintf("%.1f MB", float64(n)/1e6)
	case n >= 1e3:
		return fmt.Sprintf("%.1f kB", float64(n)/1e3)
	}
	return fmt.Sprintf("%d B", n)
}

func attrNumber(attrs map[string]any, key string) (float64, bool) {
	switch v := attrs[key].(type) {
	case float64:
		return v, v > 0
	case int:
		return float64(v), v > 0
	}
	return 0, false
}

// ---- images ----

func (s *Service) fitImageSize(a *Asset, p *File, b *ProjectBrief) []FitIssue {
	var out []FitIssue
	w, h := p.Width, p.Height
	switch a.Type {
	case "sprite", "tileset", "ui", "texture", "vfx":
	default:
		return nil
	}
	if b.MaxTextureSize != nil && max(w, h) > *b.MaxTextureSize {
		out = append(out, issue(sevError, "max_texture_size", "Image is %dx%d px, the project's maximum texture size is %d px on the longest edge.", w, h, *b.MaxTextureSize))
	}
	if b.PowerOfTwo && (!isPow2(w) || !isPow2(h)) {
		out = append(out, issue(sevWarning, "power_of_two", "Image is %dx%d px; the project wants power-of-two dimensions.", w, h))
	}
	if b.TileSize != nil {
		t := *b.TileSize
		switch a.Type {
		case "tileset":
			if w%t != 0 || h%t != 0 {
				out = append(out, issue(sevWarning, "tile_size", "Tileset is %dx%d px, not a multiple of the project's tile size %d.", w, h, t))
			}
		case "sprite", "ui":
			fw, fh := w, h
			what := "Sprite"
			if v, ok := attrNumber(a.Attrs, "frame_width"); ok {
				fw, what = int(v), "Sprite frame"
			}
			if v, ok := attrNumber(a.Attrs, "frame_height"); ok {
				fh, what = int(v), "Sprite frame"
			}
			if fw%t != 0 || fh%t != 0 {
				out = append(out, issue(sevInfo, "tile_size", "%s is %dx%d px, not a multiple of the project's tile size %d.", what, fw, fh, t))
			}
		}
	}
	if b.PixelArt && b.BaseResolution != "" && a.Type == "sprite" {
		var bw, bh int
		if _, err := fmt.Sscanf(b.BaseResolution, "%dx%d", &bw, &bh); err == nil && (w > bw || h > bh) {
			out = append(out, issue(sevWarning, "base_resolution", "Sprite is %dx%d px, larger than the project's base resolution %s.", w, h, b.BaseResolution))
		}
	}
	return out
}

func isPow2(n int) bool { return n > 0 && n&(n-1) == 0 }

// Measurements derived from file content are cached by content hash, so that
// opening a project report over and over does not decode every image again.
var measureCache = struct {
	sync.Mutex
	palette map[string]paletteResult
	audio   map[string]*AudioInfo
}{palette: map[string]paletteResult{}, audio: map[string]*AudioInfo{}}

const measureCacheMax = 4096

type paletteResult struct {
	offShare float64
	opaque   int
	skipped  string // why the image was not examined
}

func (s *Service) fitPalette(ctx context.Context, a *Asset, p *File, b *ProjectBrief) []FitIssue {
	key := p.SHA256 + "|" + strings.Join(b.Palette, ",")
	measureCache.Lock()
	res, ok := measureCache.palette[key]
	measureCache.Unlock()
	if !ok {
		var err error
		res, err = s.measurePalette(ctx, filepath.Join(s.assetDir(a.ID), "files", p.Filename), b.Palette)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return []FitIssue{issue(sevInfo, "palette_unchecked", "Palette not checked: %v.", err)}
		}
		measureCache.Lock()
		if len(measureCache.palette) >= measureCacheMax {
			clear(measureCache.palette)
		}
		measureCache.palette[key] = res
		measureCache.Unlock()
	}
	if res.skipped != "" {
		return []FitIssue{issue(sevInfo, "palette_unchecked", "Palette not checked: %s.", res.skipped)}
	}
	if res.opaque == 0 {
		return nil
	}
	pct := res.offShare * 100
	msg := "%.1f%% of the opaque pixels are not on the project palette (more than %d RGB units from the nearest palette colour)."
	switch {
	case res.offShare > paletteErrorShare:
		return []FitIssue{issue(sevError, "palette", msg, pct, paletteTolerance)}
	case res.offShare > paletteWarningShare:
		return []FitIssue{issue(sevWarning, "palette", msg, pct, paletteTolerance)}
	}
	return nil
}

// measurePalette decodes the image (within maxDecodedBytes, one image at a
// time like thumbnails) and returns the share of opaque pixels that are off
// the palette. Very large images are sampled.
func (s *Service) measurePalette(ctx context.Context, path string, palette []string) (paletteResult, error) {
	release, err := s.acquireDecode(ctx)
	if err != nil {
		return paletteResult{}, err
	}
	defer release()
	fh, err := os.Open(path)
	if err != nil {
		return paletteResult{}, err
	}
	defer fh.Close()
	cfg, _, err := image.DecodeConfig(fh)
	if err != nil {
		return paletteResult{}, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || decodedSize(cfg) > maxDecodedBytes {
		return paletteResult{skipped: fmt.Sprintf("image of %dx%d pixels is too large to decode", cfg.Width, cfg.Height)}, nil
	}
	if _, err := fh.Seek(0, 0); err != nil {
		return paletteResult{}, err
	}
	img, _, err := image.Decode(fh)
	if err != nil {
		return paletteResult{}, err
	}
	pal := make([][3]int, 0, len(palette))
	for _, c := range palette {
		var r, g, bl int
		if _, err := fmt.Sscanf(c, "#%02x%02x%02x", &r, &g, &bl); err == nil {
			pal = append(pal, [3]int{r, g, bl})
		}
	}
	bnd := img.Bounds()
	step := 1
	if px := bnd.Dx() * bnd.Dy(); px > 4_000_000 {
		step = int(math.Ceil(math.Sqrt(float64(px) / 4_000_000)))
	}
	memo := map[uint32]bool{} // packed colour -> off palette (bounded)
	var opaque, off int
	for y := bnd.Min.Y; y < bnd.Max.Y; y += step {
		if y&63 == 0 && ctx.Err() != nil {
			return paletteResult{}, ctx.Err()
		}
		for x := bnd.Min.X; x < bnd.Max.X; x += step {
			r, g, bl, al := img.At(x, y).RGBA()
			if al < 0xff00 { // transparent or anti-aliased edge
				continue
			}
			opaque++
			key := r>>8<<16 | g>>8<<8 | bl>>8
			o, seen := memo[key]
			if !seen {
				o = true
				for _, c := range pal {
					dr, dg, db := int(r>>8)-c[0], int(g>>8)-c[1], int(bl>>8)-c[2]
					if dr*dr+dg*dg+db*db <= paletteTolerance*paletteTolerance {
						o = false
						break
					}
				}
				if len(memo) < 65536 {
					memo[key] = o
				}
			}
			if o {
				off++
			}
		}
	}
	res := paletteResult{opaque: opaque}
	if opaque > 0 {
		res.offShare = float64(off) / float64(opaque)
	}
	return res, nil
}

// ---- audio ----

// acquireDecode takes the one slot shared by every operation that reads
// whole media files (thumbnails, palette and audio measurements).
func (s *Service) acquireDecode(ctx context.Context) (func(), error) {
	select {
	case s.thumbSem <- struct{}{}:
		return func() { <-s.thumbSem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// audioAttrKeys are the attrs a measurement owns.
var audioAttrKeys = []string{"duration_s", "sample_rate", "channels", "bit_depth", "peak_dbfs", "loudness_approx_db"}

// audioFromAttrs rebuilds what an earlier measurement stored.
func audioFromAttrs(a *Asset, ext string) *AudioInfo {
	sr, ok1 := attrNumber(a.Attrs, "sample_rate")
	ch, ok2 := attrNumber(a.Attrs, "channels")
	if !ok1 || !ok2 {
		return nil
	}
	info := &AudioInfo{Format: strings.TrimPrefix(ext, "."), SampleRate: int(sr), Channels: int(ch)}
	if ext == ".opus" {
		info.Format = "opus"
	}
	if v, ok := attrNumber(a.Attrs, "bit_depth"); ok {
		info.BitDepth = int(v)
	}
	if pk, ok := a.Attrs["peak_dbfs"].(float64); ok {
		info.Measured, info.PeakDBFS = true, pk
		info.LoudnessApprox = dbfsFloor
		if l, ok := a.Attrs["loudness_approx_db"].(float64); ok {
			info.LoudnessApprox = l
		}
	}
	return info
}

func (s *Service) fitAudio(ctx context.Context, a *Asset, p *File, b *ProjectBrief, measure bool) []FitIssue {
	e := ext(p.Filename)
	var info *AudioInfo
	if !measure {
		if info = audioFromAttrs(a, e); info == nil {
			return []FitIssue{issue(sevInfo, "audio_unmeasured", "Audio not measured yet (sample rate, channels, peak, loudness); run check_asset_fit or open the asset's Fit section to measure it.")}
		}
	} else {
		key := p.SHA256 + e
		measureCache.Lock()
		cached, ok := measureCache.audio[key]
		measureCache.Unlock()
		if ok {
			info = cached
		} else {
			release, err := s.acquireDecode(ctx)
			if err != nil {
				return nil
			}
			var aerr error
			info, aerr = AnalyzeAudio(ctx, filepath.Join(s.assetDir(a.ID), "files", p.Filename))
			release()
			if ctx.Err() != nil {
				return nil
			}
			if aerr != nil {
				if isAudioHeaderFormat(e) {
					return []FitIssue{issue(sevInfo, "audio_unchecked", "Audio not checked: %v.", aerr)}
				}
				info = nil
			}
			measureCache.Lock()
			if len(measureCache.audio) >= measureCacheMax {
				clear(measureCache.audio)
			}
			measureCache.audio[key] = info
			measureCache.Unlock()
		}
		if info == nil {
			return []FitIssue{issue(sevInfo, "audio_unchecked", "Audio format %s is not checked (sample rate, channels and loudness are only read from WAV, Ogg Vorbis/Opus and FLAC).", e)}
		}
		s.storeAudioAttrs(a, p, info)
	}

	var out []FitIssue
	if t := b.Audio; t != nil {
		if t.SampleRate != nil && info.SampleRate != *t.SampleRate {
			if info.Format == "opus" {
				// Opus is always decoded at 48 kHz; the header's rate is only the original.
				if *t.SampleRate != 48000 {
					out = append(out, issue(sevInfo, "sample_rate", "Opus plays at 48000 Hz, project wants %d Hz.", *t.SampleRate))
				}
			} else {
				out = append(out, issue(sevWarning, "sample_rate", "%d Hz, project wants %d Hz.", info.SampleRate, *t.SampleRate))
			}
		}
		if t.Channels != nil && info.Channels != *t.Channels {
			sev := sevWarning
			if info.Channels == 1 && *t.Channels == 2 {
				sev = sevInfo // engines pan mono sounds anyway
			}
			out = append(out, issue(sev, "channels", "%d channel(s), project wants %d.", info.Channels, *t.Channels))
		}
		if info.Measured {
			if t.MaxPeakDBFS != nil && info.PeakDBFS > *t.MaxPeakDBFS {
				word := "exceeds"
				if info.PeakDBFS >= -0.1 {
					word = "reaches full scale and probably clips; it exceeds"
				}
				out = append(out, issue(sevWarning, "peak", "Peak %.1f dBFS %s the project's maximum of %.1f dBFS.", info.PeakDBFS, word, *t.MaxPeakDBFS))
			}
			if t.LoudnessLUFS != nil && info.LoudnessApprox > dbfsFloor {
				if diff := info.LoudnessApprox - *t.LoudnessLUFS; math.Abs(diff) > loudnessTolerance {
					sev := sevWarning
					if a.Type == "sfx" {
						sev = sevInfo // short effects are rarely loudness-normalised
					}
					out = append(out, issue(sev, "loudness", "Approximate loudness %.1f LUFS (energy of the non-silent parts, no K-weighting, not true LUFS) is %.1f dB %s the project's target of %.0f LUFS.",
						info.LoudnessApprox, math.Abs(diff), map[bool]string{true: "above", false: "below"}[diff > 0], *t.LoudnessLUFS))
				}
			}
		}
	}
	if measure && !info.Measured {
		out = append(out, issue(sevInfo, "audio_header_only", "Peak and loudness are only measured for PCM WAV; %s was checked by header only.", info.Format))
	}
	return out
}

func isAudioHeaderFormat(e string) bool {
	switch e {
	case ".wav", ".ogg", ".oga", ".opus", ".flac":
		return true
	}
	return false
}

// storeAudioAttrs writes measured values into attrs. It deliberately bypasses
// UpdateAsset: updated_at and the activity feed stay untouched, because
// measuring is not an edit. Values already present are kept, unless the primary
// file changed since the last measurement (recorded as measured_sha256), in
// which case they are replaced. Trashed assets are left alone.
func (s *Service) storeAudioAttrs(a *Asset, p *File, info *AudioInfo) {
	if a.DeletedAt != nil {
		return
	}
	measured := map[string]any{
		"duration_s":  math.Round(info.DurationS*1000) / 1000,
		"sample_rate": info.SampleRate,
		"channels":    info.Channels,
	}
	if info.BitDepth > 0 {
		measured["bit_depth"] = info.BitDepth
	}
	if info.Measured {
		measured["peak_dbfs"] = math.Round(info.PeakDBFS*10) / 10
		measured["loudness_approx_db"] = math.Round(info.LoudnessApprox*10) / 10
	}
	if info.DurationS <= 0 {
		delete(measured, "duration_s")
	}
	ctx := context.Background()
	changed := false
	unlock := s.lockAssets(a.ID)
	defer unlock()
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var raw string
		if err := tx.QueryRowContext(ctx, `SELECT attrs FROM assets WHERE id = ?`, a.ID).Scan(&raw); err != nil {
			return err
		}
		cur := map[string]any{}
		_ = json.Unmarshal([]byte(raw), &cur)
		prev, _ := cur["measured_sha256"].(string)
		stale := prev != "" && prev != p.SHA256
		if stale { // another file now: forget what the old one measured
			for _, k := range audioAttrKeys {
				if _, ok := measured[k]; !ok {
					if _, had := cur[k]; had {
						delete(cur, k)
						changed = true
					}
				}
			}
		}
		for k, v := range measured {
			if _, ok := cur[k]; !ok || stale {
				cur[k] = v
				changed = true
			}
		}
		if prev != p.SHA256 {
			cur["measured_sha256"] = p.SHA256
			changed = true
		}
		if !changed {
			return nil
		}
		blob, err := json.Marshal(cur)
		if err != nil || len(blob) > 64<<10 {
			changed = false
			return nil
		}
		_, err = tx.ExecContext(ctx, `UPDATE assets SET attrs = ? WHERE id = ?`, string(blob), a.ID)
		return err
	})
	if err != nil {
		s.log.Warn("store audio attrs", "asset", a.ID, "err", err)
		return
	}
	if changed {
		s.syncSidecar(ctx, a.ID)
	}
}
