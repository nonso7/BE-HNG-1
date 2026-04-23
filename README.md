# Insighta Labs — Profile Query API (Go)

**Live**: https://be-hng-1-production.up.railway.app

A Go service that exposes a queryable store of 2,026 demographic profiles.
It supports combinable filtering, sorting, pagination, and a rule-based
natural-language search endpoint.

## Tech

- Go 1.22+ (`net/http` with method+path routing, generics)
- SQLite via `modernc.org/sqlite` (pure Go, no CGO)
- UUID v7 via `github.com/google/uuid`
- Seed data embedded into the binary via `//go:embed`

## Run locally

```bash
go run .                     # listens on :8080
PORT=3000 go run .           # custom port
DB_PATH=./data.db go run .   # custom SQLite file
```

The seeder runs automatically on startup. If the table already contains
2,026 rows it is skipped; otherwise the 2,026 profiles are inserted inside a
single transaction with `INSERT OR IGNORE`, keyed on the UNIQUE name column,
so re-running is idempotent.

## Schema

```
id                   TEXT PRIMARY KEY        -- UUID v7
name                 TEXT NOT NULL UNIQUE
gender               TEXT NOT NULL           -- "male" | "female"
gender_probability   REAL NOT NULL
age                  INTEGER NOT NULL
age_group            TEXT NOT NULL           -- child | teenager | adult | senior
country_id           TEXT NOT NULL           -- ISO 3166-1 alpha-2
country_name         TEXT NOT NULL
country_probability  REAL NOT NULL
created_at           TEXT NOT NULL           -- RFC 3339 UTC
```

Indexes cover every filter and sort column so no request ever does a
full-table scan.

## Endpoints

### `GET /api/profiles`

Combines filtering + sorting + pagination in one request. Every parameter is
optional; multiple filters AND together.

| Param                      | Type                             | Notes                                           |
| -------------------------- | -------------------------------- | ----------------------------------------------- |
| `gender`                   | `male` \| `female`               | Case-insensitive                                |
| `age_group`                | `child` \| `teenager` \| `adult` \| `senior` | Case-insensitive                    |
| `country_id`               | 2-letter ISO code                | Case-insensitive                                |
| `min_age` / `max_age`      | integer ≥ 0                      | Inclusive                                       |
| `min_gender_probability`   | float 0-1                        | Inclusive                                       |
| `min_country_probability`  | float 0-1                        | Inclusive                                       |
| `sort_by`                  | `age` \| `created_at` \| `gender_probability` | Defaults to `created_at`                 |
| `order`                    | `asc` \| `desc`                  | Defaults to `asc`                               |
| `page`                     | integer ≥ 1                      | Default 1                                       |
| `limit`                    | integer 1-50                     | Default 10; values above 50 are capped          |

Example:

```bash
curl 'http://localhost:8080/api/profiles?gender=male&country_id=NG&min_age=25&sort_by=age&order=desc&page=1&limit=10'
```

Response (`200 OK`):

```json
{
  "status": "success",
  "page": 1,
  "limit": 10,
  "total": 43,
  "data": [
    {
      "id": "b3f9c1e2-7d4a-4c91-9c2a-1f0a8e5b6d12",
      "name": "emmanuel",
      "gender": "male",
      "gender_probability": 0.99,
      "age": 34,
      "age_group": "adult",
      "country_id": "NG",
      "country_name": "Nigeria",
      "country_probability": 0.85,
      "created_at": "2026-04-01T12:00:00Z"
    }
  ]
}
```

### `GET /api/profiles/search?q=...`

Rule-based natural-language search. Pagination (`page`, `limit`) and sort
(`sort_by`, `order`) apply here too. See the next section for full parsing
behavior.

```bash
curl 'http://localhost:8080/api/profiles/search?q=young%20males%20from%20nigeria'
```

### `GET /api/profiles/{id}`, `DELETE /api/profiles/{id}`, `POST /api/profiles`

Preserved from stage 1. `POST` enriches a name via Genderize/Agify/Nationalize
and resolves `country_name` from a lookup derived from the seed.

## Natural language parsing

The parser is deterministic and built on compiled regex patterns. No LLM, no
ML. The input is lower-cased and stripped, then four scanners run
independently; the resulting filters are AND-ed together. If none of them
match anything, the endpoint returns `422` with
`{"status":"error","message":"Unable to interpret query"}`.

### 1. Gender

Word-boundary matches (so `male` will not match inside `female`):

| Keyword group                               | Filter          |
| ------------------------------------------- | --------------- |
| `male`, `males`, `men`, `boy`, `boys`, `guys`, `gentlemen` | `gender=male`   |
| `female`, `females`, `women`, `girl`, `girls`, `ladies`    | `gender=female` |

If **both** families appear (e.g. `"male and female teenagers"`), no gender
filter is emitted — the query is treated as "either gender".

### 2. Age group

Longest-alias-first match against the canonical groups:

| Keyword                                 | age_group   |
| --------------------------------------- | ----------- |
| `child`, `children`, `kid`, `kids`      | `child`     |
| `teenager`, `teenagers`, `teen`, `teens` | `teenager` |
| `adult`, `adults`                       | `adult`     |
| `senior`, `seniors`, `elder`, `elders`, `elderly` | `senior` |

### 3. Age bounds (inclusive, numeric)

| Pattern                                                    | Effect                 |
| ---------------------------------------------------------- | ---------------------- |
| `above N`, `over N`, `older than N`, `greater than N`, `at least N`, `>N`, `>=N` | `min_age = N` |
| `below N`, `under N`, `younger than N`, `less than N`, `at most N`, `<N`, `<=N`  | `max_age = N` |
| `between N and M`                                          | `min_age=N`, `max_age=M` |
| `N-M`, `N to M`                                            | `min_age=N`, `max_age=M` |
| `aged N`, `age N`                                          | `min_age = max_age = N`  |
| `young`                                                    | `min_age=16`, `max_age=24` (shorthand only — not a stored group) |
| `old`                                                      | `min_age=60` (when no other bound is set)                        |

Explicit numeric bounds always win over the `young` / `old` shorthands.

### 4. Country

Tried in order:

1. Common aliases: `usa`, `america`, `uk`, `britain`, `england`, `ivory coast`,
   `drc`, `congo kinshasa`, `congo brazzaville`, `sao tome`, `cape verde`,
   `swaziland`.
2. Full country names extracted from the seed file (longest match wins, so
   `south africa` beats `africa` and `central african republic` beats
   `african republic`).
3. Bare ISO-2 code (e.g. `NG`, `KE`) if it's present in the seed.

The successful phrase is emitted as `country_id`.

### Worked examples

| Query                                 | Resulting filters                                                 |
| ------------------------------------- | ----------------------------------------------------------------- |
| `young males`                         | `gender=male`, `min_age=16`, `max_age=24`                         |
| `females above 30`                    | `gender=female`, `min_age=30`                                     |
| `people from angola`                  | `country_id=AO`                                                   |
| `adult males from kenya`              | `gender=male`, `age_group=adult`, `country_id=KE`                 |
| `male and female teenagers above 17`  | `age_group=teenager`, `min_age=17`                                |
| `women in south africa`               | `gender=female`, `country_id=ZA`                                  |
| `seniors above 70`                    | `age_group=senior`, `min_age=70`                                  |
| `males between 25 and 40`             | `gender=male`, `min_age=25`, `max_age=40`                         |
| `aged 42`                             | `min_age=max_age=42`                                              |

## Parser limitations

Things the current rule-based parser deliberately does **not** handle:

- **Demonyms** — `nigerians`, `kenyan women`, `a french girl`. Only explicit
  country names / codes / aliases are resolved. Say `from kenya` instead.
- **OR semantics** — `males or females` or `nigerians or ghanaians` are not
  distinguished from AND-combinations. When conflicting filters appear
  (`from nigeria and from kenya`) only the longest match wins.
- **Number words** — `twenty-five`, `over fifty`. Only numeric digits are
  recognised.
- **Free-form negation** — `not male`, `non-senior`, `excluding children`.
  Anything preceded by a negation is still matched.
- **Fuzzy / misspelt tokens** — `nigera`, `teenagrs`. Matching is exact.
- **Relative dates / recency** — `recent signups`, `created this week`.
  `created_at` filtering is not exposed in the NL query.
- **Probability thresholds in prose** — `highly confident females`, `likely
  from angola`. Use the typed parameters (`min_gender_probability`,
  `min_country_probability`) instead.
- **Exact-age ranges without a verb** — `42-year-olds`, `45s`. Use `aged 42`
  or `42 to 44`.
- **Contradictions** — `aged 10 and over 30` resolves to the last-parsed
  bound; `between 40 and 20` is normalised (swapped). A fully nonsensical
  query that still matches something (e.g. `aged 60 under 20`) will return
  an empty result set rather than a validation error.
- **Country-less location phrases** — `from the capital`, `in europe`,
  `african people`. The parser only resolves countries, not regions or
  cities.
- **Language other than English.**

When a query contains *no* recognised token at all, the endpoint returns the
canonical error instead of a (misleading) empty success response:

```json
{ "status": "error", "message": "Unable to interpret query" }
```

## Error responses

```json
{ "status": "error", "message": "<reason>" }
```

| Status | When                                                       |
| ------ | ---------------------------------------------------------- |
| 400    | Missing or empty required parameter (e.g. `q`)             |
| 404    | Unknown profile id                                         |
| 422    | Invalid parameter type or value (`Invalid query parameters`); or an NL query that cannot be interpreted |
| 502    | An upstream API (POST enrichment only) returned bad data   |
| 500    | Any other server failure                                   |

## CORS

Every response carries `Access-Control-Allow-Origin: *`. `OPTIONS`
preflights are handled.

## Deployment

A `Dockerfile` is provided. The seed file is embedded into the binary so
there is nothing to upload at runtime. SQLite lives at `/data/profiles.db`
in the container — mount a volume there on Railway/Fly/etc. to keep data
between deploys.

```bash
docker build -t insighta-api .
docker run --rm -p 8080:8080 -v $PWD/data:/data insighta-api
```

`railway.json` is included for Railway deployments.

## Project layout

```
main.go       server bootstrap, graceful shutdown, seeder invocation
handlers.go   HTTP handlers, query validation, CORS middleware
store.go      SQLite schema, migration, filter/sort/paginate SQL
search.go     rule-based natural-language parser
seed.go       embedded JSON + idempotent seeder
classify.go   age-group classifier (POST endpoint)
external.go   Genderize / Agify / Nationalize clients (POST endpoint)
models.go     request/response types + UTCTime marshaller
profiles_seed.json  2,026 seeded profiles (embedded via go:embed)
```
