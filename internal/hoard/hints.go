package hoard

// Engine import hints: recommended import settings per asset for Godot 4,
// Unity and Phaser, and a generated Godot SpriteFrames resource for sprite
// sheets. The hints are advice from general engine knowledge; none of it has
// been checked against a running engine (see CLAUDE.md, "Verified and not").

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// HintsOptions select the engine and the Godot resource directory.
type HintsOptions struct {
	Engine    string // godot | unity | phaser; default from the project's brief
	ProjectID string
	ResDir    string // godot: directory the files will have, e.g. res://art/sprites
}

// FileHints are the recommendations for one file.
type FileHints struct {
	Filename     string         `json:"filename"`
	Settings     map[string]any `json:"settings,omitempty"`      // import settings by their editor labels
	ImportParams map[string]any `json:"import_params,omitempty"` // godot: keys of the [params] section of the .import file
	Snippet      string         `json:"snippet,omitempty"`       // code to load the file
	Notes        []string       `json:"notes,omitempty"`
}

// AssetHints are the recommendations for one asset.
type AssetHints struct {
	AssetID          string      `json:"asset_id"`
	Name             string      `json:"name"`
	Type             string      `json:"type"`
	Files            []FileHints `json:"files"`
	Notes            []string    `json:"notes,omitempty"`
	SpriteFramesName string      `json:"sprite_frames_filename,omitempty"`
	SpriteFrames     string      `json:"sprite_frames_tres,omitempty"` // godot: write this to the file named above
}

// HintsResult is the answer of EngineHints.
type HintsResult struct {
	Engine       string       `json:"engine"`
	EngineSource string       `json:"engine_source"`
	ProjectNotes []string     `json:"project_notes,omitempty"`
	Assets       []AssetHints `json:"assets"`
}

var reResDir = regexp.MustCompile(`^res://[A-Za-z0-9_\-./ ]{0,200}$`)

func normEngine(e string) string {
	switch strings.ToLower(strings.TrimSpace(e)) {
	case "godot":
		return "godot"
	case "unity":
		return "unity"
	case "phaser", "web", "phaser3":
		return "phaser"
	}
	return ""
}

func deliveryAttrNum(attrs map[string]any, key string) (float64, bool) {
	switch v := attrs[key].(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	}
	return 0, false
}

func deliveryAttrBool(attrs map[string]any, key string) (val, set bool) {
	v, ok := attrs[key].(bool)
	return v, ok
}

// EngineHints returns import recommendations for the given assets.
func (s *Service) EngineHints(ctx context.Context, ids []string, o HintsOptions) (*HintsResult, error) {
	res := &HintsResult{Assets: []AssetHints{}}
	var brief deliveryBrief
	if o.ProjectID != "" {
		if err := s.projectExists(ctx, o.ProjectID); err != nil {
			return nil, err
		}
		brief = s.readBrief(ctx, o.ProjectID)
	}
	switch e := normEngine(o.Engine); {
	case e != "":
		res.Engine, res.EngineSource = e, "argument"
	case normEngine(brief.Engine) != "":
		res.Engine, res.EngineSource = normEngine(brief.Engine), "project brief"
	default:
		return nil, invalid("engine is required (godot, unity or phaser): pass it, or pass a project_id whose brief names one")
	}
	if o.ResDir != "" && (!reResDir.MatchString(o.ResDir) || strings.Contains(o.ResDir, "..")) {
		return nil, invalid("res_dir must look like res://art/sprites")
	}
	if len(ids) == 0 || len(ids) > 100 {
		return nil, invalid("give between 1 and 100 asset ids")
	}

	pixelProject := brief.PixelArt
	switch res.Engine {
	case "godot":
		if pixelProject {
			res.ProjectNotes = append(res.ProjectNotes, "Pixel art project: set Project Settings > Rendering > Textures > Canvas Textures > Default Texture Filter to Nearest (setting rendering/textures/canvas_textures/default_texture_filter); the filter is a project or node setting in Godot 4, not an import option.")
		}
	case "phaser":
		if pixelProject {
			res.ProjectNotes = append(res.ProjectNotes, "Pixel art project: set pixelArt: true in the game config (nearest-neighbour filtering and rounded positions).")
		}
	}

	for _, id := range ids {
		a, err := s.GetAsset(ctx, id)
		if err != nil {
			return nil, err
		}
		pixel := pixelProject || deliveryPixelArt(a)
		ah := AssetHints{AssetID: a.ID, Name: a.Name, Type: a.Type, Files: []FileHints{}}
		for _, f := range a.Files {
			fh := FileHints{Filename: f.Filename}
			switch PreviewKind(f.Filename) {
			case KindImage:
				imageHints(res.Engine, a, f, pixel, brief, &fh)
			case KindAudio:
				audioHints(res.Engine, a, f, &fh)
			case KindModel:
				modelHints(res.Engine, f, &fh)
			case KindFont:
				fontHints(res.Engine, pixel, &fh)
			default:
				if ext(f.Filename) == ".tga" || ext(f.Filename) == ".dds" || ext(f.Filename) == ".exr" || ext(f.Filename) == ".hdr" {
					fh.Notes = append(fh.Notes, "Import as a regular texture; HDR and EXR are for environment lighting, not 2D sprites.")
				}
			}
			ah.Files = append(ah.Files, fh)
		}
		if res.Engine == "godot" {
			if name, tres, note := spriteFramesTres(a, o.ResDir, brief.Naming); tres != "" {
				ah.SpriteFramesName, ah.SpriteFrames = name, tres
			} else if note != "" {
				ah.Notes = append(ah.Notes, note)
			}
		}
		res.Assets = append(res.Assets, ah)
	}
	return res, nil
}

func deliveryPixelArt(a *Asset) bool {
	if v, ok := deliveryAttrBool(a.Attrs, "pixel_art"); ok {
		return v
	}
	for _, t := range a.Tags {
		if t == "pixel-art" || t == "pixelart" || t == "pixel_art" {
			return true
		}
	}
	return false
}

func isSpriteLike(t string) bool {
	return t == "sprite" || t == "ui" || t == "tileset" || t == "vfx" || t == "animation"
}

func imageHints(engine string, a *Asset, f File, pixel bool, b deliveryBrief, fh *FileHints) {
	sheet := false
	fw, hasFW := deliveryAttrNum(a.Attrs, "frame_width")
	fhh, hasFH := deliveryAttrNum(a.Attrs, "frame_height")
	if hasFW && hasFH && fw > 0 && fhh > 0 {
		sheet = true
	}
	flat := isSpriteLike(a.Type)
	switch engine {
	case "godot":
		fh.Settings = map[string]any{}
		fh.ImportParams = map[string]any{"mipmaps/generate": false, "process/fix_alpha_border": true}
		if flat {
			fh.Settings["Compress > Mode"] = "Lossless"
			fh.Settings["Mipmaps > Generate"] = false
			fh.Settings["Process > Fix Alpha Border"] = true
			fh.ImportParams["compress/mode"] = 0
			if pixel {
				fh.Notes = append(fh.Notes, "Pixel art: use Nearest filtering (project default texture filter, or texture_filter on the node); never lossy compression.")
			}
		} else {
			fh.Settings["Compress > Mode"] = "VRAM Compressed"
			fh.Settings["Mipmaps > Generate"] = true
			fh.ImportParams["compress/mode"] = 2
			fh.ImportParams["mipmaps/generate"] = true
			fh.Notes = append(fh.Notes, "3D surface texture: VRAM compression and mipmaps. Normal maps: set Compress > Normal Map to Enabled.")
		}
		if sheet {
			fh.Notes = append(fh.Notes, fmt.Sprintf("Sprite sheet with %gx%g frames: use AnimatedSprite2D with the generated SpriteFrames, or Sprite2D with hframes/vframes.", fw, fhh))
		}
	case "unity":
		fh.Settings = map[string]any{}
		if flat {
			fh.Settings["Texture Type"] = "Sprite (2D and UI)"
			ppu := 100.0
			if b.TileSize > 0 {
				ppu = b.TileSize
			}
			fh.Settings["Pixels Per Unit"] = ppu
			fh.Settings["Generate Mip Maps"] = false
			if pixel {
				fh.Settings["Filter Mode"] = "Point (no filter)"
				fh.Settings["Compression"] = "None"
			} else {
				fh.Settings["Filter Mode"] = "Bilinear"
				fh.Settings["Compression"] = "Normal Quality"
			}
			if sheet {
				fh.Settings["Sprite Mode"] = "Multiple"
				fh.Settings["Sprite Editor > Slice"] = fmt.Sprintf("Grid By Cell Size %gx%g", fw, fhh)
			} else {
				fh.Settings["Sprite Mode"] = "Single"
			}
			if f.Width > 0 && f.Height > 0 {
				fh.Settings["Max Size"] = nextPow2(max(f.Width, f.Height))
			}
		} else {
			fh.Settings["Texture Type"] = "Default"
			fh.Settings["Generate Mip Maps"] = true
			fh.Settings["Filter Mode"] = "Trilinear"
			fh.Settings["Compression"] = "Normal Quality"
			fh.Notes = append(fh.Notes, "Normal maps: set Texture Type to Normal map.")
		}
	case "phaser":
		key := strings.TrimSuffix(f.Filename, ext(f.Filename))
		switch {
		case sheet:
			fh.Snippet = fmt.Sprintf("this.load.spritesheet(%q, %q, { frameWidth: %g, frameHeight: %g });", key, f.Filename, fw, fhh)
			if fps, ok := deliveryAttrNum(a.Attrs, "fps"); ok {
				frames, _ := deliveryAttrNum(a.Attrs, "frames")
				loop, hasLoop := deliveryAttrBool(a.Attrs, "loop")
				rep := -1
				if hasLoop && !loop {
					rep = 0
				}
				end := max(int(frames)-1, 0)
				fh.Snippet += fmt.Sprintf("\nthis.anims.create({ key: %q, frames: this.anims.generateFrameNumbers(%q, { start: 0, end: %d }), frameRate: %g, repeat: %d });", key, key, end, fps, rep)
			}
		default:
			fh.Snippet = fmt.Sprintf("this.load.image(%q, %q);", key, f.Filename)
		}
		if pixel {
			fh.Notes = append(fh.Notes, "Pixel art: game config pixelArt: true.")
		}
		if f.Width > 4096 || f.Height > 4096 {
			fh.Notes = append(fh.Notes, "Larger than 4096 px: some mobile GPUs cannot load it; consider an atlas of smaller pages.")
		}
	}
}

func audioHints(engine string, a *Asset, f File, fh *FileHints) {
	loopAttr, _ := deliveryAttrBool(a.Attrs, "loop")
	loop := a.Type == "music" || loopAttr
	for _, t := range a.Tags {
		loop = loop || t == "loop" || t == "looping"
	}
	dur, hasDur := deliveryAttrNum(a.Attrs, "duration_s")
	short := (hasDur && dur < 5) || (!hasDur && f.Size < 512<<10)
	e := ext(f.Filename)
	switch engine {
	case "godot":
		fh.Settings = map[string]any{}
		fh.ImportParams = map[string]any{}
		switch {
		case e == ".ogg" || e == ".mp3":
			fh.Settings["Loop"] = loop
			fh.ImportParams["loop"] = loop
		case e == ".wav":
			if loop {
				fh.Settings["Edit > Loop Mode"] = "Forward"
			} else {
				fh.Settings["Edit > Loop Mode"] = "Disabled"
			}
			fh.Settings["Compress > Mode"] = "Disabled"
		}
		if a.Type == "music" && e == ".wav" {
			fh.Notes = append(fh.Notes, "Long music as WAV is large: export OGG Vorbis for the game and keep the WAV as the source.")
		}
		if a.Type == "sfx" && e != ".wav" && short {
			fh.Notes = append(fh.Notes, "Short effects are best as WAV (no decode latency); OGG is fine for longer sounds.")
		}
		if loop {
			fh.Notes = append(fh.Notes, "Seamless loop: avoid MP3 (encoder padding causes a gap); prefer OGG or WAV.")
		}
	case "unity":
		fh.Settings = map[string]any{}
		switch {
		case a.Type == "music" || (hasDur && dur > 30):
			fh.Settings["Load Type"] = "Streaming"
			fh.Settings["Compression Format"] = "Vorbis"
			fh.Settings["Quality"] = 70
		case short:
			fh.Settings["Load Type"] = "Decompress On Load"
			fh.Settings["Compression Format"] = "ADPCM"
			fh.Settings["Preload Audio Data"] = true
		default:
			fh.Settings["Load Type"] = "Compressed In Memory"
			fh.Settings["Compression Format"] = "Vorbis"
		}
		if a.Type == "sfx" {
			fh.Settings["Force To Mono"] = "only for positional 3D sounds"
		}
		if loop {
			fh.Notes = append(fh.Notes, "Looping is set on the AudioSource (Loop), not on the clip.")
		}
	case "phaser":
		key := strings.TrimSuffix(f.Filename, e)
		fh.Snippet = fmt.Sprintf("this.load.audio(%q, [%q]);", key, f.Filename)
		play := fmt.Sprintf("this.sound.add(%q, { loop: %t })", key, loop)
		fh.Snippet += "\n" + play + ".play();"
		fh.Notes = append(fh.Notes, "Ship OGG plus an M4A or MP3 fallback for Safari: this.load.audio(key, ['a.ogg', 'a.m4a']). Browsers start audio only after a user gesture.")
		if loop && e == ".mp3" {
			fh.Notes = append(fh.Notes, "MP3 loops with a small gap; use OGG or M4A for seamless loops.")
		}
	}
}

func modelHints(engine string, f File, fh *FileHints) {
	e := ext(f.Filename)
	switch engine {
	case "godot":
		if e == ".glb" || e == ".gltf" {
			fh.Settings = map[string]any{"Import As": "Scene"}
			fh.Notes = append(fh.Notes, "glTF/GLB is the best-supported format in Godot 4.")
		} else {
			fh.Notes = append(fh.Notes, "Prefer exporting GLB; "+e+" support is limited or goes through an importer.")
		}
	case "unity":
		if e == ".fbx" {
			fh.Settings = map[string]any{"Scale Factor": 1, "Convert Units": true, "Import Animation": "only when the file has clips"}
		} else {
			fh.Notes = append(fh.Notes, "Unity imports FBX natively; GLB/glTF needs a package such as glTFast.")
		}
	case "phaser":
		fh.Notes = append(fh.Notes, "Phaser is 2D: 3D models need a separate library such as three.js.")
	}
}

func fontHints(engine string, pixel bool, fh *FileHints) {
	switch engine {
	case "godot":
		if pixel {
			fh.Settings = map[string]any{"Antialiasing": "None", "Hinting": "None", "Subpixel Positioning": "Disabled"}
		} else {
			fh.Notes = append(fh.Notes, "Default import is fine; enable Multichannel Signed Distance Field for text that scales a lot.")
		}
	case "unity":
		fh.Notes = append(fh.Notes, "For UI use TextMeshPro: create a Font Asset from the font (Window > TextMeshPro > Font Asset Creator).")
	case "phaser":
		fh.Notes = append(fh.Notes, "Load web fonts with CSS @font-face (or the WebFont loader) before the scene starts; bitmap fonts via this.load.bitmapFont.")
	}
}

var reAnimName = regexp.MustCompile(`[^A-Za-z0-9_\- ]+`)

// spriteFramesTres builds a Godot 4 SpriteFrames resource for a sprite sheet
// with frame attributes. It returns the file name, the text and, when no
// resource can be made, a note explaining why.
func spriteFramesTres(a *Asset, resDir, naming string) (name, tres, note string) {
	fw, ok1 := deliveryAttrNum(a.Attrs, "frame_width")
	fh, ok2 := deliveryAttrNum(a.Attrs, "frame_height")
	if !ok1 || !ok2 || fw < 1 || fh < 1 {
		return "", "", ""
	}
	p := a.Primary()
	if p == nil || p.Width == 0 || p.Height == 0 || PreviewKind(p.Filename) != KindImage {
		return "", "", "SpriteFrames: the primary file has no known pixel size."
	}
	frameW, frameH := int(fw), int(fh)
	cols := p.Width / frameW
	if c, ok := deliveryAttrNum(a.Attrs, "columns"); ok && c >= 1 && int(c) <= cols {
		cols = int(c)
	}
	rows := p.Height / frameH
	if cols < 1 || rows < 1 {
		return "", "", "SpriteFrames: the frame size is larger than the image."
	}
	total := cols * rows
	if n, ok := deliveryAttrNum(a.Attrs, "frames"); ok && n >= 1 && int(n) <= total {
		total = int(n)
	}
	total = min(total, 1024)
	fps := 10.0
	if v, ok := deliveryAttrNum(a.Attrs, "fps"); ok && v > 0 && v <= 240 {
		fps = v
	}
	loop := true
	if v, ok := deliveryAttrBool(a.Attrs, "loop"); ok {
		loop = v
	}

	type anim struct {
		name        string
		first, last int
	}
	var anims []anim
	usedNames := map[string]bool{}
	if m, ok := a.Attrs["animations"].(map[string]any); ok {
		names := make([]string, 0, len(m))
		for k := range m {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			r, ok := m[k].([]any)
			if !ok || len(r) != 2 {
				continue
			}
			f0, ok0 := r[0].(float64)
			f1, ok1 := r[1].(float64)
			clean := strings.TrimSpace(reAnimName.ReplaceAllString(k, "_"))
			if !ok0 || !ok1 || clean == "" || int(f0) < 0 || int(f1) < int(f0) || int(f1) >= total {
				continue
			}
			clean = clean[:min(len(clean), 64)]
			base := clean
			for n := 2; usedNames[clean]; n++ {
				clean = fmt.Sprintf("%s_%d", base, n)
			}
			usedNames[clean] = true
			anims = append(anims, anim{clean, int(f0), int(f1)})
		}
	}
	if len(anims) == 0 {
		anims = []anim{{"default", 0, total - 1}}
	}

	used := map[int]bool{}
	for _, an := range anims {
		for i := an.first; i <= an.last; i++ {
			used[i] = true
		}
	}
	var idxs []int
	for i := range used {
		idxs = append(idxs, i)
	}
	sort.Ints(idxs)

	if resDir == "" {
		resDir = "res://"
	}
	if !strings.HasSuffix(resDir, "/") {
		resDir += "/"
	}
	// The file name sync_project gives the image when the brief names a convention.
	texPath := resDir + ApplyNaming(p.Filename, naming)
	var b strings.Builder
	fmt.Fprintf(&b, "[gd_resource type=\"SpriteFrames\" load_steps=%d format=3]\n\n", len(idxs)+2)
	fmt.Fprintf(&b, "[ext_resource type=\"Texture2D\" path=\"%s\" id=\"1_sheet\"]\n\n", texPath)
	for _, i := range idxs {
		fmt.Fprintf(&b, "[sub_resource type=\"AtlasTexture\" id=\"AtlasTexture_%d\"]\natlas = ExtResource(\"1_sheet\")\nregion = Rect2(%d, %d, %d, %d)\n\n",
			i, (i%cols)*frameW, (i/cols)*frameH, frameW, frameH)
	}
	b.WriteString("[resource]\nanimations = [")
	for ai, an := range anims {
		if ai > 0 {
			b.WriteString(", ")
		}
		b.WriteString("{\n\"frames\": [")
		for i := an.first; i <= an.last; i++ {
			if i > an.first {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "{\n\"duration\": 1.0,\n\"texture\": SubResource(\"AtlasTexture_%d\")\n}", i)
		}
		fmt.Fprintf(&b, "],\n\"loop\": %t,\n\"name\": &\"%s\",\n\"speed\": %s\n}", loop, an.name, godotFloat(fps))
	}
	b.WriteString("]\n")

	base := nameStem(a.Name)
	if base == "" || base == "file" {
		base = strings.TrimSuffix(p.Filename, ext(p.Filename))
	}
	return base + ".tres", b.String(), ""
}

// godotFloat prints a number the way Godot writes floats: always with a decimal point.
func godotFloat(f float64) string {
	v := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.Contains(v, ".") {
		v += ".0"
	}
	return v
}
