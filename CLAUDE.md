# Athena

Single-user "personal brain" backend: Go REST API + MCP server (one binary, one port) over Postgres + pgvector. Memories (short facts) and notes (chunked documents) with hybrid keyword+vector search.

## Commands

- Build: `go build ./...`
- Test: `go test -race ./...` (integration tests need `TEST_DATABASE_URL` pointing at a pgvector Postgres; they skip if unset)
- Vet: `go vet ./...`
- Local dev DB: `docker compose up -d db`
- Run: `go run ./cmd/athena` (env vars per `.env.example`)

## Architecture rules

- REST handlers (`internal/api`) and MCP tools (`internal/mcpserver`) are thin adapters. Shared logic lives in `internal/service/brain.go`; SQL lives only in `internal/store`.
- Migrations are embedded goose SQL files in `migrations/`, run automatically at startup. Never edit an applied migration; add a new one.
- Embedding failures must never fail a write: rows persist with `embed_status='failed'` and a background loop retries.
- The embedder is an interface (`internal/embed.Embedder`); tests use `FakeEmbedder`, never the network.
