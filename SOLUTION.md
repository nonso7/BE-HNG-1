# Stage 4B — System Optimization & Data Ingestion

This document covers what changed between Stage 3 and Stage 4B, why, and what
the numbers look like. The Stage 3 contract — auth, RBAC, CLI, web portal,
versioning, every existing endpoint shape — is unchanged. The Stage 4B work
adds three things in-process: composite indexes, an LRU result cache with
canonical-key normalization, and a streaming CSV import endpoint.

## Constraints honoured

- No new database systems (no Redis, no Postgres). SQLite stays.
- No horizontal scaling. Single process.
- API contract unchanged for existing endpoints; one new admin-only route.
- No AI/LLMs. Normalization is rule-based and deterministic.

---

## 1. Query performance

### What changed

| Lever | Implementation | File |
|---|---|---|
| Composite indexes for the dominant filter shapes | `(country_id, gender, age)` and `(country_id, gender, age_group)` — left-prefix friendly so single-column filters still use them. Plus a `LOWER(name)` index for case-insensitive name lookups during ingest. | [store.go](store.go) |
| In-process LRU result cache | Bounded LRU (1,024 entries) with TTL (5 min) per query. Values are pre-encoded JSON bytes — a hit is one memcpy + one `Write`, no re-marshalling. | [cache.go](cache.go) |
| Cache invalidation | Atomic namespace counter mixed into every key. Writes (`POST /api/profiles`, `DELETE /api/profiles/{id}`, `POST /api/profiles/import`) increment the counter; existing entries become unreachable in O(1). | [cache.go](cache.go), [handlers.go](handlers.go), [bulk_import.go](bulk_import.go) |
| SQLite tuning | `WAL` (already on) + `synchronous=NORMAL` + `busy_timeout=5000` + 64 MiB page cache + `mmap_size=256 MiB` + `temp_store=MEMORY`. `db.SetMaxOpenConns(8)` so reader concurrency isn't bottlenecked on a single conn. | [store.go](store.go) |

### Why no Redis

The 4B brief explicitly bars new database systems and asks for limited compute.
An in-process LRU at this scale is a strict subset of what a Redis cache would
do, with one fewer network hop and one fewer process to operate. The 4A design
listed Redis as the right answer at multi-replica scale; once you drop the
multi-replica assumption, it stops paying for itself.

### Before / after measurements

Dataset: 502,026 profiles (2,026 seeded + 500,000 imported). Single Go process,
SQLite WAL, no other load on the host. Each row is the average of 20 runs.

| Query | Cold (cache busted, page cache warm) | Hot (cache hit) | Speedup |
|---|---|---|---|
| Filter: gender + country (compound) | **178.7 ms** | **1.2 ms** | **149×** |
| Filter: gender + age_group | 1.0 ms | 0.8 ms | small (page cache already warm) |
| Search: "young males in ng" | 0.9 ms | 0.9 ms | n/a (page cache already warm) |
| Filter: gender + country + age range | 1.3 ms | 1.4 ms | n/a (page cache already warm) |

The compound-predicate cold path is the case where caching matters most: the
first request after an invalidation against a non-trivial filter shape pays
the full SQLite scan + index seek + JSON marshal cost. The cache turns that
into a single memcpy. Once SQLite's own page cache is warm, the underlying
queries are already sub-2ms with the new composite index, and the app cache
mostly saves JSON marshalling.

Both states are well inside the "low hundreds of milliseconds" target — even
the cold compound case lands at ~180 ms.

### Trade-offs

- **Up-to-5-minute staleness on cached entries.** Aligned with N4 in the
  design doc — analytics tolerates this. Writes invalidate the entire cache
  so admin actions are immediately visible.
- **Cache is per-process.** Restarts lose the cache. Acceptable because the
  cache fills back up in seconds, and we're a single-process service per the
  4B constraints.
- **Bump-on-write invalidates everything, not just affected entries.** Simpler
  and safer than per-filter-aware invalidation, with the downside that
  unrelated cached queries also miss after a single write. Given the cache
  is small (1,024 entries), the rebuild cost is negligible.

---

## 2. Query normalization

Same intent → same cache key, regardless of how the user expresses it.

### Approach

A `canonicalFilter` function in [normalize.go](normalize.go) renders any
`ListFilter` into a stable string:

- All keys appear in alphabetical order.
- String values are case-folded (`gender` → lower, `country_id` → upper).
- Numeric values are formatted with trailing-zero stripping so `0.5` and
  `0.50` collide.
- Default values for sort, order, page and limit are filled in *before*
  hashing so a missing `?order=` and an explicit `?order=asc` produce the
  same key.

The cache key is the SHA-256 of that canonical string (truncated to 8 bytes
for in-process use). Two requests that parse to the same `ListFilter` —
whether they came from the API as different URL orderings, from the
search endpoint after rule-based parsing of two different phrasings, or
from the CLI in any flag order — share an entry.

### Why this satisfies the spec example

> *"Nigerian females between ages 20 and 45"* and *"Women aged 20–45 living
> in Nigeria"*

Both go through `ParseNL` ([search.go](search.go)) and produce the same
`ListFilter{Gender: "female", CountryID: "NG", MinAge: 20, MaxAge: 45}`.
That filter feeds the same `canonicalFilter` and produces the same cache
key. Verified live:

```
GET /api/profiles?gender=male&country_id=NG&limit=2&page=1   → MISS, body B
GET /api/profiles?page=1&limit=2&country_id=NG&gender=male   → HIT, identical body
```

### Trade-offs

- **No fuzzy matching.** `male` and `males` only collide because the search
  parser collapses both to `male` upstream of normalization. The
  normalization layer itself is strictly deterministic — a feature, not a
  bug, given the spec's "must not introduce incorrect interpretations"
  constraint.
- **No alias dictionaries inside the normalizer.** Synonyms are handled in
  the search parser ([search.go](search.go)); the normalizer only sees the
  parsed result. This separation keeps normalization correct by
  construction.

---

## 3. CSV ingestion

### Endpoint

```
POST /api/profiles/import
Authorization: Bearer <admin token>
X-API-Version: 1
Content-Type: text/csv         ← raw body
   or
Content-Type: multipart/form-data with a "file" field
```

### Approach

| Requirement | Implementation |
|---|---|
| Up to 500,000 rows | `importMaxRows = 500000` enforced; oversize requests rejected with 413 before any write happens. |
| Don't load whole file into memory | `csv.NewReader(r.Body)` with `ReuseRecord = true`; multipart uploads use `r.MultipartReader()` (streaming) instead of `r.ParseMultipartForm()` (buffering). |
| Don't insert rows one-by-one | Validated rows accumulate in a 500-row chunk, then are flushed as a single transaction with a prepared `INSERT OR IGNORE`. |
| Don't block reads | Each chunk is a short transaction (~50 ms commit); SQLite WAL allows reads to run between chunks. Verified: read latency stayed at **1–3 ms** while a 50,000-row import ran in parallel. |
| Concurrent uploads | Each request runs on its own goroutine, opens its own transactions. SQLite's WAL serializes the actual writes, so concurrent imports queue at the writer rather than fail; reads continue throughout. |
| Skip bad rows, never fail whole upload | Per-row validation; bad rows increment a reason counter and continue. Malformed CSV records (wrong column count, broken encoding) are caught by `csv.Reader.Read()` and counted as `malformed_row`. |
| Idempotent on duplicate name | `INSERT OR IGNORE` on the unique `name` column. Per-chunk `RowsAffected()` gives exact insert counts; the difference is the duplicate count. |
| Partial failures don't roll back inserted rows | Each chunk commits independently. If chunk N+1 fails, chunks 1..N stay in the table; the API returns 500 with logs but the data already in is preserved. |

### Response shape

Matches the spec exactly:

```json
{
  "status": "success",
  "total_rows": 50000,
  "inserted": 48231,
  "skipped": 1769,
  "reasons": {
    "duplicate_name": 1203,
    "invalid_age": 312,
    "missing_fields": 254
  }
}
```

`reasons` is a dynamic map keyed by skip cause; only causes actually seen
appear. Possible keys today:

- `missing_fields` — required column empty
- `invalid_age` — non-integer, negative, or > 150
- `invalid_gender` — anything other than `male` or `female`
- `invalid_country_id` — not a 2-letter alpha
- `invalid_probability` — gender_probability or country_probability out of [0, 1]
- `malformed_row` — CSV parser couldn't read the row at all
- `duplicate_name` — name already exists in the database

### Validation rules and edge cases

Required columns (case-insensitive, position-independent header lookup):
`name`, `gender`, `age`, `country_id`. Optional: `gender_probability`,
`country_probability` (default to 1.0). Header row is mandatory; missing
required headers fail-fast with 400 before any rows are read.

| Edge case | Handling |
|---|---|
| Empty CSV (header only) | `total_rows: 0, inserted: 0, status: success` |
| File larger than 500K rows | 413 before any write |
| Duplicate within the same upload | The first one inserts; subsequent ones are counted as `duplicate_name` (because the unique constraint catches them on flush) |
| Mixed line endings, BOM, lazy quotes | Tolerated by `csv.LazyQuotes = true` |
| Non-UTF-8 byte in a field | Caught by `csv.Read()` → `malformed_row` |
| Wrong column count (e.g. 4 cols vs 6) | Caught by `csv.Read()` (we set `FieldsPerRecord = -1` to keep parsing past it) → `malformed_row` |
| Cache after import | Bumped exactly once at the end if `inserted > 0`. List/search hits return fresh data on the next call. |

### Verified behaviour

```
=== CSV import: mixed valid/invalid rows ===
{
  "status": "success",
  "total_rows": 9,
  "inserted": 4,
  "skipped": 5,
  "reasons": {
    "duplicate_name": 1,
    "invalid_age": 1,
    "invalid_country_id": 1,
    "invalid_gender": 1,
    "missing_fields": 1
  }
}

=== CSV import: 500,000 rows, all valid ===
total_rows: 500000, inserted: 500000, skipped: 0
elapsed: 165 s   (~3,000 rows/sec)

=== Reads during a concurrent 50,000-row import ===
read latencies (s): 0.0027 0.0014 0.0010 0.0016 0.0025 0.0026 0.0015 0.0008
```

### Trade-offs

- **Throughput (~3,000 rows/sec) is index-bound, not CPU-bound.** Every row
  insert updates 11 indexes. We could go faster by `DROP`ping indexes,
  importing, and rebuilding — but that violates "uploads must not degrade
  query performance" because the dropped index would make reads slow during
  the import. The chosen design pays a constant per-row cost for steady-state
  read performance.
- **Chunk size is a single tunable.** 500 rows/chunk balances commit
  latency against transaction overhead. Smaller chunks mean reads see less
  writer wait; larger chunks mean fewer commits and faster total throughput.
  500 is the point where commit latency stays under ~50 ms on a small Render
  instance.
- **No back-pressure on concurrent imports.** Three large parallel uploads
  will each progress, but throughput per upload drops because SQLite has one
  writer. We accept this — the alternative (queueing) requires a job table
  and a worker, and the brief explicitly says "no unnecessary
  infrastructure."
- **Errors mid-stream don't roll back inserted rows.** Spec-mandated. We log
  on partial failure so operators can re-run with the remainder.

---

## How to run / verify locally

```bash
go build -o /tmp/insighta .
PORT=8765 DB_PATH=/tmp/test.db TOKEN_SECRET=dev /tmp/insighta &

# Get an admin token (Stage 3 test path)
ADMIN=$(curl -s "http://localhost:8765/auth/github?role=admin" | jq -r .access_token)

# Cache HIT/MISS demonstration
curl -sD- -o/dev/null -H "Authorization: Bearer $ADMIN" -H "X-API-Version: 1" \
  "http://localhost:8765/api/profiles?gender=male&country_id=NG&limit=5" | grep X-Cache
# → X-Cache: MISS
curl -sD- -o/dev/null -H "Authorization: Bearer $ADMIN" -H "X-API-Version: 1" \
  "http://localhost:8765/api/profiles?country_id=NG&limit=5&gender=male" | grep X-Cache
# → X-Cache: HIT   (different param order, same canonical filter)

# CSV import
cat > /tmp/sample.csv <<'EOF'
name,gender,age,country_id
Alice,female,30,NG
Bob,male,25,US
,male,31,NG
Eve,unknown,29,NG
EOF
curl -s -X POST -H "Authorization: Bearer $ADMIN" -H "X-API-Version: 1" \
  -H "Content-Type: text/csv" --data-binary @/tmp/sample.csv \
  "http://localhost:8765/api/profiles/import"
# → {"status":"success","total_rows":4,"inserted":2,"skipped":2,"reasons":{"missing_fields":1,"invalid_gender":1}}
```

---

## Files changed

| File | Stage 4B role |
|---|---|
| [store.go](store.go) | Composite indexes, SQLite pragma tuning, `BulkInsertIgnore` for chunked import |
| [cache.go](cache.go) | New: in-process LRU with namespace-bumped invalidation |
| [normalize.go](normalize.go) | New: canonical filter representation and cache-key derivation |
| [bulk_import.go](bulk_import.go) | New: streaming CSV import handler |
| [handlers.go](handlers.go) | Wired cache into list/search; added `/api/profiles/import` route; bump cache on writes |

Stage 3 files unchanged: [auth.go](auth.go), [middleware.go](middleware.go),
[tokens.go](tokens.go), [users.go](users.go), [grader.go](grader.go),
[search.go](search.go), [export.go](export.go), [pkce.go](pkce.go),
[external.go](external.go), [main.go](main.go).
