package service

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tmatti/athena/internal/store"
)

// Minimal leading bytes that http.DetectContentType recognises for each
// allowed type; no real image files are needed.
var (
	testPNG  = append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)
	testJPEG = append([]byte("\xff\xd8\xff\xe0"), make([]byte, 32)...)
	testGIF  = append([]byte("GIF89a"), make([]byte, 32)...)
	testWebP = append([]byte("RIFF\x00\x00\x00\x00WEBPVP8 "), make([]byte, 32)...)
)

func TestSniffImageType(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"png", testPNG, "image/png"},
		{"jpeg", testJPEG, "image/jpeg"},
		{"gif", testGIF, "image/gif"},
		{"webp", testWebP, "image/webp"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := sniffImageType(tc.data)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}

	for name, data := range map[string][]byte{
		"empty":      nil,
		"text/plain": []byte("just some text, definitely not an image"),
		"html":       []byte("<html><body>hi</body></html>"),
		"svg":        []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`),
		"pdf":        []byte("%PDF-1.7\n"),
	} {
		t.Run("rejects "+name, func(t *testing.T) {
			_, err := sniffImageType(data)
			require.ErrorIs(t, err, ErrUnsupportedImageType)
		})
	}
}

func TestSanitizeFilename(t *testing.T) {
	cases := []struct {
		in, contentType, want string
	}{
		{"cat.png", "image/png", "cat.png"},
		{"  spaced.gif  ", "image/gif", "spaced.gif"},
		{"../../etc/passwd", "image/png", "passwd"},
		{`C:\Users\me\photo.jpg`, "image/jpeg", "photo.jpg"},
		{"dir/", "image/png", "dir"},
		{"bad\x00na\nme\x7f.png", "image/png", "badname.png"},
		{"bad\xffutf8.png", "image/png", "badutf8.png"},
		{"", "image/png", "image.png"},
		{"", "image/jpeg", "image.jpg"},
		{"", "image/gif", "image.gif"},
		{"", "image/webp", "image.webp"},
		{"/", "image/png", "image.png"},
		{"..", "image/png", "image.png"},
		{"\x01\x02", "image/png", "image.png"},
	}
	for _, tc := range cases {
		require.Equal(t, tc.want, sanitizeFilename(tc.in, tc.contentType), "input %q", tc.in)
	}

	long := strings.Repeat("é", 300) + ".png"
	got := sanitizeFilename(long, "image/png")
	require.Equal(t, 255, len([]rune(got)))
}

func TestNormalizeCaption(t *testing.T) {
	require.Nil(t, normalizeCaption(nil))
	blank := "   "
	require.Nil(t, normalizeCaption(&blank))
	messy := "  a\x00 cat\xff  "
	require.Equal(t, "a cat", *normalizeCaption(&messy))
}

func TestAttachImageValidation(t *testing.T) {
	b := testBrain(t, nil)
	ctx := context.Background()
	m, err := b.CreateMemory(ctx, "owner", nil, nil)
	require.NoError(t, err)
	owner := store.MemoryOwner(m.ID)

	tooBig := append(bytes.Clone(testPNG), make([]byte, MaxImageBytes)...)
	_, err = b.AttachImage(ctx, owner, "big.png", tooBig, nil)
	require.ErrorIs(t, err, ErrImageTooLarge)

	_, err = b.AttachImage(ctx, owner, "empty.png", nil, nil)
	require.ErrorIs(t, err, ErrUnsupportedImageType)

	_, err = b.AttachImage(ctx, owner, "notes.png", []byte("plain text pretending to be png"), nil)
	require.ErrorIs(t, err, ErrUnsupportedImageType)

	_, err = b.AttachImage(ctx, store.ImageOwner{}, "x.png", testPNG, nil)
	require.ErrorIs(t, err, ErrInvalidImageOwner)

	_, err = b.AttachImage(ctx, store.MemoryOwner("00000000-0000-0000-0000-000000000000"), "x.png", testPNG, nil)
	require.ErrorIs(t, err, ErrNotFound)

	// Exactly at the limit is accepted.
	atLimit := append(bytes.Clone(testPNG), make([]byte, MaxImageBytes-len(testPNG))...)
	img, err := b.AttachImage(ctx, owner, "limit.png", atLimit, nil)
	require.NoError(t, err)
	require.Equal(t, int64(MaxImageBytes), img.SizeBytes)
}

func TestAttachImageSniffsTypeAndDefaultsFilename(t *testing.T) {
	b := testBrain(t, nil)
	ctx := context.Background()
	m, err := b.CreateMemory(ctx, "owner", nil, nil)
	require.NoError(t, err)

	// A misleading ".png" name does not change the sniffed JPEG type.
	img, err := b.AttachImage(ctx, store.MemoryOwner(m.ID), "photo.png", testJPEG, nil)
	require.NoError(t, err)
	require.Equal(t, "image/jpeg", img.ContentType)
	require.Equal(t, "photo.png", img.Filename)

	img, err = b.AttachImage(ctx, store.MemoryOwner(m.ID), "", testWebP, nil)
	require.NoError(t, err)
	require.Equal(t, "image/webp", img.ContentType)
	require.Equal(t, "image.webp", img.Filename)

	blank := "  "
	img, err = b.AttachImage(ctx, store.MemoryOwner(m.ID), "x.gif", testGIF, &blank)
	require.NoError(t, err)
	require.Nil(t, img.Caption)
}

func TestGetMemoryAndNotePopulateImages(t *testing.T) {
	b := testBrain(t, nil)
	ctx := context.Background()

	m, err := b.CreateMemory(ctx, "memory with images", nil, nil)
	require.NoError(t, err)
	got, err := b.GetMemory(ctx, m.ID)
	require.NoError(t, err)
	require.Empty(t, got.Images)

	caption := "first"
	first, err := b.AttachImage(ctx, store.MemoryOwner(m.ID), "a.png", testPNG, &caption)
	require.NoError(t, err)
	second, err := b.AttachImage(ctx, store.MemoryOwner(m.ID), "b.gif", testGIF, nil)
	require.NoError(t, err)

	got, err = b.GetMemory(ctx, m.ID)
	require.NoError(t, err)
	require.Equal(t, []store.Image{first, second}, got.Images)

	// Lists never carry images.
	memories, _, err := b.ListMemories(ctx, store.ListMemoriesParams{})
	require.NoError(t, err)
	require.Len(t, memories, 1)
	require.Nil(t, memories[0].Images)

	n, err := b.CreateNote(ctx, "Note", "note with an image", nil)
	require.NoError(t, err)
	noteImg, err := b.AttachImage(ctx, store.NoteOwner(n.ID), "n.jpg", testJPEG, nil)
	require.NoError(t, err)
	gotNote, err := b.GetNote(ctx, n.ID)
	require.NoError(t, err)
	require.Equal(t, []store.Image{noteImg}, gotNote.Images)

	notes, _, err := b.ListNotes(ctx, store.ListNotesParams{})
	require.NoError(t, err)
	require.Len(t, notes, 1)
	require.Nil(t, notes[0].Images)
}

func TestImageLifecycle(t *testing.T) {
	b := testBrain(t, nil)
	ctx := context.Background()

	n, err := b.CreateNote(ctx, "Note", "body", nil)
	require.NoError(t, err)
	owner := store.NoteOwner(n.ID)

	images, err := b.ListImages(ctx, owner)
	require.NoError(t, err)
	require.Empty(t, images)

	_, err = b.ListImages(ctx, store.NoteOwner("00000000-0000-0000-0000-000000000000"))
	require.ErrorIs(t, err, ErrNotFound)

	img, err := b.AttachImage(ctx, owner, "x.png", testPNG, nil)
	require.NoError(t, err)

	meta, data, err := b.GetImageData(ctx, img.ID)
	require.NoError(t, err)
	require.Equal(t, img, meta)
	require.Equal(t, testPNG, data)

	caption := "  described  "
	updated, err := b.UpdateImageCaption(ctx, img.ID, &caption)
	require.NoError(t, err)
	require.Equal(t, "described", *updated.Caption)

	got, err := b.GetImage(ctx, img.ID)
	require.NoError(t, err)
	require.Equal(t, updated, got)

	require.NoError(t, b.DeleteImage(ctx, img.ID))
	_, err = b.GetImage(ctx, img.ID)
	require.ErrorIs(t, err, ErrNotFound)
}
