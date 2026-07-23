package oauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/tmatti/athena/internal/store"
	"github.com/tmatti/athena/internal/testdb"
)

const (
	testIssuer   = "https://athena.example.com"
	testLoginKey = "brain-key-0123456789"
	testRedirect = "https://claude.ai/api/mcp/auth_callback"
)

func newTestServer(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	s := New(store.New(testdb.Pool(t)), testIssuer, testLoginKey, discard())
	r := chi.NewRouter()
	s.Routes(r)
	return s, r
}

func register(t *testing.T, h http.Handler) string {
	t.Helper()
	body := `{"client_name":"Claude","redirect_uris":["` + testRedirect + `"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/oauth/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	var resp struct {
		ClientID   string `json:"client_id"`
		AuthMethod string `json:"token_endpoint_auth_method"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.ClientID)
	require.Equal(t, "none", resp.AuthMethod)
	return resp.ClientID
}

// authorize drives GET (form) then POST (login) and returns the code that
// the server issued via the redirect back to the client.
func authorize(t *testing.T, h http.Handler, clientID, challenge, key string) *httptest.ResponseRecorder {
	t.Helper()
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {testRedirect},
		"state":                 {"st4te"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"resource":              {testIssuer + "/mcp"},
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+q.Encode(), nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `name="code_challenge" value="`+challenge+`"`)

	form := q
	form.Set("key", key)
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/oauth/authorize", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(rec, req)
	return rec
}

func exchange(t *testing.T, h http.Handler, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(rec, req)
	return rec
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Error        string `json:"error"`
}

func TestFullAuthorizationCodeFlow(t *testing.T) {
	s, h := newTestServer(t)
	ctx := context.Background()

	clientID := register(t, h)

	verifier := "test-verifier-string-that-is-long-enough-12345"
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	// Wrong key: no redirect, form re-rendered with an error.
	rec := authorize(t, h, clientID, challenge, "wrong-key")
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Contains(t, rec.Body.String(), "not correct")

	// Right key: 302 back to the client with code + state.
	rec = authorize(t, h, clientID, challenge, testLoginKey)
	require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
	loc, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)
	require.Equal(t, "claude.ai", loc.Host)
	require.Equal(t, "st4te", loc.Query().Get("state"))
	code := loc.Query().Get("code")
	require.True(t, strings.HasPrefix(code, "athc_"), code)

	// Happy-path exchange: code + verifier -> access/refresh pair.
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"client_id":     {clientID},
		"redirect_uri":  {testRedirect},
		"code_verifier": {verifier},
		"resource":      {testIssuer + "/mcp"},
	}
	rec = exchange(t, h, form)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var tok tokenResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &tok))
	require.Equal(t, "Bearer", tok.TokenType)
	require.True(t, strings.HasPrefix(tok.AccessToken, "athat_"))
	require.True(t, strings.HasPrefix(tok.RefreshToken, "athrt_"))
	require.Positive(t, tok.ExpiresIn)

	// The access token authenticates and resolves to the single subject.
	subject, err := s.ValidateAccessToken(ctx, tok.AccessToken)
	require.NoError(t, err)
	require.Equal(t, store.SubjectOwner, subject)
	_, err = s.ValidateAccessToken(ctx, "athat_forged")
	require.Error(t, err)

	// Codes are single-use: replaying the same code fails.
	rec = exchange(t, h, form)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	var replay tokenResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &replay))
	require.Equal(t, "invalid_grant", replay.Error)

	// Refresh rotation: old refresh token dies, new pair works.
	rec = exchange(t, h, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {tok.RefreshToken},
		"client_id":     {clientID},
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var tok2 tokenResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &tok2))
	require.NotEqual(t, tok.AccessToken, tok2.AccessToken)

	subject, err = s.ValidateAccessToken(ctx, tok2.AccessToken)
	require.NoError(t, err)
	require.Equal(t, store.SubjectOwner, subject)

	// Reusing the rotated refresh token is treated as theft: the request
	// fails and the whole family — including the fresh pair — is revoked.
	rec = exchange(t, h, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {tok.RefreshToken},
		"client_id":     {clientID},
	})
	require.Equal(t, http.StatusBadRequest, rec.Code, "rotated refresh token must be single-use")
	require.Contains(t, rec.Body.String(), "reuse")

	_, err = s.ValidateAccessToken(ctx, tok2.AccessToken)
	require.Error(t, err, "reuse must revoke the family's live access token")
	rec = exchange(t, h, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {tok2.RefreshToken},
		"client_id":     {clientID},
	})
	require.Equal(t, http.StatusBadRequest, rec.Code, "reuse must revoke the family's live refresh token")
}

func TestRefreshRequiresClientID(t *testing.T) {
	_, h := newTestServer(t)
	clientID := register(t, h)

	verifier := "verifier-for-client-id-test-that-is-long-enough"
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	rec := authorize(t, h, clientID, challenge, testLoginKey)
	require.Equal(t, http.StatusFound, rec.Code)
	loc, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)

	rec = exchange(t, h, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {loc.Query().Get("code")},
		"client_id":     {clientID},
		"redirect_uri":  {testRedirect},
		"code_verifier": {verifier},
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var tok tokenResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &tok))

	// Missing client_id is rejected before the token is consumed…
	rec = exchange(t, h, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {tok.RefreshToken},
	})
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "invalid_request")

	// …so a corrected retry with the same token still succeeds.
	rec = exchange(t, h, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {tok.RefreshToken},
		"client_id":     {clientID},
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func TestResourceIndicatorIsCanonicalized(t *testing.T) {
	_, h := newTestServer(t)
	clientID := register(t, h)

	verifier := "verifier-for-resource-test-that-is-long-enough"
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	// Trailing slash and host case differences must not fail the flow.
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {testRedirect},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"resource":              {"https://ATHENA.example.com/mcp/"},
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+q.Encode(), nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	form := q
	form.Set("key", testLoginKey)
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/oauth/authorize", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
	loc, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)

	rec = exchange(t, h, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {loc.Query().Get("code")},
		"client_id":     {clientID},
		"redirect_uri":  {testRedirect},
		"code_verifier": {verifier},
		"resource":      {"https://ATHENA.example.com/mcp/"},
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// A genuinely different resource still fails.
	rec = httptest.NewRecorder()
	q.Set("resource", "https://other.example.com/mcp")
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+q.Encode(), nil))
	require.Equal(t, http.StatusFound, rec.Code)
	loc, err = url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)
	require.Equal(t, "invalid_target", loc.Query().Get("error"))
}

func TestTokenExchangeRejectsBadPKCEAndTampering(t *testing.T) {
	_, h := newTestServer(t)
	clientID := register(t, h)

	verifier := "another-verifier-string-that-is-long-enough"
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	getCode := func() string {
		rec := authorize(t, h, clientID, challenge, testLoginKey)
		require.Equal(t, http.StatusFound, rec.Code)
		loc, err := url.Parse(rec.Header().Get("Location"))
		require.NoError(t, err)
		return loc.Query().Get("code")
	}

	base := func(code string) url.Values {
		return url.Values{
			"grant_type":    {"authorization_code"},
			"code":          {code},
			"client_id":     {clientID},
			"redirect_uri":  {testRedirect},
			"code_verifier": {verifier},
		}
	}

	// Wrong PKCE verifier.
	form := base(getCode())
	form.Set("code_verifier", "not-the-right-verifier")
	rec := exchange(t, h, form)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "PKCE")

	// Wrong redirect_uri.
	form = base(getCode())
	form.Set("redirect_uri", "https://evil.example.com/cb")
	rec = exchange(t, h, form)
	require.Equal(t, http.StatusBadRequest, rec.Code)

	// Wrong resource indicator.
	form = base(getCode())
	form.Set("resource", "https://other.example.com/mcp")
	rec = exchange(t, h, form)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "invalid_target")

	// Code issued to a different client.
	otherClient := register(t, h)
	form = base(getCode())
	form.Set("client_id", otherClient)
	rec = exchange(t, h, form)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestLoginPageSecurityHeadersAndRedirectHost(t *testing.T) {
	_, h := newTestServer(t)
	clientID := register(t, h)

	wantHeaders := map[string]string{
		"Content-Security-Policy": "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'",
		"X-Frame-Options":         "DENY",
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "no-referrer",
	}

	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {testRedirect},
		"state":                 {"st4te"},
		"code_challenge":        {"a-challenge"},
		"code_challenge_method": {"S256"},
	}

	// GET form: headers present, and the consent page names the redirect host.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+q.Encode(), nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	for k, v := range wantHeaders {
		require.Equal(t, v, rec.Header().Get(k), k)
	}
	require.Contains(t, rec.Body.String(), "claude.ai")

	// Wrong-key POST re-renders the form and must carry the headers too.
	form := q
	form.Set("key", "wrong-key")
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/oauth/authorize", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
	for k, v := range wantHeaders {
		require.Equal(t, v, rec.Header().Get(k), k)
	}
	require.Contains(t, rec.Body.String(), "claude.ai")
}

func TestLoginAttemptsAreRateLimited(t *testing.T) {
	s, h := newTestServer(t)
	clientID := register(t, h)

	// Two attempts allowed, no refill: deterministic exhaustion.
	s.loginLimiter = &limiter{tokens: 2, burst: 2, rate: 0, now: time.Now}

	rec := authorize(t, h, clientID, "a-challenge", "wrong-key")
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	rec = authorize(t, h, clientID, "a-challenge", "wrong-key")
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	// Bucket empty: even the correct key is rejected until tokens refill.
	rec = authorize(t, h, clientID, "a-challenge", testLoginKey)
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.Contains(t, rec.Body.String(), "Too many attempts")
}

func TestRegistrationIsRateLimitedAndCapped(t *testing.T) {
	s, h := newTestServer(t)

	post := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/oauth/register", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		h.ServeHTTP(rec, req)
		return rec
	}

	// Too many redirect URIs in one registration.
	uris := make([]string, maxRedirectURIs+1)
	for i := range uris {
		uris[i] = "https://a.example/cb"
	}
	body, err := json.Marshal(map[string]any{"redirect_uris": uris})
	require.NoError(t, err)
	rec := post(string(body))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "too many redirect_uris")

	// Overlong client name.
	body, err = json.Marshal(map[string]any{
		"redirect_uris": []string{"https://a.example/cb"},
		"client_name":   strings.Repeat("x", maxClientNameLen+1),
	})
	require.NoError(t, err)
	rec = post(string(body))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "client_name")

	// Exhausted bucket: registration is refused outright.
	s.registerLimiter = &limiter{tokens: 0, burst: 1, rate: 0, now: time.Now}
	rec = post(`{"redirect_uris":["https://a.example/cb"]}`)
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
}

func TestAuthorizeRejectsUnregisteredRedirectAndBadParams(t *testing.T) {
	_, h := newTestServer(t)
	clientID := register(t, h)

	// Unregistered redirect_uri: 400, never a redirect.
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {"https://evil.example.com/cb"},
		"code_challenge":        {"x"},
		"code_challenge_method": {"S256"},
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+q.Encode(), nil))
	require.Equal(t, http.StatusBadRequest, rec.Code)

	// Missing PKCE challenge: error is delivered via redirect to the
	// registered URI, per RFC 6749.
	q = url.Values{
		"response_type": {"code"},
		"client_id":     {clientID},
		"redirect_uri":  {testRedirect},
		"state":         {"s"},
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+q.Encode(), nil))
	require.Equal(t, http.StatusFound, rec.Code)
	loc, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)
	require.Equal(t, "invalid_request", loc.Query().Get("error"))
	require.Equal(t, "s", loc.Query().Get("state"))

	// Unknown client: 400.
	q.Set("client_id", "6b6ff6f4-8ba5-4be9-9ffe-8d4441ae1dcc")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+q.Encode(), nil))
	require.Equal(t, http.StatusBadRequest, rec.Code)

	// Registration with a non-loopback http redirect URI is rejected.
	body := `{"redirect_uris":["http://example.com/cb"]}`
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/oauth/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "invalid_redirect_uri")
}
