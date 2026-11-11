package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/toriath/hoard/internal/ai"
	"github.com/toriath/hoard/internal/config"
	"github.com/toriath/hoard/internal/hoard"
)

func TestClientIP(t *testing.T) {
	proxies, bad := config.ParseProxies("10.0.0.0/8, 192.168.1.5, fd00::/8, nonsense")
	if len(proxies) != 3 || len(bad) != 1 {
		t.Fatalf("parse: %v %v", proxies, bad)
	}
	s := &Server{cfg: config.Config{TrustedProxies: proxies}}
	untrusting := &Server{}
	for _, tc := range []struct {
		name, peer string
		xff        []string
		want       string
		srv        *Server
	}{
		{"direct client, no header", "203.0.113.9:5555", nil, "203.0.113.9", s},
		{"untrusted peer cannot set the header", "203.0.113.9:5555", []string{"1.1.1.1"}, "203.0.113.9", s},
		{"trusted proxy forwards a client", "10.1.2.3:80", []string{"198.51.100.7"}, "198.51.100.7", s},
		{"client-supplied prefix is ignored", "10.1.2.3:80", []string{"6.6.6.6, 198.51.100.7"}, "198.51.100.7", s},
		{"chain of trusted proxies", "10.1.2.3:80", []string{"198.51.100.7, 10.9.9.9, 192.168.1.5"}, "198.51.100.7", s},
		{"several header lines", "10.1.2.3:80", []string{"6.6.6.6", "198.51.100.7, 10.0.0.2"}, "198.51.100.7", s},
		{"port in header", "10.1.2.3:80", []string{"198.51.100.7:4444"}, "198.51.100.7", s},
		{"ipv6 client is keyed by its /64", "[fd00::1]:80", []string{"2001:db8:1:2:aaaa::7"}, "2001:db8:1:2::/64", s},
		{"another address in the same /64", "[fd00::1]:80", []string{"2001:db8:1:2:bbbb::9"}, "2001:db8:1:2::/64", s},
		{"direct ipv6 peer", "[2001:db8:9:9:1::1]:5", nil, "2001:db8:9:9::/64", untrusting},
		{"mapped ipv4 peer is plain ipv4", "[::ffff:203.0.113.9]:5", nil, "203.0.113.9", untrusting},
		{"mapped ipv4 in the header", "10.1.2.3:80", []string{"::ffff:198.51.100.7"}, "198.51.100.7", s},
		{"zone is dropped", "10.1.2.3:80", []string{"fe80::1%eth0"}, "fe80::/64", s},
		{"garbage falls back to the peer", "10.1.2.3:80", []string{"198.51.100.7, not-an-ip"}, "10.1.2.3", s},
		{"only trusted hops falls back to the peer", "10.1.2.3:80", []string{"10.5.5.5"}, "10.1.2.3", s},
		{"no proxies configured ignores the header", "10.1.2.3:80", []string{"198.51.100.7"}, "10.1.2.3", untrusting},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = tc.peer
		for _, h := range tc.xff {
			r.Header.Add("X-Forwarded-For", h)
		}
		if got := tc.srv.clientIP(r); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// signIns sends n failed sign-ins for alice, each with the header produced by
// xff(i), and returns the statuses seen.
func signIns(t *testing.T, trusted string, n int, xff func(i int) string) []int {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc, err := hoard.Open(t.TempDir(), log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Close() })
	if _, err := svc.CreateUser(context.Background(), "alice", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{MaxUploadBytes: 1 << 20}
	cfg.TrustedProxies, _ = config.ParseProxies(trusted)
	web := fstest.MapFS{"index.html": {Data: []byte("x")}}
	ts := httptest.NewServer(New(svc, ai.New(svc, cfg, log), cfg, log, "test", web))
	t.Cleanup(ts.Close)

	var got []int
	for i := range n {
		req, _ := http.NewRequest("POST", ts.URL+"/api/v1/auth/login", strings.NewReader(`{"username":"alice","password":"wrong-guess-number"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forwarded-For", xff(i))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		got = append(got, resp.StatusCode)
	}
	return got
}

func TestThrottleIgnoresSpoofedForwardedFor(t *testing.T) {
	// Without trusted proxies a rotating header buys nothing: the peer is
	// throttled after the allowed number of failures.
	got := signIns(t, "", 12, func(i int) string { return "198.51.100." + string(rune('1'+i%9)) })
	if got[len(got)-1] != http.StatusTooManyRequests {
		t.Errorf("spoofed X-Forwarded-For evaded the throttle: %v", got)
	}

	// Behind a trusted proxy, rotating the part the client controls (the left)
	// is throttled as one client...
	got = signIns(t, "127.0.0.1", 12, func(i int) string { return "6.6.6." + string(rune('1'+i%9)) + ", 198.51.100.7" })
	if got[len(got)-1] != http.StatusTooManyRequests {
		t.Errorf("forged left-hand addresses evaded the throttle: %v", got)
	}
	// ...while genuinely different clients, as seen by the proxy, are not
	// lumped together into one bucket.
	got = signIns(t, "127.0.0.1", 12, func(i int) string { return "198.51.100." + string(rune('1'+i%9)) })
	for _, st := range got {
		if st == http.StatusTooManyRequests {
			t.Errorf("distinct clients behind the proxy shared a bucket: %v", got)
			break
		}
	}
}
