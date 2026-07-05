package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/tmatti/athena/internal/store"
)

type memoryCreateRequest struct {
	Content string   `json:"content"`
	Tags    []string `json:"tags"`
	Source  *string  `json:"source"`
}

type memoryUpdateRequest struct {
	Content *string   `json:"content"`
	Tags    *[]string `json:"tags"`
	Source  *string   `json:"source"`
}

type memoryListResponse struct {
	Memories   []store.Memory `json:"memories"`
	NextCursor string         `json:"next_cursor,omitempty"`
}

func (h *Handlers) createMemory(w http.ResponseWriter, r *http.Request) {
	var req memoryCreateRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Content) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "content is required")
		return
	}
	m, err := h.Brain.CreateMemory(r.Context(), req.Content, req.Tags, req.Source)
	if err != nil {
		h.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, m)
}

func (h *Handlers) listMemories(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	memories, next, err := h.Brain.ListMemories(r.Context(), store.ListMemoriesParams{
		Tag:    r.URL.Query().Get("tag"),
		Limit:  limit,
		Cursor: r.URL.Query().Get("cursor"),
	})
	if err != nil {
		h.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, memoryListResponse{Memories: memories, NextCursor: next})
}

func (h *Handlers) getMemory(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	m, err := h.Brain.GetMemory(r.Context(), id)
	if err != nil {
		h.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (h *Handlers) updateMemory(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req memoryUpdateRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if req.Content == nil && req.Tags == nil && req.Source == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "nothing to update")
		return
	}
	if req.Content != nil && strings.TrimSpace(*req.Content) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "content cannot be empty")
		return
	}
	m, err := h.Brain.UpdateMemory(r.Context(), id, store.UpdateMemoryParams{
		Content: req.Content,
		Tags:    req.Tags,
		Source:  req.Source,
	})
	if err != nil {
		h.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (h *Handlers) deleteMemory(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := h.Brain.DeleteMemory(r.Context(), id); err != nil {
		h.writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
