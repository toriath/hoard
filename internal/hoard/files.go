package hoard

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif" // register decoders
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	_ "golang.org/x/image/bmp"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

const (
	thumbSize = 512 // longest edge of generated thumbnails
	// maxDecodedBytes bounds the memory one decoded image may take. A small,
	// highly compressible file can declare enormous dimensions, so the check
	// is on the decoded size (64 MP at 8 bits per channel, 32 MP at 16).
	maxDecodedBytes = 256 << 20
)

// decodedSize estimates the memory needed to decode an image.
func decodedSize(cfg image.Config) int64 {
	perPixel := int64(4)
	switch cfg.ColorModel {
	case color.RGBA64Model, color.NRGBA64Model:
		perPixel = 8
	}
	return int64(cfg.Width) * int64(cfg.Height) * perPixel
}

// ErrTooLarge is returned when an upload exceeds the configured limit.
var ErrTooLarge = errors.New("file exceeds the upload size limit")

// AddFileResult describes a stored file.
type AddFileResult struct {
	File        File     `json:"file"`
	Replaced    bool     `json:"replaced,omitempty"`     // an existing file with the same name was overwritten
	DuplicateOf []string `json:"duplicate_of,omitempty"` // other assets holding identical content
	SimilarTo   []string `json:"similar_to,omitempty"`   // other assets whose picture is near-identical (perceptual hash)
}

// AddFile streams r into the asset's files directory. A file with the same
// name is replaced. The first file decides name and type of an asset that did
// not get them explicitly.
func (s *Service) AddFile(ctx context.Context, assetID, filename string, r io.Reader, maxBytes int64) (*AddFileResult, error) {
	if !ValidAssetID(assetID) {
		return nil, fmt.Errorf("asset %q: %w", assetID, ErrNotFound)
	}
	a, err := s.GetAsset(ctx, assetID)
	if err != nil {
		return nil, err
	}
	if a.DeletedAt != nil {
		return nil, invalid("asset %s is in the trash; restore it before adding files", assetID)
	}
	filename = SanitizeFilename(filename)

	// 1. Spool to tmp/ on the same filesystem while hashing.
	tmp, err := os.CreateTemp(filepath.Join(s.dataDir, "tmp"), "upload-*")
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	h := sha256.New()
	limited := io.Reader(r)
	if maxBytes > 0 {
		limited = io.LimitReader(r, maxBytes+1)
	}
	size, err := io.Copy(io.MultiWriter(tmp, h), limited)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, fmt.Errorf("store upload: %w", err)
	}
	if maxBytes > 0 && size > maxBytes {
		return nil, ErrTooLarge
	}
	if size == 0 {
		return nil, invalid("file %q is empty", filename)
	}
	sum := hex.EncodeToString(h.Sum(nil))

	// The content has arrived completely. From here on the bookkeeping is
	// finished even if the client disconnects, so that the file on disk and
	// the index cannot drift apart.
	ctx = context.WithoutCancel(ctx)

	f := File{
		ID: newID("fil"), Filename: filename, Mime: MimeFor(filename), Size: size, SHA256: sum,
		Kind: PreviewKind(filename), CreatedAt: now(),
	}
	if decodableImage[ext(filename)] {
		if fh, err := os.Open(tmpPath); err == nil {
			if cfg, _, err := image.DecodeConfig(fh); err == nil {
				f.Width, f.Height = cfg.Width, cfg.Height
			}
			fh.Close()
		}
	}

	// 2. Move into place.
	dir := filepath.Join(s.assetDir(assetID), "files")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	dst := filepath.Join(dir, filename)
	// A file whose name differs only in letter case counts as the same file:
	// on a case-insensitive dataset both names are one directory entry, so
	// keeping two index rows would let one delete the other's content.
	var replaced []string // ids of the rows this upload supersedes
	exists := false
	for _, old := range a.Files {
		if !strings.EqualFold(old.Filename, filename) {
			continue
		}
		replaced = append(replaced, old.ID)
		exists = true
		if old.Filename != filename {
			_ = os.Remove(filepath.Join(dir, old.Filename))
		}
	}
	if err := os.Rename(tmpPath, dst); err != nil {
		return nil, err
	}
	_ = os.Chmod(dst, 0o640)

	// 3. Index.
	res := &AddFileResult{File: f}
	makePrimary := false
	err = s.tx(ctx, func(tx *sql.Tx) error {
		for _, id := range replaced {
			if _, err := tx.ExecContext(ctx, `DELETE FROM asset_files WHERE id = ?`, id); err != nil {
				return err
			}
			res.Replaced = true
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO asset_files(id, asset_id, filename, mime, size, sha256, width, height, created_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`, f.ID, assetID, f.Filename, f.Mime, f.Size, f.SHA256, f.Width, f.Height, f.CreatedAt); err != nil {
			return err
		}

		// The primary file drives the preview: prefer one that can be previewed.
		cur := a.Primary()
		replacedPrimary := cur != nil && slices.Contains(replaced, cur.ID)
		makePrimary = cur == nil || replacedPrimary || (cur.Kind == KindNone && f.Kind != KindNone)
		if makePrimary {
			if _, err := tx.ExecContext(ctx, `UPDATE assets SET primary_file_id = ? WHERE id = ?`, f.ID, assetID); err != nil {
				return err
			}
		}
		if a.typeAuto && (cur == nil || makePrimary) {
			if _, err := tx.ExecContext(ctx, `UPDATE assets SET type = ? WHERE id = ?`, GuessType(filename, size), assetID); err != nil {
				return err
			}
		}
		if a.nameAuto && a.Name == "" {
			stem := strings.TrimSuffix(filename, filepath.Ext(filename))
			if _, err := tx.ExecContext(ctx, `UPDATE assets SET name = ? WHERE id = ?`, stem, assetID); err != nil {
				return err
			}
		}
		if err := refreshAggregates(ctx, tx, assetID); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT DISTINCT f.asset_id FROM asset_files f JOIN assets a ON a.id = f.asset_id
			WHERE f.sha256 = ? AND f.asset_id != ? AND a.deleted_at IS NULL LIMIT 10`, sum, assetID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			res.DuplicateOf = append(res.DuplicateOf, id)
		}
		rows.Close()
		verb := "added"
		if res.Replaced {
			verb = "replaced"
		}
		s.record(ctx, tx, "asset.file", assetID, f.ID, verb+" "+filename)
		return reindex(ctx, tx, assetID)
	})
	if err != nil {
		if !exists { // do not leave a file behind that the index does not know
			_ = os.Remove(dst)
		}
		return nil, err
	}

	// 4. Preview of the primary file (best effort): a thumbnail from an image,
	// or a waveform / font specimen when there is none yet. Images also yield
	// a perceptual hash and dominant colours.
	if makePrimary {
		if st := s.previewPrimary(ctx, assetID, f.ID, filename, dst); st != nil && st.PHash != "" {
			res.SimilarTo = s.nearAssets(ctx, st.PHash, assetID, NearIdenticalDistance, 10, res.DuplicateOf)
		}
	}
	s.syncSidecar(ctx, assetID)
	return res, nil
}

func refreshAggregates(ctx context.Context, tx *sql.Tx, assetID string) error {
	_, err := tx.ExecContext(ctx, `UPDATE assets SET
		file_count = (SELECT COUNT(*) FROM asset_files WHERE asset_id = ?1),
		total_size = (SELECT COALESCE(SUM(size), 0) FROM asset_files WHERE asset_id = ?1),
		updated_at = ?2 WHERE id = ?1`, assetID, now())
	return err
}

// DeleteFile removes one file from an asset.
func (s *Service) DeleteFile(ctx context.Context, assetID, fileID string) error {
	var filename string
	err := s.tx(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `SELECT filename FROM asset_files WHERE id = ? AND asset_id = ?`, fileID, assetID).Scan(&filename)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("file %q: %w", fileID, ErrNotFound)
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM asset_files WHERE id = ?`, fileID); err != nil {
			return err
		}
		// Re-point the primary file if it was the one removed.
		if _, err := tx.ExecContext(ctx, `UPDATE assets SET primary_file_id = COALESCE(
			(SELECT id FROM asset_files WHERE asset_id = ?1 ORDER BY created_at, filename LIMIT 1), '')
			WHERE id = ?1 AND primary_file_id = ?2`, assetID, fileID); err != nil {
			return err
		}
		if err := refreshAggregates(ctx, tx, assetID); err != nil {
			return err
		}
		s.record(ctx, tx, "asset.file", assetID, fileID, "deleted "+filename)
		return reindex(ctx, tx, assetID)
	})
	if err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(s.assetDir(assetID), "files", filename)); err != nil && !os.IsNotExist(err) {
		s.log.Warn("remove file", "asset", assetID, "file", filename, "err", err)
	}
	s.syncSidecar(ctx, assetID)
	return nil
}

// SetPrimaryFile selects which file drives preview and thumbnail.
func (s *Service) SetPrimaryFile(ctx context.Context, assetID, fileID string) error {
	var filename string
	err := s.db.QueryRowContext(ctx, `SELECT filename FROM asset_files WHERE id = ? AND asset_id = ?`, fileID, assetID).Scan(&filename)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("file %q: %w", fileID, ErrNotFound)
	}
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE assets SET primary_file_id = ?, updated_at = ? WHERE id = ?`, fileID, now(), assetID); err != nil {
		return err
	}
	s.previewPrimary(ctx, assetID, fileID, filename, filepath.Join(s.assetDir(assetID), "files", filename))
	s.syncSidecar(ctx, assetID)
	return nil
}

// OpenFile opens a stored file by name for reading. The name is resolved
// through the index, so arbitrary paths can never be read.
func (s *Service) OpenFile(ctx context.Context, assetID, filename string) (*os.File, *File, error) {
	if !ValidAssetID(assetID) {
		return nil, nil, fmt.Errorf("asset %q: %w", assetID, ErrNotFound)
	}
	var f File
	err := s.db.QueryRowContext(ctx, `SELECT id, filename, mime, size, sha256, width, height, created_at
		FROM asset_files WHERE asset_id = ? AND filename = ?`, assetID, filename).
		Scan(&f.ID, &f.Filename, &f.Mime, &f.Size, &f.SHA256, &f.Width, &f.Height, &f.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, fmt.Errorf("file %q: %w", filename, ErrNotFound)
	}
	if err != nil {
		return nil, nil, err
	}
	f.Kind = PreviewKind(f.Filename)
	fh, err := os.Open(filepath.Join(s.assetDir(assetID), "files", f.Filename))
	if err != nil {
		return nil, nil, fmt.Errorf("file %q: %w", filename, ErrNotFound)
	}
	return fh, &f, nil
}

// ---- thumbnails ----

// ThumbPath returns the thumbnail file of an asset and its content type, or
// "" when there is none.
func (s *Service) ThumbPath(assetID string) (path, mime string) {
	if !ValidAssetID(assetID) {
		return "", ""
	}
	for _, c := range [][2]string{{"thumb.png", "image/png"}, {"thumb.jpg", "image/jpeg"}} {
		p := filepath.Join(s.assetDir(assetID), c[0])
		if _, err := os.Stat(p); err == nil {
			return p, c[1]
		}
	}
	return "", ""
}

// previewPrimary makes what the library shows for a new primary file: a
// thumbnail (plus perceptual hash and colours) for a decodable image, a
// waveform or font specimen for audio and fonts that have no thumbnail yet.
// It returns the image statistics when there are any. Failures are logged:
// a file without preview is still a valid asset.
func (s *Service) previewPrimary(ctx context.Context, assetID, fileID, filename, path string) *imageStats {
	e := ext(filename)
	switch {
	case decodableImage[e]:
		st, err := s.thumbFromFile(ctx, assetID, fileID, path)
		if err != nil {
			s.log.Debug("thumbnail", "asset", assetID, "file", filename, "err", err)
		}
		return st
	case audioPreview[e]:
		if err := s.audioPreviewFile(ctx, assetID, path, e); err != nil {
			s.log.Debug("audio preview", "asset", assetID, "file", filename, "err", err)
		}
	case fontPreview[e]:
		if err := s.fontPreviewFile(ctx, assetID, path); err != nil {
			s.log.Debug("font preview", "asset", assetID, "file", filename, "err", err)
		}
	}
	return nil
}

// thumbFromFile renders the thumbnail of an image file and records the
// perceptual hash and colours of that same decoded image for file fileID.
func (s *Service) thumbFromFile(ctx context.Context, assetID, fileID, path string) (*imageStats, error) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	st, err := s.writeThumb(ctx, assetID, fh)
	if err != nil {
		return nil, err
	}
	if err := s.storeImageStats(ctx, assetID, fileID, st); err != nil {
		s.log.Debug("image statistics", "asset", assetID, "err", err)
	}
	return st, nil
}

// SetThumbnail stores a client-rendered preview (e.g. a 3D render, a video
// frame, a waveform). The image is decoded and re-encoded, never stored as is.
func (s *Service) SetThumbnail(ctx context.Context, assetID string, data []byte) error {
	if !ValidAssetID(assetID) {
		return fmt.Errorf("asset %q: %w", assetID, ErrNotFound)
	}
	var one int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM assets WHERE id = ?`, assetID).Scan(&one); err != nil {
		return fmt.Errorf("asset %q: %w", assetID, ErrNotFound)
	}
	if _, err := s.writeThumb(ctx, assetID, bytes.NewReader(data)); err != nil {
		return invalid("thumbnail must be a PNG, JPEG, GIF, WebP or BMP image: %v", err)
	}
	s.syncSidecar(ctx, assetID)
	return nil
}

// DeleteThumbnail removes the stored thumbnail.
func (s *Service) DeleteThumbnail(ctx context.Context, assetID string) error {
	if !ValidAssetID(assetID) {
		return fmt.Errorf("asset %q: %w", assetID, ErrNotFound)
	}
	_ = os.Remove(filepath.Join(s.assetDir(assetID), "thumb.png"))
	_ = os.Remove(filepath.Join(s.assetDir(assetID), "thumb.jpg"))
	_, err := s.db.ExecContext(ctx, `UPDATE assets SET thumb_version = 0 WHERE id = ?`, assetID)
	s.syncSidecar(ctx, assetID)
	return err
}

// scaleToThumb scales an image down to the thumbnail size (never up).
func scaleToThumb(src image.Image) *image.NRGBA {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w > thumbSize || h > thumbSize {
		if w >= h {
			h = max(1, h*thumbSize/w)
			w = thumbSize
		} else {
			w = max(1, w*thumbSize/h)
			h = thumbSize
		}
	}
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	if w == b.Dx() && h == b.Dy() {
		draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Src)
	} else {
		draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Src, nil)
	}
	return dst
}

// decodeImage decodes an image within the memory budget. The caller must hold
// the thumbSem slot.
func decodeImage(r io.ReadSeeker) (image.Image, error) {
	cfg, _, err := image.DecodeConfig(r)
	if err != nil {
		return nil, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || decodedSize(cfg) > maxDecodedBytes {
		return nil, fmt.Errorf("image of %dx%d pixels is too large to make a thumbnail from", cfg.Width, cfg.Height)
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	src, _, err := image.Decode(r)
	return src, err
}

func (s *Service) acquireThumb(ctx context.Context) (release func(), err error) {
	select {
	case s.thumbSem <- struct{}{}:
		return func() { <-s.thumbSem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// writeThumb decodes an image (once), stores its thumbnail and returns the
// perceptual hash and dominant colours computed from that same decode.
func (s *Service) writeThumb(ctx context.Context, assetID string, r io.ReadSeeker) (*imageStats, error) {
	release, err := s.acquireThumb(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	src, err := decodeImage(r)
	if err != nil {
		return nil, err
	}
	dst := scaleToThumb(src)
	if err := s.putThumb(ctx, assetID, dst); err != nil {
		return nil, err
	}
	return statsOf(dst), nil
}

// putThumb encodes and stores a thumbnail image and bumps the thumbnail version.
func (s *Service) putThumb(ctx context.Context, assetID string, dst *image.NRGBA) error {
	w, h := dst.Bounds().Dx(), dst.Bounds().Dy()
	// Opaque pictures compress far better as JPEG; keep PNG for transparency
	// and for small pixel art, where JPEG artefacts would be visible.
	var buf bytes.Buffer
	var err error
	name := "thumb.png"
	if dst.Opaque() && w*h > 128*128 {
		name = "thumb.jpg"
		err = jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 86})
	} else {
		err = (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&buf, dst)
	}
	if err != nil {
		return err
	}

	dir := s.assetDir(assetID)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(dir, name), buf.Bytes()); err != nil {
		return err
	}
	for _, other := range []string{"thumb.png", "thumb.jpg"} {
		if other != name {
			_ = os.Remove(filepath.Join(dir, other))
		}
	}
	_, err = s.db.ExecContext(ctx, `UPDATE assets SET thumb_version = thumb_version + 1 WHERE id = ?`, assetID)
	return err
}
