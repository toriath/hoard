package hoard

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"os"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// Server-side font specimens for TrueType and OpenType files. WOFF and WOFF2
// are not supported by golang.org/x/image and are left to the browser.

var fontPreview = map[string]bool{".ttf": true, ".otf": true, ".ttc": true}

// maxFontBytes bounds the memory used to parse a font.
const maxFontBytes = 32 << 20

var (
	fallbackOnce sync.Once
	fallbackFont *opentype.Font
)

func fallback() *opentype.Font {
	fallbackOnce.Do(func() { fallbackFont, _ = opentype.Parse(goregular.TTF) })
	return fallbackFont
}

// hasGlyphs reports whether the font has a glyph for every rune of s.
func hasGlyphs(f *opentype.Font, s string) bool {
	var b sfnt.Buffer
	for _, r := range s {
		if r == ' ' {
			continue
		}
		if g, err := f.GlyphIndex(&b, r); err != nil || g == 0 {
			return false
		}
	}
	return true
}

func fontName(f *opentype.Font) string {
	var b sfnt.Buffer
	for _, id := range []sfnt.NameID{sfnt.NameIDFull, sfnt.NameIDFamily} {
		if n, err := f.Name(&b, id); err == nil && n != "" {
			return n
		}
	}
	return ""
}

// drawCentered writes s centred on the baseline y, shrinking the type until
// it fits the width. It returns false when nothing could be drawn.
func drawCentered(img *image.NRGBA, f *opentype.Font, s string, size float64, y int, col color.Color) bool {
	for try := 0; try < 12; try++ {
		face, err := opentype.NewFace(f, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingNone})
		if err != nil {
			return false
		}
		d := &font.Drawer{Dst: img, Src: image.NewUniform(col), Face: face}
		w := d.MeasureString(s)
		if w.Ceil() <= thumbSize-32 || size < 8 {
			d.Dot = fixed.Point26_6{X: fixed.I(thumbSize/2) - w/2, Y: fixed.I(y)}
			d.DrawString(s)
			face.Close()
			return true
		}
		face.Close()
		size *= 0.85
	}
	return false
}

// renderSpecimen draws "Aa Bb Cc", digits and the font's name in the colours
// of the web UI's own font thumbnails.
func renderSpecimen(f *opentype.Font) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, thumbSize, thumbSize))
	bg := color.NRGBA{0x1e, 0x1c, 0x18, 0xff}
	for i := 0; i < len(img.Pix); i += 4 {
		copy(img.Pix[i:], []uint8{bg.R, bg.G, bg.B, bg.A})
	}
	bright := color.NRGBA{0xf1, 0xed, 0xe4, 0xff}
	dim := color.NRGBA{0xa9, 0xa2, 0x94, 0xff}
	name := fontName(f)
	if hasGlyphs(f, "AaBbCc0123") {
		drawCentered(img, f, "Aa Bb Cc", 120, 200, bright)
		drawCentered(img, f, "0123456789", 60, 300, dim)
	}
	if name != "" {
		nf := f
		if !hasGlyphs(f, name) {
			nf = fallback()
		}
		y := 440
		if !hasGlyphs(f, "AaBbCc0123") {
			y = thumbSize / 2
		}
		drawCentered(img, nf, name, 34, y, dim)
	}
	return img
}

// fontPreviewFile writes a specimen thumbnail when the asset has none.
func (s *Service) fontPreviewFile(ctx context.Context, assetID, path string) (err error) {
	needThumb, _, err := s.previewNeeds(ctx, assetID)
	if err != nil || !needThumb {
		return err
	}
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if st.Size() > maxFontBytes {
		return fmt.Errorf("font of %d bytes is too large to make a specimen from", st.Size())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	release, err := s.acquireThumb(ctx)
	if err != nil {
		return err
	}
	defer release()
	defer func() { // the parser is fed untrusted files
		if r := recover(); r != nil {
			err = fmt.Errorf("font parser failed: %v", r)
		}
	}()
	var f *opentype.Font
	if coll, cerr := opentype.ParseCollection(data); cerr == nil && coll.NumFonts() > 0 {
		f, err = coll.Font(0)
	} else {
		f, err = opentype.Parse(data)
	}
	if err != nil {
		return err
	}
	return s.putThumb(ctx, assetID, renderSpecimen(f))
}
