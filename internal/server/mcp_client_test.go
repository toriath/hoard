package server

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

// TestMCPWithSDKClient drives the endpoint with a real MCP client (the
// official SDK's Streamable HTTP transport) rather than hand-written
// JSON-RPC, covering the full handshake a client such as Claude Code performs.
func TestMCPWithSDKClient(t *testing.T) {
	e := newEnv(t)
	e.upload("coin_gold.png", []byte("not really a png"), `{"tags":["coin"]}`)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   e.ts.URL + "/mcp",
		HTTPClient: &http.Client{Transport: bearerTransport{e.token}},
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer session.Close()

	if init := session.InitializeResult(); init == nil || init.ServerInfo.Name != "hoard" || init.Instructions == "" {
		t.Errorf("initialize result = %+v", init)
	}
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if tool.Description == "" || tool.InputSchema == nil {
			t.Errorf("tool %s lacks description or schema", tool.Name)
		}
	}
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "search_assets", Arguments: map[string]any{"query": "coin"}})
	if err != nil {
		t.Fatal(err)
	}
	text, ok := res.Content[0].(*mcp.TextContent)
	if res.IsError || !ok || !strings.Contains(text.Text, `"coin_gold"`) {
		t.Errorf("search result = %+v", res.Content[0])
	}
	// Schema validation happens before the handler: a wrong argument type is a readable tool error.
	res, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "search_assets", Arguments: map[string]any{"limit": "many"}})
	if err == nil && !res.IsError {
		t.Error("invalid arguments accepted")
	}
}
