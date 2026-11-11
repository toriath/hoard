package hoard

import (
	"bytes"
	"compress/zlib"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"html"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Generator metadata import. When a file is added, hoard looks for the recipe
// that image generators and audio-studio leave behind and records it on the
// asset: prompt and the structured generator. Best effort: nothing here can
// fail an upload, only empty fields are filled, and reads are bounded.

const (
	genmetaMaxText   = 1 << 20 // bytes of text chunks / sidecar read per file
	genmetaMaxChunks = 4096    // PNG chunks inspected
)

// afterFileAdded is called by AddFile once the file is stored and indexed.
func (s *Service) afterFileAdded(ctx context.Context, assetID string, f File, path string) {
	defer func() {
		if r := recover(); r != nil {
			s.log.Warn("generator metadata import panicked", "asset", assetID, "file", f.Filename, "panic", r)
		}
	}()
	var imp *imported
	switch ext(f.Filename) {
	case ".png":
		imp = importPNG(path)
	case ".json":
		imp = s.importAudioSidecar(ctx, assetID, f.Filename, "")
	default:
		// An audio file arriving after its sidecar.
		imp = s.importAudioSidecar(ctx, assetID, "", f.Filename)
	}
	if imp == nil || (imp.prompt == "" && len(imp.gen) == 0) {
		return
	}
	if err := s.applyImported(ctx, assetID, imp); err != nil {
		s.log.Warn("generator metadata import", "asset", assetID, "file", f.Filename, "err", err)
	}
}

type imported struct {
	prompt string
	gen    map[string]any
}

// applyImported fills empty fields only: the prompt when it is empty, and
// every generator key the asset does not have yet.
func (s *Service) applyImported(ctx context.Context, assetID string, imp *imported) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		var prompt, genJSON string
		if err := tx.QueryRowContext(ctx, `SELECT prompt, generator FROM assets WHERE id = ?`, assetID).Scan(&prompt, &genJSON); err != nil {
			return err
		}
		gen := map[string]any{}
		if genJSON != "" {
			_ = json.Unmarshal([]byte(genJSON), &gen)
		}
		changed := false
		if strings.TrimSpace(prompt) == "" && imp.prompt != "" {
			prompt, changed = imp.prompt, true
		}
		for k, v := range imp.gen {
			if cur, ok := gen[k]; ok && !isEmptyValue(cur) {
				continue
			}
			gen[k], changed = v, true
		}
		if !changed {
			return nil
		}
		b, err := json.Marshal(gen)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE assets SET prompt = ?, generator = ?, updated_at = ? WHERE id = ?`, prompt, string(b), now(), assetID); err != nil {
			return err
		}
		s.record(ctx, tx, "asset.update", assetID, "", "generator metadata imported")
		return reindex(ctx, tx, assetID)
	})
}

func isEmptyValue(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(x) == ""
	case map[string]any:
		return len(x) == 0
	case []any:
		return len(x) == 0
	}
	return false
}

// ---- PNG ----

type textChunk struct{ keyword, text string }

// readPNGText returns the text chunks (tEXt, zTXt, iTXt) of a PNG without
// reading image data. At most genmetaMaxText bytes of chunk data are read.
func readPNGText(r io.ReadSeeker) []textChunk {
	sig := make([]byte, 8)
	if _, err := io.ReadFull(r, sig); err != nil || string(sig) != "\x89PNG\r\n\x1a\n" {
		return nil
	}
	var out []textChunk
	budget := int64(genmetaMaxText)
	for i := 0; i < genmetaMaxChunks; i++ {
		var hdr [8]byte
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			break
		}
		n := int64(binary.BigEndian.Uint32(hdr[:4]))
		typ := string(hdr[4:])
		if typ == "IEND" {
			break
		}
		if typ != "tEXt" && typ != "zTXt" && typ != "iTXt" {
			if _, err := r.Seek(n+4, io.SeekCurrent); err != nil {
				break
			}
			continue
		}
		if n > budget {
			if _, err := r.Seek(n+4, io.SeekCurrent); err != nil {
				break
			}
			continue
		}
		budget -= n
		data := make([]byte, n)
		if _, err := io.ReadFull(r, data); err != nil {
			break
		}
		if _, err := r.Seek(4, io.SeekCurrent); err != nil { // CRC
			break
		}
		if c, ok := parseTextChunk(typ, data); ok {
			out = append(out, c)
		}
	}
	return out
}

func parseTextChunk(typ string, d []byte) (textChunk, bool) {
	z := bytes.IndexByte(d, 0)
	if z <= 0 {
		return textChunk{}, false
	}
	key, rest := string(d[:z]), d[z+1:]
	var text []byte
	switch typ {
	case "tEXt":
		text = rest
	case "zTXt":
		if len(rest) < 1 {
			return textChunk{}, false
		}
		text = inflate(rest[1:])
	case "iTXt":
		if len(rest) < 2 {
			return textChunk{}, false
		}
		compressed := rest[0] == 1
		rest = rest[2:]
		for range 2 { // language tag, translated keyword
			i := bytes.IndexByte(rest, 0)
			if i < 0 {
				return textChunk{}, false
			}
			rest = rest[i+1:]
		}
		text = rest
		if compressed {
			text = inflate(rest)
		}
	}
	if text == nil {
		return textChunk{}, false
	}
	return textChunk{keyword: key, text: strings.ToValidUTF8(string(text), "")}, true
}

func inflate(b []byte) []byte {
	zr, err := zlib.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil
	}
	defer zr.Close()
	out, _ := io.ReadAll(io.LimitReader(zr, genmetaMaxText))
	return out
}

func importPNG(path string) *imported {
	fh, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer fh.Close()
	return importFromChunks(readPNGText(fh))
}

// importFromChunks interprets text chunks: Automatic1111 "parameters",
// ComfyUI "prompt"/"workflow", and Draw Things JSON.
func importFromChunks(chunks []textChunk) *imported {
	var comfy, workflow string
	var drawThings *imported
	var a1111 *imported
	for _, c := range chunks {
		switch {
		case c.keyword == "parameters":
			if a1111 == nil {
				a1111 = parseA1111(c.text)
			}
		case c.keyword == "prompt" && strings.HasPrefix(strings.TrimSpace(c.text), "{"):
			comfy = c.text
		case c.keyword == "workflow" && strings.HasPrefix(strings.TrimSpace(c.text), "{"):
			workflow = c.text
		default:
			if drawThings == nil {
				drawThings = parseDrawThings(c.text)
			}
		}
	}
	switch {
	case a1111 != nil:
		return a1111
	case comfy != "" || workflow != "":
		return parseComfy(comfy, workflow != "")
	}
	return drawThings
}

var a1111Pair = regexp.MustCompile(`\s*([A-Za-z][\w .\-]*?):\s*("(?:\\.|[^\\"])*"|[^,]*)(?:,|$)`)

// parseA1111 reads the Automatic1111 / Forge "parameters" text:
//
//	<prompt>
//	Negative prompt: <negative>
//	Steps: 20, Sampler: Euler a, CFG scale: 7, Seed: 1234, Size: 512x768, Model: name, …
func parseA1111(text string) *imported {
	text = strings.TrimSpace(text)
	lines := strings.Split(text, "\n")
	last := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "Steps:") {
			last = i
			break
		}
	}
	body := lines
	var settings string
	if last >= 0 {
		body, settings = lines[:last], strings.TrimSpace(lines[last])
	}
	var prompt, neg []string
	inNeg := false
	for _, l := range body {
		if strings.HasPrefix(l, "Negative prompt:") {
			inNeg = true
			l = strings.TrimSpace(strings.TrimPrefix(l, "Negative prompt:"))
		}
		if inNeg {
			neg = append(neg, l)
		} else {
			prompt = append(prompt, l)
		}
	}
	imp := &imported{prompt: strings.TrimSpace(strings.Join(prompt, "\n")), gen: map[string]any{"tool": "Automatic1111"}}
	if n := strings.TrimSpace(strings.Join(neg, "\n")); n != "" {
		imp.gen["negative_prompt"] = n
	}
	extra := map[string]any{}
	for _, m := range a1111Pair.FindAllStringSubmatch(settings, -1) {
		k, v := strings.TrimSpace(m[1]), strings.Trim(strings.TrimSpace(m[2]), `"`)
		switch strings.ToLower(k) {
		case "steps":
			imp.gen["steps"] = numOrString(v)
		case "sampler":
			imp.gen["sampler"] = v
		case "cfg scale":
			imp.gen["cfg"] = numOrString(v)
		case "seed":
			imp.gen["seed"] = numOrString(v)
		case "model":
			imp.gen["model"] = v
		case "size":
			if w, h, ok := strings.Cut(v, "x"); ok {
				if wi, err := strconv.Atoi(w); err == nil {
					imp.gen["width"] = wi
				}
				if hi, err := strconv.Atoi(h); err == nil {
					imp.gen["height"] = hi
				}
			}
		default:
			if len(extra) < 40 && v != "" {
				extra[k] = v
			}
		}
	}
	if len(extra) > 0 {
		imp.gen["settings"] = extra
	}
	if imp.prompt == "" && len(imp.gen) == 1 {
		return nil
	}
	return imp
}

func numOrString(v string) any {
	if i, err := strconv.ParseInt(v, 10, 64); err == nil {
		return i
	}
	if f, err := strconv.ParseFloat(v, 64); err == nil {
		return f
	}
	return v
}

// parseComfy reads the ComfyUI "prompt" graph: only what is trivially
// findable (first KSampler, its text encoders, checkpoint, latent size).
func parseComfy(promptJSON string, hasWorkflow bool) *imported {
	imp := &imported{gen: map[string]any{"tool": "ComfyUI"}}
	if hasWorkflow {
		imp.gen["settings"] = map[string]any{"comfyui_workflow_embedded": true}
	}
	var graph map[string]struct {
		Class  string         `json:"class_type"`
		Inputs map[string]any `json:"inputs"`
	}
	if promptJSON == "" || json.Unmarshal([]byte(promptJSON), &graph) != nil {
		return imp
	}
	ids := make([]string, 0, len(graph))
	for id := range graph {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	textOf := func(link any) string {
		ref, ok := link.([]any)
		if !ok || len(ref) == 0 {
			return ""
		}
		id, _ := ref[0].(string)
		if id == "" {
			if f, ok := ref[0].(float64); ok {
				id = strconv.Itoa(int(f))
			}
		}
		t, _ := graph[id].Inputs["text"].(string)
		return t
	}
	for _, id := range ids {
		n := graph[id]
		switch {
		case strings.HasPrefix(n.Class, "KSampler"):
			if _, done := imp.gen["sampler"]; done {
				continue
			}
			for _, k := range [][2]string{{"seed", "seed"}, {"noise_seed", "seed"}, {"steps", "steps"}, {"cfg", "cfg"}, {"sampler_name", "sampler"}} {
				if v, ok := n.Inputs[k[0]]; ok {
					if _, isLink := v.([]any); !isLink {
						imp.gen[k[1]] = v
					}
				}
			}
			imp.prompt = textOf(n.Inputs["positive"])
			if neg := textOf(n.Inputs["negative"]); neg != "" {
				imp.gen["negative_prompt"] = neg
			}
		case n.Class == "CheckpointLoaderSimple" || n.Class == "CheckpointLoader":
			if v, ok := n.Inputs["ckpt_name"].(string); ok {
				imp.gen["model"] = v
			}
		case n.Class == "EmptyLatentImage":
			if w, ok := n.Inputs["width"].(float64); ok {
				imp.gen["width"] = int(w)
			}
			if h, ok := n.Inputs["height"].(float64); ok {
				imp.gen["height"] = int(h)
			}
		}
	}
	return imp
}

// parseDrawThings is a tolerant reader for Draw Things metadata. The format
// is not verified against the app: it looks for a JSON object with a prompt
// ("c") and generation keys ("uc", "seed", "steps", "scale", "sampler",
// "model", "size"), either as the whole text of a chunk or inside the
// exif:UserComment of an XMP packet.
func parseDrawThings(text string) *imported {
	cand := text
	if i := strings.Index(text, "<exif:UserComment"); i >= 0 {
		rest := text[i:]
		if j := strings.Index(rest, "</exif:UserComment>"); j > 0 {
			cand = html.UnescapeString(rest[:j])
		}
	}
	a, b := strings.Index(cand, "{"), strings.LastIndex(cand, "}")
	if a < 0 || b <= a {
		return nil
	}
	dec := json.NewDecoder(strings.NewReader(cand[a : b+1]))
	dec.UseNumber()
	var m map[string]any
	if dec.Decode(&m) != nil {
		return nil
	}
	prompt, ok := m["c"].(string)
	if !ok {
		return nil
	}
	imp := &imported{prompt: strings.TrimSpace(prompt), gen: map[string]any{"tool": "Draw Things"}}
	found := false
	for _, k := range [][2]string{{"seed", "seed"}, {"steps", "steps"}, {"scale", "cfg"}, {"sampler", "sampler"}, {"model", "model"}, {"uc", "negative_prompt"}} {
		if v, ok := m[k[0]]; ok && !isEmptyValue(v) {
			imp.gen[k[1]] = v
			if k[0] != "uc" {
				found = true
			}
		}
	}
	if !found {
		return nil
	}
	if sz, ok := m["size"].(string); ok {
		if w, h, ok := strings.Cut(strings.ToLower(sz), "x"); ok {
			if wi, err := strconv.Atoi(strings.TrimSpace(w)); err == nil {
				imp.gen["width"] = wi
			}
			if hi, err := strconv.Atoi(strings.TrimSpace(h)); err == nil {
				imp.gen["height"] = hi
			}
		}
	}
	return imp
}

// ---- audio-studio sidecars ----

// importAudioSidecar handles the <name>.json that audio-studio writes next to
// <name>.wav. Pass the sidecar's file name when it was just added, or the
// name of another file that was just added to look for its sidecar.
func (s *Service) importAudioSidecar(ctx context.Context, assetID, sidecar, added string) *imported {
	rows, err := s.db.QueryContext(ctx, `SELECT filename FROM asset_files WHERE asset_id = ?`, assetID)
	if err != nil {
		return nil
	}
	var names []string
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil {
			names = append(names, n)
		}
	}
	rows.Close()

	stemOf := func(n string) string { return strings.TrimSuffix(n, filepath.Ext(n)) }
	if sidecar != "" {
		stem := stemOf(sidecar)
		found := false
		for _, n := range names {
			if n != sidecar && ext(n) != ".json" && strings.EqualFold(stemOf(n), stem) {
				found = true
			}
		}
		if !found {
			return nil
		}
	} else {
		stem := stemOf(added)
		for _, n := range names {
			if ext(n) == ".json" && strings.EqualFold(stemOf(n), stem) {
				sidecar = n
			}
		}
		if sidecar == "" {
			return nil
		}
	}
	fh, err := os.Open(filepath.Join(s.assetDir(assetID), "files", sidecar))
	if err != nil {
		return nil
	}
	defer fh.Close()
	return parseAudioSidecar(io.LimitReader(fh, genmetaMaxText))
}

func parseAudioSidecar(r io.Reader) *imported {
	dec := json.NewDecoder(r)
	dec.UseNumber()
	var m map[string]any
	if dec.Decode(&m) != nil {
		return nil
	}
	prompt, _ := m["prompt"].(string)
	seed, hasSeed := m["seed"]
	if strings.TrimSpace(prompt) == "" || !hasSeed {
		return nil
	}
	imp := &imported{prompt: strings.TrimSpace(prompt), gen: map[string]any{"tool": "audio-studio", "seed": seed}}
	if v, ok := m["audio_type"].(string); ok && v != "" {
		imp.gen["audio_type"] = v
	}
	if v, ok := m["duration"].(json.Number); ok {
		imp.gen["duration_s"] = v
	}
	if v, ok := m["negative_prompt"].(string); ok && v != "" {
		imp.gen["negative_prompt"] = v
	}
	if set, ok := m["settings"].(map[string]any); ok && len(set) > 0 {
		imp.gen["settings"] = set
		for _, k := range [][2]string{{"steps", "steps"}, {"cfg", "cfg"}, {"model", "model"}} {
			if v, ok := set[k[0]]; ok && !isEmptyValue(v) {
				imp.gen[k[1]] = v
			}
		}
	}
	return imp
}
