package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/tmatti/athena/internal/service"
	"github.com/tmatti/athena/internal/store"
)

type Handlers struct {
	Brain *service.Brain
	Log   *slog.Logger
}

// logger returns h.Log, defaulting to slog.Default() so Handlers constructed
// without a Log (e.g. in tests) don't panic.
func (h *Handlers) logger() *slog.Logger {
	if h.Log != nil {
		return h.Log
	}
	return slog.Default()
}

func (h *Handlers) Routes(r chi.Router) {
	r.Route("/memories", func(r chi.Router) {
		r.Post("/", h.createMemory)
		r.Get("/", h.listMemories)
		r.Get("/{id}", h.getMemory)
		r.Patch("/{id}", h.updateMemory)
		r.Delete("/{id}", h.deleteMemory)
	})
	r.Route("/notes", func(r chi.Router) {
		r.Post("/", h.createNote)
		r.Get("/", h.listNotes)
		r.Get("/{id}", h.getNote)
		r.Patch("/{id}", h.updateNote)
		r.Delete("/{id}", h.deleteNote)
	})
	r.Get("/search", h.search)
	r.Get("/tags", h.listTags)
}

const maxBodyBytes = 1 << 20 // 1 MiB

func decodeBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "request body must not exceed 1 MiB")
			return false
		}
		writeError(w, http.StatusBadRequest, "invalid_body", err.Error())
		return false
	}
	return true
}

// pathID validates the {id} URL parameter as a UUID.
func pathID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "id")
	if _, err := uuid.Parse(id); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "id must be a UUID")
		return "", false
	}
	return id, true
}

// writeServiceError maps service-layer errors to HTTP responses.
func (h *Handlers) writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "no such record")
	case errors.Is(err, service.ErrInvalidSearch):
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, store.ErrInvalidCursor):
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
	default:
		// Never echo raw internal errors to the client: they can carry DB or
		// provider details. Log the real error server-side instead.
		h.logger().Error("internal error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "internal error")
	}
}
