package mcpserver

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/toriath/hoard/internal/hoard"
)

// syncDownloadCap bounds the commands returned by one sync_project call.
const syncDownloadCap = 300

// addDeliveryTools registers the tools of one feature area.
// Getting assets into a game repository: audit and sync against a project, atlas packing, derivatives and engine import hints.
func (t *tools) addDeliveryTools(srv *mcp.Server) {
	ro := &mcp.ToolAnnotations{ReadOnlyHint: true}
	mcp.AddTool(srv, &mcp.Tool{Name: "get_repo_audit_command", Annotations: ro,
		Description: "Get ONE shell command to run inside a game repository (POSIX shell, needs find, sha256sum or shasum, and curl). It lists the asset-like files (images, audio, models, fonts, video; not code or text; skipping .git, node_modules, .godot, Library, build folders…) with sha256, size and relative path, and uploads that list to hoard, which stores it as the project's repository snapshot. `root` is the repository directory: pass an absolute path (no `~`; it is quoted, not expanded). Files whose names contain control characters or invalid UTF-8 are left out; the summary hoard prints says how many (`skipped`). Run the command, then call audit_project or sync_project. The link in the command is valid for two hours and only writes the snapshot of this project."},
		t.repoAuditCommand)
	mcp.AddTool(srv, &mcp.Tool{Name: "audit_project", Annotations: ro,
		Description: "Compare the project's repository snapshot (from get_repo_audit_command) with hoard. Returns: known files (the asset each belongs to, with flags superseded = a newer version exists, rework / rejected / placeholder status, not_in_project), unknown files (untracked: candidates for create_asset, with a suggested type and name), project assets missing from the repository, and assets in the repository that are not linked to the project (link them with bulk_update_assets add_project_ids). Counts are complete; lists are cut at 500 entries. To fix outdated or missing files call sync_project."},
		t.auditProject)
	mcp.AddTool(srv, &mcp.Tool{Name: "sync_project", Annotations: ro,
		Description: "Plan the delivery of a project's assets into its repository. This tool only plans and returns shell commands; nothing is written until you run them. For every asset used in the project (superseded assets are followed to their newest version) it decides the target path: in place when the repository snapshot holds an older version of the asset (same path, same extension), otherwise the brief's folders (by asset type, then `default`, then assets/<type>) with the file name converted to the brief's naming convention. Returns curl download commands only for files that are missing or differ, each with a reason (missing, changed; conflict = a different file already sits at the target path, no command is given), curl commands (POSIX shell) that never overwrite an existing file for reason missing, a curl command for the lock file hoard.lock.json (path, asset, sha256, size, license, attribution of every asset file), and `removals`: repository files of superseded assets that were not replaced in place. hoard never deletes anything: decide yourself about removals. `root` is the repository directory: pass an absolute path (no `~`). With `only_missing` files that merely changed are skipped. Run get_repo_audit_command first so the snapshot is current, and again afterwards to verify. At most 300 commands per call."},
		t.syncProject)
	mcp.AddTool(srv, &mcp.Tool{Name: "pack_atlas", Annotations: ro,
		Description: "Pack the primary images of several assets (PNG, JPEG, GIF, WebP, BMP, TIFF; max 256) into one texture atlas with MaxRects. Returns a curl command (POSIX shell) that downloads a zip (atlas.png plus metadata) and the frame list {name, asset_id, x, y, w, h}. format: json (atlas.json with frames), phaser (atlas.json in the Phaser JSON-hash format) or godot (one AtlasTexture .tres per sprite below sprites/; `res_path` is where atlas.png will live in the project, default res://atlas.png). Options: padding 0-32 px between sprites (default 0), extrude 0-4 (repeat edge pixels, avoids bleeding), max_size up to 8192 (default 2048), power_of_two. Frame names come from the asset names; unzip into the repository, then record usage with bulk_update_assets."},
		t.packAtlas)
	mcp.AddTool(srv, &mcp.Tool{Name: "get_derivative_command", Annotations: ro,
		Description: "Get a curl command (POSIX shell) that downloads a resized copy of an image file of an asset, rendered on demand and cached by hoard. Give `width` and/or `height` (the other follows the aspect ratio) or a `scale` factor; max 8192 px per edge. `filter`: nearest keeps hard pixel edges, linear smooths; the default is nearest for pixel art and integer scales of 2 or more, otherwise linear. `format`: png (default) or jpeg (transparency becomes white; `quality` 1-100, default 85). WebP output is not supported. `output` is the local file to write."},
		t.derivativeCommand)
	mcp.AddTool(srv, &mcp.Tool{Name: "get_engine_hints", Annotations: ro,
		Description: "Recommended import settings for assets in Godot 4, Unity or Phaser (`engine`; default: the engine in the project's brief when `project_id` is given). Per file: settings by their editor labels (e.g. pixel art: Godot lossless compression and no mipmaps, Unity Filter Mode Point and Compression None with Pixels Per Unit = the brief's tile size; music loop flags; SFX format choices), Godot import parameter keys, Phaser load snippets, and notes. For a sprite sheet with frame_width/frame_height attributes (and optionally columns, frames, fps, loop, animations) on Godot it also returns the text of a SpriteFrames .tres: write it to `sprite_frames_filename` next to the image (`res_dir` is the res:// directory the image will have, e.g. res://art/sprites). These are recommendations from engine knowledge, not checked against a running engine: say so when it matters."},
		t.engineHints)
}

type repoAuditIn struct {
	ProjectID  string   `json:"project_id" jsonschema:"project the repository belongs to"`
	Root       string   `json:"root,omitempty" jsonschema:"repository directory to scan (default: current directory)"`
	Extensions []string `json:"extensions,omitempty" jsonschema:"file extensions to include, e.g. [\"png\",\"ogg\"] (default: all asset types hoard knows, minus code and text)"`
}

func (t *tools) repoAuditCommand(ctx context.Context, req *mcp.CallToolRequest, in repoAuditIn) (*mcp.CallToolResult, any, error) {
	cmd, err := t.svc.RepoAuditCommand(ctx, baseURL(req), in.ProjectID, in.Root, in.Extensions)
	if err != nil {
		return nil, nil, err
	}
	return result(map[string]any{
		"command": cmd,
		"note":    "Run in a POSIX shell. It prints nothing on success except hoard's JSON summary. The link is valid for 2 hours. Afterwards call audit_project or sync_project.",
	})
}

type auditProjectIn struct {
	ProjectID string `json:"project_id" jsonschema:"project id"`
}

func (t *tools) auditProject(ctx context.Context, _ *mcp.CallToolRequest, in auditProjectIn) (*mcp.CallToolResult, any, error) {
	a, err := t.svc.AuditProject(ctx, in.ProjectID)
	if err != nil {
		return nil, nil, err
	}
	var next []string
	if a.HasSnapshot {
		c := a.Counts
		if c.Outdated+c.Missing > 0 {
			next = append(next, fmt.Sprintf("%d outdated files, %d assets missing (the newer version of an outdated file also counts as missing): call sync_project to get the download commands", c.Outdated, c.Missing))
		}
		if c.Untracked > 0 {
			next = append(next, fmt.Sprintf("%d untracked files: create_asset for the ones that are assets (suggested_type and suggested_name are guesses), then link them to the project", c.Untracked))
		}
		if c.Unlinked > 0 {
			next = append(next, fmt.Sprintf("%d assets are in the repository but not linked to the project: bulk_update_assets with add_project_ids", c.Unlinked))
		}
		if c.Attention > 0 {
			next = append(next, fmt.Sprintf("%d files have status rework, rejected or placeholder", c.Attention))
		}
	}
	return result(map[string]any{"audit": a, "next": next})
}

type syncIn struct {
	ProjectID   string `json:"project_id" jsonschema:"project id"`
	Root        string `json:"root,omitempty" jsonschema:"repository directory the files are written below (default: current directory)"`
	OnlyMissing bool   `json:"only_missing,omitempty" jsonschema:"skip files that exist but differ; only fetch missing ones"`
}

func deliveryJoinRoot(root, p string) string {
	root = strings.TrimSpace(root)
	if root == "" || root == "." {
		return p
	}
	return strings.TrimRight(root, "/") + "/" + p
}

func (t *tools) syncProject(ctx context.Context, req *mcp.CallToolRequest, in syncIn) (*mcp.CallToolResult, any, error) {
	root, err := hoard.CleanRoot(in.Root)
	if err != nil {
		return nil, nil, err
	}
	in.Root = root
	plan, err := t.svc.PlanSync(ctx, in.ProjectID)
	if err != nil {
		return nil, nil, err
	}
	base := baseURL(req)
	type download struct {
		Path    string `json:"path"`
		Reason  string `json:"reason"`
		AssetID string `json:"asset_id"`
		Name    string `json:"name"`
		Size    int64  `json:"size"`
		Note    string `json:"note,omitempty"`
		Command string `json:"command"`
	}
	downloads := []download{}
	conflicts, skipped := []hoard.SyncEntry{}, []hoard.SyncEntry{}
	unchanged, remaining := 0, 0
	for _, e := range plan.Entries {
		switch {
		case e.Reason == "unchanged":
			unchanged++
		case e.Reason == "conflict":
			conflicts = append(conflicts, e)
		case e.Reason == "changed" && in.OnlyMissing:
			skipped = append(skipped, e)
		case len(downloads) >= syncDownloadCap:
			remaining++
		default:
			target := shellQuote(deliveryJoinRoot(in.Root, e.Path))
			cmd := "curl -fsS --globoff --create-dirs -o " + target + " " + shellQuote(t.downloadURL(base, e.AssetID, e.Filename))
			if e.Reason == "missing" { // never clobber a file that appeared since the snapshot
				cmd = "if [ -e " + target + " ]; then echo " + shellQuote("skip: "+deliveryJoinRoot(in.Root, e.Path)+" exists") + "; else " + cmd + "; fi"
			}
			downloads = append(downloads, download{Path: e.Path, Reason: e.Reason, AssetID: e.AssetID, Name: e.Name, Size: e.Size, Note: e.Note, Command: cmd})
		}
	}
	lockTarget := deliveryJoinRoot(in.Root, "hoard.lock.json")
	out := map[string]any{
		"project":      map[string]string{"id": plan.ProjectID, "name": plan.ProjectName},
		"has_snapshot": plan.HasSnapshot,
		"summary": map[string]int{"assets_files": len(plan.Entries), "unchanged": unchanged, "to_download": len(downloads), "conflicts": len(conflicts),
			"skipped_changed": len(skipped), "not_listed_cap": remaining, "removals": len(plan.Removals)},
		"downloads":    downloads,
		"conflicts":    conflicts,
		"removals":     plan.Removals,
		"warnings":     plan.Warnings,
		"lock_command": "curl -fsS --globoff --create-dirs -o " + shellQuote(lockTarget) + " " + shellQuote(t.svc.RepoLockURL(base, plan.ProjectID)),
		"note":         "Run the download commands in a POSIX shell, then the lock_command, then get_repo_audit_command and run it again to refresh the snapshot. removals lists repository files of superseded assets that were not replaced in place; hoard never deletes: remove them yourself if the game no longer uses them. Check references to a moved file in scenes and code.",
	}
	if remaining > 0 {
		out["more"] = fmt.Sprintf("%d further commands were not listed (limit %d per call): after running these, refresh the snapshot and call sync_project again.", remaining, syncDownloadCap)
	}
	return result(out)
}

type atlasIn struct {
	AssetIDs   []string `json:"asset_ids" jsonschema:"assets whose primary image is packed (max 256)"`
	Padding    int      `json:"padding,omitempty" jsonschema:"transparent pixels between sprites, 0-32"`
	MaxSize    int      `json:"max_size,omitempty" jsonschema:"longest edge of the atlas, 16-8192 (default 2048)"`
	PowerOfTwo bool     `json:"power_of_two,omitempty" jsonschema:"round the atlas size up to powers of two"`
	Extrude    int      `json:"extrude,omitempty" jsonschema:"repeat each sprite's edge pixels 0-4 times to avoid bleeding"`
	Format     string   `json:"format,omitempty" jsonschema:"json (default), phaser or godot"`
	ResPath    string   `json:"res_path,omitempty" jsonschema:"godot only: res:// path of atlas.png in the project, e.g. res://art/atlas.png"`
	Output     string   `json:"output,omitempty" jsonschema:"local file name for the zip (default atlas.zip)"`
}

func (t *tools) packAtlas(ctx context.Context, req *mcp.CallToolRequest, in atlasIn) (*mcp.CallToolResult, any, error) {
	opts, err := hoard.AtlasOptions{Padding: in.Padding, MaxSize: in.MaxSize, PowerOfTwo: in.PowerOfTwo, Extrude: in.Extrude, Format: in.Format, ResPath: in.ResPath}.Normalize()
	if err != nil {
		return nil, nil, err
	}
	ids, err := hoard.ParseIDList(strings.Join(in.AssetIDs, ","), 256)
	if err != nil {
		return nil, nil, err
	}
	// A layout-only dry run validates the request and yields the frame list
	// without decoding pixels; the zip is built when the link is fetched (the
	// layout is deterministic).
	atlas, err := t.svc.PlanAtlas(ctx, ids, opts)
	if err != nil {
		return nil, nil, err
	}
	out := in.Output
	if out == "" {
		out = "atlas.zip"
	}
	return result(map[string]any{
		"command": "curl -fsS --globoff -o " + shellQuote(out) + " " + shellQuote(t.svc.AtlasURL(baseURL(req), ids, opts)),
		"width":   atlas.Width, "height": atlas.Height, "format": opts.Format, "frames": atlas.Frames,
		"note": "The zip contains atlas.png and the metadata for the chosen format. The link is valid for 2 hours; changing any option needs a new call.",
	})
}

type derivIn struct {
	AssetID  string  `json:"asset_id" jsonschema:"asset id"`
	Filename string  `json:"filename" jsonschema:"image file of the asset to scale"`
	Width    int     `json:"width,omitempty" jsonschema:"target width in px"`
	Height   int     `json:"height,omitempty" jsonschema:"target height in px"`
	Scale    float64 `json:"scale,omitempty" jsonschema:"scale factor instead of width/height, e.g. 2 or 0.5"`
	Filter   string  `json:"filter,omitempty" jsonschema:"nearest or linear (default: nearest for pixel art and integer scales >= 2, else linear)"`
	Format   string  `json:"format,omitempty" jsonschema:"png (default) or jpeg"`
	Quality  int     `json:"quality,omitempty" jsonschema:"jpeg quality 1-100 (default 85)"`
	Output   string  `json:"output,omitempty" jsonschema:"local file to write (default: derived from the file name)"`
}

func (t *tools) derivativeCommand(ctx context.Context, req *mcp.CallToolRequest, in derivIn) (*mcp.CallToolResult, any, error) {
	a, err := t.svc.GetAsset(ctx, in.AssetID)
	if err != nil {
		return nil, nil, err
	}
	var file *hoard.File
	for i := range a.Files {
		if a.Files[i].Filename == in.Filename {
			file = &a.Files[i]
		}
	}
	if file == nil {
		return nil, nil, fmt.Errorf("asset %s has no file %q (files: %s)", a.ID, in.Filename, deliveryFileNames(a))
	}
	opts := hoard.DeriveOptions{Width: in.Width, Height: in.Height, Scale: in.Scale, Filter: in.Filter, Format: in.Format, Quality: in.Quality}
	if opts.Filter == "" {
		opts.Filter = t.svc.DefaultDeriveFilter(ctx, a, in.Scale)
	}
	if opts, err = opts.Normalize(); err != nil {
		return nil, nil, err
	}
	ow, oh, err := opts.DeriveSize(file.Width, file.Height)
	if err != nil {
		return nil, nil, err
	}
	out := in.Output
	if out == "" {
		stem := strings.TrimSuffix(file.Filename, path.Ext(file.Filename))
		out = fmt.Sprintf("%s@%dx%d%s", stem, ow, oh, opts.DeriveExt())
	}
	return result(map[string]any{
		"command": "curl -fsS --globoff -o " + shellQuote(out) + " " + shellQuote(t.svc.DeriveURL(baseURL(req), a.ID, file.Filename, opts)),
		"width":   ow, "height": oh, "filter": opts.Filter, "format": opts.Format, "source": map[string]int{"width": file.Width, "height": file.Height},
		"note": "Rendered on first request and cached. The link is valid for 2 hours and covers exactly these parameters.",
	})
}

func deliveryFileNames(a *hoard.Asset) string {
	var n []string
	for _, f := range a.Files {
		n = append(n, f.Filename)
	}
	return strings.Join(n, ", ")
}

type hintsIn struct {
	AssetIDs  []string `json:"asset_ids" jsonschema:"assets to get import recommendations for (max 100)"`
	Engine    string   `json:"engine,omitempty" jsonschema:"godot, unity or phaser (default: the engine of the project's brief)"`
	ProjectID string   `json:"project_id,omitempty" jsonschema:"project whose brief supplies engine, pixel-art flag and tile size"`
	ResDir    string   `json:"res_dir,omitempty" jsonschema:"godot: res:// directory the files will have, for the SpriteFrames resource"`
}

func (t *tools) engineHints(ctx context.Context, _ *mcp.CallToolRequest, in hintsIn) (*mcp.CallToolResult, any, error) {
	res, err := t.svc.EngineHints(ctx, in.AssetIDs, hoard.HintsOptions{Engine: in.Engine, ProjectID: in.ProjectID, ResDir: in.ResDir})
	if err != nil {
		return nil, nil, err
	}
	return result(res)
}
