package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Image is an image attachment's metadata. The bytes are never part of this
// struct; they are fetched separately via GetImageData.
type Image struct {
	ID          string    `json:"id"`
	MemoryID    *string   `json:"memory_id"`
	NoteID      *string   `json:"note_id"`
	Filename    string    `json:"filename"`
	ContentType string    `json:"content_type"`
	SizeBytes   int64     `json:"size_bytes"`
	SHA256      string    `json:"sha256"`
	Caption     *string   `json:"caption"`
	CreatedAt   time.Time `json:"created_at"`
}

// ImageOwner names the single memory or note an image belongs to. Exactly
// one field must be set; use MemoryOwner / NoteOwner to build one.
type ImageOwner struct {
	MemoryID string
	NoteID   string
}

// ErrInvalidImageOwner is returned when an ImageOwner does not name exactly
// one of a memory or a note.
var ErrInvalidImageOwner = errors.New("exactly one of memory_id or note_id is required")

func MemoryOwner(id string) ImageOwner { return ImageOwner{MemoryID: id} }
func NoteOwner(id string) ImageOwner   { return ImageOwner{NoteID: id} }

// Validate reports ErrInvalidImageOwner unless exactly one owner is set.
func (o ImageOwner) Validate() error {
	if (o.MemoryID == "") == (o.NoteID == "") {
		return ErrInvalidImageOwner
	}
	return nil
}

// Kind returns "memory" or "note".
func (o ImageOwner) Kind() string {
	if o.MemoryID != "" {
		return "memory"
	}
	return "note"
}

// ID returns the owning memory's or note's id.
func (o ImageOwner) ID() string {
	if o.MemoryID != "" {
		return o.MemoryID
	}
	return o.NoteID
}

// args returns the (memory_id, note_id) pair as nullable SQL arguments.
func (o ImageOwner) args() (*string, *string) {
	var memoryID, noteID *string
	if o.MemoryID != "" {
		memoryID = &o.MemoryID
	}
	if o.NoteID != "" {
		noteID = &o.NoteID
	}
	return memoryID, noteID
}

// imageColumns deliberately excludes data: metadata reads must never pull
// the image bytes off disk.
const imageColumns = `id, memory_id, note_id, filename, content_type, size_bytes, sha256, caption, created_at`

func scanImage(row pgx.Row, extra ...any) (Image, error) {
	var img Image
	dest := append([]any{&img.ID, &img.MemoryID, &img.NoteID, &img.Filename, &img.ContentType,
		&img.SizeBytes, &img.SHA256, &img.Caption, &img.CreatedAt}, extra...)
	err := row.Scan(dest...)
	if errors.Is(err, pgx.ErrNoRows) {
		return Image{}, ErrNotFound
	}
	return img, err
}

// CreateImage stores an image for owner. size_bytes and sha256 are derived
// from data here so they can never disagree with the stored bytes. A missing
// owner (foreign-key violation) is reported as ErrNotFound.
func (s *Store) CreateImage(ctx context.Context, owner ImageOwner, filename, contentType string, data []byte, caption *string) (Image, error) {
	if err := owner.Validate(); err != nil {
		return Image{}, err
	}
	memoryID, noteID := owner.args()
	sum := sha256.Sum256(data)
	img, err := scanImage(s.pool.QueryRow(ctx,
		`INSERT INTO images (memory_id, note_id, filename, content_type, size_bytes, sha256, caption, data)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 RETURNING `+imageColumns,
		memoryID, noteID, filename, contentType, int64(len(data)), hex.EncodeToString(sum[:]), caption, data))
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" { // foreign_key_violation
		return Image{}, ErrNotFound
	}
	return img, err
}

// GetImage returns an image's metadata without reading its bytes.
func (s *Store) GetImage(ctx context.Context, id string) (Image, error) {
	return scanImage(s.pool.QueryRow(ctx,
		`SELECT `+imageColumns+` FROM images WHERE id = $1`, id))
}

// GetImageData returns an image's metadata and bytes.
func (s *Store) GetImageData(ctx context.Context, id string) (Image, []byte, error) {
	var data []byte
	img, err := scanImage(s.pool.QueryRow(ctx,
		`SELECT `+imageColumns+`, data FROM images WHERE id = $1`, id), &data)
	if err != nil {
		return Image{}, nil, err
	}
	return img, data, nil
}

// ListImages returns an owner's image metadata oldest-first. Per-owner
// lists are small, so there is no pagination.
func (s *Store) ListImages(ctx context.Context, owner ImageOwner) ([]Image, error) {
	if err := owner.Validate(); err != nil {
		return nil, err
	}
	column, id := "memory_id", owner.MemoryID
	if owner.NoteID != "" {
		column, id = "note_id", owner.NoteID
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+imageColumns+` FROM images WHERE `+column+` = $1 ORDER BY created_at, id`, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Image, error) {
		return scanImage(row)
	})
}

// UpdateImageCaption sets (or, with nil, clears) an image's caption.
func (s *Store) UpdateImageCaption(ctx context.Context, id string, caption *string) (Image, error) {
	return scanImage(s.pool.QueryRow(ctx,
		`UPDATE images SET caption = $2 WHERE id = $1 RETURNING `+imageColumns, id, caption))
}

func (s *Store) DeleteImage(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM images WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
