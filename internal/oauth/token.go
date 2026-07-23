package oauth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/tmatti/athena/internal/store"
)

func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "body must be application/x-www-form-urlencoded")
		return
	}
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		s.tokenFromCode(w, r)
	case "refresh_token":
		s.tokenFromRefresh(w, r)
	default:
		writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type", "supported grant types: authorization_code, refresh_token")
	}
}

func (s *Server) tokenFromCode(w http.ResponseWriter, r *http.Request) {
	f := r.PostForm
	clientID := f.Get("client_id")
	if _, err := uuid.Parse(clientID); err != nil {
		writeOAuthError(w, http.StatusUnauthorized, "invalid_client", "unknown client_id")
		return
	}

	code, err := s.store.ConsumeAuthCode(r.Context(), hashToken(f.Get("code")))
	if errors.Is(err, store.ErrNotFound) {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "authorization code is invalid, expired, or already used")
		return
	} else if err != nil {
		s.log.Error("consume auth code", "error", err)
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "internal error")
		return
	}

	switch {
	case code.ClientID != clientID:
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "authorization code was issued to a different client")
		return
	case code.RedirectURI != f.Get("redirect_uri"):
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "redirect_uri does not match the authorization request")
		return
	case !verifyPKCE(f.Get("code_verifier"), code.CodeChallenge):
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "PKCE verification failed")
		return
	}
	// RFC 8707: a resource named at the token endpoint must be one this
	// server issued the grant for.
	if res := f.Get("resource"); res != "" && !s.resourceMatches(res) {
		writeOAuthError(w, http.StatusBadRequest, "invalid_target", "unknown resource: "+res)
		return
	}

	// A fresh authorization starts a fresh token family.
	s.issueTokens(w, r, code.ClientID, code.Subject, code.Scope, uuid.NewString(), time.Now())
}

func (s *Server) tokenFromRefresh(w http.ResponseWriter, r *http.Request) {
	f := r.PostForm
	// RFC 6749 §3.2.1: public clients must identify themselves. Checked
	// before consumption so a malformed request does not burn the token.
	clientID := f.Get("client_id")
	if clientID == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "client_id is required")
		return
	}
	tok, err := s.store.ConsumeRefreshToken(r.Context(), hashToken(f.Get("refresh_token")))
	if errors.Is(err, store.ErrRefreshTokenReused) {
		s.log.Warn("oauth: rotated refresh token reused; grant family revoked")
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "refresh token reuse detected; the grant has been revoked")
		return
	} else if errors.Is(err, store.ErrNotFound) {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "refresh token is invalid, expired, or already used")
		return
	} else if err != nil {
		s.log.Error("consume refresh token", "error", err)
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "internal error")
		return
	}
	if clientID != tok.ClientID {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "refresh token was issued to a different client")
		return
	}
	s.issueTokens(w, r, tok.ClientID, tok.Subject, tok.Scope, tok.FamilyID, tok.FamilyCreatedAt)
}

// issueTokens mints a fresh access + refresh pair in the given family and
// writes the RFC 6749 token response. Expired rows are swept opportunistically
// on each issuance.
func (s *Server) issueTokens(w http.ResponseWriter, r *http.Request, clientID, subject, scope, familyID string, familyCreated time.Time) {
	access, accessHash := newToken("athat_")
	refresh, refreshHash := newToken("athrt_")
	now := time.Now()

	err := s.store.InsertOAuthTokens(r.Context(),
		store.OAuthTokenParams{Hash: accessHash, Kind: store.TokenKindAccess, ClientID: clientID, Subject: subject, Scope: scope, FamilyID: familyID, FamilyCreatedAt: familyCreated, ExpiresAt: now.Add(accessTTL)},
		store.OAuthTokenParams{Hash: refreshHash, Kind: store.TokenKindRefresh, ClientID: clientID, Subject: subject, Scope: scope, FamilyID: familyID, FamilyCreatedAt: familyCreated, ExpiresAt: refreshExpiry(now, familyCreated)},
	)
	if err != nil {
		s.log.Error("insert oauth tokens", "error", err)
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "internal error")
		return
	}
	if err := s.store.DeleteExpiredOAuth(r.Context()); err != nil {
		s.log.Warn("sweep expired oauth rows", "error", err)
	}

	w.Header().Set("Cache-Control", "no-store")
	resp := map[string]any{
		"access_token":  access,
		"token_type":    "Bearer",
		"expires_in":    int(accessTTL.Seconds()),
		"refresh_token": refresh,
	}
	if scope != "" {
		resp["scope"] = scope
	}
	writeJSON(w, http.StatusOK, resp)
}

// refreshExpiry clamps the rolling refresh TTL to the family's absolute cap:
// rotation must not extend a grant's total lifetime forever, or a stolen
// refresh chain would stay alive indefinitely as long as it kept being used.
func refreshExpiry(now, familyCreated time.Time) time.Time {
	exp := now.Add(refreshTTL)
	if cap := familyCreated.Add(familyMaxTTL); exp.After(cap) {
		return cap
	}
	return exp
}

// verifyPKCE checks code_verifier against the S256 challenge recorded at
// authorization time: base64url(sha256(verifier)) == challenge.
func verifyPKCE(verifier, challenge string) bool {
	if verifier == "" || challenge == "" {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	computed := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(computed), []byte(challenge)) == 1
}
