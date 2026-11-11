package hoard

import (
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

// AssetType describes one of the built-in asset categories.
type AssetType struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

// AssetTypes is the fixed catalogue of categories, in display order.
var AssetTypes = []AssetType{
	{"sprite", "2D Sprite", "Characters, items, icons, sprite sheets and other 2D art"},
	{"tileset", "Tileset", "Tile sheets, tilemaps and level building blocks"},
	{"ui", "UI", "Interface elements, HUD parts, cursors, icon sets"},
	{"texture", "Texture", "Surface textures, PBR maps, HDRIs, skyboxes"},
	{"model", "3D Model", "Meshes, rigged characters, props and scenes"},
	{"animation", "Animation", "Animation clips, rigs, motion data, skeletal/spine animations"},
	{"vfx", "VFX", "Particle systems, flipbooks and effect assets"},
	{"shader", "Shader", "Shader source and shader graphs"},
	{"sfx", "SFX", "Sound effects and foley"},
	{"music", "Music", "Soundtracks, loops, stingers and ambience"},
	{"voice", "Voice", "Dialogue, voice lines and barks"},
	{"video", "Video", "Cutscenes, trailers, video textures"},
	{"font", "Font", "Typefaces and bitmap fonts"},
	{"other", "Other", "Anything else: data, scripts, documents"},
}

// ValidType reports whether t is a known asset type id.
func ValidType(t string) bool {
	for _, at := range AssetTypes {
		if at.ID == t {
			return true
		}
	}
	return false
}

// TypeIDs returns the list of valid type ids.
func TypeIDs() []string {
	out := make([]string, len(AssetTypes))
	for i, t := range AssetTypes {
		out[i] = t.ID
	}
	return out
}

// CommonLicenses are suggestions offered by the UI and the MCP server; the
// license field itself is free text (SPDX identifiers preferred).
var CommonLicenses = []string{
	"CC0-1.0", "CC-BY-4.0", "CC-BY-SA-4.0", "CC-BY-NC-4.0", "MIT", "Apache-2.0", "OFL-1.1",
	"Royalty-Free", "Proprietary", "AI-Generated (own)", "Commissioned", "Custom", "Unknown",
}

// AssetStatuses are the review states of an asset. "" means not reviewed.
// placeholder and superseded are set by hoard itself (placeholders for open
// requests, assets replaced by an accepted rework) but may also be set by hand.
var AssetStatuses = []string{"", "draft", "approved", "final", "rework", "rejected", "placeholder", "superseded"}

// ValidStatus reports whether st is a known review status.
func ValidStatus(st string) bool {
	for _, v := range AssetStatuses {
		if v == st {
			return true
		}
	}
	return false
}

// NoteKinds classify notes on an asset. rework notes carry the feedback a
// generator agent has to address.
var NoteKinds = []string{"comment", "rework", "approval", "rejection"}

// RequestStatuses are the states of an asset request:
//
//	open      waiting for someone to work on it
//	claimed   a generator agent is working on it (the claim expires)
//	submitted at least one candidate is waiting for review
//	done      a candidate was accepted
//	cancelled withdrawn
var RequestStatuses = []string{"open", "claimed", "submitted", "done", "cancelled"}

// CollectionKinds are the allowed kinds of collection.
var CollectionKinds = []string{"pack", "style", "project", "other"}

// Preview kinds tell clients how a file can be previewed.
const (
	KindImage = "image"
	KindAudio = "audio"
	KindVideo = "video"
	KindModel = "model"
	KindFont  = "font"
	KindText  = "text"
	KindNone  = "none"
)

type extInfo struct {
	mime string
	kind string // preview kind
	typ  string // default asset type
}

// extTable maps lowercase file extensions to MIME type, preview kind and the
// asset type guessed for a file with that extension.
var extTable = map[string]extInfo{
	// images the browser can display
	".png":  {"image/png", KindImage, "sprite"},
	".jpg":  {"image/jpeg", KindImage, "sprite"},
	".jpeg": {"image/jpeg", KindImage, "sprite"},
	".gif":  {"image/gif", KindImage, "sprite"},
	".webp": {"image/webp", KindImage, "sprite"},
	".avif": {"image/avif", KindImage, "sprite"},
	".bmp":  {"image/bmp", KindImage, "sprite"},
	".svg":  {"image/svg+xml", KindImage, "sprite"},
	// images that need a server-made thumbnail or have no preview
	".tif":      {"image/tiff", KindNone, "texture"},
	".tiff":     {"image/tiff", KindNone, "texture"},
	".tga":      {"image/x-tga", KindNone, "texture"},
	".dds":      {"image/vnd-ms.dds", KindNone, "texture"},
	".exr":      {"image/x-exr", KindNone, "texture"},
	".hdr":      {"image/vnd.radiance", KindNone, "texture"},
	".ktx":      {"image/ktx", KindNone, "texture"},
	".ktx2":     {"image/ktx2", KindNone, "texture"},
	".psd":      {"image/vnd.adobe.photoshop", KindNone, "sprite"},
	".kra":      {"application/x-krita", KindNone, "sprite"},
	".xcf":      {"image/x-xcf", KindNone, "sprite"},
	".ase":      {"application/octet-stream", KindNone, "sprite"},
	".aseprite": {"application/octet-stream", KindNone, "sprite"},
	// tile data
	".tmx":  {"application/xml", KindText, "tileset"},
	".tsx":  {"application/xml", KindText, "tileset"},
	".ldtk": {"application/json", KindText, "tileset"},
	// 3D
	".glb":   {"model/gltf-binary", KindModel, "model"},
	".gltf":  {"model/gltf+json", KindModel, "model"},
	".obj":   {"model/obj", KindModel, "model"},
	".fbx":   {"application/octet-stream", KindModel, "model"},
	".stl":   {"model/stl", KindModel, "model"},
	".ply":   {"application/octet-stream", KindModel, "model"},
	".dae":   {"model/vnd.collada+xml", KindNone, "model"},
	".blend": {"application/x-blender", KindNone, "model"},
	".usdz":  {"model/vnd.usdz+zip", KindNone, "model"},
	".usd":   {"application/octet-stream", KindNone, "model"},
	".3ds":   {"application/octet-stream", KindNone, "model"},
	".vox":   {"application/octet-stream", KindNone, "model"},
	".mtl":   {"text/plain", KindText, "model"},
	".bin":   {"application/octet-stream", KindNone, "other"},
	// animation
	".bvh":   {"text/plain", KindText, "animation"},
	".anim":  {"text/plain", KindText, "animation"},
	".spine": {"application/octet-stream", KindNone, "animation"},
	".skel":  {"application/octet-stream", KindNone, "animation"},
	// audio
	".wav":  {"audio/wav", KindAudio, "sfx"},
	".mp3":  {"audio/mpeg", KindAudio, "sfx"},
	".ogg":  {"audio/ogg", KindAudio, "sfx"},
	".oga":  {"audio/ogg", KindAudio, "sfx"},
	".opus": {"audio/ogg", KindAudio, "sfx"},
	".flac": {"audio/flac", KindAudio, "sfx"},
	".m4a":  {"audio/mp4", KindAudio, "sfx"},
	".aac":  {"audio/aac", KindAudio, "sfx"},
	".aiff": {"audio/aiff", KindNone, "sfx"},
	".aif":  {"audio/aiff", KindNone, "sfx"},
	".mid":  {"audio/midi", KindNone, "music"},
	".midi": {"audio/midi", KindNone, "music"},
	".mod":  {"audio/x-mod", KindNone, "music"},
	".xm":   {"audio/x-xm", KindNone, "music"},
	".it":   {"audio/x-it", KindNone, "music"},
	".s3m":  {"audio/x-s3m", KindNone, "music"},
	// video
	".mp4":  {"video/mp4", KindVideo, "video"},
	".m4v":  {"video/mp4", KindVideo, "video"},
	".webm": {"video/webm", KindVideo, "video"},
	".mov":  {"video/quicktime", KindVideo, "video"},
	".ogv":  {"video/ogg", KindVideo, "video"},
	".mkv":  {"video/x-matroska", KindNone, "video"},
	".avi":  {"video/x-msvideo", KindNone, "video"},
	// fonts
	".ttf":   {"font/ttf", KindFont, "font"},
	".otf":   {"font/otf", KindFont, "font"},
	".woff":  {"font/woff", KindFont, "font"},
	".woff2": {"font/woff2", KindFont, "font"},
	".fnt":   {"text/plain", KindText, "font"},
	".bdf":   {"text/plain", KindText, "font"},
	// shaders
	".glsl":        {"text/plain", KindText, "shader"},
	".vert":        {"text/plain", KindText, "shader"},
	".frag":        {"text/plain", KindText, "shader"},
	".vs":          {"text/plain", KindText, "shader"},
	".fs":          {"text/plain", KindText, "shader"},
	".geom":        {"text/plain", KindText, "shader"},
	".comp":        {"text/plain", KindText, "shader"},
	".hlsl":        {"text/plain", KindText, "shader"},
	".fx":          {"text/plain", KindText, "shader"},
	".cg":          {"text/plain", KindText, "shader"},
	".cginc":       {"text/plain", KindText, "shader"},
	".shader":      {"text/plain", KindText, "shader"},
	".compute":     {"text/plain", KindText, "shader"},
	".gdshader":    {"text/plain", KindText, "shader"},
	".gdshaderinc": {"text/plain", KindText, "shader"},
	".wgsl":        {"text/plain", KindText, "shader"},
	".metal":       {"text/plain", KindText, "shader"},
	".shadergraph": {"application/json", KindText, "shader"},
	".usf":         {"text/plain", KindText, "shader"},
	// vfx
	".vfx":      {"text/plain", KindText, "vfx"},
	".efk":      {"application/octet-stream", KindNone, "vfx"},
	".efkefc":   {"application/octet-stream", KindNone, "vfx"},
	".pfx":      {"application/octet-stream", KindNone, "vfx"},
	".particle": {"text/plain", KindText, "vfx"},
	".p":        {"text/plain", KindText, "vfx"},
	// text-ish / data
	".txt":      {"text/plain", KindText, "other"},
	".md":       {"text/markdown", KindText, "other"},
	".json":     {"application/json", KindText, "other"},
	".xml":      {"application/xml", KindText, "other"},
	".yaml":     {"text/plain", KindText, "other"},
	".yml":      {"text/plain", KindText, "other"},
	".toml":     {"text/plain", KindText, "other"},
	".csv":      {"text/csv", KindText, "other"},
	".ini":      {"text/plain", KindText, "other"},
	".cfg":      {"text/plain", KindText, "other"},
	".atlas":    {"text/plain", KindText, "sprite"},
	".tres":     {"text/plain", KindText, "other"},
	".tscn":     {"text/plain", KindText, "other"},
	".material": {"text/plain", KindText, "other"},
	".mat":      {"text/plain", KindText, "other"},
	".gd":       {"text/plain", KindText, "other"},
	".cs":       {"text/plain", KindText, "other"},
	".lua":      {"text/plain", KindText, "other"},
	".py":       {"text/plain", KindText, "other"},
	".js":       {"text/plain", KindText, "other"},
	".zip":      {"application/zip", KindNone, "other"},
	".pdf":      {"application/pdf", KindNone, "other"},
}

// decodableImage lists formats the server can decode to build thumbnails.
var decodableImage = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".bmp": true, ".tif": true, ".tiff": true,
}

func ext(filename string) string { return strings.ToLower(filepath.Ext(filename)) }

// MimeFor returns the content type served for a filename. Unknown extensions
// are served as opaque bytes so that nothing is ever sniffed into HTML.
func MimeFor(filename string) string {
	if info, ok := extTable[ext(filename)]; ok {
		return info.mime
	}
	return "application/octet-stream"
}

// PreviewKind tells clients how a file can be previewed in a browser.
func PreviewKind(filename string) string {
	if info, ok := extTable[ext(filename)]; ok {
		return info.kind
	}
	return KindNone
}

var (
	reTexture = regexp.MustCompile(`(?i)(^|[_\-. ])(albedo|basecolor|base_color|diffuse|normal|nrm|roughness|rough|metallic|metalness|ao|occlusion|height|displacement|emissive|emission|specular|orm|arm|texture|tex|seamless|tileable|skybox|hdri)([_\-. ]|\d|$)`)
	reTileset = regexp.MustCompile(`(?i)(tileset|tilemap|tiles|autotile)`)
	reUI      = regexp.MustCompile(`(?i)(^|[_\-. ])(ui|hud|button|btn|icon|icons|cursor|panel|frame|9slice|nineslice|gui)([_\-. ]|\d|$)`)
	reVFX     = regexp.MustCompile(`(?i)(^|[_\-. ])(vfx|fx|particle|particles|flipbook|explosion|smoke|spark|sparks|trail|muzzle)([_\-. ]|\d|$)`)
	reMusic   = regexp.MustCompile(`(?i)(music|theme|soundtrack|ost|bgm|song|track|loop|ambien|stinger|jingle)`)
	reVoice   = regexp.MustCompile(`(?i)(^|[_\-. ])(voice|vo|dialog|dialogue|line|bark|speech|narrat\w*)([_\-. ]|\d|$)`)
	reAnim    = regexp.MustCompile(`(?i)(^|[_\-. ])(anim|animation|walk|run|idle|attack|mocap|clip)([_\-. ]|\d|$)`)
)

// GuessType infers an asset type from a filename and size. It is a heuristic
// starting point; users and the AI agent can always change the type.
func GuessType(filename string, size int64) string {
	info, ok := extTable[ext(filename)]
	if !ok {
		return "other"
	}
	stem := strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename))
	switch info.typ {
	case "sprite":
		switch {
		case reTexture.MatchString(stem):
			return "texture"
		case reTileset.MatchString(stem):
			return "tileset"
		case reVFX.MatchString(stem):
			return "vfx"
		case reUI.MatchString(stem):
			return "ui"
		}
	case "sfx":
		switch {
		case reVoice.MatchString(stem):
			return "voice"
		case reMusic.MatchString(stem), size > 4<<20 && ext(filename) != ".wav", size > 24<<20:
			return "music"
		}
	case "model":
		if reAnim.MatchString(stem) && (ext(filename) == ".fbx" || ext(filename) == ".glb") {
			return "animation"
		}
	}
	return info.typ
}

var (
	reTagClean = regexp.MustCompile(`[^\p{L}\p{N}\-_.:+#]+`)
	reDashes   = regexp.MustCompile(`-{2,}`)
)

// NormalizeTag canonicalises a tag: lowercase, kebab-case, at most 48 chars.
func NormalizeTag(t string) string {
	t = strings.ToLower(strings.TrimSpace(t))
	t = reTagClean.ReplaceAllString(t, "-")
	t = reDashes.ReplaceAllString(t, "-")
	t = strings.Trim(t, "-")
	if len(t) > 48 {
		t = strings.Trim(t[:48], "-")
	}
	return t
}

// NormalizeTags canonicalises and de-duplicates tags, preserving order.
func NormalizeTags(tags []string) []string {
	out := make([]string, 0, len(tags))
	seen := map[string]bool{}
	for _, t := range tags {
		for _, part := range strings.Split(t, ",") {
			n := NormalizeTag(part)
			if n != "" && !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	return out
}

// Besides characters that are illegal on common filesystems, quotes, backticks
// and dollar signs are replaced: file names end up inside single-quoted shell
// commands handed to AI agents, and without these characters that quoting
// holds in PowerShell as well as in POSIX shells.
var reUnsafeName = regexp.MustCompile("[\\x00-\\x1f\\x7f<>:\"/\\\\|?*'`$]+")

// SanitizeFilename reduces an untrusted filename to a safe base name.
func SanitizeFilename(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	name = reUnsafeName.ReplaceAllString(name, "_")
	name = strings.TrimLeft(strings.TrimSpace(name), ".")
	name = strings.TrimRight(name, ". ")
	name = strings.ToValidUTF8(name, "_")
	if len(name) > 180 {
		e := filepath.Ext(name)
		if len(e) > 20 {
			e = ""
		}
		stem := name[:180-len(e)]
		for !utf8.ValidString(stem) { // never cut a multi-byte character in half
			stem = stem[:len(stem)-1]
		}
		name = stem + e
	}
	if name == "" {
		name = "file"
	}
	return name
}
