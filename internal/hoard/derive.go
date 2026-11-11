package hoard

// Derivatives: scaled copies of an asset's image, rendered on demand and
// cached below <data>/cache/derived/<asset-id>/. The cache key contains every
// parameter and the content hash of the source file, so a replaced file never
// serves a stale copy. Go has no WebP encoder in x/image, so PNG and JPEG only.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"golang.org/x/image/draw"
)

const maxDeriveEdge = 8192

// DeriveOptions describe one derivative. Exactly one of Scale or Width/Height
// must be set.
type DeriveOptions struct {
	Width   int     `json:"width,omitempty"`
	Height  int     `json:"height,omitempty"`
	Scale   float64 `json:"scale,omitempty"`
	Filter  string  `json:"filter"` // nearest | linear
	Format  string  `json:"format"` // png | jpeg
	Quality int     `json:"quality,omitempty"`
}

// Normalize validates and fills defaults. The filter must be set (callers
// resolve the default with DefaultDeriveFilter).
func (o DeriveOptions) Normalize() (DeriveOptions, error) {
	if o.Format == "" {
		o.Format = "png"
	}
	if o.Format == "jpg" {
		o.Format = "jpeg"
	}
	if o.Format != "png" && o.Format != "jpeg" {
		return o, invalid("format must be png or jpeg")
	}
	if o.Filter == "" {
		o.Filter = "linear"
	}
	if o.Filter != "nearest" && o.Filter != "linear" {
		return o, invalid("filter must be nearest or linear")
	}
	if o.Format == "jpeg" {
		if o.Quality == 0 {
			o.Quality = 85
		}
		if o.Quality < 1 || o.Quality > 100 {
			return o, invalid("quality must be between 1 and 100")
		}
	} else {
		o.Quality = 0
	}
	if o.Width < 0 || o.Height < 0 || o.Width > maxDeriveEdge || o.Height > maxDeriveEdge {
		return o, invalid("width and height must be between 1 and %d", maxDeriveEdge)
	}
	if math.IsNaN(o.Scale) || o.Scale < 0 || o.Scale > maxDeriveEdge {
		return o, invalid("scale must be a positive number")
	}
	if o.Scale > 0 && (o.Width > 0 || o.Height > 0) {
		return o, invalid("give either scale or width/height, not both")
	}
	if o.Scale == 0 && o.Width == 0 && o.Height == 0 {
		return o, invalid("give a scale, a width or a height")
	}
	return o, nil
}

// Payload is what a signed derivative link covers besides the asset and file.
func (o DeriveOptions) Payload() string {
	return fmt.Sprintf("w=%d|h=%d|s=%s|f=%s|fmt=%s|q=%d", o.Width, o.Height, strconv.FormatFloat(o.Scale, 'g', -1, 64), o.Filter, o.Format, o.Quality)
}

// Query encodes the options as URL parameters.
func (o DeriveOptions) Query() url.Values {
	q := url.Values{}
	if o.Width > 0 {
		q.Set("width", strconv.Itoa(o.Width))
	}
	if o.Height > 0 {
		q.Set("height", strconv.Itoa(o.Height))
	}
	if o.Scale > 0 {
		q.Set("scale", strconv.FormatFloat(o.Scale, 'g', -1, 64))
	}
	q.Set("filter", o.Filter)
	q.Set("format", o.Format)
	if o.Quality > 0 {
		q.Set("quality", strconv.Itoa(o.Quality))
	}
	return q
}

// ParseDeriveQuery is the inverse of Query, normalised.
func ParseDeriveQuery(q url.Values) (DeriveOptions, error) {
	var o DeriveOptions
	num := func(k string) (int, error) {
		if q.Get(k) == "" {
			return 0, nil
		}
		n, err := strconv.Atoi(q.Get(k))
		if err != nil {
			return 0, invalid("%s must be a whole number", k)
		}
		return n, nil
	}
	var err error
	if o.Width, err = num("width"); err != nil {
		return o, err
	}
	if o.Height, err = num("height"); err != nil {
		return o, err
	}
	if o.Quality, err = num("quality"); err != nil {
		return o, err
	}
	if v := q.Get("scale"); v != "" {
		if o.Scale, err = strconv.ParseFloat(v, 64); err != nil {
			return o, invalid("scale must be a number")
		}
	}
	o.Filter, o.Format = q.Get("filter"), q.Get("format")
	return o.Normalize()
}

// DeriveExt is the file extension of the derivative.
func (o DeriveOptions) DeriveExt() string {
	if o.Format == "jpeg" {
		return ".jpg"
	}
	return ".png"
}

// DefaultDeriveFilter picks nearest for pixel art (the asset says so, or one
// of its projects' briefs) and for integer scales of 2 or more, else linear.
func (s *Service) DefaultDeriveFilter(ctx context.Context, a *Asset, scale float64) string {
	if scale >= 2 && scale == math.Trunc(scale) {
		return "nearest"
	}
	if v, ok := a.Attrs["pixel_art"].(bool); ok && v {
		return "nearest"
	}
	for _, t := range a.Tags {
		if t == "pixel-art" || t == "pixelart" || t == "pixel_art" {
			return "nearest"
		}
	}
	for _, p := range a.Projects {
		if s.readBrief(ctx, p.ID).PixelArt {
			return "nearest"
		}
	}
	return "linear"
}

// DeriveSize computes the output size for a source of w x h.
func (o DeriveOptions) DeriveSize(w, h int) (int, int, error) {
	if w <= 0 || h <= 0 {
		return 0, 0, invalid("source image has no size")
	}
	ow, oh := o.Width, o.Height
	switch {
	case o.Scale > 0:
		ow, oh = int(math.Round(float64(w)*o.Scale)), int(math.Round(float64(h)*o.Scale))
	case ow > 0 && oh == 0:
		oh = int(math.Round(float64(h) * float64(ow) / float64(w)))
	case oh > 0 && ow == 0:
		ow = int(math.Round(float64(w) * float64(oh) / float64(h)))
	}
	ow, oh = max(ow, 1), max(oh, 1)
	if ow > maxDeriveEdge || oh > maxDeriveEdge {
		return 0, 0, invalid("the result would be %dx%d pixels; the limit is %d on each edge", ow, oh, maxDeriveEdge)
	}
	return ow, oh, nil
}

func (s *Service) derivedDir(assetID string) string {
	return filepath.Join(s.dataDir, "cache", "derived", assetID)
}

// derivedCacheMaxPixels: larger results are streamed from a temporary file
// instead of being cached. Variable so that tests can shrink it.
var derivedCacheMaxPixels = 16_000_000

const (
	// derivedMemBudget bounds source plus result pixels held at once.
	derivedMemBudget = 512 << 20
)

// derivedCacheBudget is the most disk the derivative cache may use. Variable
// so that tests can shrink it.
var derivedCacheBudget int64 = 1 << 30

// DerivedFile is a rendered derivative. Close it after serving: a Temp file is
// deleted, a cached one stays.
type DerivedFile struct {
	Path string
	Mime string
	Temp bool
}

// Close removes a temporary derivative.
func (d *DerivedFile) Close() {
	if d != nil && d.Temp {
		_ = os.Remove(d.Path)
	}
}

// Derive renders (or finds in the cache) a derivative of one stored file.
// The file name is resolved through the index; trashed assets are refused.
func (s *Service) Derive(ctx context.Context, assetID, filename string, o DeriveOptions) (*DerivedFile, error) {
	o, err := o.Normalize()
	if err != nil {
		return nil, err
	}
	if !ValidAssetID(assetID) {
		return nil, fmt.Errorf("asset %q: %w", assetID, ErrNotFound)
	}
	if !decodableImage[ext(filename)] {
		return nil, invalid("%s is not an image the server can decode (PNG, JPEG, GIF, WebP, BMP, TIFF)", filename)
	}
	var trashed bool
	if err := s.db.QueryRowContext(ctx, `SELECT deleted_at IS NOT NULL FROM assets WHERE id = ?`, assetID).Scan(&trashed); err != nil {
		return nil, fmt.Errorf("asset %q: %w", assetID, ErrNotFound)
	}
	if trashed {
		return nil, invalid("asset %s is in the trash; restore it first", assetID)
	}
	fh, f, err := s.OpenFile(ctx, assetID, filename)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	sum := sha256.Sum256([]byte(o.Payload() + "|" + f.SHA256))
	dir := s.derivedDir(assetID)
	cached := filepath.Join(dir, hex.EncodeToString(sum[:16])+o.DeriveExt())
	mime := "image/png"
	if o.Format == "jpeg" {
		mime = "image/jpeg"
	}
	hit := func() bool {
		if _, err := os.Stat(cached); err != nil {
			return false
		}
		now := time.Now()
		_ = os.Chtimes(cached, now, now) // recently used entries are evicted last
		return true
	}
	if hit() {
		return &DerivedFile{Path: cached, Mime: mime}, nil
	}

	select {
	case s.thumbSem <- struct{}{}:
		defer func() { <-s.thumbSem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if hit() { // rendered while we waited
		return &DerivedFile{Path: cached, Mime: mime}, nil
	}
	cfg, _, err := image.DecodeConfig(fh)
	if err != nil {
		return nil, invalid("cannot read image: %v", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || decodedSize(cfg) > maxDecodedBytes {
		return nil, invalid("image of %dx%d pixels is too large to scale", cfg.Width, cfg.Height)
	}
	ow, oh, err := o.DeriveSize(cfg.Width, cfg.Height)
	if err != nil {
		return nil, err
	}
	if decodedSize(cfg)+decodedSize(image.Config{Width: ow, Height: oh}) > derivedMemBudget {
		return nil, invalid("source and result together would need too much memory (%dx%d to %dx%d)", cfg.Width, cfg.Height, ow, oh)
	}
	if _, err := fh.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	src, _, err := image.Decode(fh)
	if err != nil {
		return nil, invalid("cannot decode image: %v", err)
	}
	dst := image.NewNRGBA(image.Rect(0, 0, ow, oh))
	var scaler draw.Interpolator = draw.BiLinear
	if o.Filter == "nearest" {
		scaler = draw.NearestNeighbor
	}
	var buf bytes.Buffer
	if o.Format == "jpeg" {
		// JPEG has no alpha: scale straight onto a white canvas.
		draw.Draw(dst, dst.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
		scaler.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Over, nil)
		err = jpeg.Encode(&buf, dst, &jpeg.Options{Quality: o.Quality})
	} else {
		scaler.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Src, nil)
		err = png.Encode(&buf, dst)
	}
	if err != nil {
		return nil, err
	}
	if ow*oh > derivedCacheMaxPixels {
		tmp, err := os.CreateTemp(filepath.Join(s.dataDir, "tmp"), "derive-*"+o.DeriveExt())
		if err != nil {
			return nil, err
		}
		_, werr := tmp.Write(buf.Bytes())
		if cerr := tmp.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			_ = os.Remove(tmp.Name())
			return nil, werr
		}
		return &DerivedFile{Path: tmp.Name(), Mime: mime, Temp: true}, nil
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	if err := writeFileAtomic(cached, buf.Bytes()); err != nil {
		return nil, err
	}
	s.evictDerived(cached)
	return &DerivedFile{Path: cached, Mime: mime}, nil
}

// evictDerived keeps the cache within derivedCacheBudget by deleting the
// least recently used entries; keep is never deleted.
func (s *Service) evictDerived(keep string) {
	root := filepath.Join(s.dataDir, "cache", "derived")
	type entry struct {
		path string
		size int64
		at   time.Time
	}
	var all []entry
	var total int64
	dirs, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, d := range dirs {
		if !d.IsDir() || !ValidAssetID(d.Name()) {
			continue
		}
		files, err := os.ReadDir(filepath.Join(root, d.Name()))
		if err != nil {
			continue
		}
		for _, f := range files {
			info, err := f.Info()
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			all = append(all, entry{filepath.Join(root, d.Name(), f.Name()), info.Size(), info.ModTime()})
			total += info.Size()
		}
	}
	if total <= derivedCacheBudget {
		return
	}
	sort.Slice(all, func(i, j int) bool { return all[i].at.Before(all[j].at) })
	for _, e := range all {
		if total <= derivedCacheBudget {
			break
		}
		if e.path == keep {
			continue
		}
		if os.Remove(e.path) == nil {
			total -= e.size
		}
	}
}

// removeDerived deletes the cached derivatives of a purged asset.
func (s *Service) removeDerived(assetID string) {
	if ValidAssetID(assetID) {
		if err := os.RemoveAll(s.derivedDir(assetID)); err != nil {
			s.log.Warn("remove derivative cache", "asset", assetID, "err", err)
		}
	}
}

// SweepDerived removes cache directories of assets that no longer exist
// (e.g. purged while the server was not running). Call at startup.
func (s *Service) SweepDerived(ctx context.Context) {
	root := filepath.Join(s.dataDir, "cache", "derived")
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		id := e.Name()
		if !ValidAssetID(id) {
			continue
		}
		var one int
		if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM assets WHERE id = ?`, id).Scan(&one); errors.Is(err, sql.ErrNoRows) {
			s.removeDerived(id)
		}
	}
}
