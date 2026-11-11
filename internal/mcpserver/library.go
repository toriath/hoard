package mcpserver

import (
	"context"
	"fmt"
	"net/url"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/toriath/hoard/internal/hoard"
)

// addLibraryTools registers the library maintenance tools: duplicates,
// similar assets and zip pack imports. (Colour search is the `colors` filter
// of search_assets.)
func (t *tools) addLibraryTools(srv *mcp.Server) {
	ro := &mcp.ToolAnnotations{ReadOnlyHint: true}
	no := false
	add := &mcp.ToolAnnotations{DestructiveHint: &no}

	mcp.AddTool(srv, &mcp.Tool{Name: "find_similar", Annotations: ro,
		Description: "Find assets that duplicate or resemble one asset, best match first. Each hit carries a score (0..1) and the reasons: \"identical file\" (same bytes), \"visually near-identical (distance 3)\" or \"visually similar\" (perceptual hash of the picture; distance 0-64, lower is closer, only for images), and \"shares 4 tags\" (same type, overlapping tags). Use it before creating an asset to avoid storing the same thing twice, or to find alternatives in the same style. Pictures stored before this feature are indexed in the background after the server starts."},
		t.findSimilar)
	mcp.AddTool(srv, &mcp.Tool{Name: "find_duplicates", Annotations: ro,
		Description: "Scan the whole library for duplicates: groups of assets with byte-identical files (\"identical\") and groups of near-identical pictures (\"near-identical\", perceptual hash distance up to 5). Returns up to 200 groups, identical ones and larger groups first, with the member assets. To clean up, keep the best member (the one with the most metadata and project usage), merge tags and project usage of the others into it with bulk_update_assets, then move the others to the trash with delete_assets."},
		t.findDuplicates)
	mcp.AddTool(srv, &mcp.Tool{Name: "get_import_command", Annotations: add,
		Description: "Import a zip archive of assets (a downloaded asset pack) as a pack. Returns one `curl -T` command to run in the shell where the zip is; hoard unpacks it on the server and curl prints the summary as JSON (created asset ids, the detected licence, skipped and failed files). Modes: per_file (default, one asset per file), per_folder (one asset per top-level folder, nested file names are flattened with _) or single (the whole archive is one asset). The pack becomes a collection of kind pack named after the zip (or collection_name). The licence is detected from LICENSE, README and .txt files in the archive root (CC0, CC-BY, CC-BY-SA, OFL, MIT and others) and applied to all assets unless you pass `license`; check the printed summary and correct it with bulk_update_assets if the pack says otherwise. Hidden files, __MACOSX and folders are skipped; archives with more than 10000 entries, an absurd compression ratio or more data than the upload limit are refused."},
		t.importCommand)
}

type similarIn struct {
	ID    string `json:"id" jsonschema:"asset id to find look-alikes for"`
	Limit int    `json:"limit,omitempty" jsonschema:"maximum number of hits, default 20, max 100"`
}

func (t *tools) findSimilar(ctx context.Context, _ *mcp.CallToolRequest, in similarIn) (*mcp.CallToolResult, any, error) {
	hits, err := t.svc.FindSimilar(ctx, in.ID, in.Limit)
	if err != nil {
		return nil, nil, err
	}
	type hit struct {
		Asset    compact  `json:"asset"`
		Score    float64  `json:"score"`
		Reasons  []string `json:"reasons"`
		Distance *int     `json:"distance,omitempty"`
	}
	out := make([]hit, len(hits))
	for i, h := range hits {
		out[i] = hit{Asset: toCompact(h.Asset), Score: h.Score, Reasons: h.Reasons, Distance: h.Distance}
	}
	return result(map[string]any{"asset_id": in.ID, "count": len(out), "similar": out})
}

func (t *tools) findDuplicates(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, any, error) {
	groups, err := t.svc.FindDuplicates(ctx)
	if err != nil {
		return nil, nil, err
	}
	type group struct {
		Kind   string    `json:"kind"`
		Reason string    `json:"reason"`
		Assets []compact `json:"assets"`
	}
	out := make([]group, len(groups))
	for i, g := range groups {
		out[i] = group{Kind: g.Kind, Reason: g.Reason, Assets: []compact{}}
		for _, a := range g.Assets {
			out[i].Assets = append(out[i].Assets, toCompact(a))
		}
	}
	return result(map[string]any{"groups": len(out), "duplicates": out})
}

type importIn struct {
	Filename       string   `json:"filename" jsonschema:"local path of the zip archive, e.g. downloads/kenney-ui-pack.zip"`
	Mode           string   `json:"mode,omitempty" jsonschema:"per_file (default), per_folder or single"`
	CollectionName string   `json:"collection_name,omitempty" jsonschema:"name of the pack collection; default: the zip's file name"`
	License        string   `json:"license,omitempty" jsonschema:"license for all assets, overriding the one detected in the archive (SPDX id such as CC0-1.0)"`
	Tags           []string `json:"tags,omitempty" jsonschema:"tags added to every imported asset"`
	ProjectID      string   `json:"project_id,omitempty" jsonschema:"record usage of every asset in this project"`
}

func (t *tools) importCommand(ctx context.Context, req *mcp.CallToolRequest, in importIn) (*mcp.CallToolResult, any, error) {
	if in.Filename == "" {
		return nil, nil, fmt.Errorf("filename is required")
	}
	opts := hoard.ImportOptions{Mode: in.Mode, CollectionName: in.CollectionName, License: in.License, Tags: in.Tags, ProjectID: in.ProjectID}
	if err := opts.Validate(); err != nil {
		return nil, nil, err
	}
	if opts.ProjectID != "" {
		if _, err := t.svc.GetProject(ctx, opts.ProjectID); err != nil {
			return nil, nil, err
		}
	}
	name := hoard.SanitizeFilename(in.Filename)
	tok := hoard.EncodeImportOptions(opts)
	// The signature binds the file name and every option: the link cannot be
	// reused to import into another project or with another licence.
	exp, sig := t.svc.Sign("import:"+name+":"+tok, linkTTL)
	link := fmt.Sprintf("%s/api/v1/import/%s?o=%s&exp=%s&sig=%s", baseURL(req), url.PathEscape(name), tok, exp, sig)
	return result(map[string]any{
		"command": "curl -fsS --globoff -T " + shellQuote(in.Filename) + " " + shellQuote(link),
		"mode":    opts.Mode,
		"note":    "Run this in a POSIX shell. The server unpacks the archive and curl prints the import summary as JSON: created assets, detected licence and attribution, skipped and failed files. The link is valid for 2 hours and only for this import.",
	})
}
