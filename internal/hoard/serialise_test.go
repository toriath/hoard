package hoard

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

// sidecarMatchesDB fails unless asset.json describes exactly what the index
// says right now.
func sidecarMatchesDB(t *testing.T, s *Service, id string) {
	t.Helper()
	a, err := s.GetAsset(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(s.assetDir(id), "asset.json"))
	if err != nil {
		t.Fatal(err)
	}
	var side, db map[string]any
	if err := json.Unmarshal(raw, &side); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(a)
	json.Unmarshal(b, &db)
	if !reflect.DeepEqual(side, db) {
		t.Errorf("sidecar differs from the index:\n sidecar %v\n index   %v", side, db)
	}
}

// burst runs functions concurrently and reports every error.
type burst struct {
	t    *testing.T
	wg   sync.WaitGroup
	mu   sync.Mutex
	errs []error
}

func (b *burst) run(f func() error) {
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		if err := f(); err != nil {
			b.mu.Lock()
			b.errs = append(b.errs, err)
			b.mu.Unlock()
		}
	}()
}

func (b *burst) wait() {
	b.t.Helper()
	b.wg.Wait()
	for _, err := range b.errs {
		b.t.Errorf("concurrent mutation failed: %v", err)
	}
	b.errs = nil
}

func TestConcurrentMutationsOfOneAsset(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	a := mustAsset(t, s, AssetInput{Name: ptr("Hero")}, "hero.png", pngBytes(t, 4, 4, color.White))
	b := &burst{t: t}

	for i := range 12 {
		// The same file name from many uploads at once: each must replace the
		// previous one instead of colliding on the unique name.
		data := pngBytes(t, 4+i, 4, color.White)
		b.run(func() error {
			_, err := s.AddFile(ctx, a.ID, "same.png", bytes.NewReader(data), 0)
			return err
		})
		b.run(func() error {
			_, err := s.UpdateAsset(ctx, a.ID, AssetInput{AddTags: []string{fmt.Sprintf("t%d", i)}, Description: ptr(fmt.Sprint("d", i))})
			return err
		})
		b.run(func() error {
			_, err := s.AddFile(ctx, a.ID, fmt.Sprintf("extra%d.png", i), bytes.NewReader(data), 0)
			return err
		})
	}
	b.wait()

	got, _ := s.GetAsset(ctx, a.ID)
	if len(got.Tags) != 12 || got.FileCount != len(got.Files) || len(got.Files) != 14 {
		t.Fatalf("tags %d, files %d (count %d)", len(got.Tags), len(got.Files), got.FileCount)
	}
	var total int64
	for _, f := range got.Files {
		total += f.Size
		st, err := os.Stat(filepath.Join(s.assetDir(a.ID), "files", f.Filename))
		if err != nil || st.Size() != f.Size {
			t.Errorf("%s: index says %d bytes, disk says %v (%v)", f.Filename, f.Size, st, err)
		}
	}
	if total != got.TotalSize {
		t.Errorf("total_size %d, files add up to %d", got.TotalSize, total)
	}
	sidecarMatchesDB(t, s, a.ID)

	// Deleting and re-pointing the primary file while uploads go on.
	for _, f := range got.Files[:6] {
		b.run(func() error { return s.DeleteFile(ctx, a.ID, f.ID) })
	}
	b.run(func() error { return s.SetPrimaryFile(ctx, a.ID, got.Files[10].ID) })
	b.run(func() error {
		_, err := s.AddFile(ctx, a.ID, "late.png", bytes.NewReader(pngBytes(t, 3, 3, color.Black)), 0)
		return err
	})
	b.wait()
	sidecarMatchesDB(t, s, a.ID)
	after, _ := s.GetAsset(ctx, a.ID)
	if after.FileCount != len(after.Files) || after.Primary() == nil {
		t.Errorf("inconsistent after deletes: %d files, count %d, primary %q", len(after.Files), after.FileCount, after.PrimaryFileID)
	}
}
