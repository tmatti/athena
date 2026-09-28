package mcpserver

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tmatti/athena/internal/service"
	"github.com/tmatti/athena/internal/store"
)

func registerImageTools(s *mcp.Server, b *service.Brain) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "attach_image",
		Description: "Attach an image (PNG, JPEG, GIF, or WebP; max 10 MiB) to a memory or a note. " +
			"Provide exactly one of memory_id or note_id. The image type is detected from the bytes.",
	}, attachImageHandler(b))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_image",
		Description: "Fetch an image by id: returns its metadata and the image itself.",
	}, getImageHandler(b))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_images",
		Description: "List the images attached to a memory or a note. Provide exactly one of memory_id or note_id.",
	}, listImagesHandler(b))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "delete_image",
		Description: "Delete an image by id.",
	}, deleteImageHandler(b))
}

// imageOwner validates that exactly one of memoryID/noteID is set and is a
// UUID, returning the store owner it names.
func imageOwner(memoryID, noteID string) (store.ImageOwner, error) {
	owner := store.ImageOwner{MemoryID: memoryID, NoteID: noteID}
	if err := owner.Validate(); err != nil {
		return store.ImageOwner{}, err
	}
	field := "memory_id"
	if noteID != "" {
		field = "note_id"
	}
	if err := parseUUID(field, owner.ID()); err != nil {
		return store.ImageOwner{}, err
	}
	return owner, nil
}

// decodeImageData decodes base64 image bytes, accepting standard or
// URL-safe alphabets, with or without padding, embedded whitespace, and an
// optional "data:<type>;base64," prefix.
func decodeImageData(s string) ([]byte, error) {
	if rest, ok := strings.CutPrefix(s, "data:"); ok {
		if _, payload, ok := strings.Cut(rest, ","); ok {
			s = payload
		}
	}
	s = strings.Join(strings.Fields(s), "")
	if s == "" {
		return nil, errors.New("data is required: base64-encoded image bytes")
	}
	// Reject grossly oversized payloads before allocating a decode buffer.
	if base64.RawStdEncoding.DecodedLen(len(s)) > service.MaxImageBytes+2 {
		return nil, service.ErrImageTooLarge
	}
	s = strings.TrimRight(s, "=")
	enc := base64.RawStdEncoding
	if strings.ContainsAny(s, "-_") {
		enc = base64.RawURLEncoding
	}
	data, err := enc.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("data must be base64-encoded image bytes: %w", err)
	}
	return data, nil
}

type attachImageArgs struct {
	MemoryID string `json:"memory_id,omitempty" jsonschema:"the memory to attach the image to (provide exactly one of memory_id or note_id)"`
	NoteID   string `json:"note_id,omitempty" jsonschema:"the note to attach the image to (provide exactly one of memory_id or note_id)"`
	Data     string `json:"data" jsonschema:"the image bytes, base64-encoded (PNG, JPEG, GIF, or WebP; max 10 MiB)"`
	Filename string `json:"filename,omitempty" jsonschema:"optional filename (defaults to image.<ext>)"`
	Caption  string `json:"caption,omitempty" jsonschema:"optional description of what the image shows"`
}

func attachImageHandler(b *service.Brain) mcp.ToolHandlerFor[attachImageArgs, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, args attachImageArgs) (*mcp.CallToolResult, any, error) {
		owner, err := imageOwner(args.MemoryID, args.NoteID)
		if err != nil {
			return nil, nil, err
		}
		data, err := decodeImageData(args.Data)
		if err != nil {
			return nil, nil, err
		}
		var caption *string
		if args.Caption != "" {
			caption = &args.Caption
		}
		img, err := b.AttachImage(ctx, owner, args.Filename, data, caption)
		if err != nil {
			return nil, nil, notFoundOr(err, owner.Kind(), owner.ID())
		}
		text := fmt.Sprintf("Attached image id=%s to %s %s: %s, %s, %d bytes",
			img.ID, owner.Kind(), owner.ID(), img.Filename, img.ContentType, img.SizeBytes)
		return textResult(text), nil, nil
	}
}

type getImageArgs struct {
	ImageID string `json:"image_id" jsonschema:"the id of the image to fetch"`
}

func getImageHandler(b *service.Brain) mcp.ToolHandlerFor[getImageArgs, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, args getImageArgs) (*mcp.CallToolResult, any, error) {
		if err := parseUUID("image_id", args.ImageID); err != nil {
			return nil, nil, err
		}
		img, data, err := b.GetImageData(ctx, args.ImageID)
		if err != nil {
			return nil, nil, notFoundOr(err, "image", args.ImageID)
		}
		owner := "memory " + deref(img.MemoryID)
		if img.NoteID != nil {
			owner = "note " + *img.NoteID
		}
		text := formatImage(img) + "\nattached to: " + owner
		return &mcp.CallToolResult{Content: []mcp.Content{
			&mcp.TextContent{Text: text},
			&mcp.ImageContent{Data: data, MIMEType: img.ContentType},
		}}, nil, nil
	}
}

type listImagesArgs struct {
	MemoryID string `json:"memory_id,omitempty" jsonschema:"list images attached to this memory (provide exactly one of memory_id or note_id)"`
	NoteID   string `json:"note_id,omitempty" jsonschema:"list images attached to this note (provide exactly one of memory_id or note_id)"`
}

func listImagesHandler(b *service.Brain) mcp.ToolHandlerFor[listImagesArgs, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, args listImagesArgs) (*mcp.CallToolResult, any, error) {
		owner, err := imageOwner(args.MemoryID, args.NoteID)
		if err != nil {
			return nil, nil, err
		}
		images, err := b.ListImages(ctx, owner)
		if err != nil {
			return nil, nil, notFoundOr(err, owner.Kind(), owner.ID())
		}
		if len(images) == 0 {
			return textResult(fmt.Sprintf("No images attached to %s %s.", owner.Kind(), owner.ID())), nil, nil
		}
		var sb strings.Builder
		for i, img := range images {
			fmt.Fprintf(&sb, "%d. %s\n", i+1, formatImage(img))
		}
		return textResult(strings.TrimRight(sb.String(), "\n")), nil, nil
	}
}

type deleteImageArgs struct {
	ImageID string `json:"image_id" jsonschema:"the id of the image to delete"`
}

func deleteImageHandler(b *service.Brain) mcp.ToolHandlerFor[deleteImageArgs, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, args deleteImageArgs) (*mcp.CallToolResult, any, error) {
		if err := parseUUID("image_id", args.ImageID); err != nil {
			return nil, nil, err
		}
		if err := b.DeleteImage(ctx, args.ImageID); err != nil {
			return nil, nil, notFoundOr(err, "image", args.ImageID)
		}
		return textResult(fmt.Sprintf("Deleted image id=%s", args.ImageID)), nil, nil
	}
}

// formatImage renders one image's metadata on a single line.
func formatImage(img store.Image) string {
	caption := "(none)"
	if img.Caption != nil {
		caption = oneLine(*img.Caption)
	}
	return fmt.Sprintf("id=%s filename=%q content_type=%s size_bytes=%d caption: %s",
		img.ID, img.Filename, img.ContentType, img.SizeBytes, caption)
}

// formatImagesSection renders the "images:" block appended to get_memory and
// get_note output, or "" when there are none.
func formatImagesSection(images []store.Image) string {
	if len(images) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n\nimages:")
	for _, img := range images {
		sb.WriteString("\n- ")
		sb.WriteString(formatImage(img))
	}
	return sb.String()
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
