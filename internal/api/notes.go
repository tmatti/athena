package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/tmatti/athena/internal/service"
	"github.com/tmatti/athena/internal/store"
)

type noteCreateRequest struct {
	Title   string   `json:"title"`
	Content string   `json:"content"`
	Tags    []string `json:"tags"`
}

type noteUpdateRequest struct {
	Title   *string   `json:"title"`
	Content *string   `json:"content"`
	Tags    *[]string `json:"tags"`
}

type noteListResponse struct {
	Notes      []store.Note `json:"notes"`
	NextCursor string       `json:"next_cursor,omitempty"`
}

func (h *Handlers) createNote(w http.ResponseWriter, r *http.Request) {
	var req noteCreateRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Title) == "" || strings.TrimSpace(req.Content) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "title and content are required")
		return
	}
	n, err := h.Brain.CreateNote(r.Context(), req.Title, req.Content, req.Tags)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, n)
}

func (h *Handlers) listNotes(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	notes, next, err := h.Brain.ListNotes(r.Context(), store.ListNotesParams{
		Tag:    r.URL.Query().Get("tag"),
		Limit:  limit,
		Cursor: r.URL.Query().Get("cursor"),
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, noteListResponse{Notes: notes, NextCursor: next})
}

func (h *Handlers) getNote(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	n, err := h.Brain.GetNote(r.Context(), id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, n)
}

func (h *Handlers) updateNote(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req noteUpdateRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if req.Title == nil && req.Content == nil && req.Tags == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "nothing to update")
		return
	}
	if req.Content != nil && strings.TrimSpace(*req.Content) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "content cannot be empty")
		return
	}
	if req.Title != nil && strings.TrimSpace(*req.Title) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "title cannot be empty")
		return
	}
	n, err := h.Brain.UpdateNote(r.Context(), id, service.UpdateNoteParams{
		Title:   req.Title,
		Content: req.Content,
		Tags:    req.Tags,
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, n)
}

func (h *Handlers) deleteNote(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := h.Brain.DeleteNote(r.Context(), id); err != nil {
		writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
