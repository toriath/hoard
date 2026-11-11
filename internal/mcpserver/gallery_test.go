package mcpserver

import (
	"strings"
	"testing"
)

// The gallery shows asset names, descriptions and tags, which are untrusted,
// inside a client's conversation, and it declares no content security policy
// entries. Both only hold while the view builds its DOM from text nodes and
// loads nothing over the network.
func TestGalleryViewIsSelfContained(t *testing.T) {
	for _, banned := range []string{
		"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "eval(", "new Function",
		"fetch(", "XMLHttpRequest", "WebSocket", "EventSource", "import(",
		"<script src", "<link", "<iframe", "url(", "@import",
	} {
		if strings.Contains(galleryHTML, banned) {
			t.Errorf("gallery.html contains %q", banned)
		}
	}
	// Only ids may be sent back into the conversation: the message is built
	// from the id list and nothing else.
	if !strings.Contains(galleryHTML, "`I pick these hoard assets: ${key}`") {
		t.Error("the selection message changed: check that it still carries ids only")
	}
}
