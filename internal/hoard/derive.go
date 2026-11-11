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
	"strconv"

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

// Derive returns the path of the cached derivative of one stored file,
// rendering it first when needed. The file name is resolved through the index.
func (s *Service) Derive(ctx context.Context, assetID, filename string, o DeriveOptions) (path, mime string, err error) {
	o, err = o.Normalize()
	if err != nil {
		return "", "", err
	}
	if !ValidAssetID(assetID) {
		return "", "", fmt.Errorf("asset %q: %w", assetID, ErrNotFound)
	}
	if !decodableImage[ext(filename)] {
		return "", "", invalid("%s is not an image the server can decode (PNG, JPEG, GIF, WebP, BMP, TIFF)", filename)
	}
	fh, f, err := s.OpenFile(ctx, assetID, filename)
	if err != nil {
		return "", "", err
	}
	defer fh.Close()
	sum := sha256.Sum256([]byte(o.Payload() + "|" + f.SHA256))
	dir := s.derivedDir(assetID)
	path = filepath.Join(dir, hex.EncodeToString(sum[:16])+o.DeriveExt())
	mime = "image/png"
	if o.Format == "jpeg" {
		mime = "image/jpeg"
	}
	if _, err := os.Stat(path); err == nil {
		return path, mime, nil
	}

	select {
	case s.thumbSem <- struct{}{}:
		defer func() { <-s.thumbSem }()
	case <-ctx.Done():
		return "", "", ctx.Err()
	}
	if _, err := os.Stat(path); err == nil { // rendered while we waited
		return path, mime, nil
	}
	cfg, _, err := image.DecodeConfig(fh)
	if err != nil {
		return "", "", invalid("cannot read image: %v", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || decodedSize(cfg) > maxDecodedBytes {
		return "", "", invalid("image of %dx%d pixels is too large to scale", cfg.Width, cfg.Height)
	}
	ow, oh, err := o.DeriveSize(cfg.Width, cfg.Height)
	if err != nil {
		return "", "", err
	}
	if decodedSize(image.Config{Width: ow, Height: oh}) > maxDecodedBytes {
		return "", "", invalid("the result would be too large to render")
	}
	if _, err := fh.Seek(0, io.SeekStart); err != nil {
		return "", "", err
	}
	src, _, err := image.Decode(fh)
	if err != nil {
		return "", "", invalid("cannot decode image: %v", err)
	}
	dst := image.NewNRGBA(image.Rect(0, 0, ow, oh))
	var scaler draw.Interpolator = draw.BiLinear
	if o.Filter == "nearest" {
		scaler = draw.NearestNeighbor
	}
	scaler.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Src, nil)

	var buf bytes.Buffer
	if o.Format == "jpeg" {
		// JPEG has no alpha: composite over white.
		flat := image.NewNRGBA(dst.Bounds())
		draw.Draw(flat, flat.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
		draw.Draw(flat, flat.Bounds(), dst, image.Point{}, draw.Over)
		err = jpeg.Encode(&buf, flat, &jpeg.Options{Quality: o.Quality})
	} else {
		err = png.Encode(&buf, dst)
	}
	if err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", "", err
	}
	if err := writeFileAtomic(path, buf.Bytes()); err != nil {
		return "", "", err
	}
	return path, mime, nil
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
