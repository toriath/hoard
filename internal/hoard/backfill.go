package hoard

import (
	"context"
	"os"
	"time"
)

// RunBackfill catches up on derived data for assets that were stored before
// it existed or without a server-side preview: perceptual hashes and colours
// of images, waveform and font thumbnails for assets that have none. It works
// through one file at a time (each holding the thumbnail slot while it
// decodes), pauses between files and stops when ctx is cancelled. Files that
// cannot be decoded are marked, so they are not retried at every start; a
// missing preview is retried, because the cost is only reading a header.
func (s *Service) RunBackfill(ctx context.Context) {
	type job struct{ assetID, fileID, filename string }
	collect := func(query string, want func(ext string) bool) []job {
		rows, err := s.db.QueryContext(ctx, query)
		if err != nil {
			s.log.Warn("backfill query", "err", err)
			return nil
		}
		defer rows.Close()
		var out []job
		for rows.Next() {
			var j job
			if err := rows.Scan(&j.assetID, &j.fileID, &j.filename); err == nil && want(ext(j.filename)) {
				out = append(out, j)
			}
		}
		return out
	}

	images := collect(`SELECT a.id, f.id, f.filename FROM assets a JOIN asset_files f ON f.id = a.primary_file_id
		WHERE a.deleted_at IS NULL AND f.phash = '' ORDER BY a.created_at DESC`, func(e string) bool { return decodableImage[e] })
	previews := collect(`SELECT a.id, f.id, f.filename FROM assets a JOIN asset_files f ON f.id = a.primary_file_id
		WHERE a.deleted_at IS NULL AND a.thumb_version = 0 ORDER BY a.created_at DESC`, func(e string) bool { return audioPreview[e] || fontPreview[e] })
	if len(images)+len(previews) == 0 {
		return
	}
	s.log.Info("backfilling image hashes, colours and previews", "images", len(images), "previews", len(previews))
	done := 0
	pause := func() bool {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(25 * time.Millisecond):
			return true
		}
	}

	for _, j := range images {
		path := s.previewFilePath(j.assetID, j.filename)
		st, err := s.statsFromFile(ctx, path)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			s.log.Debug("backfill image", "asset", j.assetID, "err", err)
			st = &imageStats{} // remember that there is nothing to find
		}
		if err := s.storeImageStats(ctx, j.assetID, j.fileID, st); err != nil {
			s.log.Debug("backfill image", "asset", j.assetID, "err", err)
		}
		done++
		if !pause() {
			return
		}
	}
	for _, j := range previews {
		path := s.previewFilePath(j.assetID, j.filename)
		e := ext(j.filename)
		var err error
		if audioPreview[e] {
			err = s.audioPreviewFile(ctx, j.assetID, path, e)
		} else {
			err = s.fontPreviewFile(ctx, j.assetID, path)
		}
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			s.log.Debug("backfill preview", "asset", j.assetID, "err", err)
		}
		done++
		if !pause() {
			return
		}
	}
	s.log.Info("backfill finished", "files", done)
}

// statsFromFile decodes an image and returns its hash and colours without
// touching the thumbnail.
func (s *Service) statsFromFile(ctx context.Context, path string) (*imageStats, error) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	release, err := s.acquireThumb(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	src, err := decodeImage(fh)
	if err != nil {
		return nil, err
	}
	return statsOf(scaleToThumb(src)), nil
}
