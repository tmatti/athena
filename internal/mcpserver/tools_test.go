package mcpserver

import (
	"context"
	"log/slog"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/tmatti/athena/internal/embed"
	"github.com/tmatti/athena/internal/service"
	"github.com/tmatti/athena/internal/store"
	"github.com/tmatti/athena/internal/testdb"
)

// testClient wires up an in-memory MCP client/server pair backed by a real
// Brain against the test database.
func testClient(t *testing.T) *mcp.ClientSession {
	t.Helper()
	pool := testdb.Pool(t)
	brain := service.New(store.New(pool), &embed.FakeEmbedder{Dims: 1536}, slog.New(slog.DiscardHandler))

	server := New(brain)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	ctx := context.Background()
	_, err := server.Connect(ctx, serverTransport, nil)
	require.NoError(t, err)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })

	return cs
}

func callTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err)
	return res
}

func resultText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	require.NotEmpty(t, res.Content)
	tc, ok := res.Content[0].(*mcp.TextContent)
	require.True(t, ok, "expected text content, got %T", res.Content[0])
	return tc.Text
}

func TestRememberAndRecall(t *testing.T) {
	cs := testClient(t)

	rememberRes := callTool(t, cs, "remember", map[string]any{
		"content": "the user's favorite editor is neovim",
	})
	require.False(t, rememberRes.IsError, resultText(t, rememberRes))
	rememberText := resultText(t, rememberRes)
	require.Contains(t, rememberText, "neovim")

	recallRes := callTool(t, cs, "recall", map[string]any{
		"query": "favorite editor",
	})
	require.False(t, recallRes.IsError, resultText(t, recallRes))
	recallText := resultText(t, recallRes)
	require.Contains(t, recallText, "neovim")

	// The memory id printed by remember must reappear in recall's output so
	// follow-up calls (e.g. forget) can target it.
	id := extractID(t, rememberText)
	require.Contains(t, recallText, id)
}

func TestCreateNoteAndGetNoteRoundTrip(t *testing.T) {
	cs := testClient(t)

	createRes := callTool(t, cs, "create_note", map[string]any{
		"title":   "Deployment Guide",
		"content": "To deploy, build the docker image and set DATABASE_URL.",
		"tags":    []string{"ops"},
	})
	require.False(t, createRes.IsError, resultText(t, createRes))
	createText := resultText(t, createRes)
	id := extractID(t, createText)

	getRes := callTool(t, cs, "get_note", map[string]any{"note_id": id})
	require.False(t, getRes.IsError, resultText(t, getRes))
	getText := resultText(t, getRes)
	require.Contains(t, getText, "Deployment Guide")
	require.Contains(t, getText, "DATABASE_URL")
	require.Contains(t, getText, "ops")
}

func TestForgetNonexistentReturnsToolError(t *testing.T) {
	cs := testClient(t)

	res := callTool(t, cs, "forget", map[string]any{
		"memory_id": "00000000-0000-0000-0000-000000000000",
	})
	require.True(t, res.IsError)
	require.Contains(t, resultText(t, res), "no memory found")
}

func TestForgetInvalidUUIDReturnsToolError(t *testing.T) {
	cs := testClient(t)

	res := callTool(t, cs, "forget", map[string]any{
		"memory_id": "not-a-uuid",
	})
	require.True(t, res.IsError)
	require.Contains(t, resultText(t, res), "memory_id must be a valid UUID")
}

// extractID pulls the "id=<uuid>" token out of a tool result's text.
func extractID(t *testing.T, text string) string {
	t.Helper()
	const marker = "id="
	i := indexAfter(text, marker)
	require.NotEqual(t, -1, i, "no id= marker found in: %s", text)
	end := i
	for end < len(text) && text[end] != '\n' && text[end] != ' ' {
		end++
	}
	return text[i:end]
}

func indexAfter(s, marker string) int {
	for i := 0; i+len(marker) <= len(s); i++ {
		if s[i:i+len(marker)] == marker {
			return i + len(marker)
		}
	}
	return -1
}
