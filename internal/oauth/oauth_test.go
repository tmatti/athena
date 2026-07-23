package oauth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
)

func discard() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func TestVerifyPKCE(t *testing.T) {
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	require.True(t, verifyPKCE(verifier, challenge))
	require.False(t, verifyPKCE("wrong-verifier", challenge))
	require.False(t, verifyPKCE("", challenge))
	require.False(t, verifyPKCE(verifier, ""))
}

func TestValidRedirectURI(t *testing.T) {
	valid := []string{
		"https://claude.ai/api/mcp/auth_callback",
		"http://localhost:5173/callback",
		"http://127.0.0.1:8123/cb",
	}
	invalid := []string{
		"http://example.com/cb", // plain http off loopback
		"myapp://callback",      // custom scheme
		"not a url",
		"",
		"/relative/path",
	}
	for _, u := range valid {
		require.True(t, validRedirectURI(u), u)
	}
	for _, u := range invalid {
		require.False(t, validRedirectURI(u), u)
	}
}

func TestMetadataEndpoints(t *testing.T) {
	s := New(nil, "https://athena.example.com/", "key-key-key-key-1", discard())
	r := chi.NewRouter()
	s.Routes(r)

	// Trailing slash on the issuer is normalized away.
	require.Equal(t, "https://athena.example.com/mcp", s.Resource())

	for _, path := range []string{"/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp"} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusOK, rec.Code, path)

		var meta struct {
			Resource             string   `json:"resource"`
			AuthorizationServers []string `json:"authorization_servers"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &meta))
		require.Equal(t, "https://athena.example.com/mcp", meta.Resource)
		require.Equal(t, []string{"https://athena.example.com"}, meta.AuthorizationServers)
	}

	// The AS metadata is also served at the path-inserted and OIDC-discovery
	// variants that some client versions probe first.
	for _, path := range []string{
		"/.well-known/oauth-authorization-server/mcp",
		"/.well-known/openid-configuration",
		"/.well-known/openid-configuration/mcp",
	} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusOK, rec.Code, path)
	}

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/.well-known/oauth-authorization-server", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	var as struct {
		Issuer            string   `json:"issuer"`
		AuthorizeEndpoint string   `json:"authorization_endpoint"`
		TokenEndpoint     string   `json:"token_endpoint"`
		Registration      string   `json:"registration_endpoint"`
		ChallengeMethods  []string `json:"code_challenge_methods_supported"`
		GrantTypes        []string `json:"grant_types_supported"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &as))
	require.Equal(t, "https://athena.example.com", as.Issuer)
	require.Equal(t, "https://athena.example.com/oauth/authorize", as.AuthorizeEndpoint)
	require.Equal(t, "https://athena.example.com/oauth/token", as.TokenEndpoint)
	require.Equal(t, "https://athena.example.com/oauth/register", as.Registration)
	require.Equal(t, []string{"S256"}, as.ChallengeMethods)
	require.Equal(t, []string{"authorization_code", "refresh_token"}, as.GrantTypes)
}

func TestRefreshExpiryClampedToFamilyCap(t *testing.T) {
	familyCreated := time.Unix(1_000_000, 0)

	// Early in the family's life the rolling TTL applies unchanged.
	now := familyCreated
	require.Equal(t, now.Add(refreshTTL), refreshExpiry(now, familyCreated))

	// Near the cap the expiry is clamped to it.
	now = familyCreated.Add(familyMaxTTL - time.Hour)
	require.Equal(t, familyCreated.Add(familyMaxTTL), refreshExpiry(now, familyCreated))
}

func TestCanonicalResource(t *testing.T) {
	require.Equal(t, canonicalResource("https://a.example/mcp"), canonicalResource("https://A.EXAMPLE/mcp/"))
	require.Equal(t, canonicalResource("https://a.example/mcp"), canonicalResource("HTTPS://a.example/mcp"))
	require.NotEqual(t, canonicalResource("https://a.example/mcp"), canonicalResource("https://a.example/MCP"))
	require.NotEqual(t, canonicalResource("https://a.example/mcp"), canonicalResource("https://b.example/mcp"))
}

func TestTokenPrefixesDiffer(t *testing.T) {
	a, ah := newToken("athat_")
	b, bh := newToken("athat_")
	require.NotEqual(t, a, b)
	require.NotEqual(t, ah, bh)
	require.Equal(t, hashToken(a), ah)
}
