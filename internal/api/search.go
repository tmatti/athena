package api

import (
	"net/http"
	"strconv"

	"github.com/tmatti/athena/internal/service"
	"github.com/tmatti/athena/internal/store"
)

type searchResponse struct {
	Results []store.SearchResult `json:"results"`
}

func (h *Handlers) search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	results, err := h.Brain.Search(r.Context(), service.SearchParams{
		Query: q.Get("q"),
		Mode:  q.Get("mode"),
		Type:  q.Get("type"),
		Tag:   q.Get("tag"),
		Limit: limit,
	})
	if err != nil {
		h.writeServiceError(w, err)
		return
	}
	if results == nil {
		results = []store.SearchResult{}
	}
	writeJSON(w, http.StatusOK, searchResponse{Results: results})
}

func (h *Handlers) listTags(w http.ResponseWriter, r *http.Request) {
	tags, err := h.Brain.ListTags(r.Context())
	if err != nil {
		h.writeServiceError(w, err)
		return
	}
	if tags == nil {
		tags = []store.TagCount{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tags": tags})
}
