# Athena architecture (C4)

How an AI agent's tool call travels through the MCP server to Postgres, how
memories and notes are saved and referenced, and how search works. Diagrams
follow the [C4 model](https://c4model.com): context → containers → components,
then dynamic diagrams for the three flows that matter.

- [Level 1 — System context](#level-1--system-context)
- [Level 2 — Containers](#level-2--containers)
- [Level 3 — Components](#level-3--components-inside-the-binary)
- [Data model](#data-model)
- [Flow: saving a memory](#flow-saving-a-memory-remember)
- [Flow: saving a note](#flow-saving-a-note-create_note)
- [Flow: search](#flow-search-recall)
- [How things are referenced](#how-things-are-referenced)

## Level 1 — System context

Athena is a single-user "personal brain". Two kinds of clients talk to it: AI
agents over MCP, and anything else (scripts, curl, a future UI) over REST.
The only external dependency is an OpenAI-compatible embedding API — and it is
optional: without it Athena runs keyword-only.

```mermaid
C4Context
    title System context — Athena
    Person(user, "You", "The single user. Talks to an agent, or hits the REST API directly.")
    System(agent, "AI agent", "Claude (or any MCP client). Stores facts and recalls them mid-conversation.")
    System(athena, "Athena", "Personal brain: memories (atomic facts) and notes (chunked documents) with hybrid keyword+vector search.")
    System_Ext(embedapi, "Embedding provider", "OpenAI-compatible API (OpenRouter, OpenAI, Ollama, ...). Optional — EMBEDDING_PROVIDER=none degrades to keyword-only search.")

    Rel(user, agent, "converses with")
    Rel(user, athena, "REST /v1", "HTTPS + bearer key")
    Rel(agent, athena, "remember / recall / notes tools", "MCP")
    Rel(athena, embedapi, "embeds content and queries", "HTTPS")
    UpdateLayoutConfig($c4ShapeInRow="2")
```

## Level 2 — Containers

One Go binary, one port (default `:8080`). The MCP server is not a separate
process — it is mounted at `/mcp` behind the same bearer-auth middleware as
the REST API (or served over stdio with the `--stdio` flag). Both are thin
adapters over the same `Brain` service.

```mermaid
C4Container
    title Containers — Athena (single Go binary, one port)
    System(agent, "AI agent", "MCP client")
    Person(user, "You", "REST client")

    System_Boundary(athena, "Athena binary (:8080)") {
        Container(router, "HTTP router", "chi", "Recoverer + request log. /healthz open; bearer auth (BRAIN_API_KEY) guards everything else.")
        Container(mcp, "MCP server", "go-sdk, Streamable HTTP at /mcp (or --stdio)", "10 tools: remember, recall, get_memory, forget, list_memories, create_note, get_note, update_note, delete_note, list_tags")
        Container(rest, "REST API", "Go, /v1/*", "CRUD for memories, notes, tags + /v1/search")
        Container(brain, "Brain service", "internal/service", "All shared logic: chunking, embedding orchestration, search fusion, validation")
        Container(retry, "Embed retry loop", "goroutine, 1-min tick", "Re-embeds pending/failed rows in batches of 32 with exponential backoff")
        ContainerDb(pg, "PostgreSQL + pgvector", "memories, notes, note_chunks, embedding_meta", "tsvector generated columns (GIN) + vector(1536) columns (HNSW cosine)")
    }
    System_Ext(embedapi, "Embedding provider", "OpenAI-compatible /embeddings")

    Rel(agent, router, "MCP tool calls", "Streamable HTTP /mcp")
    Rel(user, router, "JSON", "REST /v1")
    Rel(router, mcp, "routes")
    Rel(router, rest, "routes")
    Rel(mcp, brain, "calls")
    Rel(rest, brain, "calls")
    Rel(brain, pg, "SQL via internal/store", "pgx pool")
    Rel(brain, embedapi, "Embed(texts) → [][]float32", "10s timeout")
    Rel(retry, pg, "polls pending/failed rows")
    Rel(retry, embedapi, "re-embeds")
    UpdateLayoutConfig($c4ShapeInRow="3")
```

## Level 3 — Components (inside the binary)

Layering rule (from `CLAUDE.md`): handlers and tools are thin adapters, shared
logic lives in `internal/service`, SQL lives only in `internal/store`.

```mermaid
flowchart TD
    main["cmd/athena<br/><i>wires everything; runs goose migrations at startup</i>"]

    subgraph adapters [Adapters — no business logic]
        api["internal/api<br/><i>chi router, bearer auth, REST handlers</i>"]
        mcps["internal/mcpserver<br/><i>MCP tool registrations + arg schemas</i>"]
    end

    subgraph core [Core]
        brain["internal/service — Brain<br/><i>CreateMemory / CreateNote / Search /<br/>UpdateNote / RunEmbedRetryLoop</i>"]
        chunk["internal/chunk<br/><i>Split(): paragraphs packed to ~1200 chars,<br/>max 3000, 300-char overlap</i>"]
        embed["internal/embed — Embedder interface<br/><i>OpenAICompatible impl; FakeEmbedder in tests;<br/>nil ⇒ keyword-only mode</i>"]
        store["internal/store<br/><i>all SQL: memories, notes, chunks,<br/>search (RRF), pending embeds</i>"]
    end

    db["internal/db + migrations/<br/><i>pgx pool, goose embedded SQL,<br/>embedding_meta guard</i>"]
    pg[("PostgreSQL + pgvector")]
    ext["Embedding provider (external)"]

    main --> api & mcps
    api --> brain
    mcps --> brain
    brain --> chunk
    brain --> embed
    brain --> store
    main --> db
    store --> pg
    db --> pg
    embed --> ext
```

## Data model

```mermaid
erDiagram
    notes ||--o{ note_chunks : "1:N, ON DELETE CASCADE"
    memories {
        uuid id PK
        text content
        text_arr tags "GIN index"
        text source "optional origin"
        vector_1536 embedding "HNSW cosine index, NULLable"
        text embed_status "pending | ok | failed"
        tsvector search "GENERATED from content, GIN index"
        int embed_attempts "backoff counter"
    }
    notes {
        uuid id PK
        text title
        text content "full document, source of truth"
        text_arr tags "GIN index"
    }
    note_chunks {
        uuid id PK
        uuid note_id FK
        int idx "order within note, UNIQUE(note_id, idx)"
        text content "the chunk text that gets embedded"
        vector_1536 embedding "HNSW cosine index"
        text embed_status "pending | ok | failed"
        tsvector search "GENERATED from content, GIN index"
        int embed_attempts "backoff counter"
    }
```

Key asymmetry: **memories are indexed whole** (one row = one fact = one
embedding), while **notes are indexed by chunk** — the note row keeps the full
content for retrieval, but all searching happens against `note_chunks`.
A single-row `embedding_meta` table records provider/model/dimensions and is
checked at startup so vectors from a different model are never silently mixed.

## Flow: saving a memory (`remember`)

The core guarantee: **an embedding failure never fails a write.** The row is
persisted first; the embedding is best-effort with a background safety net.

```mermaid
sequenceDiagram
    autonumber
    participant A as AI agent
    participant M as MCP tool "remember"
    participant B as Brain
    participant S as Store
    participant P as Postgres
    participant E as Embedding API

    A->>M: remember(content, tags?, source?)
    M->>B: CreateMemory
    B->>S: INSERT INTO memories (embed_status='pending')
    Note over P: tsvector "search" column is GENERATED —<br/>keyword search works immediately, no embedding needed
    S-->>B: memory row (id, ...)
    B->>E: Embed([content]) — 10s timeout
    alt embedding succeeds
        E-->>B: vector(1536)
        B->>S: SetMemoryEmbedding(id, content, vec)
        Note over S,P: UPDATE ... WHERE id=$1 AND content=$3<br/>content-guarded: a stale vector is dropped if the row changed mid-flight
        B-->>M: memory (embed_status='ok')
    else embedding fails (or provider = none)
        B->>S: MarkMemoryEmbedFailed (attempts+1)
        B-->>M: memory (embed_status='failed') — write still succeeds
        Note over B: retry loop picks it up: every 1 min, batches of 32,<br/>backoff 4/8/16 min... capped at 1 h per row
    end
    M-->>A: "Stored memory id=<uuid>"
```

`update` of a memory re-embeds only when the content actually changed.

## Flow: saving a note (`create_note`)

Notes go through the chunker first; note + chunks are inserted in **one
transaction**, then all chunks are embedded in **one batched API call**.

```mermaid
sequenceDiagram
    autonumber
    participant A as AI agent
    participant M as MCP tool "create_note"
    participant B as Brain
    participant C as chunk.Split
    participant S as Store
    participant P as Postgres
    participant E as Embedding API

    A->>M: create_note(title, content, tags?)
    M->>B: CreateNote
    B->>C: Split(content)
    C-->>B: chunks — split on blank lines, packed to ~1200 chars<br/>(hard max 3000), last ≤300 chars carried into next chunk as overlap
    B->>S: CreateNote(title, content, tags, chunks)
    S->>P: BEGIN — INSERT note; INSERT note_chunks(idx 0..n, 'pending'); COMMIT
    S-->>B: note + ChunkRefs
    B->>E: Embed([all chunk texts]) — one batched call
    alt batch succeeds
        B->>S: SetChunkEmbedding per chunk (content-guarded)
        B-->>M: note (embed_status='ok')
    else batch fails
        B->>S: MarkChunkEmbedFailed for each chunk
        B-->>M: note (embed_status='failed') — note is saved regardless
        Note over B: retry loop re-embeds; if a batch keeps failing it falls back<br/>to per-item so one poison chunk can't starve the rest
    end
    M-->>A: "Created note id=<uuid>"
```

`update_note` with new content **deletes and re-creates all chunks** in the
same transaction, then re-embeds them. A note's `embed_status` is an aggregate
over its chunks: `failed` if any failed, else `pending` if any pending, else `ok`.

## Flow: search (`recall`)

Hybrid search = keyword leg + vector leg, fused with Reciprocal Rank Fusion
(RRF). Memories and note chunks are searched separately, then merged on a
shared score scale.

```mermaid
flowchart TD
    Q["recall(query, limit?, type?, tag?)<br/><i>MCP always uses hybrid; REST /v1/search also exposes mode=keyword|vector</i>"]
    EMB["Embed query → vector (10s timeout)"]
    DEG["Degrade to keyword-only<br/><i>hybrid mode never fails because the<br/>embedder is down or unconfigured</i>"]

    Q --> EMB
    EMB -- "embed fails / no provider" --> DEG

    subgraph MEM ["SearchMemories — whole rows"]
        MK["Keyword leg<br/>websearch_to_tsquery + ts_rank_cd<br/>over GIN tsvector — top 40"]
        MV["Vector leg<br/>embedding &lt;=&gt; query (cosine)<br/>over HNSW — top 40"]
        MF["RRF fuse: score = Σ 1/(60 + rank)<br/>FULL OUTER JOIN of both legs"]
        MK --> MF
        MV --> MF
    end

    subgraph NOT ["SearchNotes — chunk level"]
        NK["Keyword leg over note_chunks — top 40"]
        NV["Vector leg over note_chunks — top 40"]
        NF["RRF fuse per chunk"]
        ND["Dedupe: DISTINCT ON (note_id)<br/>keep best-scoring chunk as the snippet"]
        NK --> NF
        NV --> NF
        NF --> ND
    end

    EMB --> MK & MV & NK & NV
    DEG --> MK & NK

    MERGE["Merge memory + note results<br/>stable sort by score desc — comparable because single-leg<br/>modes use the same 1/(60+rank) scale — cap at limit (10 default, 50 max)"]
    RES["Ranked results: type, id, score, snippet,<br/>title + chunk_id for notes, tags"]

    MF --> MERGE
    ND --> MERGE
    MERGE --> RES
```

Details worth knowing:

- **Tag filter** (`tag=` / `AND $n = ANY(tags)`) applies inside both legs, so
  the candidate pool is filtered before ranking, not after.
- **Rows without embeddings still surface** via the keyword leg (`FULL OUTER
  JOIN` in the fusion), so a just-written memory whose embedding is still
  pending is findable immediately.
- **Pure vector mode** errors if no embedder is configured; **hybrid** silently
  degrades to keyword. Keyword mode never touches the embedding API.

## How things are referenced

Search results are pointers, not payloads:

1. `recall` returns ranked results with `type` (`memory`/`note`), `id`,
   `score`, a one-line `snippet` (memory content, or the best-matching chunk),
   and for notes the `title` and `chunk_id`.
2. The agent follows the reference: `get_note(note_id)` fetches the full
   original content (never the chunks — the note row is the source of truth)
   and `get_memory(memory_id)` fetches a memory's full content, tags, and
   source; `update_note(note_id)` / `forget(memory_id)` mutate by id.
3. Deleting a note cascades to its chunks (`ON DELETE CASCADE`); deleting a
   memory is a single-row delete.
4. Tags are the cross-cutting reference: `list_tags` aggregates counts across
   memories and notes so agents reuse vocabulary instead of inventing it, and
   any list/search call can filter by tag.
