package mcpserver

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/tmatti/athena/internal/service"
)

var testPNG = append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)

func TestImageToolsRoundTrip(t *testing.T) {
	cs := testClient(t)

	rememberRes := callTool(t, cs, "remember", map[string]any{"content": "a fact with a picture"})
	require.False(t, rememberRes.IsError, resultText(t, rememberRes))
	memoryID := extractID(t, resultText(t, rememberRes))

	// No images yet: get_memory has no images section.
	getRes := callTool(t, cs, "get_memory", map[string]any{"memory_id": memoryID})
	require.False(t, getRes.IsError, resultText(t, getRes))
	require.NotContains(t, resultText(t, getRes), "images:")

	attachRes := callTool(t, cs, "attach_image", map[string]any{
		"memory_id": memoryID,
		"data":      base64.StdEncoding.EncodeToString(testPNG),
		"filename":  "chart.png",
		"caption":   "quarterly chart",
	})
	require.False(t, attachRes.IsError, resultText(t, attachRes))
	attachText := resultText(t, attachRes)
	require.Contains(t, attachText, "to memory "+memoryID)
	require.Contains(t, attachText, "chart.png, image/png")
	imageID := extractID(t, attachText)

	listRes := callTool(t, cs, "list_images", map[string]any{"memory_id": memoryID})
	require.False(t, listRes.IsError, resultText(t, listRes))
	listText := resultText(t, listRes)
	require.Contains(t, listText, imageID)
	require.Contains(t, listText, `filename="chart.png"`)
	require.Contains(t, listText, "quarterly chart")

	getRes = callTool(t, cs, "get_memory", map[string]any{"memory_id": memoryID})
	require.False(t, getRes.IsError, resultText(t, getRes))
	require.Contains(t, resultText(t, getRes), "images:\n- id="+imageID)

	imgRes := callTool(t, cs, "get_image", map[string]any{"image_id": imageID})
	require.False(t, imgRes.IsError, resultText(t, imgRes))
	require.Len(t, imgRes.Content, 2)
	require.Contains(t, resultText(t, imgRes), "quarterly chart")
	ic, ok := imgRes.Content[1].(*mcp.ImageContent)
	require.True(t, ok, "expected image content, got %T", imgRes.Content[1])
	require.Equal(t, "image/png", ic.MIMEType)
	require.Equal(t, testPNG, ic.Data)

	delRes := callTool(t, cs, "delete_image", map[string]any{"image_id": imageID})
	require.False(t, delRes.IsError, resultText(t, delRes))

	listRes = callTool(t, cs, "list_images", map[string]any{"memory_id": memoryID})
	require.False(t, listRes.IsError, resultText(t, listRes))
	require.Contains(t, resultText(t, listRes), "No images attached")

	res := callTool(t, cs, "get_image", map[string]any{"image_id": imageID})
	require.True(t, res.IsError)
	require.Contains(t, resultText(t, res), "no image found")
	res = callTool(t, cs, "delete_image", map[string]any{"image_id": imageID})
	require.True(t, res.IsError)
	require.Contains(t, resultText(t, res), "no image found")
}

func TestAttachImageToNoteShowsInGetNote(t *testing.T) {
	cs := testClient(t)

	createRes := callTool(t, cs, "create_note", map[string]any{"title": "Diagrams", "content": "see image"})
	require.False(t, createRes.IsError, resultText(t, createRes))
	noteID := extractID(t, resultText(t, createRes))

	// URL-safe, unpadded base64 with a data: prefix is accepted too.
	attachRes := callTool(t, cs, "attach_image", map[string]any{
		"note_id": noteID,
		"data":    "data:image/png;base64," + base64.RawURLEncoding.EncodeToString(testPNG),
	})
	require.False(t, attachRes.IsError, resultText(t, attachRes))
	require.Contains(t, resultText(t, attachRes), "to note "+noteID+": image.png")

	getRes := callTool(t, cs, "get_note", map[string]any{"note_id": noteID})
	require.False(t, getRes.IsError, resultText(t, getRes))
	require.Contains(t, resultText(t, getRes), "images:\n- id=")
}

func TestImageToolsValidation(t *testing.T) {
	cs := testClient(t)
	missing := "00000000-0000-0000-0000-000000000000"
	data := base64.StdEncoding.EncodeToString(testPNG)

	cases := []struct {
		name, tool string
		args       map[string]any
		want       string
	}{
		{"attach no owner", "attach_image", map[string]any{"data": data}, "exactly one of memory_id or note_id"},
		{"attach both owners", "attach_image", map[string]any{"memory_id": missing, "note_id": missing, "data": data}, "exactly one of memory_id or note_id"},
		{"attach bad uuid", "attach_image", map[string]any{"note_id": "nope", "data": data}, "note_id must be a valid UUID"},
		{"attach missing owner", "attach_image", map[string]any{"memory_id": missing, "data": data}, "no memory found"},
		{"attach bad base64", "attach_image", map[string]any{"memory_id": missing, "data": "!!!not base64!!!"}, "data must be base64"},
		{"attach empty data", "attach_image", map[string]any{"memory_id": missing, "data": ""}, "data is required"},
		{"attach not an image", "attach_image", map[string]any{"memory_id": missing, "data": base64.StdEncoding.EncodeToString([]byte("hello"))}, "unsupported image type"},
		{"attach too large", "attach_image", map[string]any{"memory_id": missing, "data": strings.Repeat("A", base64.StdEncoding.EncodedLen(service.MaxImageBytes+16))}, "must not exceed 10 MiB"},
		{"list no owner", "list_images", map[string]any{}, "exactly one of memory_id or note_id"},
		{"list both owners", "list_images", map[string]any{"memory_id": missing, "note_id": missing}, "exactly one of memory_id or note_id"},
		{"list missing owner", "list_images", map[string]any{"note_id": missing}, "no note found"},
		{"get bad uuid", "get_image", map[string]any{"image_id": "nope"}, "image_id must be a valid UUID"},
		{"delete bad uuid", "delete_image", map[string]any{"image_id": "nope"}, "image_id must be a valid UUID"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := callTool(t, cs, tc.tool, tc.args)
			require.True(t, res.IsError, resultText(t, res))
			require.Contains(t, resultText(t, res), tc.want)
		})
	}
}
