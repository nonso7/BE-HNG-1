# Insighta Labs+ — Backend (Stage 3)

**Live:** https://be-hng-1-production.up.railway.app

A secure, multi-interface profile intelligence platform written in Go. Stage 3
adds GitHub OAuth (PKCE), role-based access control, refresh-token rotation,
API versioning, CSV export, rate limiting, and request logging on top of the
Stage 2 query/search/pagination engine.

The CLI and Web Portal are separate repositories that talk to this backend.

- **CLI repo:** _(link here)_
- **Web Portal repo:** _(link here)_
- **Live web portal:** _(link here)_

---

## Table of contents

1. [System architecture](#system-architecture)
2. [Authentication flow](#authentication-flow)
3. [CLI usage](#cli-usage)
4. [Token handling approach](#token-handling-approach)
5. [Role enforcement logic](#role-enforcement-logic)
6. [Natural-language parsing approach](#natural-language-parsing-approach)
7. [Endpoint reference](#endpoint-reference)
8. [Configuration](#configuration)
9. [Local development](#local-development)
10. [Deployment](#deployment)

---

## System architecture

```
                ┌──────────────┐    ┌──────────────┐
                │     CLI      │    │  Web Portal  │
                │  (separate)  │    │  (separate)  │
                └──────┬───────┘    └──────┬───────┘
                       │  Bearer token     │  HTTP-only cookies + CSRF
                       │                   │
                       └─────────┬─────────┘
                                 ▼
                ┌────────────────────────────────┐
                │          Backend (Go)          │
                │  ┌──────────────────────────┐  │
                │  │ requestLog → CORS → mux  │  │
                │  └────────────┬─────────────┘  │
                │               │                │
                │   /auth/*  →  authRateLimit ─► handlers
                │               (10/min per IP)            │
                │   /api/*   →  versionHeader              │
                │              → requireAuth               │
                │              → requireCSRF               │
                │              → apiRateLimit (60/min/user)│
                │              → requireAdminForMutation   │
                │              → handler                   │
                │                                          │
                │   ┌──────────────────────────────┐       │
                │   │   SQLite (modernc, pure Go)  │       │
                │   │   profiles, users,           │       │
                │   │   refresh_tokens             │       │
                │   └──────────────────────────────┘       │
                └──────────────────────────────────────────┘
                                 │
                                 ▼
                ┌──────────┬──────────┬──────────────┐
                │ GitHub   │ Genderize│ Agify        │
                │ OAuth    │ Agify    │ Nationalize  │
                └──────────┴──────────┴──────────────┘
```

**Key choices**

- **Pure-Go SQLite** (`modernc.org/sqlite`) keeps deployment trivial — no CGO,
  no external DB. Tables: `profiles`, `users`, `refresh_tokens`.
- **Stateless access tokens** (HS256 JWTs, 3-minute expiry) — no DB hit on the
  hot path. **Stateful refresh tokens** (opaque random, SHA-256 hashed in the
  DB, 5-minute expiry, single-use rotation) — so a leaked refresh can be
  revoked.
- **Middleware chains per route family**, not scattered handler-side checks.
  Auth/role/version/rate-limit are composable wrappers; handlers stay free of
  cross-cutting concerns.
- **Method-based RBAC**: `analyst` may only `GET`, `admin` may use any method.
  Single rule, single middleware, no per-endpoint permission table to drift.

---

## Authentication flow

GitHub OAuth with PKCE. Two callers (CLI and Web) share the same backend but
take slightly different paths because of how the redirect lands.

### Web portal flow

```
Browser                Backend                 GitHub
   │  GET /auth/github   │                       │
   ├────────────────────►│                       │
   │                     │  generates state +    │
   │                     │  code_verifier;       │
   │                     │  stores both;         │
   │                     │  derives challenge    │
   │  302 to GitHub      │                       │
   │◄────────────────────┤                       │
   │  authorize(challenge, state)                │
   ├────────────────────────────────────────────►│
   │                     │  302 callback?code+state
   │◄────────────────────────────────────────────┤
   │ GET /auth/github/callback?code=...&state=...│
   ├────────────────────►│                       │
   │                     │  pop verifier by state│
   │                     │  POST token exchange  │
   │                     │  with code+verifier   │
   │                     ├──────────────────────►│
   │                     │◄──── access_token ────┤
   │                     │  GET /user            │
   │                     ├──────────────────────►│
   │                     │◄──── user JSON ───────┤
   │                     │  upsert user          │
   │                     │  issue access+refresh │
   │  302 → WEB_APP_URL  │  Set-Cookie: HttpOnly │
   │◄────────────────────┤  access_token,        │
   │                     │  refresh_token,       │
   │                     │  csrf_token (readable)│
```

### CLI flow

```
CLI                  Browser            GitHub                 Backend
 │  generate state +    │                  │                      │
 │  code_verifier +     │                  │                      │
 │  challenge           │                  │                      │
 │  start localhost     │                  │                      │
 │  callback server     │                  │                      │
 │                      │  open authorize  │                      │
 │  ─────────────────► browser ───────────►│                      │
 │                      │                  │  user authenticates  │
 │                      │  302 callback ◄──┤                      │
 │  capture code+state  │                  │                      │
 │  validate state                                                │
 │  POST /auth/github/exchange (code, code_verifier, redirect_uri)│
 │  ─────────────────────────────────────────────────────────────►│
 │                                          backend exchanges     │
 │                                          with GitHub, fetches  │
 │                                          user, upserts,        │
 │                                          issues tokens         │
 │  ◄──── { access_token, refresh_token, user } ──────────────────│
 │  store tokens at ~/.insighta/credentials.json                  │
```

### Token refresh

`POST /auth/refresh` (body or cookie) → validates the refresh token, **revokes
it immediately**, issues a new access+refresh pair. Reusing a revoked or
expired refresh token returns `401`.

### Logout

`POST /auth/logout` revokes the refresh token server-side and clears cookies.

---

## CLI usage

The CLI lives in its own repo; this is the contract it uses.

```bash
# install (npm package or Go binary; see CLI repo)
insighta login           # opens browser, completes PKCE flow
insighta whoami          # GET /api/users/me
insighta logout

insighta profiles list
insighta profiles list --gender male --country NG --age-group adult
insighta profiles list --min-age 25 --max-age 40
insighta profiles list --sort-by age --order desc
insighta profiles list --page 2 --limit 20
insighta profiles get <id>
insighta profiles search "young males from nigeria"
insighta profiles create --name "Harriet Tubman"     # admin only
insighta profiles export --format csv --gender male --country NG
```

Credentials are stored at `~/.insighta/credentials.json`:

```json
{
  "access_token": "...",
  "refresh_token": "...",
  "expires_at": "2026-04-29T12:30:00Z"
}
```

The CLI sends `Authorization: Bearer <access_token>` and `X-API-Version: 1` on
every request. On `401 invalid_token` it tries `/auth/refresh` once; if that
also fails it prompts re-login.

---

## Token handling approach

### Access tokens (stateless)

- **Format:** standard HS256 JWT — `base64url(header).base64url(payload).base64url(sig)`.
- **Claims:** `sub` (user id), `username`, `role`, `iat`, `exp`.
- **Expiry:** **3 minutes** (per spec). Short by design — a stolen token is
  worthless almost immediately.
- **Verification:** signature + `exp` only. No DB hit. The user is then loaded
  by `sub` to enforce `is_active`.
- **Secret:** `TOKEN_SECRET` env var. If unset, an ephemeral one is generated
  at startup with a warning (tokens won't survive a restart in that case).

### Refresh tokens (stateful, rotated)

- **Format:** 32 random bytes, hex-encoded (64 chars). Opaque to the holder.
- **Storage:** SHA-256 hash of the token in the `refresh_tokens` table — never
  the raw token. A DB compromise does not directly leak active refresh tokens.
- **Expiry:** **5 minutes** (per spec).
- **Rotation:** every successful `/auth/refresh` revokes the presented token
  and issues a brand-new pair. **One refresh token, one use.** A
  refresh-token-replay attack is detectable (the second use 401s).
- **Revocation:** `/auth/logout` sets `revoked_at`. `is_active=false` on the
  user blocks all future refresh attempts via the `requireAuth` path.

### Web vs. CLI delivery

- **Web:** tokens are set as HTTP-only cookies (access + refresh) plus a
  non-HttpOnly `csrf_token` cookie that the browser must echo as
  `X-CSRF-Token` on state-changing requests. SameSite=Lax. Secure when behind
  HTTPS.
- **CLI:** tokens are returned in the JSON body of `/auth/github/exchange` and
  the CLI persists them at `~/.insighta/credentials.json`. CSRF doesn't apply
  to bearer-token requests.

---

## Role enforcement logic

**One rule:** `analyst` may only issue `GET` requests under `/api/*`. Any
non-`GET` method (`POST`, `DELETE`, `PUT`, `PATCH`) requires `admin`.

This is enforced in a single middleware (`requireAdminForMutation`) chained
after `requireAuth`:

```go
if r.Method != "GET" && user.Role != "admin" {
    return 403 "Admin role required"
}
```

Rationale:

- One rule means no per-endpoint permission table that can drift.
- Maps cleanly to the spec: admin "can create and delete", analyst is
  "read-only".
- Future endpoints inherit the right behavior automatically: a new `POST`
  requires admin without any code change.

`/auth/*` endpoints (login, refresh, logout, callback) bypass role checks —
they are user-self operations that only require a valid identity (or none, for
login).

**Granting admin.** New users default to `analyst`. Set
`ADMIN_GITHUB_USERNAMES=user1,user2` and those usernames are auto-promoted on
first login. Existing users' roles are not modified by this env var (avoids
accidental privilege escalation on rename).

---

## Natural-language parsing approach

Rule-based, deterministic, regex-driven. **No LLM, no ML.** Lower-cases and
strips the input, then runs four independent scanners and AND-combines the
results.

| Scanner | Recognises |
| --- | --- |
| Gender | `male`, `males`, `men`, `boys`, `girls`, `females`, `women`, `ladies` (word-boundary; both families present → no filter) |
| Age group | `child`/`children`/`kid`, `teen`/`teenager`, `adult`, `senior`/`elder`/`elderly` (longest alias wins) |
| Age bounds | `above N`, `over N`, `older than N`, `under N`, `between N and M`, `N-M`, `N to M`, `aged N`, `young` (16-24), `old` (60+) |
| Country | aliases (`usa`, `uk`, `britain`, `ivory coast`, `drc`...) → full names from the seed (`south africa` beats `africa`) → bare ISO-2 (`NG`, `KE`) |

If no scanner matches anything, the endpoint returns `422 Unable to interpret
query`. If contradictory bounds are inferred, the query returns matched=false.

Worked examples:

| Query | Filters |
| --- | --- |
| `young males from nigeria` | gender=male, min_age=16, max_age=24, country_id=NG |
| `senior women in south africa` | gender=female, age_group=senior, country_id=ZA |
| `males between 25 and 40 from kenya` | gender=male, min_age=25, max_age=40, country_id=KE |
| `aged 42` | min_age=max_age=42 |

Limitations: no demonyms (`nigerians`), no number words (`twenty-five`), no
free-form negation, no fuzzy spelling, no relative dates.

---

## Endpoint reference

All `/api/*` endpoints require `X-API-Version: 1` and a valid access token
(`Authorization: Bearer <token>` or `access_token` cookie).

### Auth

| Method | Path | Description |
| --- | --- | --- |
| `GET` | `/auth/github` | Redirects to GitHub OAuth (web flow) |
| `GET` | `/auth/github/callback` | OAuth callback (web flow); sets cookies, redirects to `WEB_APP_URL` |
| `POST` | `/auth/github/exchange` | CLI flow: `{code, code_verifier, redirect_uri}` → `{access_token, refresh_token, user}` |
| `POST` | `/auth/refresh` | Rotate `{refresh_token}` → new pair |
| `POST` | `/auth/logout` | Revoke refresh token + clear cookies |
| `GET` | `/auth/csrf` | Issue a CSRF cookie for the web portal |

### Profiles

| Method | Path | Role | Description |
| --- | --- | --- | --- |
| `GET` | `/api/profiles` | analyst+ | Filter, sort, paginate (Stage 2 unchanged + new pagination shape) |
| `GET` | `/api/profiles/{id}` | analyst+ | Single profile by id |
| `GET` | `/api/profiles/search?q=...` | analyst+ | NL search |
| `GET` | `/api/profiles/export?format=csv` | analyst+ | CSV export, applies same filters as list |
| `POST` | `/api/profiles` | admin | Create — calls Genderize/Agify/Nationalize, persists, returns the row |
| `DELETE` | `/api/profiles/{id}` | admin | Delete by id |

### Users

| Method | Path | Description |
| --- | --- | --- |
| `GET` | `/api/users/me` | The authenticated user |

### Pagination shape (list + search)

```json
{
  "status": "success",
  "page": 1,
  "limit": 10,
  "total": 2026,
  "total_pages": 203,
  "links": {
    "self": "/api/profiles?limit=10&page=1",
    "next": "/api/profiles?limit=10&page=2",
    "prev": null
  },
  "data": [ ... ]
}
```

### Error envelope

```json
{ "status": "error", "message": "<human readable>" }
```

| Status | When |
| --- | --- |
| 400 | Missing `X-API-Version`, malformed body, missing required field |
| 401 | Missing/invalid access or refresh token |
| 403 | Non-admin attempting a mutation; CSRF mismatch; account disabled |
| 404 | Unknown profile/user |
| 422 | Bad query parameter, NL query couldn't be parsed |
| 429 | Rate limit exceeded |
| 502 | Upstream API (Genderize/Agify/Nationalize/GitHub) returned bad data |

### Rate limits

| Scope | Limit |
| --- | --- |
| `/auth/*` | 10 requests / minute / IP |
| `/api/*` | 60 requests / minute / authenticated user |

Sliding window. Returns `429` with `Retry-After: 60`.

---

## Configuration

| Env var | Required | Default | Notes |
| --- | --- | --- | --- |
| `PORT` | no | `8080` | HTTP port |
| `DB_PATH` | no | `profiles.db` | SQLite file path |
| `TOKEN_SECRET` | yes (prod) | random ephemeral | HMAC secret for access tokens |
| `GITHUB_CLIENT_ID` | yes (web) | — | **Web** OAuth App client id (used by `/auth/github` + web callback) |
| `GITHUB_CLIENT_SECRET` | yes (web) | — | **Web** OAuth App client secret |
| `GITHUB_REDIRECT_URI` | yes (web) | derived | e.g. `https://your-portal/auth/github/callback` |
| `GITHUB_CLI_CLIENT_ID` | yes (CLI) | — | **CLI** OAuth App client id (used by `/auth/github/exchange` + `/auth/cli/config`); the CLI's redirect URI is fixed at `http://127.0.0.1:9876/callback` |
| `GITHUB_CLI_CLIENT_SECRET` | yes (CLI) | — | **CLI** OAuth App client secret |
| `WEB_APP_URL` | no | empty | Where to redirect after web login; if empty, the callback returns JSON |
| `ADMIN_GITHUB_USERNAMES` | no | empty | Comma-separated list of GitHub usernames that get admin on first login |
| `GRADER_TOKEN` | no | empty | If set, exposes `POST /auth/test/issue` for the grader bot to obtain admin/analyst tokens without the OAuth dance |

---

## Local development

```bash
# Run with defaults
go run .

# Realistic local config
PORT=8080 \
DB_PATH=./profiles.db \
TOKEN_SECRET=$(openssl rand -hex 32) \
GITHUB_CLIENT_ID=Iv1.xxxxx \
GITHUB_CLIENT_SECRET=xxxxx \
GITHUB_REDIRECT_URI=http://localhost:8080/auth/github/callback \
WEB_APP_URL=http://localhost:3000 \
ADMIN_GITHUB_USERNAMES=nonso7 \
go run .
```

The seeder runs at startup. The 2,026 profiles are inserted in a single
transaction with `INSERT OR IGNORE` keyed on `name`, so re-runs are a no-op.

### Smoke test against a live build

```bash
# Issue an admin token (requires GRADER_TOKEN)
curl -sS -H "X-Grader-Token: $GRADER_TOKEN" \
     -H 'Content-Type: application/json' \
     -d '{"username":"adminbot","role":"admin"}' \
     http://localhost:8080/auth/test/issue

# Use it
TOK="<access_token from above>"
curl -sS -H "Authorization: Bearer $TOK" -H 'X-API-Version: 1' \
     http://localhost:8080/api/profiles?limit=2
```

---

## Deployment

A `Dockerfile` and `railway.json` are included. SQLite lives at
`/data/profiles.db` in the container — mount a volume there to persist data
between deploys.

```bash
docker build -t insighta-api .
docker run --rm -p 8080:8080 -v $PWD/data:/data \
  -e TOKEN_SECRET=$(openssl rand -hex 32) \
  -e GITHUB_CLIENT_ID=... \
  -e GITHUB_CLIENT_SECRET=... \
  -e GITHUB_REDIRECT_URI=http://localhost:8080/auth/github/callback \
  insighta-api
```

CI runs on every PR to `main` (`.github/workflows/ci.yml`): `go vet`, `go
build`, `go test ./...`.

---

## Project layout

```
main.go            bootstrap, env wiring, graceful shutdown
handlers.go        Routes(), profile + user handlers, query parsing
auth.go            OAuth handlers, refresh, logout, exchange, cookies
middleware.go      requireAuth, requireAdminForMutation, requireVersionHeader,
                   requireCSRF, rate limiters, request log
tokens.go          HS256 access tokens, refresh-token primitives
pkce.go            in-memory state↔verifier store with TTL
users.go           User model, GitHub upsert, refresh-token storage
grader.go          GRADER_TOKEN-gated test-issue endpoint
export.go          CSV export
pagination.go      total_pages + self/next/prev links
store.go           SQLite schema + filter/sort/paginate SQL
search.go          rule-based NL parser
seed.go            embedded JSON + idempotent seeder
classify.go        age-group classifier
external.go        Genderize / Agify / Nationalize clients
models.go          response envelopes + UTCTime
profiles_seed.json 2,026 seeded profiles (embedded via go:embed)
```
