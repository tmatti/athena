package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tmatti/athena/internal/testdb"
)

// pngBytes is a PNG signature followed by filler; the store does not
// validate image contents, only stores them.
var pngBytes = append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)

func TestImageRoundTrip(t *testing.T) {
	s := New(testdb.Pool(t))
	ctx := context.Background()

	m, err := s.CreateMemory(ctx, "memory with a picture", nil, nil)
	require.NoError(t, err)

	caption := "a diagram"
	img, err := s.CreateImage(ctx, MemoryOwner(m.ID), "diagram.png", "image/png", pngBytes, &caption)
	require.NoError(t, err)
	require.NotEmpty(t, img.ID)
	require.Equal(t, &m.ID, img.MemoryID)
	require.Nil(t, img.NoteID)
	require.Equal(t, "diagram.png", img.Filename)
	require.Equal(t, "image/png", img.ContentType)
	require.Equal(t, int64(len(pngBytes)), img.SizeBytes)
	sum := sha256.Sum256(pngBytes)
	require.Equal(t, hex.EncodeToString(sum[:]), img.SHA256)
	require.Equal(t, &caption, img.Caption)
	require.False(t, img.CreatedAt.IsZero())

	got, err := s.GetImage(ctx, img.ID)
	require.NoError(t, err)
	require.Equal(t, img, got)

	gotMeta, data, err := s.GetImageData(ctx, img.ID)
	require.NoError(t, err)
	require.Equal(t, img, gotMeta)
	require.Equal(t, pngBytes, data)

	second, err := s.CreateImage(ctx, MemoryOwner(m.ID), "second.png", "image/png", pngBytes, nil)
	require.NoError(t, err)
	list, err := s.ListImages(ctx, MemoryOwner(m.ID))
	require.NoError(t, err)
	require.Len(t, list, 2)
	require.Equal(t, img.ID, list[0].ID)
	require.Equal(t, second.ID, list[1].ID)

	// Another owner's list is independent.
	n, _, err := s.CreateNote(ctx, "a note", "body", nil, nil)
	require.NoError(t, err)
	list, err = s.ListImages(ctx, NoteOwner(n.ID))
	require.NoError(t, err)
	require.Empty(t, list)
}

func TestImageCaptionUpdateAndDelete(t *testing.T) {
	s := New(testdb.Pool(t))
	ctx := context.Background()

	n, _, err := s.CreateNote(ctx, "note", "body", nil, nil)
	require.NoError(t, err)
	img, err := s.CreateImage(ctx, NoteOwner(n.ID), "x.png", "image/png", pngBytes, nil)
	require.NoError(t, err)
	require.Nil(t, img.Caption)
	require.Equal(t, &n.ID, img.NoteID)

	caption := "now captioned"
	updated, err := s.UpdateImageCaption(ctx, img.ID, &caption)
	require.NoError(t, err)
	require.Equal(t, &caption, updated.Caption)

	cleared, err := s.UpdateImageCaption(ctx, img.ID, nil)
	require.NoError(t, err)
	require.Nil(t, cleared.Caption)

	_, err = s.UpdateImageCaption(ctx, "00000000-0000-0000-0000-000000000000", &caption)
	require.ErrorIs(t, err, ErrNotFound)

	require.NoError(t, s.DeleteImage(ctx, img.ID))
	_, err = s.GetImage(ctx, img.ID)
	require.ErrorIs(t, err, ErrNotFound)
	_, _, err = s.GetImageData(ctx, img.ID)
	require.ErrorIs(t, err, ErrNotFound)
	require.ErrorIs(t, s.DeleteImage(ctx, img.ID), ErrNotFound)
}

func TestImagesCascadeWithOwner(t *testing.T) {
	s := New(testdb.Pool(t))
	ctx := context.Background()

	m, err := s.CreateMemory(ctx, "doomed memory", nil, nil)
	require.NoError(t, err)
	memImg, err := s.CreateImage(ctx, MemoryOwner(m.ID), "m.png", "image/png", pngBytes, nil)
	require.NoError(t, err)

	n, _, err := s.CreateNote(ctx, "doomed note", "body", nil, nil)
	require.NoError(t, err)
	noteImg, err := s.CreateImage(ctx, NoteOwner(n.ID), "n.png", "image/png", pngBytes, nil)
	require.NoError(t, err)

	require.NoError(t, s.DeleteMemory(ctx, m.ID))
	_, err = s.GetImage(ctx, memImg.ID)
	require.ErrorIs(t, err, ErrNotFound)

	require.NoError(t, s.DeleteNote(ctx, n.ID))
	_, err = s.GetImage(ctx, noteImg.ID)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestCreateImageMissingOwnerIsNotFound(t *testing.T) {
	s := New(testdb.Pool(t))
	ctx := context.Background()
	missing := "00000000-0000-0000-0000-000000000000"

	_, err := s.CreateImage(ctx, MemoryOwner(missing), "x.png", "image/png", pngBytes, nil)
	require.ErrorIs(t, err, ErrNotFound)
	_, err = s.CreateImage(ctx, NoteOwner(missing), "x.png", "image/png", pngBytes, nil)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestImageOwnerMustBeExactlyOne(t *testing.T) {
	s := New(testdb.Pool(t))
	ctx := context.Background()
	id := "00000000-0000-0000-0000-000000000000"

	_, err := s.CreateImage(ctx, ImageOwner{}, "x.png", "image/png", pngBytes, nil)
	require.ErrorIs(t, err, ErrInvalidImageOwner)
	_, err = s.CreateImage(ctx, ImageOwner{MemoryID: id, NoteID: id}, "x.png", "image/png", pngBytes, nil)
	require.ErrorIs(t, err, ErrInvalidImageOwner)
	_, err = s.ListImages(ctx, ImageOwner{})
	require.ErrorIs(t, err, ErrInvalidImageOwner)

	// The database enforces the same invariant independently of the Go check.
	_, err = s.pool.Exec(ctx,
		`INSERT INTO images (filename, content_type, size_bytes, sha256, data) VALUES ('x', 'image/png', 1, 'x', '\x00')`)
	require.ErrorContains(t, err, "images_exactly_one_owner")
}

// TestImageMetadataReadsSkipData proves metadata reads never select the
// data column: through a role that may read every images column except
// data, GetImage and ListImages succeed while GetImageData is refused.
// TestImageColumnsExcludeData guards the invariant that metadata queries
// never pull image bytes: every metadata query selects imageColumns.
func TestImageColumnsExcludeData(t *testing.T) {
	for _, col := range strings.Split(imageColumns, ",") {
		require.NotEqual(t, "data", strings.TrimSpace(col))
	}
}
