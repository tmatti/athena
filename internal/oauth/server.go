// Package oauth is a minimal embedded OAuth 2.1 authorization server plus
// resource-server metadata, implementing the MCP authorization spec
// (2025-06-18): authorization-code + PKCE, dynamic client registration,
// RFC 8414 / RFC 9728 discovery, and refresh-token rotation. There is a
// single resource owner; every credential resolves to store.SubjectOwner.
// Protocol logic lives here; all SQL lives in internal/store.
package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/tmatti/athena/internal/store"
)

const (
	codeTTL    = 10 * time.Minute
	accessTTL  = time.Hour
	refreshTTL = 30 * 24 * time.Hour
	// familyMaxTTL caps a grant's total lifetime across refresh rotations;
	// after it, the owner must authorize again.
	familyMaxTTL = 90 * 24 * time.Hour
)

type Server struct {
	store    *store.Store
	issuer   string // public base URL, no trailing slash
	loginKey string // the resource owner's credential (BRAIN_API_KEY)
	log      *slog.Logger

	// Both endpoints are unauthenticated by nature, so attempt volume is
	// bounded here rather than by credentials.
	loginLimiter    *limiter // key attempts on POST /oauth/authorize
	registerLimiter *limiter // client registrations
}

func New(st *store.Store, issuer, loginKey string, log *slog.Logger) *Server {
	return &Server{
		store:           st,
		issuer:          strings.TrimRight(issuer, "/"),
		loginKey:        loginKey,
		log:             log,
		loginLimiter:    newLimiter(10, 2),
		registerLimiter: newLimiter(10, 1),
	}
}

// Resource is the canonical RFC 8707 resource identifier of the MCP endpoint.
func (s *Server) Resource() string { return s.issuer + "/mcp" }

// ResourceMetadataURL is advertised in WWW-Authenticate on 401 responses.
func (s *Server) ResourceMetadataURL() string {
	return s.issuer + "/.well-known/oauth-protected-resource/mcp"
}

// resourceMatches compares a client-supplied RFC 8707 resource indicator
// against the canonical MCP resource. Scheme and host compare
// case-insensitively and a trailing slash is ignored, per the MCP spec's
// robustness guidance — a byte-exact match would reject a client that was
// configured with "https://HOST/mcp/".
func (s *Server) resourceMatches(raw string) bool {
	return canonicalResource(raw) == canonicalResource(s.Resource())
}

func canonicalResource(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	return strings.TrimRight(u.String(), "/")
}

// Routes registers all OAuth endpoints. They must be mounted outside the
// bearer-auth group: clients hit them precisely because they have no token.
// The AS metadata is served at the RFC 8414 root path plus the path-inserted
// and OIDC-discovery variants some client versions probe first.
func (s *Server) Routes(r chi.Router) {
	r.Get("/.well-known/oauth-protected-resource", s.handleResourceMetadata)
	r.Get("/.well-known/oauth-protected-resource/mcp", s.handleResourceMetadata)
	r.Get("/.well-known/oauth-authorization-server", s.handleASMetadata)
	r.Get("/.well-known/oauth-authorization-server/mcp", s.handleASMetadata)
	r.Get("/.well-known/openid-configuration", s.handleASMetadata)
	r.Get("/.well-known/openid-configuration/mcp", s.handleASMetadata)
	r.Post("/oauth/register", s.handleRegister)
	r.Get("/oauth/authorize", s.handleAuthorizeForm)
	r.Post("/oauth/authorize", s.handleAuthorizeSubmit)
	r.Post("/oauth/token", s.handleToken)
}

// ValidateAccessToken resolves a presented bearer token to its subject. It
// satisfies api.TokenValidator.
func (s *Server) ValidateAccessToken(ctx context.Context, token string) (string, error) {
	t, err := s.store.GetAccessToken(ctx, hashToken(token))
	if err != nil {
		return "", err
	}
	return t.Subject, nil
}

func (s *Server) handleResourceMetadata(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":                 s.Resource(),
		"authorization_servers":    []string{s.issuer},
		"bearer_methods_supported": []string{"header"},
		"resource_name":            "Athena",
	})
}

func (s *Server) handleASMetadata(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                s.issuer,
		"authorization_endpoint":                s.issuer + "/oauth/authorize",
		"token_endpoint":                        s.issuer + "/oauth/token",
		"registration_endpoint":                 s.issuer + "/oauth/register",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
	})
}

// newToken mints an opaque credential. The prefix makes leaked strings
// identifiable; only the SHA-256 hash is ever stored.
func newToken(prefix string) (token string, hash []byte) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failure is unrecoverable; never issue weak tokens.
		panic(err)
	}
	token = prefix + hex.EncodeToString(b)
	return token, hashToken(token)
}

func hashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeOAuthError emits the RFC 6749 error body shape.
func writeOAuthError(w http.ResponseWriter, status int, code, description string) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, map[string]string{"error": code, "error_description": description})
}
