package hoard

import (
	"context"
	"fmt"
	"image"
	"math"
	"regexp"
	"sort"
	"strings"
)

// Colours are searched through a coarse palette of buckets, so that "red"
// finds every reddish picture and not only one exact value. The palette has
// twelve hues in four variants (muted/vivid, dark/light) and five greys. A
// bucket is named by its canonical colour as a hex string.

const (
	// colorMatchWeight is the share of an image a bucket needs to count as a
	// colour of that image in a search.
	colorMatchWeight = 0.1
	maxImageColors   = 5
	// MaxColorFilters is the most colours one search may combine.
	MaxColorFilters = 10
	// colorMinWeight drops buckets too small to describe the picture.
	colorMinWeight = 0.03
)

// ColorWeight is one dominant colour bucket of an image.
type ColorWeight struct {
	Color  string  `json:"color"`  // canonical bucket colour, e.g. "#d94c4c"
	Weight float64 `json:"weight"` // share of the opaque pixels, 0..1
}

var reHexColor = regexp.MustCompile(`^#?([0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`)

// parseHexColor reads "#rgb" or "#rrggbb" (the hash sign is optional).
func parseHexColor(s string) (r, g, b uint8, err error) {
	m := reHexColor.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, 0, 0, invalid("colour %q is not a hex colour like #d94c4c", s)
	}
	h := m[1]
	if len(h) == 3 {
		h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
	}
	var v uint32
	fmt.Sscanf(h, "%06x", &v)
	return uint8(v >> 16), uint8(v >> 8), uint8(v), nil
}

// QuantizeColor maps a hex colour to the hex of its palette bucket.
func QuantizeColor(hex string) (string, error) {
	r, g, b, err := parseHexColor(hex)
	if err != nil {
		return "", err
	}
	return bucketOf(r, g, b), nil
}

func bucketOf(r, g, b uint8) string {
	rf, gf, bf := float64(r)/255, float64(g)/255, float64(b)/255
	mx := math.Max(rf, math.Max(gf, bf))
	mn := math.Min(rf, math.Min(gf, bf))
	v := mx
	var s float64
	if mx > 0 {
		s = (mx - mn) / mx
	}
	switch {
	case v < 0.16:
		return "#101010"
	case s < 0.16:
		switch {
		case v < 0.4:
			return "#404040"
		case v < 0.65:
			return "#808080"
		case v < 0.88:
			return "#c0c0c0"
		}
		return "#f0f0f0"
	}
	var h float64
	d := mx - mn
	switch mx {
	case rf:
		h = math.Mod((gf-bf)/d, 6)
	case gf:
		h = (bf-rf)/d + 2
	default:
		h = (rf-gf)/d + 4
	}
	h *= 60
	if h < 0 {
		h += 360
	}
	hue := (int(math.Round(h/30)) % 12) * 30
	sat, val := 0.85, 0.95
	if s < 0.5 {
		sat = 0.4
	}
	if v < 0.55 {
		val = 0.45
	}
	return hsvHex(float64(hue), sat, val)
}

func hsvHex(h, s, v float64) string {
	c := v * s
	x := c * (1 - math.Abs(math.Mod(h/60, 2)-1))
	m := v - c
	var r, g, b float64
	switch int(h/60) % 6 {
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
	to := func(f float64) int { return int(math.Round((f + m) * 255)) }
	return fmt.Sprintf("#%02x%02x%02x", to(r), to(g), to(b))
}

// dominantColors returns up to five colour buckets of an image, largest
// first. Pixels that are mostly transparent are ignored.
func dominantColors(img *image.NRGBA) []ColorWeight {
	b := img.Bounds()
	step := 1
	for (b.Dx()/step)*(b.Dy()/step) > 65536 {
		step++
	}
	counts := map[string]int{}
	total := 0
	for y := 0; y < b.Dy(); y += step {
		row := img.Pix[y*img.Stride:]
		for x := 0; x < b.Dx(); x += step {
			p := row[x*4 : x*4+4]
			if p[3] < 128 {
				continue
			}
			counts[bucketOf(p[0], p[1], p[2])]++
			total++
		}
	}
	if total == 0 {
		return nil
	}
	out := make([]ColorWeight, 0, len(counts))
	for c, n := range counts {
		if w := float64(n) / float64(total); w >= colorMinWeight {
			out = append(out, ColorWeight{Color: c, Weight: math.Round(w*1000) / 1000})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Weight != out[j].Weight {
			return out[i].Weight > out[j].Weight
		}
		return out[i].Color < out[j].Color
	})
	if len(out) > maxImageColors {
		out = out[:maxImageColors]
	}
	return out
}

// AssetColors returns the dominant colours stored for an asset, largest first.
func (s *Service) AssetColors(ctx context.Context, assetID string) ([]ColorWeight, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT color, weight FROM asset_colors WHERE asset_id = ? ORDER BY weight DESC, color`, assetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ColorWeight{}
	for rows.Next() {
		var c ColorWeight
		if err := rows.Scan(&c.Color, &c.Weight); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
