package oauth

import (
	"crypto/subtle"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/tmatti/athena/internal/store"
)

var loginPage = template.Must(template.New("login").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>Athena — authorize</title>
<style>
  body{font-family:system-ui,sans-serif;background:#f4f6f5;color:#20282b;display:flex;justify-content:center;padding:12vh 16px}
  main{background:#fff;border:1px solid #d9e0de;border-radius:12px;padding:28px;max-width:380px;width:100%}
  h1{font-size:18px;margin:0 0 4px}
  p{font-size:14px;color:#5c6b70;margin:0 0 18px;line-height:1.5}
  strong{color:#20282b}
  input[type=password]{width:100%;box-sizing:border-box;padding:10px;font-size:15px;border:1px solid #c6d0cd;border-radius:8px;margin-bottom:14px}
  button{width:100%;padding:10px;font-size:15px;border:0;border-radius:8px;background:#0b7261;color:#fff;cursor:pointer}
  .dest{background:#eef3f1;border:1px solid #d9e0de;border-radius:8px;padding:8px 12px;font-size:13.5px;margin-bottom:14px}
  .err{background:#fbeae7;border:1px solid #e0b9b1;color:#a8453a;border-radius:8px;padding:8px 12px;font-size:13.5px;margin-bottom:14px}
</style>
</head>
<body>
<main>
  <h1>Authorize {{if .ClientName}}{{.ClientName}}{{else}}this application{{end}}</h1>
  <p><strong>{{if .ClientName}}{{.ClientName}}{{else}}The application{{end}}</strong> is requesting full access to your Athena brain — memories, notes, and search. Enter your brain key to allow it.</p>
  <p class="dest">After you approve, you'll be redirected to <strong>{{.RedirectHost}}</strong>.</p>
  {{if .Error}}<div class="err">{{.Error}}</div>{{end}}
  <form method="post" action="/oauth/authorize">
    <input type="hidden" name="response_type" value="code">
    <input type="hidden" name="client_id" value="{{.ClientID}}">
    <input type="hidden" name="redirect_uri" value="{{.RedirectURI}}">
    <input type="hidden" name="state" value="{{.State}}">
    <input type="hidden" name="code_challenge" value="{{.CodeChallenge}}">
    <input type="hidden" name="code_challenge_method" value="S256">
    <input type="hidden" name="scope" value="{{.Scope}}">
    <input type="hidden" name="resource" value="{{.Resource}}">
    <input type="password" name="key" placeholder="Brain key" autofocus autocomplete="current-password">
    <button type="submit">Allow access</button>
  </form>
</main>
</body>
</html>
`))

type loginPageData struct {
	ClientName    string
	ClientID      string
	RedirectURI   string
	RedirectHost  string
	State         string
	CodeChallenge string
	Scope         string
	Resource      string
	Error         string
}

// authorizeRequest holds the validated query/form parameters of an
// authorization request.
type authorizeRequest struct {
	client        store.OAuthClient
	redirectURI   string
	state         string
	codeChallenge string
	scope         string
	resource      string
}

// validateAuthorize checks the parameters common to the GET form and the POST
// submit. Client and redirect URI failures render a 400 page (the spec
// forbids redirecting to an unverified URI); all later failures redirect back
// to the client with an OAuth error code.
func (s *Server) validateAuthorize(w http.ResponseWriter, r *http.Request) (authorizeRequest, bool) {
	q := r.Form
	var req authorizeRequest

	clientID := q.Get("client_id")
	if _, err := uuid.Parse(clientID); err != nil {
		http.Error(w, "unknown client_id", http.StatusBadRequest)
		return req, false
	}
	client, err := s.store.GetOAuthClient(r.Context(), clientID)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "unknown client_id", http.StatusBadRequest)
		return req, false
	} else if err != nil {
		s.log.Error("load oauth client", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return req, false
	}

	req.client = client
	req.redirectURI = q.Get("redirect_uri")
	if !slices.Contains(client.RedirectURIs, req.redirectURI) {
		http.Error(w, "redirect_uri is not registered for this client", http.StatusBadRequest)
		return req, false
	}

	req.state = q.Get("state")
	req.scope = q.Get("scope")
	req.codeChallenge = q.Get("code_challenge")
	req.resource = q.Get("resource")

	fail := func(code, desc string) (authorizeRequest, bool) {
		s.redirectError(w, r, req.redirectURI, req.state, code, desc)
		return req, false
	}
	if rt := q.Get("response_type"); rt != "code" {
		return fail("unsupported_response_type", "only response_type=code is supported")
	}
	if req.codeChallenge == "" {
		return fail("invalid_request", "code_challenge is required (PKCE)")
	}
	if m := q.Get("code_challenge_method"); m != "S256" {
		return fail("invalid_request", "code_challenge_method must be S256")
	}
	if req.resource != "" && req.resource != s.Resource() {
		return fail("invalid_target", "unknown resource: "+req.resource)
	}
	return req, true
}

func (s *Server) handleAuthorizeForm(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	req, ok := s.validateAuthorize(w, r)
	if !ok {
		return
	}
	s.renderLogin(w, http.StatusOK, req, "")
}

func (s *Server) handleAuthorizeSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	// Hidden fields are re-validated in full: they are client-controlled.
	req, ok := s.validateAuthorize(w, r)
	if !ok {
		return
	}

	key := r.PostForm.Get("key")
	if subtle.ConstantTimeCompare([]byte(key), []byte(s.loginKey)) != 1 {
		// Blunt brute force: this endpoint is unauthenticated by nature.
		time.Sleep(750 * time.Millisecond)
		s.log.Warn("oauth authorize: wrong key", "client_id", req.client.ID)
		s.renderLogin(w, http.StatusUnauthorized, req, "That key is not correct.")
		return
	}

	code, hash := newToken("athc_")
	err := s.store.CreateAuthCode(r.Context(), store.AuthCodeParams{
		CodeHash:      hash,
		ClientID:      req.client.ID,
		RedirectURI:   req.redirectURI,
		CodeChallenge: req.codeChallenge,
		Scope:         req.scope,
		Resource:      req.resource,
		Subject:       store.SubjectOwner,
		ExpiresAt:     time.Now().Add(codeTTL),
	})
	if err != nil {
		s.log.Error("create auth code", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	dest, _ := url.Parse(req.redirectURI)
	q := dest.Query()
	q.Set("code", code)
	if req.state != "" {
		q.Set("state", req.state)
	}
	dest.RawQuery = q.Encode()
	http.Redirect(w, r, dest.String(), http.StatusFound)
}

func (s *Server) renderLogin(w http.ResponseWriter, status int, req authorizeRequest, errMsg string) {
	// The redirect URI is already validated against the client's registration;
	// showing its host lets the owner catch a phishing client whose consent
	// page is otherwise pixel-identical.
	var host string
	if u, err := url.Parse(req.redirectURI); err == nil {
		host = u.Host
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	// The login page must never be framed (clickjacking) or run any script:
	// the CSP allows only the single inline <style> block. No form-action
	// directive — Chrome enforces it against the redirect that follows the
	// POST, which would block the 302 back to the client's callback.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(status)
	_ = loginPage.Execute(w, loginPageData{
		ClientName:    req.client.Name,
		ClientID:      req.client.ID,
		RedirectURI:   req.redirectURI,
		RedirectHost:  host,
		State:         req.state,
		CodeChallenge: req.codeChallenge,
		Scope:         req.scope,
		Resource:      req.resource,
		Error:         errMsg,
	})
}

// redirectError sends an OAuth error back to a *verified* redirect URI.
func (s *Server) redirectError(w http.ResponseWriter, r *http.Request, redirectURI, state, code, desc string) {
	dest, err := url.Parse(redirectURI)
	if err != nil {
		http.Error(w, desc, http.StatusBadRequest)
		return
	}
	q := dest.Query()
	q.Set("error", code)
	q.Set("error_description", desc)
	if state != "" {
		q.Set("state", state)
	}
	dest.RawQuery = q.Encode()
	http.Redirect(w, r, dest.String(), http.StatusFound)
}
