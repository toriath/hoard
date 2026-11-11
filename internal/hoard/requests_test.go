package hoard

import (
	"bytes"
	"context"
	"errors"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func actorCtx(name string) context.Context {
	return WithActor(context.Background(), Actor{UserID: "usr_" + name, Username: name, Via: "mcp", TokenName: "t"})
}

func newProject(t *testing.T, s *Service, name string) string {
	t.Helper()
	p, err := s.SaveProject(context.Background(), "", GroupInput{Name: wkStr(name)})
	if err != nil {
		t.Fatal(err)
	}
	return p.ID
}

func TestClaimRace(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	r, err := s.CreateRequest(ctx, RequestInput{Title: "Goblin", AssetType: "sprite"})
	if err != nil {
		t.Fatal(err)
	}
	const n = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins, conflicts := 0, 0
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.ClaimRequest(actorCtx(string(rune('a'+i))), r.ID, 30)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins++
			case errors.Is(err, ErrConflict):
				conflicts++
			default:
				t.Errorf("claim: %v", err)
			}
		}()
	}
	wg.Wait()
	if wins != 1 || conflicts != n-1 {
		t.Fatalf("wins=%d conflicts=%d", wins, conflicts)
	}

	// ClaimNext racing for two requests hands each out exactly once.
	for _, title := range []string{"A", "B"} {
		if _, err := s.CreateRequest(ctx, RequestInput{Title: title, AssetType: "sfx"}); err != nil {
			t.Fatal(err)
		}
	}
	got := make(chan string, 6)
	for i := range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := s.ClaimNext(actorCtx(string(rune('p'+i))), RequestQuery{AssetTypes: []string{"sfx"}}, 0)
			if err != nil {
				t.Errorf("claim next: %v", err)
				return
			}
			if c != nil {
				got <- c.ID
			}
		}()
	}
	wg.Wait()
	close(got)
	seen := map[string]bool{}
	for id := range got {
		if seen[id] {
			t.Errorf("request %s claimed twice", id)
		}
		seen[id] = true
	}
	if len(seen) != 2 {
		t.Errorf("claimed %d of 2 requests", len(seen))
	}
}

func TestClaimOrderFiltersAndLease(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	low, _ := s.CreateRequest(ctx, RequestInput{Title: "low", AssetType: "sprite"})
	old, _ := s.CreateRequest(ctx, RequestInput{Title: "old normal", AssetType: "sprite", Priority: 1})
	hi, _ := s.CreateRequest(ctx, RequestInput{Title: "urgent", AssetType: "sprite", Priority: 5})
	snd, _ := s.CreateRequest(ctx, RequestInput{Title: "sound", AssetType: "sfx", Priority: 9})
	_ = low
	_ = old

	c, err := s.ClaimNext(actorCtx("gen"), RequestQuery{AssetTypes: []string{"sprite"}}, 5)
	if err != nil || c == nil || c.ID != hi.ID {
		t.Fatalf("expected the urgent sprite, got %+v %v", c, err)
	}
	if c.ClaimedBy != "gen via MCP (t)" || c.Status != "claimed" || c.ClaimExpiresAt == "" {
		t.Errorf("claim not recorded: %+v", c)
	}
	// Someone else cannot claim it, renew or release it; the claimer can.
	if _, err := s.ClaimRequest(actorCtx("other"), hi.ID, 5); !errors.Is(err, ErrConflict) {
		t.Errorf("double claim: %v", err)
	}
	if _, err := s.RenewClaim(actorCtx("other"), hi.ID, 5); !errors.Is(err, ErrConflict) {
		t.Errorf("foreign renew: %v", err)
	}
	if _, err := s.ReleaseClaim(actorCtx("other"), hi.ID); !errors.Is(err, ErrConflict) {
		t.Errorf("foreign release: %v", err)
	}
	if _, err := s.RenewClaim(actorCtx("gen"), hi.ID, 600); err != nil {
		t.Errorf("renew: %v", err)
	}
	mine, _ := s.ListRequests(actorCtx("gen"), RequestQuery{ClaimedByMe: true})
	if len(mine) != 1 || mine[0].ID != hi.ID {
		t.Errorf("claimed by me = %+v", mine)
	}

	// An expired lease returns the request to the queue, lazily.
	if _, err := s.db.Exec(`UPDATE asset_requests SET claim_expires_at = '2000-01-01T00:00:00Z' WHERE id = ?`, hi.ID); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetRequest(ctx, hi.ID)
	if err != nil || got.Status != "open" || got.ClaimedBy != "" {
		t.Fatalf("expired claim not released: %+v %v", got, err)
	}
	// ... and also when it is claimed without a prior read.
	if _, err := s.ClaimRequest(actorCtx("gen"), hi.ID, 5); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE asset_requests SET claim_expires_at = '2000-01-01T00:00:00Z' WHERE id = ?`, hi.ID); err != nil {
		t.Fatal(err)
	}
	if c, err := s.ClaimRequest(actorCtx("other"), hi.ID, 99999); err != nil || c.ClaimedBy != "other via MCP (t)" {
		t.Fatalf("take over expired claim: %+v %v", c, err)
	}
	// The lease is capped at 24 hours.
	if c, _ := s.GetRequest(ctx, hi.ID); c.ClaimExpiresAt > nowPlusHours(25) {
		t.Errorf("lease not capped: %s", c.ClaimExpiresAt)
	}

	// The highest priority overall comes first without a filter; the queue empties.
	c, _ = s.ClaimNext(actorCtx("gen"), RequestQuery{}, 0)
	if c == nil || c.ID != snd.ID {
		t.Fatalf("unfiltered next = %+v", c)
	}
	c, _ = s.ClaimNext(actorCtx("gen"), RequestQuery{Kind: "rework"}, 0)
	if c != nil {
		t.Errorf("no rework requests exist: %+v", c)
	}
	if _, err := s.ReleaseClaim(actorCtx("gen"), snd.ID); err != nil {
		t.Errorf("release: %v", err)
	}
}

func nowPlusHours(h int) string {
	return time.Now().UTC().Add(time.Duration(h) * time.Hour).Format(time.RFC3339)
}

func TestSubmitAcceptSupersedesPlaceholder(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	proj := newProject(t, s, "Game")
	col, err := s.SaveCollection(ctx, "", GroupInput{Name: wkStr("Pack")})
	if err != nil {
		t.Fatal(err)
	}

	r, err := s.CreateRequest(ctx, RequestInput{ProjectID: proj, Title: "Hero idle", AssetType: "sprite",
		Spec: map[string]any{"width": 48.0, "height": 32.0, "filename": "hero_idle.png"}, Placeholder: true})
	if err != nil {
		t.Fatal(err)
	}
	if r.PlaceholderAssetID == "" {
		t.Fatal("no placeholder linked")
	}
	ph, _ := s.GetAsset(ctx, r.PlaceholderAssetID)
	if ph.Status != "placeholder" || ph.Name != "[placeholder] Hero idle" || len(ph.Projects) != 1 || ph.Files[0].Filename != "hero_idle.png" {
		t.Fatalf("placeholder = %+v", ph)
	}
	// Put the placeholder in a collection and give the project usage a note.
	if _, err := s.UpdateAsset(ctx, ph.ID, AssetInput{AddCollections: []string{col.ID}, AddProjects: []string{proj}, ProjectNote: "title screen"}); err != nil {
		t.Fatal(err)
	}

	cand := mustAsset(t, s, AssetInput{Name: ptr("Hero idle v1")}, "hero_idle.png", pngBytes(t, 48, 32, color.NRGBA{9, 9, 9, 255}))
	gen := actorCtx("gen")
	if _, err := s.ClaimRequest(gen, r.ID, 30); err != nil {
		t.Fatal(err)
	}
	r, err = s.SubmitRequest(gen, r.ID, []string{cand.ID}, "first try")
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "submitted" || r.ClaimedBy != "" || len(r.Submissions) != 1 || r.Submissions[0].Verdict != "" || r.Submissions[0].AssetName != "Hero idle v1" {
		t.Fatalf("after submit: %+v", r)
	}
	if c, _ := s.GetAsset(ctx, cand.ID); c.Status != "draft" {
		t.Errorf("candidate status = %q", c.Status)
	}
	// The placeholder cannot be submitted as its own replacement.
	if _, err := s.SubmitRequest(ctx, r.ID, []string{ph.ID}, ""); err == nil {
		t.Error("placeholder accepted as a candidate")
	}

	r, err = s.AcceptSubmission(ctx, r.ID, cand.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "done" || r.AcceptedAssetID != cand.ID || r.ClosedAt == "" {
		t.Fatalf("after accept: %+v", r)
	}
	cand, _ = s.GetAsset(ctx, cand.ID)
	ph, _ = s.GetAsset(ctx, ph.ID)
	if cand.Status != "approved" || ph.Status != "superseded" || ph.SupersededBy != cand.ID {
		t.Errorf("statuses: candidate %q placeholder %q by %q", cand.Status, ph.Status, ph.SupersededBy)
	}
	if len(cand.Projects) != 1 || cand.Projects[0].ID != proj || cand.Projects[0].Note != "title screen" {
		t.Errorf("project usage not moved: %+v", cand.Projects)
	}
	if len(ph.Projects) != 0 {
		t.Errorf("superseded asset still counted in project: %+v", ph.Projects)
	}
	if len(cand.Collections) != 1 || len(ph.Collections) != 1 {
		t.Errorf("collections: candidate %v placeholder %v", cand.Collections, ph.Collections)
	}
	// The sidecars follow.
	b, _ := os.ReadFile(filepath.Join(s.DataDir(), "assets", ph.ID, "asset.json"))
	if !strings.Contains(string(b), `"superseded_by": "`+cand.ID+`"`) {
		t.Errorf("sidecar of the placeholder: %s", b)
	}
	if _, err := s.AcceptSubmission(ctx, r.ID, cand.ID, true); !errors.Is(err, ErrConflict) {
		t.Errorf("second accept: %v", err)
	}
}

func TestAcceptWithoutSupersedeAndNotChosen(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	proj := newProject(t, s, "Game")
	r, _ := s.CreateRequest(ctx, RequestInput{ProjectID: proj, Title: "Door", AssetType: "sprite", Placeholder: true})
	a := mustAsset(t, s, AssetInput{Name: ptr("a")}, "", nil)
	b := mustAsset(t, s, AssetInput{Name: ptr("b")}, "", nil)
	if _, err := s.SubmitRequest(ctx, r.ID, []string{a.ID, b.ID, a.ID}, ""); err != nil {
		t.Fatal(err)
	}
	r, err := s.AcceptSubmission(ctx, r.ID, b.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	ph, _ := s.GetAsset(ctx, r.PlaceholderAssetID)
	if ph.Status != "placeholder" || len(ph.Projects) != 1 {
		t.Errorf("placeholder touched without supersede: %+v", ph)
	}
	bb, _ := s.GetAsset(ctx, b.ID)
	if len(bb.Projects) != 1 || bb.Projects[0].ID != proj {
		t.Errorf("request project not added: %+v", bb.Projects)
	}
	verdicts := map[string]string{}
	for _, sub := range r.Submissions {
		verdicts[sub.AssetID] = sub.Verdict
	}
	if verdicts[a.ID] != "rejected" || verdicts[b.ID] != "accepted" {
		t.Errorf("verdicts = %v", verdicts)
	}
	if aa, _ := s.GetAsset(ctx, a.ID); aa.Status != "draft" {
		t.Errorf("the other candidate must stay untouched, got %q", aa.Status)
	}
}

func TestRejectSubmissionReopens(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	r, _ := s.CreateRequest(ctx, RequestInput{Title: "Slime", AssetType: "sprite"})
	cand := mustAsset(t, s, AssetInput{Name: ptr("slime v1")}, "", nil)
	gen := actorCtx("gen")
	if _, err := s.ClaimRequest(gen, r.ID, 30); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SubmitRequest(gen, r.ID, []string{cand.ID}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RejectSubmission(ctx, r.ID, cand.ID, "  "); err == nil {
		t.Error("rejection without feedback accepted")
	}
	r, err := s.RejectSubmission(ctx, r.ID, cand.ID, "too green, add a highlight")
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "open" || r.ClaimedBy != "" || len(r.Submissions) != 1 || r.Submissions[0].Verdict != "rejected" || r.Submissions[0].Feedback != "too green, add a highlight" {
		t.Fatalf("after reject: %+v", r)
	}
	c, _ := s.GetAsset(ctx, cand.ID)
	notes, _ := s.ListNotes(ctx, cand.ID, false)
	if c.Status != "rejected" || len(notes) != 1 || notes[0].Kind != "rework" {
		t.Errorf("candidate %q notes %+v", c.Status, notes)
	}
	// A generator can claim it again and sees the feedback; resubmitting the fixed candidate makes it a draft again.
	got, err := s.ClaimRequest(gen, r.ID, 30)
	if err != nil || got.Submissions[0].Feedback == "" {
		t.Fatalf("reclaim: %+v %v", got, err)
	}
	if _, err := s.SubmitRequest(gen, r.ID, []string{cand.ID}, "fixed"); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.GetAsset(ctx, cand.ID); c.Status != "draft" {
		t.Errorf("resubmitted status = %q", c.Status)
	}
	// With two candidates pending, rejecting one leaves the request in review.
	other := mustAsset(t, s, AssetInput{Name: ptr("slime v2")}, "", nil)
	if _, err := s.SubmitRequest(ctx, r.ID, []string{other.ID}, ""); err != nil {
		t.Fatal(err)
	}
	if r, _ = s.RejectSubmission(ctx, r.ID, other.ID, "no"); r.Status != "submitted" {
		t.Errorf("request with another pending candidate is %q", r.Status)
	}
}

func TestMarkForReworkReusesRequestAndAccepts(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	proj := newProject(t, s, "Game")
	old := mustAsset(t, s, AssetInput{Name: ptr("Knight"), Prompt: ptr("a knight, pixel art"), AddProjects: []string{proj}, ProjectNote: "hero",
		Generator: map[string]any{"tool": "Draw Things", "seed": 7.0}, Attrs: map[string]any{"frames": 4.0}},
		"knight.png", pngBytes(t, 64, 48, color.NRGBA{1, 2, 3, 255}))

	res, err := s.ReviewAsset(ctx, old.ID, "rework", "helmet is too big", 2, "")
	if err != nil {
		t.Fatal(err)
	}
	rq := res.Request
	if res.Asset.Status != "rework" || res.Note.Kind != "rework" || rq.Kind != "rework" || rq.TargetAssetID != old.ID || rq.ProjectID != proj || rq.Priority != 2 || res.Reused {
		t.Fatalf("rework: %+v / %+v", res.Asset.Status, rq)
	}
	if rq.Spec["width"] != 64.0 || rq.Spec["prompt"] != "a knight, pixel art" || rq.Spec["frames"] != 4.0 || rq.Spec["filename"] != "knight.png" ||
		rq.Spec["generator"].(map[string]any)["tool"] != "Draw Things" || rq.AssetType != "sprite" {
		t.Errorf("spec = %v", rq.Spec)
	}
	if len(rq.Notes) != 1 {
		t.Errorf("notes of the target = %+v", rq.Notes)
	}

	// Marking again reuses the open request and keeps the higher priority.
	res2, err := s.MarkForRework(ctx, old.ID, "and the sword is blurry", 0, "")
	if err != nil || !res2.Reused || res2.Request.ID != rq.ID || res2.Request.Priority != 2 || res2.Request.Description != "and the sword is blurry" {
		t.Fatalf("second rework: %+v %v", res2, err)
	}
	if list, _ := s.ListRequests(ctx, RequestQuery{Kind: "rework"}); len(list) != 1 {
		t.Errorf("%d rework requests", len(list))
	}
	if _, err := s.MarkForRework(ctx, old.ID, "", 0, ""); err == nil {
		t.Error("rework without feedback accepted")
	}

	// Generator round trip: the candidate derives from the target; accepting supersedes it.
	gen := actorCtx("gen")
	if _, err := s.ClaimNext(gen, RequestQuery{Kind: "rework"}, 0); err != nil {
		t.Fatal(err)
	}
	cand := mustAsset(t, s, AssetInput{Name: ptr("Knight v2")}, "knight.png", pngBytes(t, 64, 48, color.NRGBA{4, 5, 6, 255}))
	if _, err := s.SubmitRequest(gen, rq.ID, []string{cand.ID}, ""); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.GetAsset(ctx, cand.ID); c.DerivedFrom != old.ID || c.Status != "draft" {
		t.Errorf("candidate lineage: %+v", c)
	}
	if _, err := s.AcceptSubmission(ctx, rq.ID, cand.ID, true); err != nil {
		t.Fatal(err)
	}
	old, _ = s.GetAsset(ctx, old.ID)
	cand, _ = s.GetAsset(ctx, cand.ID)
	if old.Status != "superseded" || old.SupersededBy != cand.ID || len(cand.Projects) != 1 || cand.Projects[0].Note != "hero" || len(old.Projects) != 0 {
		t.Errorf("after accept: old %+v cand %+v", old.Status, cand.Projects)
	}
	if notes, _ := s.ListNotes(ctx, old.ID, false); len(notes) != 0 {
		t.Errorf("rework notes should be resolved: %+v", notes)
	}
	if notes, _ := s.ListNotes(ctx, old.ID, true); len(notes) != 2 {
		t.Errorf("history lost: %+v", notes)
	}
	// A finished rework can be started again.
	if res, err := s.MarkForRework(ctx, cand.ID, "wings", 1, ""); err != nil || res.Reused {
		t.Errorf("new rework: %+v %v", res, err)
	}
}

func TestApproveRejectAndNotes(t *testing.T) {
	s := newService(t)
	ctx := actorCtx("alice")
	a := mustAsset(t, s, AssetInput{Name: ptr("coin")}, "", nil)
	res, err := s.ApproveAsset(ctx, a.ID, "")
	if err != nil || res.Asset.Status != "approved" || res.Note != nil {
		t.Fatalf("approve: %+v %v", res, err)
	}
	if _, err := s.RejectAsset(ctx, a.ID, ""); err == nil {
		t.Error("reject without reason accepted")
	}
	res, err = s.RejectAsset(ctx, a.ID, "wrong style")
	if err != nil || res.Asset.Status != "rejected" || res.Note.Kind != "rejection" || res.Note.Author != "alice via MCP (t)" {
		t.Fatalf("reject: %+v %v", res, err)
	}
	if _, err := s.ReviewAsset(ctx, a.ID, "bogus", "", 0, ""); err == nil {
		t.Error("unknown verdict accepted")
	}

	n, err := s.AddNote(ctx, a.ID, "", "looks fine to me")
	if err != nil || n.Kind != "comment" {
		t.Fatal(n, err)
	}
	if _, err := s.AddNote(ctx, a.ID, "poem", "x"); err == nil {
		t.Error("unknown note kind accepted")
	}
	if _, err := s.AddNote(ctx, a.ID, "comment", strings.Repeat("x", maxNoteBody+1)); err == nil {
		t.Error("oversized note accepted")
	}
	if _, err := s.AddNote(ctx, "ast_000000000000", "comment", "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("note on unknown asset: %v", err)
	}
	if _, err := s.ResolveNote(ctx, n.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveNote(ctx, n.ID); err != nil {
		t.Error("resolving twice must be fine")
	}
	open, _ := s.ListNotes(ctx, a.ID, false)
	all, _ := s.ListNotes(ctx, a.ID, true)
	if len(open) != 1 || len(all) != 2 {
		t.Errorf("open %d all %d", len(open), len(all))
	}
	feed, _ := s.ListActivity(ctx, ActivityQuery{AssetID: a.ID, Limit: 50})
	actions := map[string]bool{}
	for _, f := range feed {
		actions[f.Action] = true
	}
	for _, want := range []string{"asset.review", "note.add", "note.resolve"} {
		if !actions[want] {
			t.Errorf("activity lacks %s: %v", want, actions)
		}
	}
	// The sidecar reflects the status.
	b, _ := os.ReadFile(filepath.Join(s.DataDir(), "assets", a.ID, "asset.json"))
	if !strings.Contains(string(b), `"status": "rejected"`) {
		t.Errorf("sidecar: %s", b)
	}
}

func TestRequestValidationAndLifecycle(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	for name, in := range map[string]RequestInput{
		"no title":       {AssetType: "sprite"},
		"bad type":       {Title: "x", AssetType: "bogus"},
		"huge spec":      {Title: "x", Spec: map[string]any{"k": strings.Repeat("x", maxSpecBytes)}},
		"no project":     {Title: "x", ProjectID: "prj_nope"},
		"no placeholder": {Title: "x", AssetType: "model", Placeholder: true},
	} {
		if _, err := s.CreateRequest(ctx, in); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	r, err := s.CreateRequest(ctx, RequestInput{Title: "Rune", AssetType: "sprite", Priority: 1000})
	if err != nil || r.Priority != 100 {
		t.Fatalf("%+v %v", r, err)
	}
	r, err = s.UpdateRequest(ctx, r.ID, RequestPatch{Title: wkStr("Rune v2"), Priority: ptr(3), Spec: map[string]any{"width": 8}})
	if err != nil || r.Title != "Rune v2" || r.Priority != 3 || r.Spec["width"] != 8.0 {
		t.Fatalf("update: %+v %v", r, err)
	}
	if _, err := s.SubmitRequest(ctx, r.ID, []string{"ast_000000000000"}, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("submit unknown asset: %v", err)
	}
	trashed := mustAsset(t, s, AssetInput{Name: ptr("t")}, "", nil)
	if _, err := s.Bulk(ctx, BulkOp{IDs: []string{trashed.ID}, Action: "trash"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SubmitRequest(ctx, r.ID, []string{trashed.ID}, ""); err == nil {
		t.Error("trashed asset submitted")
	}
	if _, err := s.CancelRequest(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelRequest(ctx, r.ID); !errors.Is(err, ErrConflict) {
		t.Errorf("second cancel: %v", err)
	}
	if _, err := s.ClaimRequest(ctx, r.ID, 5); !errors.Is(err, ErrConflict) {
		t.Errorf("claim cancelled: %v", err)
	}
	if _, err := s.ListRequests(ctx, RequestQuery{Statuses: []string{"nope"}}); err == nil {
		t.Error("bad status filter accepted")
	}
	done, _ := s.ListRequests(ctx, RequestQuery{Statuses: []string{"cancelled"}, Sort: "recent"})
	if len(done) != 1 {
		t.Errorf("cancelled list = %d", len(done))
	}
}

func TestPlaceholderFiles(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	proj := newProject(t, s, "Game")

	r, _ := s.CreateRequest(ctx, RequestInput{ProjectID: proj, Title: "Wall tiles", AssetType: "tileset",
		Spec: map[string]any{"frame_width": 16.0, "frame_height": 16.0, "frames": 6.0, "columns": 3.0}})
	a, err := s.MakePlaceholder(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	f := a.Files[0]
	if f.Filename != "wall-tiles.png" || f.Width != 48 || f.Height != 32 || a.ThumbVersion == 0 || a.Status != "placeholder" {
		t.Errorf("sheet placeholder: %+v %+v", f, a)
	}
	if again, err := s.MakePlaceholder(ctx, r.ID); err != nil || again.ID != a.ID {
		t.Errorf("not idempotent: %v", err)
	}
	fh, _, err := s.OpenFile(ctx, a.ID, f.Filename)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(fh)
	fh.Close()
	if err != nil || img.Bounds().Dx() != 48 {
		t.Fatalf("png: %v", err)
	}

	big, _ := s.CreateRequest(ctx, RequestInput{Title: "Huge", AssetType: "texture", Spec: map[string]any{"width": 100000.0, "height": "20"}})
	if w, h, _, _ := placeholderSize(big.Spec); w != 4096 || h != 20 {
		t.Errorf("size = %dx%d", w, h)
	}
	if w, h, _, _ := placeholderSize(nil); w != 64 || h != 64 {
		t.Errorf("default size = %dx%d", w, h)
	}
	// A long title in a tiny image must not panic or overflow.
	if _, err := placeholderPNG(strings.Repeat("Ünïcode title ", 20), 10, 10, 0, 0); err != nil {
		t.Error(err)
	}
	if data, err := placeholderPNG("Hello placeholder", 128, 40, 0, 0); err != nil {
		t.Error(err)
	} else if img, _ := png.Decode(bytes.NewReader(data)); img == nil || img.Bounds().Dx() != 128 {
		t.Error("128px wide png expected")
	}

	// Audio.
	snd, _ := s.CreateRequest(ctx, RequestInput{Title: "Door creak", AssetType: "sfx", Spec: map[string]any{"duration_s": 2.5, "filename": "../sfx/door.ogg"}})
	wav, err := s.MakePlaceholder(ctx, snd.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wav.Files[0].Filename != "door.wav" || wav.Attrs["duration_s"] != 2.5 || wav.PreviewKind != KindAudio {
		t.Errorf("wav placeholder: %+v %v", wav.Files, wav.Attrs)
	}
	fh, _, _ = s.OpenFile(ctx, wav.ID, "door.wav")
	raw := new(bytes.Buffer)
	raw.ReadFrom(fh)
	fh.Close()
	b := raw.Bytes()
	if string(b[:4]) != "RIFF" || string(b[8:16]) != "WAVEfmt " || len(b) != 44+int(2.5*placeholderRate)*2 {
		t.Errorf("wav header/length: %q len %d", b[:16], len(b))
	}
	if placeholderSeconds(map[string]any{"duration_s": 3600.0}) != placeholderMaxSecs || placeholderSeconds(nil) != 1 {
		t.Error("duration clamp")
	}
	loud := 0
	for i := 44; i+1 < len(b); i += 2 {
		if v := int16(uint16(b[i]) | uint16(b[i+1])<<8); v > 8000 || v < -8000 {
			loud++
		}
	}
	if loud != 0 {
		t.Errorf("placeholder beep is too loud (%d samples)", loud)
	}

	// Types without a placeholder, and rework requests, say so.
	m, _ := s.CreateRequest(ctx, RequestInput{Title: "Model", AssetType: "model"})
	if _, err := s.MakePlaceholder(ctx, m.ID); err == nil || !strings.Contains(err.Error(), "no placeholder") {
		t.Errorf("model placeholder: %v", err)
	}
	old := mustAsset(t, s, AssetInput{Name: ptr("x")}, "x.png", pngBytes(t, 4, 4, color.White))
	rw, _ := s.MarkForRework(ctx, old.ID, "redo", 0, "")
	if _, err := s.MakePlaceholder(ctx, rw.Request.ID); err == nil {
		t.Error("placeholder for a rework request")
	}
}
