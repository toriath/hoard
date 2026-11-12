package hoard

import (
	"context"
	"strings"
)

// Actor is whoever causes a change: a person in the browser, a script with an
// API token, or an agent over MCP. The HTTP layer attaches it to the request
// context; the Service records it in the activity feed and on notes,
// requests and submissions.
type Actor struct {
	UserID    string `json:"user_id,omitempty"`
	Username  string `json:"username,omitempty"`
	Via       string `json:"via"`                  // web | api | mcp | system
	TokenName string `json:"token_name,omitempty"` // name of the API token, when one was used
}

// String is the display form, e.g. "toriath via MCP (claude-code)".
func (a Actor) String() string {
	name := a.Username
	if name == "" {
		name = "hoard"
	}
	switch a.Via {
	case "", "web", "system":
		return name
	}
	s := name + " via " + strings.ToUpper(a.Via)
	if a.TokenName != "" {
		s += " (" + a.TokenName + ")"
	}
	return s
}

type actorKey struct{}

// WithActor returns a context that carries a.
func WithActor(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, a)
}

// ActorFrom returns the actor of ctx; background work is the system.
func ActorFrom(ctx context.Context) Actor {
	if a, ok := ctx.Value(actorKey{}).(Actor); ok {
		return a
	}
	return Actor{Via: "system"}
}
