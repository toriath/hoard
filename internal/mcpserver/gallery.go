package mcpserver

// The interactive asset gallery is an MCP App
// (https://modelcontextprotocol.io/extensions/apps/overview): a self-contained
// HTML view that a client which supports the extension renders in the
// conversation when show_assets is called. Clients without it ignore the
// extra metadata and get the same result as text.
//
// The view runs in the client's sandbox and makes no network request of its
// own. Thumbnails and audio reach it through tool calls that the client
// proxies (view_asset, asset_media), so it needs no entry in the sandbox's
// content security policy and works for an instance that only the user's
// machine can reach.

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/toriath/hoard/internal/hoard"
)

const (
	galleryURI = "ui://hoard/gallery"
	// appMIMEType marks an HTML resource as an MCP App view.
	appMIMEType = "text/html;profile=mcp-app"

	galleryDefault = 24
	galleryMax     = 60
	// maxInlineAudio bounds what asset_media returns: the file travels base64
	// encoded inside one tool result.
	maxInlineAudio = 2 << 20
)

//go:embed gallery.html
var galleryHTML string

func (t *tools) addGallery(srv *mcp.Server) {
	srv.AddResource(&mcp.Resource{
		URI: galleryURI, Name: "asset-gallery", Title: "hoard asset gallery", MIMEType: appMIMEType,
		Description: "Interactive view for show_assets: thumbnails, audio playback and selection.",
	}, func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
			URI: galleryURI, MIMEType: appMIMEType, Text: galleryHTML,
			// No csp entry: the view loads nothing over the network.
			Meta: mcp.Meta{"ui": map[string]any{"prefersBorder": true}},
		}}}, nil
	})

	mcp.AddTool(srv, &mcp.Tool{Name: "show_assets", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		// The flat key is the older spelling of the same link; the official
		// SDK writes both.
		Meta:        mcp.Meta{"ui": map[string]any{"resourceUri": galleryURI}, "ui/resourceUri": galleryURI},
		Description: "Show assets to the user as an interactive gallery with thumbnails and audio playback, so they can look, listen and pick. Use it whenever the user should choose between assets or review a batch; use search_assets when only you need the list. Pass `ids` for a shortlist you already narrowed down, or the same filters as search_assets. In a client that renders interactive views the user ticks assets and the choice comes back as a user message listing asset ids; act on those ids (get_asset for details). Other clients show nothing: present the returned list yourself and include the `web_url` links."},
		t.show)

	mcp.AddTool(srv, &mcp.Tool{Name: "asset_media", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		// Hidden from the model where the client honours it: only the gallery calls this.
		Meta:        mcp.Meta{"ui": map[string]any{"visibility": []string{"app"}}},
		Description: "Used by the interactive gallery to play an audio file (returned inline, up to 2 MiB). Of no use to an agent: use get_download_commands to fetch files."},
		t.media)
}

type showIn struct {
	Title        string   `json:"title,omitempty" jsonschema:"short heading or question shown above the gallery, e.g. 'Which footsteps fit the dungeon?'"`
	IDs          []string `json:"ids,omitempty" jsonschema:"show exactly these assets, in this order (up to 60); the filters below are then ignored"`
	Query        string   `json:"query,omitempty" jsonschema:"free text; every word must match (prefix match)"`
	Types        []string `json:"types,omitempty" jsonschema:"restrict to these asset types (any of)"`
	Tags         []string `json:"tags,omitempty" jsonschema:"assets must carry all of these tags"`
	CollectionID string   `json:"collection_id,omitempty" jsonschema:"only assets in this collection"`
	ProjectID    string   `json:"project_id,omitempty" jsonschema:"only assets used in this project"`
	Limit        int      `json:"limit,omitempty" jsonschema:"number of assets to show, default 24, max 60"`
}

// galleryItem is what the view draws for one asset.
type galleryItem struct {
	compact
	PreviewKind string `json:"preview_kind"`
	// AudioFile names the file the view may play through asset_media.
	AudioFile string `json:"audio_file,omitempty"`
	WebURL    string `json:"web_url"`
}

func (t *tools) show(ctx context.Context, req *mcp.CallToolRequest, in showIn) (*mcp.CallToolResult, any, error) {
	q := hoard.ListQuery{Limit: in.Limit}
	if q.Limit <= 0 {
		q.Limit = galleryDefault
	}
	q.Limit = min(q.Limit, galleryMax)
	if len(in.IDs) > 0 {
		if len(in.IDs) > galleryMax {
			return nil, nil, fmt.Errorf("at most %d assets can be shown at once, got %d", galleryMax, len(in.IDs))
		}
		for _, id := range in.IDs {
			if !hoard.ValidAssetID(id) {
				return nil, nil, fmt.Errorf("%q is not an asset id", id)
			}
		}
		q.IDs, q.Limit = in.IDs, len(in.IDs)
	} else {
		for _, ty := range in.Types {
			if !hoard.ValidType(ty) {
				return nil, nil, fmt.Errorf("unknown asset type %q (valid: %s)", ty, strings.Join(hoard.TypeIDs(), ", "))
			}
		}
		q.Q, q.Types, q.Tags, q.Collection, q.Project = in.Query, in.Types, in.Tags, in.CollectionID, in.ProjectID
	}
	res, err := t.svc.ListAssets(ctx, q)
	if err != nil {
		return nil, nil, err
	}
	assets := res.Items
	if len(in.IDs) > 0 { // keep the order the caller chose
		byID := make(map[string]*hoard.Asset, len(assets))
		for _, a := range assets {
			byID[a.ID] = a
		}
		assets = assets[:0]
		for _, id := range in.IDs {
			if a := byID[id]; a != nil {
				assets = append(assets, a)
				delete(byID, id) // an id passed twice is shown once
			}
		}
	}
	base := baseURL(req)
	items := make([]galleryItem, len(assets))
	for i, a := range assets {
		items[i] = galleryItem{compact: toCompact(a), PreviewKind: a.PreviewKind, WebURL: base + "/assets/" + a.ID}
		if f := playable(a); f != nil {
			items[i].AudioFile = f.Filename
		}
	}
	out := map[string]any{
		"title":  in.Title,
		"total":  res.Total,
		"count":  len(items),
		"assets": items,
		"note":   "A client with interactive views now shows these assets to the user as a gallery; wait for their choice, which arrives as a user message listing asset ids. In any other client nothing was shown: present this list yourself, with the web_url links.",
	}
	r, _, err := result(out)
	if err != nil {
		return nil, nil, err
	}
	r.StructuredContent = out // what the view reads
	return r, nil, nil
}

// playable returns the audio file of an asset that is small enough to send
// to the view, preferring the primary file.
func playable(a *hoard.Asset) *hoard.File {
	ok := func(f *hoard.File) bool { return f.Kind == hoard.KindAudio && f.Size <= maxInlineAudio }
	if p := a.Primary(); p != nil && ok(p) {
		return p
	}
	for i := range a.Files {
		if ok(&a.Files[i]) {
			return &a.Files[i]
		}
	}
	return nil
}

type mediaIn struct {
	ID       string `json:"id" jsonschema:"asset id"`
	Filename string `json:"filename" jsonschema:"audio file of that asset"`
}

func (t *tools) media(ctx context.Context, _ *mcp.CallToolRequest, in mediaIn) (*mcp.CallToolResult, any, error) {
	fh, f, err := t.svc.OpenFile(ctx, in.ID, in.Filename)
	if err != nil {
		return nil, nil, err
	}
	defer fh.Close()
	if f.Kind != hoard.KindAudio {
		return nil, nil, fmt.Errorf("%s is not an audio file", f.Filename)
	}
	if f.Size > maxInlineAudio {
		return nil, nil, fmt.Errorf("%s (%d bytes) is too large to play inline; open the asset in the web UI", f.Filename, f.Size)
	}
	data, err := io.ReadAll(io.LimitReader(fh, maxInlineAudio))
	if err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.AudioContent{Data: data, MIMEType: f.Mime}}}, nil, nil
}
