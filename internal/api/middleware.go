package api

import (
	"context"
	"crypto/subtle"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/tmatti/athena/internal/store"
)

// TokenValidator resolves a bearer token that is not the static API key —
// e.g. an OAuth access token — to its subject. Implementations return an
// error for unknown or expired tokens.
type TokenValidator func(ctx context.Context, token string) (subject string, err error)

type ctxKey int

const subjectKey ctxKey = iota

// SubjectFromContext returns the authenticated subject set by BearerAuth,
// or "" for unauthenticated requests.
func SubjectFromContext(ctx context.Context) string {
	s, _ := ctx.Value(subjectKey).(string)
	return s
}

// BearerAuth authenticates requests with either the static API key or, when
// validate is non-nil, an OAuth access token. When resourceMetadataURL is
// set, 401 responses carry the RFC 9728 WWW-Authenticate challenge that MCP
// clients use to discover the OAuth endpoints.
func BearerAuth(apiKey string, validate TokenValidator, resourceMetadataURL string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if ok && subtle.ConstantTimeCompare([]byte(token), []byte(apiKey)) == 1 {
				next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), subjectKey, store.SubjectOwner)))
				return
			}
			if ok && validate != nil {
				if subject, err := validate(r.Context(), token); err == nil {
					next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), subjectKey, subject)))
					return
				}
			}
			if resourceMetadataURL != "" {
				w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata=%q`, resourceMetadataURL))
			}
			writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
		})
	}
}

func RequestLogger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r)
			log.Info("request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", sw.status,
				"duration_ms", time.Since(start).Milliseconds(),
			)
		})
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

// Unwrap exposes the underlying writer to http.ResponseController, which the
// MCP SDK uses to flush each SSE event. Without it every flush fails silently
// and streamed responses are held in the buffer until the handler returns.
func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
