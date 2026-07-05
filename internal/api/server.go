package api

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

type RouterOptions struct {
	Log     *slog.Logger
	APIKey  string
	Healthy func(ctx context.Context) error
	// V1 registers the /v1 resource handlers.
	V1 func(r chi.Router)
	// Mounts lets additional handlers (e.g. the MCP endpoint) attach behind auth.
	Mounts map[string]http.Handler
}

func NewRouter(opts RouterOptions) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(RequestLogger(opts.Log))

	r.Get("/healthz", func(w http.ResponseWriter, req *http.Request) {
		if opts.Healthy != nil {
			if err := opts.Healthy(req.Context()); err != nil {
				// /healthz is unauthenticated: never echo the underlying
				// error (it can carry DB host/user/name). Log it instead.
				opts.Log.Error("health check failed", "error", err)
				writeError(w, http.StatusServiceUnavailable, "unhealthy", "service unavailable")
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	r.Group(func(r chi.Router) {
		r.Use(BearerAuth(opts.APIKey))

		r.Route("/v1", func(r chi.Router) {
			if opts.V1 != nil {
				opts.V1(r)
			}
		})

		for path, h := range opts.Mounts {
			r.Mount(path, h)
		}
	})

	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "no such route")
	})

	return r
}
