package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tmatti/athena/internal/service"
	"github.com/tmatti/athena/internal/store"
)

func registerTools(s *mcp.Server, b *service.Brain) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "remember",
		Description: "Store one atomic fact as a memory. Call this once per fact " +
			"(do not batch multiple facts into a single call) so each can be " +
			"recalled independently later.",
	}, rememberHandler(b))

	mcp.AddTool(s, &mcp.Tool{
		Name: "recall",
		Description: "Hybrid keyword+vector search over memories and notes. " +
			"Returns a ranked list of results with ids, scores, and snippets.",
	}, recallHandler(b))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "forget",
		Description: "Delete a memory by id.",
	}, forgetHandler(b))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_memories",
		Description: "List the most recent memories, optionally filtered by tag.",
	}, listMemoriesHandler(b))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "create_note",
		Description: "Create a note: a chunked, titled document (for longer content than a memory).",
	}, createNoteHandler(b))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_note",
		Description: "Fetch a note's full content by id.",
	}, getNoteHandler(b))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "update_note",
		Description: "Update a note's title, content, and/or tags. Only provided fields are changed.",
	}, updateNoteHandler(b))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "delete_note",
		Description: "Delete a note by id.",
	}, deleteNoteHandler(b))

	mcp.AddTool(s, &mcp.Tool{
		Name: "list_tags",
		Description: "List all tags currently in use across memories and notes, with " +
			"counts. Check this before inventing new tags so you reuse existing vocabulary.",
	}, listTagsHandler(b))
}

// parseUUID validates id as a UUID, returning a tool-facing error that names
// the offending field.
func parseUUID(field, id string) error {
	if _, err := uuid.Parse(id); err != nil {
		return fmt.Errorf("%s must be a valid UUID: %q", field, id)
	}
	return nil
}

// notFoundOr wraps service.ErrNotFound with a clear message, or passes other
// errors through unchanged.
func notFoundOr(err error, what, id string) error {
	if errors.Is(err, service.ErrNotFound) {
		return fmt.Errorf("no %s found with id %q", what, id)
	}
	return err
}

type rememberArgs struct {
	Content string   `json:"content" jsonschema:"the atomic fact to store"`
	Tags    []string `json:"tags,omitempty" jsonschema:"optional tags to categorize this memory"`
	Source  string   `json:"source,omitempty" jsonschema:"optional origin of this fact (e.g. a conversation, a document)"`
}

func rememberHandler(b *service.Brain) mcp.ToolHandlerFor[rememberArgs, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, args rememberArgs) (*mcp.CallToolResult, any, error) {
		if strings.TrimSpace(args.Content) == "" {
			return nil, nil, errors.New("content is required")
		}
		var source *string
		if args.Source != "" {
			source = &args.Source
		}
		m, err := b.CreateMemory(ctx, args.Content, args.Tags, source)
		if err != nil {
			return nil, nil, err
		}
		text := fmt.Sprintf("Stored memory id=%s\ncontent: %s\ntags: %s", m.ID, m.Content, formatTags(m.Tags))
		return textResult(text), nil, nil
	}
}

type recallArgs struct {
	Query string `json:"query" jsonschema:"the search query"`
	Limit int    `json:"limit,omitempty" jsonschema:"maximum number of results (default 10, max 50)"`
	Type  string `json:"type,omitempty" jsonschema:"restrict results to: all, memory, or note (default all)"`
	Tag   string `json:"tag,omitempty" jsonschema:"restrict results to this tag"`
}

func recallHandler(b *service.Brain) mcp.ToolHandlerFor[recallArgs, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, args recallArgs) (*mcp.CallToolResult, any, error) {
		results, err := b.Search(ctx, service.SearchParams{
			Query: args.Query,
			Type:  args.Type,
			Tag:   args.Tag,
			Limit: args.Limit,
		})
		if err != nil {
			return nil, nil, err
		}
		if len(results) == 0 {
			return textResult("No results found."), nil, nil
		}

		var sb strings.Builder
		for i, r := range results {
			fmt.Fprintf(&sb, "%d. [%s] score=%.4f id=%s", i+1, r.Type, r.Score, r.ID)
			if r.Type == "note" && r.Title != "" {
				fmt.Fprintf(&sb, " title=%q", r.Title)
			}
			sb.WriteString("\n")
			fmt.Fprintf(&sb, "   %s\n", oneLine(r.Snippet))
			fmt.Fprintf(&sb, "   tags: %s\n", formatTags(r.Tags))
		}
		return textResult(strings.TrimRight(sb.String(), "\n")), nil, nil
	}
}

type forgetArgs struct {
	MemoryID string `json:"memory_id" jsonschema:"the id of the memory to delete"`
}

func forgetHandler(b *service.Brain) mcp.ToolHandlerFor[forgetArgs, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, args forgetArgs) (*mcp.CallToolResult, any, error) {
		if err := parseUUID("memory_id", args.MemoryID); err != nil {
			return nil, nil, err
		}
		if err := b.DeleteMemory(ctx, args.MemoryID); err != nil {
			return nil, nil, notFoundOr(err, "memory", args.MemoryID)
		}
		return textResult(fmt.Sprintf("Deleted memory id=%s", args.MemoryID)), nil, nil
	}
}

type listMemoriesArgs struct {
	Tag   string `json:"tag,omitempty" jsonschema:"restrict to this tag"`
	Limit int    `json:"limit,omitempty" jsonschema:"maximum number of memories to return (default 50, max 100)"`
}

func listMemoriesHandler(b *service.Brain) mcp.ToolHandlerFor[listMemoriesArgs, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, args listMemoriesArgs) (*mcp.CallToolResult, any, error) {
		memories, _, err := b.ListMemories(ctx, store.ListMemoriesParams{Tag: args.Tag, Limit: args.Limit})
		if err != nil {
			return nil, nil, err
		}
		if len(memories) == 0 {
			return textResult("No memories found."), nil, nil
		}
		var sb strings.Builder
		for i, m := range memories {
			fmt.Fprintf(&sb, "%d. id=%s\n   %s\n   tags: %s\n", i+1, m.ID, oneLine(m.Content), formatTags(m.Tags))
		}
		return textResult(strings.TrimRight(sb.String(), "\n")), nil, nil
	}
}

type createNoteArgs struct {
	Title   string   `json:"title" jsonschema:"the note's title"`
	Content string   `json:"content" jsonschema:"the note's full content"`
	Tags    []string `json:"tags,omitempty" jsonschema:"optional tags to categorize this note"`
}

func createNoteHandler(b *service.Brain) mcp.ToolHandlerFor[createNoteArgs, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, args createNoteArgs) (*mcp.CallToolResult, any, error) {
		if strings.TrimSpace(args.Title) == "" || strings.TrimSpace(args.Content) == "" {
			return nil, nil, errors.New("title and content are required")
		}
		n, err := b.CreateNote(ctx, args.Title, args.Content, args.Tags)
		if err != nil {
			return nil, nil, err
		}
		text := fmt.Sprintf("Created note id=%s\ntitle: %s\ntags: %s", n.ID, n.Title, formatTags(n.Tags))
		return textResult(text), nil, nil
	}
}

type getNoteArgs struct {
	NoteID string `json:"note_id" jsonschema:"the id of the note to fetch"`
}

func getNoteHandler(b *service.Brain) mcp.ToolHandlerFor[getNoteArgs, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, args getNoteArgs) (*mcp.CallToolResult, any, error) {
		if err := parseUUID("note_id", args.NoteID); err != nil {
			return nil, nil, err
		}
		n, err := b.GetNote(ctx, args.NoteID)
		if err != nil {
			return nil, nil, notFoundOr(err, "note", args.NoteID)
		}
		text := fmt.Sprintf("id=%s\ntitle: %s\ntags: %s\n\n%s", n.ID, n.Title, formatTags(n.Tags), n.Content)
		return textResult(text), nil, nil
	}
}

type updateNoteArgs struct {
	NoteID  string    `json:"note_id" jsonschema:"the id of the note to update"`
	Title   *string   `json:"title,omitempty" jsonschema:"new title (omit to leave unchanged)"`
	Content *string   `json:"content,omitempty" jsonschema:"new content (omit to leave unchanged)"`
	Tags    *[]string `json:"tags,omitempty" jsonschema:"new tags, replacing the existing set (omit to leave unchanged)"`
}

func updateNoteHandler(b *service.Brain) mcp.ToolHandlerFor[updateNoteArgs, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, args updateNoteArgs) (*mcp.CallToolResult, any, error) {
		if err := parseUUID("note_id", args.NoteID); err != nil {
			return nil, nil, err
		}
		if args.Title == nil && args.Content == nil && args.Tags == nil {
			return nil, nil, errors.New("nothing to update: provide title, content, and/or tags")
		}
		n, err := b.UpdateNote(ctx, args.NoteID, service.UpdateNoteParams{
			Title:   args.Title,
			Content: args.Content,
			Tags:    args.Tags,
		})
		if err != nil {
			return nil, nil, notFoundOr(err, "note", args.NoteID)
		}
		text := fmt.Sprintf("Updated note id=%s\ntitle: %s\ntags: %s", n.ID, n.Title, formatTags(n.Tags))
		return textResult(text), nil, nil
	}
}

type deleteNoteArgs struct {
	NoteID string `json:"note_id" jsonschema:"the id of the note to delete"`
}

func deleteNoteHandler(b *service.Brain) mcp.ToolHandlerFor[deleteNoteArgs, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, args deleteNoteArgs) (*mcp.CallToolResult, any, error) {
		if err := parseUUID("note_id", args.NoteID); err != nil {
			return nil, nil, err
		}
		if err := b.DeleteNote(ctx, args.NoteID); err != nil {
			return nil, nil, notFoundOr(err, "note", args.NoteID)
		}
		return textResult(fmt.Sprintf("Deleted note id=%s", args.NoteID)), nil, nil
	}
}

type listTagsArgs struct{}

func listTagsHandler(b *service.Brain) mcp.ToolHandlerFor[listTagsArgs, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, _ listTagsArgs) (*mcp.CallToolResult, any, error) {
		tags, err := b.ListTags(ctx)
		if err != nil {
			return nil, nil, err
		}
		if len(tags) == 0 {
			return textResult("No tags found."), nil, nil
		}
		var sb strings.Builder
		for _, t := range tags {
			fmt.Fprintf(&sb, "%s (%d)\n", t.Tag, t.Count)
		}
		return textResult(strings.TrimRight(sb.String(), "\n")), nil, nil
	}
}

func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

func formatTags(tags []string) string {
	if len(tags) == 0 {
		return "(none)"
	}
	return strings.Join(tags, ", ")
}

// oneLine collapses newlines so list/search output stays one line per item.
func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	const maxLen = 300
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}
	return s
}
