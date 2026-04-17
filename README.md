# HNG Stage 1 — Profile Enrichment API (Go)

**Live**: be-hng-1-production.up.railway.app

A small Go service that accepts a name, enriches it using three free public
APIs (Genderize, Agify, Nationalize), classifies the result, and persists it
in SQLite. Duplicates are de-duplicated by name (idempotent creates).

## Tech

- Go 1.22+ (`net/http` with method+path routing)
- SQLite via `modernc.org/sqlite` (pure Go, no CGO)
- UUID v7 via `github.com/google/uuid`

## Run locally

```bash
go run .                     # listens on :8080
PORT=3000 go run .           # custom port
DB_PATH=./data.db go run .   # custom SQLite file
```

## Endpoints

### `POST /api/profiles`
Create (or return existing) profile.

```bash
curl -X POST http://localhost:8080/api/profiles \
  -H 'Content-Type: application/json' \
  -d '{"name":"ella"}'
```

Response (`201 Created` for new, `200 OK` for duplicate):

```json
{
  "status": "success",
  "data": {
    "id": "019d9b56-ee82-7a20-8b8c-0e9c996b1ba3",
    "name": "ella",
    "gender": "female",
    "gender_probability": 0.99,
    "sample_size": 97517,
    "age": 53,
    "age_group": "adult",
    "country_id": "CM",
    "country_probability": 0.0967,
    "created_at": "2026-04-17T12:07:38.882Z"
  }
}
```

### `GET /api/profiles/{id}`
Fetch one profile by UUID.

### `GET /api/profiles`
List all profiles. Optional filters (case-insensitive):

- `gender` — e.g. `male`, `female`
- `country_id` — e.g. `NG`, `US`
- `age_group` — `child`, `teenager`, `adult`, `senior`

```bash
curl 'http://localhost:8080/api/profiles?gender=male&country_id=NG'
```

### `DELETE /api/profiles/{id}`
Deletes a profile. Returns `204 No Content` on success.

## Classification

- Age group: `0–12` child, `13–19` teenager, `20–59` adult, `60+` senior
- Nationality: the country with the highest `probability` in the Nationalize
  response wins

## Error responses

```json
{ "status": "error", "message": "<reason>" }
```

| Status | When |
|--------|------|
| 400    | Missing or empty `name`, or malformed JSON |
| 422    | `name` has the wrong JSON type (e.g. number, array) |
| 404    | Unknown profile id |
| 502    | An upstream API returned an unusable response |
| 500    | Any other server failure |

For 502s the message is exactly `"<API> returned an invalid response"`
where `<API>` is one of `Genderize`, `Agify`, `Nationalize`. Nothing is
persisted in that case.

## CORS

Every response carries `Access-Control-Allow-Origin: *` and `OPTIONS`
preflights are handled.

## Deployment

A `Dockerfile` is provided. The container stores SQLite at `/data/profiles.db`
— mount a volume there on Railway/Fly/etc. to keep data between deploys.

```bash
docker build -t hng-stage1 .
docker run --rm -p 8080:8080 -v $PWD/data:/data hng-stage1
```

`railway.json` is included for Railway deployments.

## Project layout

```
main.go        server bootstrap, graceful shutdown
handlers.go    HTTP handlers + CORS middleware
store.go       SQLite schema, CRUD, filtering
external.go    Genderize / Agify / Nationalize clients
classify.go    age-group classifier
models.go      request/response types
```
