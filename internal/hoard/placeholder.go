package hoard

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

// Placeholders are generated stand-ins for assets that were requested but do
// not exist yet, so that a game can be built and tested meanwhile. Images are
// flat colour with a border and the title; audio is a short quiet beep.

const (
	placeholderDefaultSize = 64
	placeholderMaxSize     = 2048
	placeholderDefaultSecs = 1.0
	placeholderMaxSecs     = 30.0
	placeholderRate        = 22050
)

var (
	placeholderImageTypes = map[string]bool{"sprite": true, "ui": true, "tileset": true, "texture": true, "vfx": true}
	placeholderAudioTypes = map[string]bool{"sfx": true, "music": true, "voice": true}
)

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

// placeholderSem renders one placeholder at a time: a 2048x2048 image is 16 MB.
var placeholderSem = make(chan struct{}, 1)

// placeholderMu serialises MakePlaceholder per request (striped by id).
var placeholderMu [64]sync.Mutex

func placeholderLock(requestID string) *sync.Mutex {
	h := fnv.New32a()
	h.Write([]byte(requestID))
	return &placeholderMu[h.Sum32()%uint32(len(placeholderMu))]
}

func placeholderSupported(assetType string) error {
	t := strings.ToLower(strings.TrimSpace(assetType))
	if placeholderImageTypes[t] || placeholderAudioTypes[t] {
		return nil
	}
	return invalid("no placeholder can be generated for asset type %q: only sprite, ui, tileset, texture, vfx (PNG) and sfx, music, voice (WAV) are supported", assetType)
}

// specNumber reads a number from a request spec; JSON numbers arrive as
// float64 but numeric strings are accepted too.
func wkSpecNumber(spec map[string]any, keys ...string) (float64, bool) {
	for _, k := range keys {
		switch v := spec[k].(type) {
		case float64:
			if finite(v) {
				return v, true
			}
		case int:
			return float64(v), true
		case string:
			if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil && finite(f) {
				return f, true
			}
		}
	}
	return 0, false
}

// placeholderSize works out the pixel size from the spec: width and height,
// else the size of a sprite sheet from frame_width, frame_height, frames and
// columns, else 64x64. Values are clamped to 1..4096.
func placeholderSize(spec map[string]any) (w, h int, cellW, cellH int) {
	w, h = placeholderDefaultSize, placeholderDefaultSize
	clamp := func(f float64) int { return int(math.Max(1, math.Min(placeholderMaxSize, math.Round(f)))) }
	fw, okW := wkSpecNumber(spec, "frame_width")
	fh, okH := wkSpecNumber(spec, "frame_height")
	if okW && okH && fw >= 1 && fh >= 1 {
		frames, _ := wkSpecNumber(spec, "frames")
		cols, ok := wkSpecNumber(spec, "columns")
		if !ok || cols < 1 {
			cols = math.Max(1, frames)
		}
		rows := math.Ceil(math.Max(1, frames) / cols)
		w, h = clamp(cols*fw), clamp(rows*fh)
		cellW, cellH = clamp(fw), clamp(fh)
	}
	if v, ok := wkSpecNumber(spec, "width"); ok {
		w = clamp(v)
		cellW, cellH = 0, 0
	}
	if v, ok := wkSpecNumber(spec, "height"); ok {
		h = clamp(v)
		cellW, cellH = 0, 0
	}
	return
}

func placeholderSeconds(spec map[string]any) float64 {
	d, ok := wkSpecNumber(spec, "duration_s", "duration")
	if !ok || d <= 0 || math.IsNaN(d) {
		return placeholderDefaultSecs
	}
	return math.Min(d, placeholderMaxSecs)
}

// titleColor derives a stable, muted colour from the title.
func wkTitleColor(title string) color.NRGBA {
	h := fnv.New32a()
	h.Write([]byte(title))
	hue := float64(h.Sum32()%360) / 60
	const s, v = 0.45, 0.78
	c := v * s
	x := c * (1 - math.Abs(math.Mod(hue, 2)-1))
	m := v - c
	var r, g, b float64
	switch int(hue) {
	case 0:
		r, g, b = c, x, 0
	case 1:
		r, g, b = x, c, 0
	case 2:
		r, g, b = 0, c, x
	case 3:
		r, g, b = 0, x, c
	case 4:
		r, g, b = x, 0, c
	default:
		r, g, b = c, 0, x
	}
	return color.NRGBA{uint8((r + m) * 255), uint8((g + m) * 255), uint8((b + m) * 255), 255}
}

func wkDarken(c color.NRGBA, f float64) color.NRGBA {
	return color.NRGBA{uint8(float64(c.R) * f), uint8(float64(c.G) * f), uint8(float64(c.B) * f), 255}
}

// asciiLabel keeps the title drawable with the built-in bitmap font.
func wkASCII(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 0x20 && r < 0x7f {
			b.WriteRune(r)
		} else {
			b.WriteRune('?')
		}
	}
	return b.String()
}

// placeholderPNG draws the stand-in image: flat colour, a 1 px border (also
// around every frame of a sprite sheet) and the title when it fits.
func placeholderPNG(title string, w, h, cellW, cellH int) ([]byte, error) {
	base := wkTitleColor(title)
	edge := wkDarken(base, 0.55)
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), &image.Uniform{base}, image.Point{}, draw.Src)
	box := func(x0, y0, x1, y1 int) {
		for x := x0; x < x1; x++ {
			img.SetNRGBA(x, y0, edge)
			img.SetNRGBA(x, y1-1, edge)
		}
		for y := y0; y < y1; y++ {
			img.SetNRGBA(x0, y, edge)
			img.SetNRGBA(x1-1, y, edge)
		}
	}
	if cells := (w/max(cellW, 1) + 1) * (h/max(cellH, 1) + 1); cellW > 0 && cellH > 0 && (cellW < w || cellH < h) && cells <= 4096 {
		for y := 0; y < h; y += cellH {
			for x := 0; x < w; x += cellW {
				box(x, y, min(x+cellW, w), min(y+cellH, h))
			}
		}
	} else {
		box(0, 0, w, h)
	}
	// The title goes into the first frame only.
	tw, th := w, h
	if cellW > 0 && cellH > 0 {
		tw, th = min(cellW, w), min(cellH, h)
	}
	face := basicfont.Face7x13
	const adv, lineH = 7, 13
	if maxChars := (tw - 6) / adv; maxChars >= 1 && th >= lineH+4 {
		label := wkASCII(title)
		if len(label) > maxChars {
			if maxChars > 3 {
				label = label[:maxChars-3] + "..."
			} else {
				label = label[:maxChars]
			}
		}
		fg := color.NRGBA{255, 255, 255, 255}
		if lum := 0.299*float64(base.R) + 0.587*float64(base.G) + 0.114*float64(base.B); lum > 150 {
			fg = color.NRGBA{20, 20, 20, 255}
		}
		d := &font.Drawer{Dst: img, Src: &image.Uniform{fg}, Face: face,
			Dot: fixed.P((tw-len(label)*adv)/2, th/2+lineH/2-3)}
		d.DrawString(label)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// placeholderWAV builds mono 16-bit PCM: a quiet sine beep at the start and,
// for clips of 0.6 s or more, another at the end, silence in between. The
// pitch is derived from the title so different placeholders are told apart.
func placeholderWAV(title string, seconds float64) []byte {
	h := fnv.New32a()
	h.Write([]byte(title))
	freq := 330 + float64(h.Sum32()%550)
	n := int(seconds * placeholderRate)
	samples := make([]int16, n)
	beep := func(start, length int) {
		for i := 0; i < length && start+i < n; i++ {
			env := 1.0
			if f := placeholderRate / 100; i < f { // 10 ms fades avoid clicks
				env = float64(i) / float64(f)
			} else if i > length-f {
				env = float64(length-i) / float64(f)
			}
			samples[start+i] = int16(0.15 * env * math.Sin(2*math.Pi*freq*float64(i)/placeholderRate) * 32767)
		}
	}
	length := min(n, placeholderRate/5) // 0.2 s
	beep(0, length)
	if seconds >= 0.6 {
		beep(n-length, length)
	}
	var buf bytes.Buffer
	dataLen := uint32(n * 2)
	buf.WriteString("RIFF")
	_ = binary.Write(&buf, binary.LittleEndian, 36+dataLen)
	buf.WriteString("WAVEfmt ")
	for _, v := range []any{uint32(16), uint16(1), uint16(1), uint32(placeholderRate), uint32(placeholderRate * 2), uint16(2), uint16(16)} {
		_ = binary.Write(&buf, binary.LittleEndian, v)
	}
	buf.WriteString("data")
	_ = binary.Write(&buf, binary.LittleEndian, dataLen)
	_ = binary.Write(&buf, binary.LittleEndian, samples)
	return buf.Bytes()
}

var reSlugRun = regexp.MustCompile(`[^a-z0-9]+`)

func wkSlug(s string) string {
	s = strings.Trim(reSlugRun.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(s) > 60 {
		s = strings.Trim(s[:60], "-")
	}
	if s == "" {
		s = "placeholder"
	}
	return s
}

// placeholderFilename uses spec.filename (sanitised, extension forced to what
// is generated) or a slug of the title.
func placeholderFilename(spec map[string]any, title, ext string) string {
	if f, ok := spec["filename"].(string); ok && strings.TrimSpace(f) != "" {
		f = SanitizeFilename(f)
		if i := strings.LastIndex(f, "."); i > 0 {
			f = f[:i]
		}
		if f != "" && f != "file" {
			return f + ext
		}
	}
	return wkSlug(title) + ext
}

// MakePlaceholder generates a placeholder asset for a request and links it
// (placeholder_asset_id). The asset has status placeholder, is named
// "[placeholder] <title>", belongs to the request's project and carries the
// request description as prompt. Image types (sprite, ui, tileset, texture,
// vfx) get a PNG of spec width x height (default 64x64, at most 4096), audio
// types (sfx, music, voice) a WAV of spec duration_s (default 1 s, at most
// 30 s). If the request already has a live placeholder it is returned as is.
// When the request is accepted the placeholder is superseded.
func (s *Service) MakePlaceholder(ctx context.Context, requestID string) (*Asset, error) {
	mu := placeholderLock(requestID)
	mu.Lock()
	defer mu.Unlock()
	r, err := s.GetRequest(ctx, requestID)
	if err != nil {
		return nil, err
	}
	if err := requestFinished(r); err != nil {
		return nil, err
	}
	if r.Kind == "rework" {
		return nil, invalid("request %s is a rework request: the asset to redo already exists", requestID)
	}
	if err := placeholderSupported(r.AssetType); err != nil {
		return nil, err
	}
	if r.PlaceholderAssetID != "" {
		if a, err := s.GetAsset(ctx, r.PlaceholderAssetID); err == nil && a.DeletedAt == nil {
			return a, nil
		}
	}

	var data []byte
	var ext string
	attrs := map[string]any{}
	for _, k := range reworkSpecKeys {
		if v, ok := r.Spec[k]; ok {
			attrs[k] = v
		}
	}
	if placeholderImageTypes[r.AssetType] {
		w, h, cw, ch := placeholderSize(r.Spec)
		placeholderSem <- struct{}{}
		data, err = placeholderPNG(r.Title, w, h, cw, ch)
		<-placeholderSem
		if err != nil {
			return nil, err
		}
		ext = ".png"
	} else {
		secs := placeholderSeconds(r.Spec)
		data, ext = placeholderWAV(r.Title, secs), ".wav"
		attrs["duration_s"] = math.Round(secs*100) / 100
		attrs["sample_rate"], attrs["channels"], attrs["bit_depth"] = placeholderRate, 1, 16
	}
	in := AssetInput{
		Name: wkStr("[placeholder] " + wkTruncate(r.Title, 250)), Type: wkStr(r.AssetType), Status: wkStr("placeholder"),
		Description: wkStr("Stand-in generated by hoard for request " + r.ID + ". Replace it by accepting a real asset for that request."),
		Source:      wkStr("hoard placeholder"), License: wkStr("CC0-1.0"), AddTags: []string{"placeholder"}, Attrs: attrs,
	}
	if r.Description != "" {
		in.Prompt = wkStr(r.Description)
	}
	if r.ProjectID != "" {
		in.AddProjects = []string{r.ProjectID}
		in.ProjectNote = "placeholder for request " + r.ID
	}
	a, err := s.CreateAsset(ctx, in)
	if err != nil {
		return nil, err
	}
	if _, err := s.AddFile(ctx, a.ID, placeholderFilename(r.Spec, r.Title, ext), bytes.NewReader(data), 0); err != nil {
		_, _ = s.Bulk(context.WithoutCancel(ctx), BulkOp{IDs: []string{a.ID}, Action: "purge"})
		return nil, err
	}
	var prev any
	if r.PlaceholderAssetID != "" {
		prev = r.PlaceholderAssetID
	}
	linked := false
	err = s.tx(ctx, func(tx *sql.Tx) error {
		// Link only if nobody else did meanwhile.
		res, err := tx.ExecContext(ctx, `UPDATE asset_requests SET placeholder_asset_id = ?, updated_at = ? WHERE id = ? AND placeholder_asset_id IS ?`,
			a.ID, now(), requestID, prev)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			linked = true
			s.record(ctx, tx, "request.placeholder", a.ID, requestID, r.Title)
		}
		return nil
	})
	if err == nil && !linked {
		// Lost a race (should not happen under the lock): drop the orphan, return the winner.
		_, _ = s.Bulk(context.WithoutCancel(ctx), BulkOp{IDs: []string{a.ID}, Action: "purge"})
		if cur, gerr := s.GetRequest(ctx, requestID); gerr == nil && cur.PlaceholderAssetID != "" {
			return s.GetAsset(ctx, cur.PlaceholderAssetID)
		}
		return nil, fmt.Errorf("link placeholder: request %s changed meanwhile: %w", requestID, ErrConflict)
	}
	if err != nil {
		_, _ = s.Bulk(context.WithoutCancel(ctx), BulkOp{IDs: []string{a.ID}, Action: "purge"})
		return nil, fmt.Errorf("link placeholder: %w", err)
	}
	return s.GetAsset(ctx, a.ID)
}

func wkStr(s string) *string { return &s }
