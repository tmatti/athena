# OAuth 2.1 for the MCP endpoint

## Why

Claude's iOS/web apps add remote MCP servers as *connectors*, and connectors
only support OAuth — there is no way to configure a static bearer header
(that's a Claude Code CLI-only feature). To use Athena from those surfaces,
Athena must speak the MCP authorization spec (2025-06-18), which is OAuth 2.1.

The static `BRAIN_API_KEY` bearer auth **remains** for curl, scripts, and any
backend that holds the key. OAuth is added alongside it, not instead of it.

## Design

Athena has no upstream identity provider, so the binary plays both OAuth
roles:

- **Resource server** — `/mcp` (and `/v1`) accept OAuth access tokens.
- **Authorization server** — a minimal embedded AS that authenticates the one
  resource owner and issues tokens.

This keeps the single-binary, single-port design; no external IdP.

### Flow

**Authorization Code + PKCE (S256)** — the only flow the MCP spec allows.
End-to-end, the first connection from a Claude app looks like:

```
Claude app          Athena
    |  POST /mcp (no token)                  |
    |<--- 401 + WWW-Authenticate: Bearer     |
    |     resource_metadata="…/.well-known/oauth-protected-resource/mcp"
    |  GET /.well-known/oauth-protected-resource/mcp                |
    |<--- { resource, authorization_servers: [issuer] }             |
    |  GET /.well-known/oauth-authorization-server                  |
    |<--- { authorization_endpoint, token_endpoint, registration_endpoint, S256 } |
    |  POST /oauth/register  (RFC 7591 dynamic client registration) |
    |<--- { client_id }                                             |
    |  browser -> GET /oauth/authorize?client_id&redirect_uri&code_challenge&state&resource |
    |     user enters the brain key on a minimal login page         |
    |  browser <- 302 redirect_uri?code=…&state=…                   |
    |  POST /oauth/token (code + code_verifier)                     |
    |<--- { access_token, refresh_token, expires_in }               |
    |  POST /mcp (Authorization: Bearer <access_token>)             |
```

### Spec compliance checklist

| Requirement | How Athena meets it |
|---|---|
| 401 + `WWW-Authenticate` pointing at resource metadata (RFC 9728) | Auth middleware adds the header when OAuth is enabled |
| Protected resource metadata | `GET /.well-known/oauth-protected-resource` and `…/mcp` |
| AS metadata (RFC 8414) | `GET /.well-known/oauth-authorization-server` |
| PKCE mandatory | `code_challenge_method=S256` required; `plain` rejected |
| Dynamic client registration (RFC 7591) | `POST /oauth/register`, public clients only (`token_endpoint_auth_method: none`) |
| Resource indicators (RFC 8707) | `resource` param accepted on authorize + token; if present it must match the canonical MCP resource (`PUBLIC_BASE_URL/mcp`) |
| Refresh tokens | Issued with every grant; single-use with rotation |

### The single subject

There is deliberately **no user table**. Every token carries a `subject`
column whose value is always `owner`. The auth middleware resolves any
accepted credential (static key or OAuth token) to that subject and stores it
on the request context. If multi-user ever happens, the token format,
endpoints, and middleware do not change — only what `subject` contains and
how store queries scope by it.

Authenticating the resource owner at `/oauth/authorize` reuses
`BRAIN_API_KEY` as the login credential: one secret to manage, and it is
exactly as sensitive as what it already protects. Failed attempts get a
constant-time compare plus a short sleep, and key attempts and client
registrations are each bounded by a global token bucket (attempts run
concurrently, so a per-request sleep alone bounds nothing). The bucket is
global rather than per-IP on purpose — a single-user server behind a proxy
can't trust peer addresses, and the worst case is the owner briefly waiting
out an attacker's exhausted bucket. Registrations are also capped in size,
and clients older than 30 days holding no code or token are swept.

### Endpoints

All OAuth endpoints are mounted **outside** the bearer-auth group:

| Route | Purpose |
|---|---|
| `GET /.well-known/oauth-protected-resource[/mcp]` | RFC 9728 resource metadata |
| `GET /.well-known/oauth-authorization-server[/mcp]` | RFC 8414 AS metadata (also aliased at `/.well-known/openid-configuration[/mcp]` for clients that probe OIDC discovery paths) |
| `POST /oauth/register` | Dynamic client registration |
| `GET /oauth/authorize` | Login form (validates client, redirect URI, PKCE params) |
| `POST /oauth/authorize` | Verifies the key, issues the code, redirects back |
| `POST /oauth/token` | `authorization_code` and `refresh_token` grants |

Redirect URIs must match a registered URI exactly and must be `https`
(loopback `http://localhost` / `http://127.0.0.1` is allowed for CLI-style
clients that run a local callback server).

### Tokens

Opaque random strings (32 bytes, hex, prefixed `athc_` / `athat_` / `athrt_`
for codes / access / refresh so leaked strings are identifiable). Only the
SHA-256 hash is stored. No JWTs — nothing needs to validate tokens except
Athena itself, and opaque + DB lookup means instant revocation by row delete.

| Artifact | TTL | Notes |
|---|---|---|
| Authorization code | 10 min | Single use (`DELETE … RETURNING`) |
| Access token | 1 hour | Validated on every request |
| Refresh token | 30 days rolling, 90 days absolute | Single use; rotation issues a fresh pair |

Every access/refresh pair carries a `family_id` tying it to the grant it
descends from. Rotated refresh tokens are kept as consumed tombstones until
they expire; presenting one again is treated as theft and revokes the whole
family, including the currently live pair. Rotation extends a grant by 30
days at a time but never past 90 days from the original authorization —
after that the owner logs in again. Expired rows are swept opportunistically
whenever new tokens are issued.

### Schema (migration `00004_oauth.sql`)

```sql
oauth_clients     (id uuid PK, name, redirect_uris text[], created_at)
oauth_auth_codes  (code_hash bytea PK, client_id FK cascade, redirect_uri,
                   code_challenge, scope, resource, subject, expires_at)
oauth_tokens      (token_hash bytea PK, kind access|refresh, client_id FK cascade,
                   subject, scope, expires_at, created_at)
```

### Configuration

| Env var | Meaning |
|---|---|
| `PUBLIC_BASE_URL` | Public HTTPS origin of the server (e.g. `https://athena.example.com`). **Setting it enables OAuth**; unset keeps today's behavior exactly (static key only, no new routes). |

OAuth requires TLS and a stable hostname — token security depends on it. The
only non-HTTPS value accepted is a loopback URL for local development.

### Package layout

- `internal/oauth` — protocol logic and HTTP handlers (metadata, DCR,
  authorize, token) plus access-token validation for the middleware. No SQL.
- `internal/store/oauth.go` — all OAuth SQL, per the repo rule.
- `internal/api` — `BearerAuth` gains an optional token-validator fallback and
  emits the `WWW-Authenticate` challenge; the router mounts the public OAuth
  routes.

OAuth never touches `internal/service` — it is auth infrastructure, not brain
logic.

### Explicitly out of scope (for now)

- **Multi-user** — single subject `owner`; see above for the upgrade path.
- **Token revocation endpoint (RFC 7009)** — revoke by deleting rows.
- **Confidential clients / client secrets** — Claude registers public clients
  with PKCE; secrets add nothing here.
- **Consent screen with scopes** — a single-owner server has nothing to
  consent to beyond logging in; the login page states what is being granted.
