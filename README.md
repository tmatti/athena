# Athena

Athena is a single-user, self-hosted "personal brain": one small Go binary that serves a REST API **and** a Model Context Protocol (MCP) server on a single port, backed by Postgres + [pgvector](https://github.com/pgvector/pgvector). It stores two kinds of records — **memories** (short atomic facts your AI agents remember) and **notes** (longer documents, chunked and embedded) — and retrieves them with hybrid keyword + vector search fused by reciprocal rank fusion. The point is durable, portable memory for your AI tools: instead of relying on a vendor's built-in, lock-in memory feature, point Claude (or any MCP client) at Athena over MCP, and let any future CLI, web, or mobile client use the same REST API against the same data. MCP-first, provider-agnostic Postgres, distributed as a ~15 MB container image. MIT licensed.

## Features

- **Memories and notes** — atomic facts plus longer titled documents that are automatically chunked and embedded.
- **Hybrid search** — keyword (Postgres full-text) and vector similarity combined via reciprocal rank fusion, with `hybrid`, `vector`, and `keyword` modes.
- **Ten MCP tools** exposed over both Streamable HTTP and stdio for direct use by AI agents.
- **Pluggable embeddings** — any OpenAI-compatible embeddings endpoint (OpenRouter by default; OpenAI, Ollama, etc. also work).
- **Keyword-only mode** — set `EMBEDDING_PROVIDER=none` to run with no embedding provider at all; hybrid search degrades gracefully to keyword search.
- **Resilient writes** — embedding failures never fail a write; rows persist with `embed_status='failed'` and a background loop retries.
- **Embedded migrations** — schema migrations are baked into the binary and run automatically at startup.
- **Single bearer-token auth** — one shared secret guards the entire API and MCP surface.

## Quickstart (Docker Compose)

Requires Docker. The Compose file starts Athena plus a `pgvector/pgvector:pg17` database.

```bash
cp .env.example .env
```

Edit `.env` and set at least:

- `BRAIN_API_KEY` — a long random string (minimum 16 characters); this is your bearer token.
- `EMBEDDING_API_KEY` — an API key for your embeddings provider (an OpenRouter key by default). Omit this and set `EMBEDDING_PROVIDER=none` if you want keyword-only search.

`DATABASE_URL` is provided by Compose automatically, so you can leave it as-is in `.env`.

```bash
docker compose up
```

Athena listens on `http://localhost:8080`. Verify it is healthy (this endpoint needs no auth):

```bash
curl http://localhost:8080/healthz
# {"status":"ok"}
```

Store a memory, then search for it (every request except `/healthz` needs the bearer token):

```bash
# Create a memory
curl -X POST http://localhost:8080/v1/memories \
  -H "Authorization: Bearer $BRAIN_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"content":"Athena stores memories and notes in Postgres with pgvector.","tags":["athena","architecture"]}'

# Search for it
curl -G http://localhost:8080/v1/search \
  -H "Authorization: Bearer $BRAIN_API_KEY" \
  --data-urlencode "q=where are memories stored" \
  --data-urlencode "mode=hybrid"
```

## Connecting AI agents (MCP)

Athena's MCP server is the flagship feature: it gives your AI agents durable memory they can read and write directly.

**Streamable HTTP endpoint:** `http://localhost:8080/mcp`, authenticated with the same bearer token as the REST API:

```
Authorization: Bearer $BRAIN_API_KEY
```

### Claude Code

```bash
claude mcp add --transport http athena http://localhost:8080/mcp \
  --header "Authorization: Bearer YOUR_KEY"
```

### JSON config (clients that use an `mcpServers` config file)

```json
{
  "mcpServers": {
    "athena": {
      "type": "http",
      "url": "http://localhost:8080/mcp",
      "headers": {
        "Authorization": "Bearer YOUR_KEY"
      }
    }
  }
}
```

### Local stdio alternative

For clients that launch a subprocess over stdio instead of connecting to an HTTP endpoint, run the binary with `--stdio`. The same environment variables are still required (it needs the database and, unless in keyword-only mode, the embeddings provider):

```bash
athena --stdio
```

### MCP tools

| Tool            | Description                                                                     |
| --------------- | ------------------------------------------------------------------------------- |
| `remember`      | Store one atomic fact as a memory (one fact per call).                           |
| `recall`        | Hybrid keyword + vector search over memories and notes; returns ranked results. |
| `forget`        | Delete a memory by id.                                                           |
| `get_memory`    | Fetch a memory's full content by id.                                            |
| `list_memories` | List the most recent memories, optionally filtered by tag.                      |
| `create_note`   | Create a note: a chunked, titled document for longer content.                   |
| `get_note`      | Fetch a note's full content by id.                                              |
| `update_note`   | Update a note's title, content, and/or tags (only provided fields change).      |
| `delete_note`   | Delete a note by id.                                                            |
| `list_tags`     | List all tags in use across memories and notes, with counts.                    |

## REST API

All resource endpoints live under `/v1` and require the `Authorization: Bearer $BRAIN_API_KEY` header. `/healthz` is unauthenticated.

| Method | Path                | Description                                                        |
| ------ | ------------------- | ----------------------------------------------------------------- |
| GET    | `/healthz`          | Liveness/readiness check (pings the database). No auth required.   |
| POST   | `/v1/memories`      | Create a memory (`content`, optional `tags`, `source`).           |
| GET    | `/v1/memories`      | List memories (query: `tag`, `limit`, `cursor`).                  |
| GET    | `/v1/memories/{id}` | Fetch a memory by id.                                             |
| PATCH  | `/v1/memories/{id}` | Update a memory (`content`, `tags`, and/or `source`).            |
| DELETE | `/v1/memories/{id}` | Delete a memory.                                                 |
| POST   | `/v1/notes`         | Create a note (`title`, `content`, optional `tags`).             |
| GET    | `/v1/notes`         | List notes (query: `tag`, `limit`, `cursor`).                    |
| GET    | `/v1/notes/{id}`    | Fetch a note by id.                                              |
| PATCH  | `/v1/notes/{id}`    | Update a note (`title`, `content`, and/or `tags`).              |
| DELETE | `/v1/notes/{id}`    | Delete a note.                                                   |
| GET    | `/v1/search`        | Hybrid/vector/keyword search across memories and notes.          |
| GET    | `/v1/tags`          | List all tags in use, with counts.                              |

**Search parameters** (`GET /v1/search`): `q` (query, required); `mode` = `hybrid` (default) \| `vector` \| `keyword`; `type` = `all` (default) \| `memory` \| `note`; `tag` to restrict to a single tag; `limit` for the maximum number of results.

**Errors** are returned as JSON in the shape `{"error":{"code":"...","message":"..."}}` with an appropriate HTTP status. Each memory and note chunk carries an `embed_status` of `pending`, `ok`, or `failed`; a write always succeeds even if embedding fails, and a background loop retries `failed`/`pending` rows.

## Configuration

All configuration is via environment variables (see `.env.example`).

| Variable              | Default                             | Description                                                                                     |
| --------------------- | ----------------------------------- | ----------------------------------------------------------------------------------------------- |
| `DATABASE_URL`        | *(required)*                        | Postgres connection string. The database must have the `pgvector` extension available.          |
| `BRAIN_API_KEY`       | *(required)*                        | Shared bearer token for the REST API and MCP endpoint. Must be at least 16 characters.          |
| `PORT`                | `8080`                              | HTTP listen port.                                                                               |
| `EMBEDDING_PROVIDER`  | `openai_compatible`                 | `openai_compatible` for embeddings, or `none` for keyword-only search.                          |
| `EMBEDDING_BASE_URL`  | `https://openrouter.ai/api/v1`      | Base URL of an OpenAI-compatible embeddings API.                                                |
| `EMBEDDING_MODEL`     | `openai/text-embedding-3-small`     | Embedding model name.                                                                            |
| `EMBEDDING_DIMENSIONS`| `1536`                              | Embedding vector dimensionality. Must match the model.                                          |
| `EMBEDDING_API_KEY`   | *(empty)*                           | API key for the embeddings provider. Required unless `EMBEDDING_PROVIDER=none`.                 |
| `LOG_LEVEL`           | `info`                              | Log level (`debug`, `info`, `warn`, `error`).                                                    |

**OpenAI-compatible endpoints:** `EMBEDDING_BASE_URL` works with any OpenAI-compatible embeddings API — OpenRouter (default), OpenAI (`https://api.openai.com/v1`), a local Ollama instance, and so on. Set `EMBEDDING_PROVIDER=none` to run without any provider; in that mode `hybrid` search transparently falls back to keyword search and `vector` search returns an error.

**Embedding-dimension guard:** on first boot Athena records the embedding provider, model, and dimensions it started with. On subsequent boots, any mismatch between the running config and what is stored is a hard, fail-fast error — mixing embeddings from different models or dimensions would corrupt search. The error message includes the exact SQL (wipe stored embeddings, reset `embed_status`, update the recorded metadata, and, for a dimension change, `ALTER TABLE` the vector columns) needed to migrate deliberately.

## Deploying

Athena is a single static binary in a distroless image, so any Docker host or VPS works.

```bash
# Build the image
docker build -t athena .

# Run it, pointing at any Postgres with pgvector
docker run -p 8080:8080 \
  -e DATABASE_URL="postgres://user:pass@your-db-host:5432/athena?sslmode=require" \
  -e BRAIN_API_KEY="a-long-random-string" \
  -e EMBEDDING_API_KEY="sk-or-..." \
  athena
```

Athena runs migrations automatically at startup, so it works against any managed Postgres that supports the `pgvector` extension (for example PlanetScale for Postgres, Neon, or Supabase) — just set `DATABASE_URL` and go.

### Kamal (VPS)

The repo ships a [Kamal 2](https://kamal-deploy.org) setup (`config/deploy.yml` + `.kamal/secrets`) that deploys Athena behind kamal-proxy. Point the `DATABASE_URL` secret at any Postgres with pgvector — managed or self-hosted. If you want the database on the same box, an optional `pgvector/pgvector:pg17` accessory is included: set `ATHENA_DB_ACCESSORY=true`, boot it with `kamal accessory boot db`, and use `postgres://athena:<POSTGRES_PASSWORD>@athena-db:5432/athena?sslmode=disable` (it's only reachable over the private Docker network; port 5432 is never exposed).

The config assumes Cloudflare's proxy sits in front: kamal-proxy serves a long-lived [Cloudflare Origin CA certificate](https://developers.cloudflare.com/ssl/origin-configuration/origin-ca/) and Cloudflare terminates public TLS. If you aren't using Cloudflare, replace the `proxy.ssl` hash with `ssl: true` to get automatic Let's Encrypt instead (requires a DNS-only record).

Prerequisites:

- Kamal 2 installed locally (`gem install kamal`, or the [Docker alias](https://kamal-deploy.org/docs/installation/)) and Docker running.
- A VPS you can SSH into as root.
- A GitHub personal access token with `write:packages` (images are pushed to GHCR).
- Cloudflare zone setup: a **proxied** (orange cloud) A record for your subdomain pointing at the VPS; SSL/TLS mode **Full (strict)**; an Origin CA cert + key (SSL/TLS → Origin Server → Create Certificate) saved locally as PEM files.

`config/deploy.yml` contains no personal values — the server, domain, and registry user are read from the environment (via ERB), and secrets are resolved through `.kamal/secrets` via the 1Password adapter (a plain env-var alternative is included there as comments). One-time 1Password setup with the [`op` CLI](https://developer.1password.com/docs/cli/):

```bash
op vault create Athena

POSTGRES_PASSWORD=$(openssl rand -hex 24)
op item create --vault Athena --category "Secure Note" --title deploy \
  ATHENA_SERVER_IP[text]=YOUR_VPS_IP \
  ATHENA_HOST[text]=athena.example.com \
  KAMAL_REGISTRY_USERNAME[text]=YOUR_GITHUB_USERNAME \
  KAMAL_REGISTRY_PASSWORD[password]=ghp_YOUR_PAT \
  POSTGRES_PASSWORD[password]="$POSTGRES_PASSWORD" \
  DATABASE_URL[password]="postgres://athena:${POSTGRES_PASSWORD}@athena-db:5432/athena?sslmode=disable" \
  BRAIN_API_KEY[password]="$(openssl rand -hex 32)" \
  EMBEDDING_API_KEY[password]=sk-or-YOUR_KEY \
  CLOUDFLARE_ORIGIN_CERT[password]="$(cat ~/secrets/athena-origin-cert.pem)" \
  CLOUDFLARE_ORIGIN_KEY[password]="$(cat ~/secrets/athena-origin-key.pem)"
unset POSTGRES_PASSWORD
```

Then each deploy shell only needs the non-secret deploy target (Kamal pulls the secrets itself through `.kamal/secrets`):

```bash
export OP_ACCOUNT=my.1password.com   # your account shorthand, from `op account list`
export ATHENA_SERVER_IP=$(op read "op://Athena/deploy/ATHENA_SERVER_IP")
export ATHENA_HOST=$(op read "op://Athena/deploy/ATHENA_HOST")
export KAMAL_REGISTRY_USERNAME=$(op read "op://Athena/deploy/KAMAL_REGISTRY_USERNAME")
```

First deploy (installs Docker on the host, boots the `db` accessory, builds, pushes, and deploys):

```bash
kamal setup
```

Every deploy after that:

```bash
kamal deploy
```

Kamal-proxy health-checks `/healthz` before switching traffic, and Athena runs its migrations before binding the port, so a deploy only goes live once migrations have completed. Useful commands: `kamal app logs`, `kamal accessory logs db`, `kamal rollback [version]`, and `kamal accessory boot db` if you ever need to (re)start the database on its own.

**Backup** is a plain Postgres dump:

```bash
pg_dump "$DATABASE_URL" > athena-backup.sql
```

## Development

```bash
docker compose up -d db                 # start a local pgvector Postgres
go run ./cmd/athena                      # run the server (env vars per .env.example)
go build ./...                           # build
go vet ./...                             # vet
TEST_DATABASE_URL=postgres://... go test -race ./...   # test (integration tests skip if unset)
```

Integration tests need `TEST_DATABASE_URL` pointing at a pgvector Postgres; they skip when it is unset. Embedding tests use a fake embedder and never hit the network.

## Limitations

- **Keyword search is English-tuned.** Full-text keyword search uses Postgres's `english` text-search configuration, so keyword ranking is poor for non-English content. Vector search is unaffected and still works well for other languages.
- **Request bodies are capped at 1 MiB.** Larger request bodies are rejected with a `413` response.
- **`null` cannot clear a nullable field via PATCH.** For a memory's `source` (and other nullable fields), JSON `null` and an omitted field are indistinguishable on the wire, so PATCH treats both as "leave unchanged." To change a value, set a new one; there is currently no way to clear it back to `null` through the API.

Repository layout:

```
cmd/athena          entrypoint (HTTP server + --stdio MCP mode)
internal/api        REST handlers, router, auth middleware (thin adapters)
internal/mcpserver  MCP tools over Streamable HTTP and stdio (thin adapters)
internal/service    brain.go — shared business logic for both surfaces
internal/store      all SQL access
internal/db         connection, embedded migrations, embedding-dimension guard
internal/embed      Embedder interface + OpenAI-compatible client
internal/chunk      note chunking (paragraph packing with overlap)
migrations          embedded goose SQL migrations
```

## License

MIT.
