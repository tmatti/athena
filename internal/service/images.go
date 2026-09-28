package service

import (
	"context"
	"errors"
	"net/http"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/tmatti/athena/internal/store"
)

// MaxImageBytes caps a single image attachment.
const MaxImageBytes = 10 << 20 // 10 MiB

// maxFilenameRunes caps a stored image filename.
const maxFilenameRunes = 255

var (
	ErrImageTooLarge        = errors.New("image must not exceed 10 MiB")
	ErrUnsupportedImageType = errors.New("unsupported image type: must be a non-empty PNG, JPEG, GIF, or WebP image")
	ErrInvalidImageOwner    = store.ErrInvalidImageOwner
)

// allowedImageTypes maps each accepted sniffed content type to the file
// extension used for a defaulted filename.
var allowedImageTypes = map[string]string{
	"image/png":  "png",
	"image/jpeg": "jpg",
	"image/gif":  "gif",
	"image/webp": "webp",
}

// AttachImage validates and stores an image for owner. The content type is
// always sniffed from the bytes; any client-declared type is ignored.
func (b *Brain) AttachImage(ctx context.Context, owner store.ImageOwner, filename string, data []byte, caption *string) (store.Image, error) {
	if err := owner.Validate(); err != nil {
		return store.Image{}, err
	}
	if len(data) > MaxImageBytes {
		return store.Image{}, ErrImageTooLarge
	}
	contentType, err := sniffImageType(data)
	if err != nil {
		return store.Image{}, err
	}
	return b.store.CreateImage(ctx, owner, sanitizeFilename(filename, contentType), contentType, data, normalizeCaption(caption))
}

func (b *Brain) GetImage(ctx context.Context, id string) (store.Image, error) {
	return b.store.GetImage(ctx, id)
}

func (b *Brain) GetImageData(ctx context.Context, id string) (store.Image, []byte, error) {
	return b.store.GetImageData(ctx, id)
}

// ListImages returns owner's images. A nonexistent owner is ErrNotFound
// rather than an empty list.
func (b *Brain) ListImages(ctx context.Context, owner store.ImageOwner) ([]store.Image, error) {
	images, err := b.store.ListImages(ctx, owner)
	if err != nil {
		return nil, err
	}
	if len(images) == 0 {
		// Only pay for the existence check when there is nothing to show.
		if owner.MemoryID != "" {
			_, err = b.store.GetMemory(ctx, owner.MemoryID)
		} else {
			_, err = b.store.GetNote(ctx, owner.NoteID)
		}
		if err != nil {
			return nil, err
		}
	}
	return images, nil
}

// UpdateImageCaption sets an image's caption; nil or blank clears it.
func (b *Brain) UpdateImageCaption(ctx context.Context, id string, caption *string) (store.Image, error) {
	return b.store.UpdateImageCaption(ctx, id, normalizeCaption(caption))
}

func (b *Brain) DeleteImage(ctx context.Context, id string) error {
	return b.store.DeleteImage(ctx, id)
}

// sniffImageType detects the content type from the leading bytes and
// rejects anything outside the allow-list (including empty data).
func sniffImageType(data []byte) (string, error) {
	if len(data) == 0 {
		return "", ErrUnsupportedImageType
	}
	ct := http.DetectContentType(data)
	if _, ok := allowedImageTypes[ct]; !ok {
		return "", ErrUnsupportedImageType
	}
	return ct, nil
}

// sanitizeFilename reduces a client-supplied name to a safe display name:
// last path element only, no control characters or invalid UTF-8, at most
// 255 characters. An empty result defaults to "image.<ext>".
func sanitizeFilename(name, contentType string) string {
	name = strings.ToValidUTF8(name, "")
	// Treat backslashes as separators too so Windows-style paths lose their
	// directories.
	name = path.Base(strings.ReplaceAll(name, `\`, "/"))
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if utf8.RuneCountInString(name) > maxFilenameRunes {
		name = string([]rune(name)[:maxFilenameRunes])
	}
	switch name {
	case "", ".", "..", "/":
		return "image." + allowedImageTypes[contentType]
	}
	return name
}

// normalizeCaption trims a caption, mapping blank to nil. Invalid UTF-8 and
// NUL bytes are dropped because Postgres text columns reject them.
func normalizeCaption(caption *string) *string {
	if caption == nil {
		return nil
	}
	c := strings.ReplaceAll(strings.ToValidUTF8(*caption, ""), "\x00", "")
	c = strings.TrimSpace(c)
	if c == "" {
		return nil
	}
	return &c
}
