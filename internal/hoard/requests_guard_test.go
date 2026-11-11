package hoard

import (
	"context"
	"encoding/json"
	"errors"
	"image/color"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func webCtx() context.Context {
	return WithActor(context.Background(), Actor{UserID: "usr_u", Username: "user", Via: "web"})
}

func submitted(t *testing.T, s *Service, title string, by context.Context) (*Request, *Asset) {
	t.Helper()
	r, err := s.CreateRequest(context.Background(), RequestInput{Title: title, AssetType: "sprite"})
	if err != nil {
		t.Fatal(err)
	}
	a := mustAsset(t, s, AssetInput{Name: ptr(title + " cand")}, "", nil)
	r, err = s.SubmitRequest(by, r.ID, []string{a.ID}, "")
	if err != nil {
		t.Fatal(err)
	}
	return r, a
}

func TestReviewStateGuards(t *testing.T) {
	s := newService(t)
	gen := actorCtx("gen")
	user := webCtx()

	// double reject: the second is a conflict and must not touch the request
	r, a := submitted(t, s, "one", gen)
	if _, err := s.RejectSubmission(user, r.ID, a.ID, "no"); err != nil {
		t.Fatal(err)
	}
	// another agent claims the reopened request ...
	other := actorCtx("other")
	if _, err := s.ClaimRequest(other, r.ID, 30); err != nil {
		t.Fatal(err)
	}
	// ... and a stale second reject must not wipe that claim
	if _, err := s.RejectSubmission(user, r.ID, a.ID, "no again"); !errors.Is(err, ErrConflict) {
		t.Fatalf("double reject: %v", err)
	}
	if got, _ := s.GetRequest(user, r.ID); got.Status != "claimed" || got.ClaimedBy != "other via MCP (t)" {
		t.Fatalf("claim was wiped: %+v", got)
	}
	if _, err := s.AcceptSubmission(user, r.ID, a.ID, true); !errors.Is(err, ErrConflict) {
		t.Errorf("accept while claimed: %v", err)
	}

	// accept after cancel
	r2, a2 := submitted(t, s, "two", gen)
	if _, err := s.CancelRequest(user, r2.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptSubmission(user, r2.ID, a2.ID, true); !errors.Is(err, ErrConflict) {
		t.Errorf("accept after cancel: %v", err)
	}

	// concurrent accept/accept and accept/reject: exactly one wins
	for _, second := range []string{"accept", "reject"} {
		r3, a3 := submitted(t, s, "race "+second, gen)
		var wg sync.WaitGroup
		var mu sync.Mutex
		wins := 0
		do := func(f func() error) {
			defer wg.Done()
			if err := f(); err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			} else if !errors.Is(err, ErrConflict) {
				t.Errorf("unexpected: %v", err)
			}
		}
		wg.Add(2)
		go do(func() error { _, err := s.AcceptSubmission(user, r3.ID, a3.ID, true); return err })
		go do(func() error {
			if second == "accept" {
				_, err := s.AcceptSubmission(user, r3.ID, a3.ID, true)
				return err
			}
			_, err := s.RejectSubmission(user, r3.ID, a3.ID, "late")
			return err
		})
		wg.Wait()
		if wins != 1 {
			t.Errorf("accept/%s: %d winners", second, wins)
		}
	}
}

func TestSelfReviewRefused(t *testing.T) {
	s := newService(t)
	gen := actorCtx("gen")
	r, a := submitted(t, s, "mine", gen)

	if _, err := s.AcceptSubmission(gen, r.ID, a.ID, true); err == nil || !strings.Contains(err.Error(), "cannot be reviewed by the agent that made it") {
		t.Fatalf("self accept: %v", err)
	}
	if _, err := s.RejectSubmission(gen, r.ID, a.ID, "x"); err == nil {
		t.Error("self reject")
	}
	if _, err := s.ApproveAsset(gen, a.ID, ""); err == nil {
		t.Error("self approve of a candidate under review")
	}
	if _, err := s.RejectAsset(gen, a.ID, "x"); err == nil {
		t.Error("self reject of a candidate under review")
	}
	// the same token with a different label is a different agent
	if _, err := s.RejectSubmission(WithAgent(gen, "reviewer"), r.ID, a.ID, "fine, another agent"); err != nil {
		t.Errorf("other agent: %v", err)
	}
	// the user in the browser always may; so may a different token
	r2, a2 := submitted(t, s, "mine2", gen)
	if _, err := s.AcceptSubmission(actorCtx("coder"), r2.ID, a2.ID, true); err != nil {
		t.Errorf("other token: %v", err)
	}
	r3, a3 := submitted(t, s, "mine3", gen)
	if _, err := s.AcceptSubmission(webCtx(), r3.ID, a3.ID, true); err != nil {
		t.Errorf("web: %v", err)
	}
	// once the work is no longer under review the creator may judge it
	if _, err := s.ApproveAsset(gen, a3.ID, ""); err != nil {
		t.Errorf("approve after review: %v", err)
	}
}

func TestAgentLabelsTellSharedTokenAgentsApart(t *testing.T) {
	s := newService(t)
	r, _ := s.CreateRequest(context.Background(), RequestInput{Title: "x", AssetType: "sprite"})
	a1, a2 := WithAgent(actorCtx("tok"), "one"), WithAgent(actorCtx("tok"), "two")
	got, err := s.ClaimRequest(a1, r.ID, 10)
	if err != nil || got.ClaimedBy != "tok via MCP (t) / one" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := s.RenewClaim(a2, r.ID, 10); !errors.Is(err, ErrConflict) {
		t.Errorf("foreign renew: %v", err)
	}
	if _, err := s.RenewClaim(a1, r.ID, 10); err != nil {
		t.Error(err)
	}
	cand := mustAsset(t, s, AssetInput{Name: ptr("c")}, "", nil)
	if _, err := s.SubmitRequest(a2, r.ID, []string{cand.ID}, ""); !errors.Is(err, ErrConflict) {
		t.Errorf("submit by the other agent: %v", err)
	}
}

func TestReviewOfSupersededAndPlaceholder(t *testing.T) {
	s := newService(t)
	ctx := webCtx()
	r, _ := s.CreateRequest(ctx, RequestInput{Title: "p", AssetType: "sprite", Placeholder: true})
	ph, _ := s.GetAsset(ctx, r.PlaceholderAssetID)
	if _, err := s.ApproveAsset(ctx, ph.ID, ""); err == nil || !strings.Contains(err.Error(), "placeholders are replaced") {
		t.Errorf("approve placeholder: %v", err)
	}
	if _, err := s.MarkForRework(ctx, ph.ID, "x", 0, ""); err == nil {
		t.Error("rework of a placeholder")
	}
	cand := mustAsset(t, s, AssetInput{Name: ptr("real")}, "", nil)
	s.SubmitRequest(ctx, r.ID, []string{cand.ID}, "")
	if _, err := s.AcceptSubmission(ctx, r.ID, cand.ID, true); err != nil {
		t.Fatal(err)
	}
	// ph is superseded: the error names the head of the chain
	_, err := s.RejectAsset(ctx, ph.ID, "x")
	if err == nil || !strings.Contains(err.Error(), cand.ID) {
		t.Errorf("review of superseded: %v", err)
	}
	if _, err := s.MarkForRework(ctx, ph.ID, "x", 0, ""); err == nil || !strings.Contains(err.Error(), cand.ID) {
		t.Errorf("rework of superseded: %v", err)
	}
}

func TestSupersedeChainsStayAcyclic(t *testing.T) {
	s := newService(t)
	ctx := webCtx()
	px := mustAsset(t, s, AssetInput{Name: ptr("X")}, "", nil)
	py := mustAsset(t, s, AssetInput{Name: ptr("Y")}, "", nil)
	// X was replaced by Y; its status was then set back by hand (superseded_by stays)
	if _, err := s.db.Exec(`UPDATE assets SET status = 'superseded', superseded_by = ? WHERE id = ?`, py.ID, px.ID); err != nil {
		t.Fatal(err)
	}
	rw, err := s.MarkForRework(ctx, py.ID, "make it like X again", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	// a superseded asset cannot be a candidate
	if _, err := s.SubmitRequest(ctx, rw.Request.ID, []string{px.ID}, ""); err == nil {
		t.Fatal("superseded candidate accepted")
	}
	// reviving X by hand: draft with a stale superseded_by
	if _, err := s.UpdateAsset(ctx, px.ID, AssetInput{Status: ptr("draft")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SubmitRequest(ctx, rw.Request.ID, []string{px.ID}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptSubmission(ctx, rw.Request.ID, px.ID, true); err != nil {
		t.Fatal(err)
	}
	x, _ := s.GetAsset(ctx, px.ID)
	y, _ := s.GetAsset(ctx, py.ID)
	if x.SupersededBy != "" || x.Status != "approved" || y.Status != "superseded" || y.SupersededBy != x.ID {
		t.Errorf("X %q/%q Y %q/%q", x.Status, x.SupersededBy, y.Status, y.SupersededBy)
	}
	if _, _, err := supersedeHead(ctx, s.db, py.ID); err != nil {
		t.Errorf("chain has a loop: %v", err)
	}

	// target already replaced by the candidate itself: refused, no loop created
	pa := mustAsset(t, s, AssetInput{Name: ptr("A")}, "", nil)
	pb := mustAsset(t, s, AssetInput{Name: ptr("B")}, "", nil)
	rw2, _ := s.MarkForRework(ctx, pa.ID, "again", 0, "")
	s.SubmitRequest(ctx, rw2.Request.ID, []string{pb.ID}, "")
	if _, err := s.db.Exec(`UPDATE assets SET superseded_by = ? WHERE id = ?`, pb.ID, pa.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptSubmission(ctx, rw2.Request.ID, pb.ID, true); err == nil || !strings.Contains(err.Error(), "loop") {
		t.Errorf("loop not refused: %v", err)
	}

	// a target that was superseded meanwhile: the head of its chain is replaced
	pc := mustAsset(t, s, AssetInput{Name: ptr("C")}, "", nil)
	pd := mustAsset(t, s, AssetInput{Name: ptr("D")}, "", nil)
	pe := mustAsset(t, s, AssetInput{Name: ptr("E")}, "", nil)
	rw3, _ := s.MarkForRework(ctx, pc.ID, "c", 0, "")
	if _, err := s.db.Exec(`UPDATE assets SET superseded_by = ?, status = 'superseded' WHERE id = ?`, pd.ID, pc.ID); err != nil {
		t.Fatal(err)
	}
	s.SubmitRequest(ctx, rw3.Request.ID, []string{pe.ID}, "")
	if _, err := s.AcceptSubmission(ctx, rw3.Request.ID, pe.ID, true); err != nil {
		t.Fatal(err)
	}
	if d, _ := s.GetAsset(ctx, pd.ID); d.SupersededBy != pe.ID || d.Status != "superseded" {
		t.Errorf("head not superseded: %+v", d)
	}
}

func TestSubmitKeepsReviewStatusAndExclusivity(t *testing.T) {
	s := newService(t)
	ctx := webCtx()
	r1, _ := s.CreateRequest(ctx, RequestInput{Title: "a", AssetType: "sprite"})
	r2, _ := s.CreateRequest(ctx, RequestInput{Title: "b", AssetType: "sprite"})
	for _, st := range []string{"rework", "rejected", "approved"} {
		a := mustAsset(t, s, AssetInput{Name: ptr(st), Status: ptr(st)}, "", nil)
		rq, _ := s.CreateRequest(ctx, RequestInput{Title: st, AssetType: "sprite"})
		if _, err := s.SubmitRequest(ctx, rq.ID, []string{a.ID}, ""); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.GetAsset(ctx, a.ID); got.Status != st {
			t.Errorf("status %q became %q", st, got.Status)
		}
	}
	a := mustAsset(t, s, AssetInput{Name: ptr("shared")}, "", nil)
	if _, err := s.SubmitRequest(ctx, r1.ID, []string{a.ID}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SubmitRequest(ctx, r2.ID, []string{a.ID}, ""); !errors.Is(err, ErrConflict) {
		t.Errorf("pending in two requests: %v", err)
	}
	if got, _ := s.GetAsset(ctx, a.ID); got.Status != "draft" {
		t.Errorf("status = %q", got.Status)
	}
}

func TestReworkSpecAlwaysFits(t *testing.T) {
	s := newService(t)
	ctx := webCtx()
	anim := map[string]any{}
	for i := range 600 {
		anim[strings.Repeat("a", 30)+string(rune('A'+i%26))+strings.Repeat("b", i%7)+itoa(i)] = []int{i, i + 1}
	}
	a := mustAsset(t, s, AssetInput{Name: ptr("big"), Prompt: ptr(strings.Repeat("ü", 20000)),
		Generator: map[string]any{"settings": strings.Repeat("g", 12<<10)},
		Attrs:     map[string]any{"animations": anim, "frames": 4.0}}, "big.png", pngBytes(t, 8, 8, color.White))
	if b := marshalLen(a.Attrs["animations"]); b < 24<<10 {
		t.Fatalf("test data too small: %d", b)
	}
	res, err := s.MarkForRework(ctx, a.ID, "redo", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, has := res.Request.Spec["animations"]; has || res.Request.Spec["frames"] != 4.0 {
		t.Errorf("spec = %v", res.Request.Spec)
	}
	if p, _ := res.Request.Spec["prompt"].(string); len(p) > 3100 || !strings.HasPrefix(p, "ü") {
		t.Errorf("prompt not truncated: %d bytes", len(p))
	}
	if _, has := res.Request.Spec["generator"]; has {
		t.Error("oversized generator kept")
	}
}

func TestPlaceholderSpecNumbersAndSize(t *testing.T) {
	for _, v := range []any{"NaN", "Inf", "-Inf", "+Inf"} {
		if _, ok := wkSpecNumber(map[string]any{"width": v}, "width"); ok {
			t.Errorf("%v accepted", v)
		}
	}
	w, h, _, _ := placeholderSize(map[string]any{"width": "Inf", "height": "NaN"})
	if w != 64 || h != 64 {
		t.Errorf("size = %dx%d", w, h)
	}
	if w, h, _, _ := placeholderSize(map[string]any{"width": 99999.0, "height": 5000.0}); w != 2048 || h != 2048 {
		t.Errorf("cap = %dx%d", w, h)
	}
	// 1x1 cells in a big sheet must not take forever
	if _, err := placeholderPNG("x", 2048, 2048, 1, 1); err != nil {
		t.Error(err)
	}
	if d := placeholderSeconds(map[string]any{"duration_s": "NaN"}); d != placeholderDefaultSecs {
		t.Errorf("duration = %v", d)
	}
}

func TestMakePlaceholderConcurrent(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	r, _ := s.CreateRequest(ctx, RequestInput{Title: "conc", AssetType: "sprite"})
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, err := s.MakePlaceholder(ctx, r.ID)
			if err != nil {
				t.Errorf("placeholder: %v", err)
				return
			}
			ids <- a.ID
		}()
	}
	wg.Wait()
	close(ids)
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		} else if id != first {
			t.Errorf("two placeholders %s and %s", first, id)
		}
	}
	res, _ := s.ListAssets(ctx, ListQuery{Statuses: []string{"placeholder"}})
	if res.Total != 1 {
		t.Errorf("%d placeholder assets exist", res.Total)
	}
}

func TestExpiryIsReadOnlyOnGET(t *testing.T) {
	s := newService(t)
	ctx := actorCtx("gen")
	r, _ := s.CreateRequest(ctx, RequestInput{Title: "lease", AssetType: "sprite"})
	if _, err := s.ClaimRequest(ctx, r.ID, 5); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE asset_requests SET claim_expires_at = '2000-01-01T00:00:00Z' WHERE id = ?`, r.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetRequest(ctx, r.ID)
	list, _ := s.ListRequests(ctx, RequestQuery{Statuses: []string{"open"}})
	claimed, _ := s.ListRequests(ctx, RequestQuery{Statuses: []string{"claimed"}})
	if got.Status != "open" || got.ClaimedBy != "" || len(list) != 1 || len(claimed) != 0 {
		t.Fatalf("effective status: %+v open=%d claimed=%d", got, len(list), len(claimed))
	}
	var st string
	_ = s.db.QueryRow(`SELECT status FROM asset_requests WHERE id = ?`, r.ID).Scan(&st)
	if st != "claimed" {
		t.Fatalf("a read wrote: status %q", st)
	}
	// the next write settles it and records the expiry
	if _, err := s.ClaimRequest(actorCtx("other"), r.ID, 5); err != nil {
		t.Fatal(err)
	}
	feed, _ := s.ListActivity(ctx, ActivityQuery{Action: "request.expire"})
	if len(feed) != 1 {
		t.Errorf("expire activity = %+v", feed)
	}
}

func TestCaps(t *testing.T) {
	s := newService(t)
	ctx := webCtx()
	a := mustAsset(t, s, AssetInput{Name: ptr("n")}, "", nil)
	if _, err := s.db.Exec(`WITH RECURSIVE c(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM c WHERE i < ?)
		INSERT INTO asset_notes(id, asset_id, kind, body, author, created_at) SELECT 'not_' || i, ?, 'comment', 'x', '', '2026-01-01T00:00:00Z' FROM c`, maxNotesPerAsset, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddNote(ctx, a.ID, "comment", "one too many"); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Errorf("notes cap: %v", err)
	}
	r, _ := s.CreateRequest(ctx, RequestInput{Title: "cap", AssetType: "sprite"})
	if _, err := s.db.Exec(`WITH RECURSIVE c(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM c WHERE i < ?)
		INSERT INTO assets(id, name, created_at, updated_at) SELECT 'ast_' || printf('%012d', i), 'x', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z' FROM c`, maxSubmissionsPerRequest); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO request_submissions(request_id, asset_id, verdict, submitted_by, submitted_at)
		SELECT ?, id, 'rejected', '', '2026-01-01T00:00:00Z' FROM assets WHERE id LIKE 'ast_0%'`, r.ID); err != nil {
		t.Fatal(err)
	}
	extra := mustAsset(t, s, AssetInput{Name: ptr("extra")}, "", nil)
	if _, err := s.SubmitRequest(ctx, r.ID, []string{extra.ID}, ""); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Errorf("submissions cap: %v", err)
	}
}

func itoa(i int) string { return strconv.Itoa(i) }

func marshalLen(v any) int {
	b, _ := json.Marshal(v)
	return len(b)
}
