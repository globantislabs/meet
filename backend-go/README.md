# Palabra Group Translator — Go Backend

Control plane (API) + media plane (worker) for a browser-based multilingual
group voice room: every participant hears every other speaker in their own
preferred language, using LiveKit WebRTC transport and Palabra
Speech-to-Speech translation.

```
Browser (Next.js, livekit-client)
   │  REST: create/join room, language prefs     │ WebRTC: mic ↑, translated audio ↓
   ▼                                             ▼
┌─────────────────────┐   Redis pub/sub   ┌──────────────────────────────┐
│  cmd/api (control)  │ ────────────────▶ │  cmd/worker (media)          │
│  rooms · tokens ·   │   plan_changed    │  LiveKit server participant  │
│  LiveKit tokens ·   │                   │  ├ source mic → sourcePump   │
│  admission · usage  │                   │  ├ route → Palabra S2S sess  │
└─────────┬───────────┘                   │  └ output track + permissions│
          │                               └──────────────────────────────┘
   PostgreSQL (rooms, participants, usage)          │
                                          Palabra S2S WebSocket (server-side creds only)
```

## Layout

| Path | Purpose |
| --- | --- |
| `cmd/api` | REST control plane (chi) — blueprint §10 endpoints |
| `cmd/worker` | Media worker — LiveKit participant, Palabra sessions, routing |
| `cmd/scheduler` | Optional room→worker assignment at scale (leases + health) |
| `internal/routing` | **Routing engine**: plan/dedup/diff + subscription permissions |
| `internal/palabra` | S2S WebSocket adapter, session manager, fake test server |
| `internal/livekit` | RoomService, token issuance, track subscribe/publish (Opus) |
| `internal/audio` | PCM16 primitives, streaming resampler, bounded queues |
| `internal/httpapi` | REST handlers, CORS (Vercel), rate limit, auth middleware |
| `internal/rooms` · `participants` | Postgres persistence + business rules |
| `internal/observability` | Zerolog + Prometheus metrics (T0..T6 latency stages) |
| `migrations/0001_init.sql` | Schema (rooms, participants, usage) |
| `deploy/` | Dockerfiles, compose, **render.yaml** (Render Blueprint) |

## Quick start (local)

Prereqs: **Go 1.22+** (CGO for the worker), **PostgreSQL**, **Redis**,
**LiveKit Cloud** project, **Palabra** S2S credentials.

```bash
cd backend-go
cp .env.example .env              # fill LIVEKIT_*, PALABRA_*
psql "$DATABASE_URL" -f migrations/0001_init.sql
go mod tidy
go test ./internal/routing/ -v    # routing engine tests (no creds needed)
go run ./cmd/api                  # terminal 1
CGO_ENABLED=1 go run ./cmd/worker # terminal 2
```

Or with Docker: `docker compose -f deploy/compose.yaml up --build`.

## Deploy on Render (recommended path)

1. Push this repo to GitHub.
2. Render → **New → Blueprint** → select the repo → Render reads
   `backend-go/deploy/render.yaml` and creates: `palabra-api` (Web Service),
   `palabra-worker` (Background Worker), Postgres, Key Value (Redis).
3. Set the `sync: false` env vars in the dashboard:
   `LIVEKIT_HOST`, `LIVEKIT_API_KEY`, `LIVEKIT_API_SECRET`,
   `PALABRA_CLIENT_ID`, `PALABRA_CLIENT_SECRET`, `CORS_ORIGINS`
   (your Vercel URL, e.g. `https://your-app.vercel.app`).
4. Apply the migration once: use the Render Postgres shell or psql with the
   internal connection string → run `migrations/0001_init.sql`.
5. Point the frontend at the API: on Vercel set
   `NEXT_PUBLIC_API_URL=https://palabra-api.onrender.com`.

## Deploy frontend on Vercel

The Next.js frontend lives in the repo root (see root `README.md`). Vercel
env vars: `NEXT_PUBLIC_API_URL` (Render API URL). The API already serves CORS
for the Vercel origin.

## VERIFY against provider docs before production

Everything provider-specific is isolated and configurable on purpose. Confirm
these before going live:

1. **Palabra S2S protocol** (`internal/palabra/protocol.go`): message type
   names (`init` / `input_audio` / `session_ready` / `output_audio` / `error`
   / `usage`), auth exchange shape (`internal/palabra/stream.go`), audio
   format (sample rate, PCM16 LE, chunk duration), session-ready ack, error
   taxonomy, max message size. Overrides exist via `PALABRA_MSG_TYPE_*` env
   vars — no code change needed for name drift.
2. **Palabra supported-language matrix** (`internal/languages/languages.go`):
   codes are validated against a built-in allowlist; extend via
   `EXTRA_LANGUAGES` after checking the current matrix.
3. **Multi-target capability**: this code assumes ONE session per
   source→target pair. If Palabra S2S supports multiple target outputs per
   session, merge routes per speaker in `internal/routing/planner.go`
   (`Plan.SessionCount` drops accordingly).
4. **LiveKit SDK APIs** (`internal/livekit/media.go`): the
   subscribe/decode/publish glue is isolated in one file; if the
   server-sdk-go version drifted, fix there. The worker needs
   `CGO_ENABLED=1` (Opus codec).
5. **Token grants** (`internal/livekit/tokens.go`): `CanPublishSources:
   ["microphone"]` restricts publishing to mics.

## Testing

```bash
go test ./... -race                     # unit tests (routing engine, palabra protocol)
# Dev against the FAKE palabra server (internal/palabra/fake/server.go):
# start it, then run the worker with PALABRA_FAKE_ADDR=127.0.0.1:<port>
# and PALABRA_SKIP_AUTH=true.
```

## Security posture (blueprint §12)

- Palabra credentials live only in api/worker env — never in the browser.
- LiveKit join tokens are short-lived, microphone-publish-only.
- Per-recipient subscription permissions are enforced twice: in the routing
  planner (`SubscriptionGrants`) and at publish time (worker excludes
  self-routes).
- Rate limiting on room create/join; room capacity + session budget
  admission control; no audio or credentials in logs.

## Ops

- `GET /v1/health` — liveness. `GET /v1/ready` — Postgres+Redis checks.
- `GET /v1/metrics` — Prometheus (guard with `METRICS_TOKEN` in prod).
- Key metrics: `palabra_translation_routes`, `palabra_active_palabra_sessions`,
  `palabra_first_translated_audio_seconds`, `palabra_end_to_end_seconds`,
  `palabra_queue_dropped_frames_total`, `palabra_stage_seconds` (T0..T6).
- Rooms auto-expire (`ROOM_TTL`); LiveKit rooms self-clean when empty.
