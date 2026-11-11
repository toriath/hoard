package hoard

// Texture atlas packing: the primary images of several assets are packed into
// one PNG with MaxRects, and delivered with metadata for Godot, Phaser or as
// plain JSON. Source images are decoded one at a time through thumbSem and
// within maxDecodedBytes each and maxAtlasSourceBytes in total.

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"io"
	"math"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	maxAtlasAssets      = 256
	maxAtlasSize        = 8192
	maxAtlasSourceBytes = 256 << 20 // estimated decoded size of all sources together
)

// AtlasFormats are the metadata flavours.
var AtlasFormats = []string{"json", "phaser", "godot"}

// AtlasOptions control packing and output.
type AtlasOptions struct {
	Padding    int    `json:"padding"`      // transparent pixels between sprites, 0..32
	MaxSize    int    `json:"max_size"`     // longest allowed edge of the atlas, up to 8192
	PowerOfTwo bool   `json:"power_of_two"` // round the atlas to powers of two
	Extrude    int    `json:"extrude"`      // replicate each sprite's edge pixels n times, 0..4
	Format     string `json:"format"`       // json | phaser | godot
	ResPath    string `json:"res_path"`     // godot only: where atlas.png will live, e.g. res://art/atlas.png
}

var reResPath = regexp.MustCompile(`^res://[A-Za-z0-9_\-./ ]{1,200}\.png$`)

// Normalize validates the options and fills defaults.
func (o AtlasOptions) Normalize() (AtlasOptions, error) {
	if o.MaxSize == 0 {
		o.MaxSize = 2048
	}
	if o.MaxSize < 16 || o.MaxSize > maxAtlasSize {
		return o, invalid("max_size must be between 16 and %d", maxAtlasSize)
	}
	if o.Padding < 0 || o.Padding > 32 {
		return o, invalid("padding must be between 0 and 32")
	}
	if o.Extrude < 0 || o.Extrude > 4 {
		return o, invalid("extrude must be between 0 and 4")
	}
	if o.Format == "" {
		o.Format = "json"
	}
	ok := false
	for _, f := range AtlasFormats {
		ok = ok || f == o.Format
	}
	if !ok {
		return o, invalid("format must be one of %s", strings.Join(AtlasFormats, ", "))
	}
	if o.ResPath == "" {
		o.ResPath = "res://atlas.png"
	}
	if !reResPath.MatchString(o.ResPath) || strings.Contains(o.ResPath, "..") {
		return o, invalid("res_path must look like res://art/atlas.png")
	}
	return o, nil
}

// AtlasPayload is the string a signed atlas link covers: asset ids (in order)
// and every option.
func AtlasPayload(ids []string, o AtlasOptions) string {
	return fmt.Sprintf("atlas:%s|p=%d|m=%d|pot=%t|x=%d|f=%s|r=%s", strings.Join(ids, ","), o.Padding, o.MaxSize, o.PowerOfTwo, o.Extrude, o.Format, o.ResPath)
}

// AtlasQuery encodes ids and options as URL parameters.
func AtlasQuery(ids []string, o AtlasOptions) url.Values {
	q := url.Values{}
	q.Set("ids", strings.Join(ids, ","))
	q.Set("padding", strconv.Itoa(o.Padding))
	q.Set("max_size", strconv.Itoa(o.MaxSize))
	q.Set("power_of_two", strconv.FormatBool(o.PowerOfTwo))
	q.Set("extrude", strconv.Itoa(o.Extrude))
	q.Set("format", o.Format)
	q.Set("res_path", o.ResPath)
	return q
}

// ParseAtlasQuery is the inverse of AtlasQuery. Options are normalised, so the
// payload of what is parsed is what a link signs.
func ParseAtlasQuery(q url.Values) ([]string, AtlasOptions, error) {
	var o AtlasOptions
	ids, err := ParseIDList(q.Get("ids"), maxAtlasAssets)
	if err != nil {
		return nil, o, err
	}
	atoi := func(k string) (int, error) {
		if q.Get(k) == "" {
			return 0, nil
		}
		n, err := strconv.Atoi(q.Get(k))
		if err != nil {
			return 0, invalid("%s must be a number", k)
		}
		return n, nil
	}
	if o.Padding, err = atoi("padding"); err != nil {
		return nil, o, err
	}
	if o.MaxSize, err = atoi("max_size"); err != nil {
		return nil, o, err
	}
	if o.Extrude, err = atoi("extrude"); err != nil {
		return nil, o, err
	}
	o.PowerOfTwo = q.Get("power_of_two") == "true" || q.Get("power_of_two") == "1"
	o.Format, o.ResPath = q.Get("format"), q.Get("res_path")
	o, err = o.Normalize()
	return ids, o, err
}

// ParseIDList splits a comma-separated list of asset ids, validating each and
// dropping duplicates.
func ParseIDList(s string, max int) ([]string, error) {
	var ids []string
	seen := map[string]bool{}
	for _, id := range strings.Split(s, ",") {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		if !ValidAssetID(id) {
			return nil, invalid("invalid asset id %q", id)
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, invalid("ids is required")
	}
	if len(ids) > max {
		return nil, invalid("at most %d assets per atlas", max)
	}
	return ids, nil
}

// AtlasFrame is one packed sprite.
type AtlasFrame struct {
	Name    string `json:"name"`
	AssetID string `json:"asset_id"`
	X       int    `json:"x"`
	Y       int    `json:"y"`
	W       int    `json:"w"`
	H       int    `json:"h"`
}

// Atlas is a packed texture with its metadata.
type Atlas struct {
	Width, Height int
	PNG           []byte
	Frames        []AtlasFrame
	Options       AtlasOptions
}

type packRect struct{ x, y, w, h int }

// maxRects packs sizes into a bin using MaxRects with the best-short-side-fit
// heuristic. Positions are returned in the order of the input.
func maxRects(bw, bh int, sizes [][2]int, order []int) ([]packRect, bool) {
	free := []packRect{{0, 0, bw, bh}}
	out := make([]packRect, len(sizes))
	for _, i := range order {
		w, h := sizes[i][0], sizes[i][1]
		best, bestShort, bestLong := -1, math.MaxInt, math.MaxInt
		for fi, f := range free {
			if w > f.w || h > f.h {
				continue
			}
			short := min(f.w-w, f.h-h)
			long := max(f.w-w, f.h-h)
			if short < bestShort || (short == bestShort && long < bestLong) {
				best, bestShort, bestLong = fi, short, long
			}
		}
		if best < 0 {
			return nil, false
		}
		p := packRect{free[best].x, free[best].y, w, h}
		out[i] = p
		var next []packRect
		for _, f := range free {
			if p.x >= f.x+f.w || p.x+p.w <= f.x || p.y >= f.y+f.h || p.y+p.h <= f.y {
				next = append(next, f)
				continue
			}
			if p.x > f.x {
				next = append(next, packRect{f.x, f.y, p.x - f.x, f.h})
			}
			if p.x+p.w < f.x+f.w {
				next = append(next, packRect{p.x + p.w, f.y, f.x + f.w - p.x - p.w, f.h})
			}
			if p.y > f.y {
				next = append(next, packRect{f.x, f.y, f.w, p.y - f.y})
			}
			if p.y+p.h < f.y+f.h {
				next = append(next, packRect{f.x, p.y + p.h, f.w, f.y + f.h - p.y - p.h})
			}
		}
		// Drop free rectangles contained in another one.
		free = free[:0]
	outer:
		for i, a := range next {
			for j, b := range next {
				if i != j && a.x >= b.x && a.y >= b.y && a.x+a.w <= b.x+b.w && a.y+a.h <= b.y+b.h && (a != b || j < i) {
					continue outer
				}
			}
			free = append(free, a)
		}
	}
	return out, true
}

func nextPow2(n int) int {
	p := 1
	for p < n {
		p <<= 1
	}
	return p
}

// packLayout finds the smallest atlas that holds all sizes (each including its
// extrude and padding) and returns positions and the final atlas size.
func packLayout(sizes [][2]int, o AtlasOptions) (pos []packRect, w, h int, err error) {
	order := make([]int, len(sizes))
	area, maxW, maxH := 0, 0, 0
	for i, s := range sizes {
		order[i] = i
		area += s[0] * s[1]
		maxW, maxH = max(maxW, s[0]), max(maxH, s[1])
	}
	sort.SliceStable(order, func(a, b int) bool {
		sa, sb := sizes[order[a]], sizes[order[b]]
		if ma, mb := max(sa[0], sa[1]), max(sb[0], sb[1]); ma != mb {
			return ma > mb
		}
		return sa[0]*sa[1] > sb[0]*sb[1]
	})
	pad := o.Padding
	fail := invalid("the sprites do not fit into %dx%d pixels; raise max_size or pack fewer sprites", o.MaxSize, o.MaxSize)
	if maxW-pad > o.MaxSize || maxH-pad > o.MaxSize {
		return nil, 0, 0, fail
	}
	if o.PowerOfTwo {
		type cand struct{ w, h int }
		var cands []cand
		for cw := 1; cw <= o.MaxSize; cw <<= 1 {
			for ch := 1; ch <= o.MaxSize; ch <<= 1 {
				if cw*ch >= area-pad*(cw+ch) && cw+pad >= maxW && ch+pad >= maxH {
					cands = append(cands, cand{cw, ch})
				}
			}
		}
		sort.Slice(cands, func(i, j int) bool {
			ai, aj := cands[i].w*cands[i].h, cands[j].w*cands[j].h
			if ai != aj {
				return ai < aj
			}
			return abs(cands[i].w-cands[i].h) < abs(cands[j].w-cands[j].h)
		})
		for _, c := range cands {
			if p, ok := maxRects(c.w+pad, c.h+pad, sizes, order); ok {
				return p, c.w, c.h, nil
			}
		}
		return nil, 0, 0, fail
	}
	side := max(int(math.Ceil(math.Sqrt(float64(area)))), maxW, maxH)
	for {
		side = min(side, o.MaxSize+pad)
		if p, ok := maxRects(side, side, sizes, order); ok {
			for _, r := range p {
				w, h = max(w, r.x+r.w-pad), max(h, r.y+r.h-pad)
			}
			return p, w, h, nil
		}
		if side >= o.MaxSize+pad {
			return nil, 0, 0, fail
		}
		side += max(1, side/32)
	}
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// atlasSem allows one atlas build at a time: a build holds the canvas and the
// encoded PNG, up to several hundred MB, so parallel signed fetches must queue.
var atlasSem = make(chan struct{}, 1)

type atlasItem struct {
	assetID, name, filename string
	w, h                    int
}

// atlasItems validates the assets and reads their image sizes without
// decoding any pixels, within the decode limits.
func (s *Service) atlasItems(ctx context.Context, ids []string, o AtlasOptions) ([]atlasItem, error) {
	if len(ids) == 0 {
		return nil, invalid("ids is required")
	}
	if len(ids) > maxAtlasAssets {
		return nil, invalid("at most %d assets per atlas", maxAtlasAssets)
	}
	var items []atlasItem
	var total int64
	usedNames := map[string]bool{}
	for _, id := range ids {
		a, err := s.GetAsset(ctx, id)
		if err != nil {
			return nil, err
		}
		if a.DeletedAt != nil {
			return nil, invalid("asset %s is in the trash", id)
		}
		f := a.Primary()
		if f == nil || !decodableImage[ext(f.Filename)] {
			return nil, invalid("asset %s (%s) has no PNG, JPEG, GIF, WebP, BMP or TIFF primary file", id, a.Name)
		}
		cfg, err := s.imageConfig(ctx, id, f.Filename)
		if err != nil {
			return nil, invalid("asset %s (%s): %v", id, a.Name, err)
		}
		size := decodedSize(cfg)
		if cfg.Width <= 0 || cfg.Height <= 0 || size > maxDecodedBytes {
			return nil, invalid("asset %s (%s): image of %dx%d pixels is too large", id, a.Name, cfg.Width, cfg.Height)
		}
		if total += size; total > maxAtlasSourceBytes {
			return nil, invalid("the sources together exceed the decode budget of %d MiB; pack fewer or smaller images", maxAtlasSourceBytes>>20)
		}
		name := nameStem(a.Name)
		if name == "" || name == "file" {
			name = strings.TrimPrefix(id, "ast_")
		}
		base := name
		for n := 2; usedNames[strings.ToLower(name)]; n++ {
			name = fmt.Sprintf("%s_%d", base, n)
		}
		usedNames[strings.ToLower(name)] = true
		items = append(items, atlasItem{assetID: id, name: name, filename: f.Filename, w: cfg.Width, h: cfg.Height})
	}
	return items, nil
}

func (s *Service) imageConfig(ctx context.Context, assetID, filename string) (image.Config, error) {
	fh, _, err := s.OpenFile(ctx, assetID, filename)
	if err != nil {
		return image.Config{}, err
	}
	defer fh.Close()
	cfg, _, err := image.DecodeConfig(fh)
	if err != nil {
		return cfg, fmt.Errorf("cannot read image: %v", err)
	}
	return cfg, nil
}

// layoutAtlas places the items and returns their frames (without pixel data).
func layoutAtlas(items []atlasItem, o AtlasOptions) ([]packRect, *Atlas, error) {
	e, pad := o.Extrude, o.Padding
	sizes := make([][2]int, len(items))
	for i, it := range items {
		sizes[i] = [2]int{it.w + 2*e + pad, it.h + 2*e + pad}
	}
	pos, w, h, err := packLayout(sizes, o)
	if err != nil {
		return nil, nil, err
	}
	if decodedSize(image.Config{Width: w, Height: h}) > maxDecodedBytes {
		return nil, nil, invalid("the atlas would be too large to build")
	}
	atlas := &Atlas{Width: w, Height: h, Options: o}
	for i, it := range items {
		atlas.Frames = append(atlas.Frames, AtlasFrame{Name: it.name, AssetID: it.assetID, X: pos[i].x + e, Y: pos[i].y + e, W: it.w, H: it.h})
	}
	return pos, atlas, nil
}

// PlanAtlas computes the layout only: frames and size, without decoding any
// pixels or building the texture. The result has no PNG.
func (s *Service) PlanAtlas(ctx context.Context, ids []string, o AtlasOptions) (*Atlas, error) {
	o, err := o.Normalize()
	if err != nil {
		return nil, err
	}
	items, err := s.atlasItems(ctx, ids, o)
	if err != nil {
		return nil, err
	}
	_, atlas, err := layoutAtlas(items, o)
	return atlas, err
}

// PackAtlas packs the primary images of the given assets. Only formats the
// server can decode (PNG, JPEG, GIF, WebP, BMP, TIFF) are accepted. Builds are
// serialised; sources are decoded one at a time and dropped after drawing.
func (s *Service) PackAtlas(ctx context.Context, ids []string, o AtlasOptions) (*Atlas, error) {
	o, err := o.Normalize()
	if err != nil {
		return nil, err
	}
	select {
	case atlasSem <- struct{}{}:
		defer func() { <-atlasSem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	items, err := s.atlasItems(ctx, ids, o)
	if err != nil {
		return nil, err
	}
	pos, atlas, err := layoutAtlas(items, o)
	if err != nil {
		return nil, err
	}
	e := o.Extrude
	canvas := image.NewNRGBA(image.Rect(0, 0, atlas.Width, atlas.Height))
	for i, it := range items {
		img, err := s.decodeOne(ctx, it.assetID, it.filename)
		if err != nil {
			return nil, invalid("asset %s: %v", it.assetID, err)
		}
		b := img.Bounds()
		sw, sh := b.Dx(), b.Dy()
		if sw != it.w || sh != it.h {
			return nil, invalid("asset %s changed while packing", it.assetID)
		}
		x0, y0 := pos[i].x+e, pos[i].y+e
		draw.Draw(canvas, image.Rect(x0, y0, x0+sw, y0+sh), img, b.Min, draw.Src)
		for dy := -e; dy < sh+e; dy++ {
			for dx := -e; dx < sw+e; dx++ {
				if dx >= 0 && dx < sw && dy >= 0 && dy < sh {
					continue
				}
				cx, cy := min(max(dx, 0), sw-1), min(max(dy, 0), sh-1)
				canvas.Set(x0+dx, y0+dy, img.At(b.Min.X+cx, b.Min.Y+cy))
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, canvas); err != nil {
		return nil, err
	}
	atlas.PNG = buf.Bytes()
	return atlas, nil
}

// decodeOne decodes one stored image under the global decode lock.
func (s *Service) decodeOne(ctx context.Context, assetID, filename string) (image.Image, error) {
	select {
	case s.thumbSem <- struct{}{}:
		defer func() { <-s.thumbSem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	fh, _, err := s.OpenFile(ctx, assetID, filename)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	cfg, _, err := image.DecodeConfig(fh)
	if err != nil {
		return nil, fmt.Errorf("cannot read image: %v", err)
	}
	if decodedSize(cfg) > maxDecodedBytes {
		return nil, fmt.Errorf("image of %dx%d pixels is too large", cfg.Width, cfg.Height)
	}
	if _, err := fh.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	img, _, err := image.Decode(fh)
	if err != nil {
		return nil, fmt.Errorf("cannot decode image: %v", err)
	}
	return img, nil
}

// WriteZip writes atlas.png and the metadata for the chosen format.
func (a *Atlas) WriteZip(w io.Writer) error {
	zw := zip.NewWriter(w)
	add := func(name string, data []byte) error {
		f, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = f.Write(data)
		return err
	}
	if err := add("atlas.png", a.PNG); err != nil {
		return err
	}
	switch a.Options.Format {
	case "godot":
		for _, f := range a.Frames {
			tres := fmt.Sprintf("[gd_resource type=\"AtlasTexture\" load_steps=2 format=3]\n\n"+
				"[ext_resource type=\"Texture2D\" path=\"%s\" id=\"1_atlas\"]\n\n"+
				"[resource]\natlas = ExtResource(\"1_atlas\")\nregion = Rect2(%d, %d, %d, %d)\n", a.Options.ResPath, f.X, f.Y, f.W, f.H)
			if err := add("sprites/"+f.Name+".tres", []byte(tres)); err != nil {
				return err
			}
		}
	case "phaser":
		frames := map[string]any{}
		for _, f := range a.Frames {
			frames[f.Name] = map[string]any{
				"frame":            map[string]int{"x": f.X, "y": f.Y, "w": f.W, "h": f.H},
				"rotated":          false,
				"trimmed":          false,
				"spriteSourceSize": map[string]int{"x": 0, "y": 0, "w": f.W, "h": f.H},
				"sourceSize":       map[string]int{"w": f.W, "h": f.H},
			}
		}
		doc := map[string]any{"frames": frames, "meta": map[string]any{
			"app": "hoard", "version": "1.0", "image": "atlas.png", "format": "RGBA8888",
			"size": map[string]int{"w": a.Width, "h": a.Height}, "scale": "1"}}
		b, _ := json.MarshalIndent(doc, "", "  ")
		if err := add("atlas.json", b); err != nil {
			return err
		}
	default:
		doc := map[string]any{"image": "atlas.png", "width": a.Width, "height": a.Height, "frames": a.Frames}
		b, _ := json.MarshalIndent(doc, "", "  ")
		if err := add("atlas.json", b); err != nil {
			return err
		}
	}
	return zw.Close()
}

// nameStem turns an asset name into a safe file stem, dropping a file
// extension the name may carry ("coin.png").
func nameStem(name string) string {
	n := SanitizeFilename(name)
	if e := filepath.Ext(n); e != "" {
		if _, ok := extTable[strings.ToLower(e)]; ok {
			n = strings.TrimSuffix(n, e)
		}
	}
	return n
}
