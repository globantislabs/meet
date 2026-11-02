# VoiceBridge — Multilingual Group Voice Translator

Browser-based group voice room: **everyone speaks their own language and hears
every other speaker in their own preferred language**, in real time.

```
A speaks English  →  B hears Tamil,   C hears Japanese, D hears Hindi
B speaks Tamil    →  A hears English, C hears Japanese, D hears Hindi
...
```

The product UI (frontend, docs, branding) is **VoiceBridge**. The speech
translation provider is used server-side only — its name never appears in the
product UI. Light theme, audio-first interface per the blueprint (§11).

| Layer | Tech |
| --- | --- |
| Frontend (repo root) | Next.js 16 · TypeScript · Tailwind · shadcn/ui · Zustand · livekit-client · Web Audio API |
| Backend (`backend-go/`) | Go · Chi · LiveKit Go SDK · S2S adapter · goroutine audio pipelines · Redis · PostgreSQL |
| Transport | LiveKit WebRTC (LiveKit Cloud) |
| Translation | Speech-to-Speech provider, server-side credentials only |
| Deploy | **Backend → Render** (Docker) · **Frontend → Vercel** |

## Repository layout

```
├── src/                        # Next.js frontend (light theme, VoiceBridge UI)
│   ├── app/page.tsx            # landing → create → join → room → ended
│   ├── components/room/        # forms, participant grid, controls, states
│   ├── lib/api.ts              # typed Go API client (contract in §10)
│   ├── lib/livekit-engine.ts   # real LiveKit connection + subscription rules
│   ├── lib/audio-engine.ts     # mic permission, device picker, real RMS meter
│   └── stores/session-store.ts # Zustand app state
├── backend-go/
│   ├── cmd/api|worker|scheduler/
│   ├── internal/
│   │   ├── httpapi/            # REST API + CORS + rate limit + auth
│   │   ├── rooms/ participants/  # control plane (PostgreSQL)
│   │   ├── routing/            # planner, registry, permissions (blueprint §5)
│   │   ├── palabra/            # S2S adapter (session, manager, fake server)
│   │   ├── worker/             # media plane: room runtime, source pumps,
│   │   │                       # translation pipelines, output tracks
│   │   ├── livekit/            # join tokens + roomservice client
│   │   ├── audio/              # PCM16 frames, resampler, bounded queues
│   │   ├── languages/          # allowlist (28 languages + EXTRA_LANGUAGES)
│   │   ├── auth/               # HMAC participant tokens
│   │   ├── store/              # Postgres + Redis helpers
│   │   └── observability/      # zerolog + Prometheus metrics
│   ├── migrations/0001_init.sql
│   └── deploy/                 # Dockerfiles + compose.yaml
├── render.yaml                 # Render blueprint (API + worker + scheduler + Postgres + Key Value)
└── .env.example                # → backend-go/.env.example
```

## How a conversation flows

1. **Create/Join** — browser calls the Go API, gets a participant token (HMAC)
   and a short-lived **LiveKit join token**.
2. **Media** — browser publishes its mic as track `mic_{participantId}` into the
   LiveKit room; every participant auto-subscribes **only** to allowed tracks.
3. **Worker** — a Go media worker (assigned by the scheduler) joins the room,
   decodes each mic (Opus→PCM, 48k→16k mono) and fans frames out to one
   **translation pipeline per unique (speaker, source→target) pair** — listeners
   sharing a target language share one stream (blueprint §5).
4. **Publish** — each pipeline publishes track `tr_{speaker}_{targetLang}` once
   its S2S session is **ready** (never before — blueprint §6.4) and the worker
   enforces publisher-side subscription permissions so **you never hear your own
   translation**.
5. **UI status** — the worker reports real route states (`starting/ready/error`)
   to Redis; the API exposes them; the browser polls the room snapshot every
   2.5 s and switches subscriptions only when routes are live.

---

## Deploy: backend on Render

### Option A — Blueprint (recommended)

1. Push this repository to GitHub.
2. In Render: **New → Blueprint**, select the repo, set **Root Directory =
   `backend-go`** (where `render.yaml` lives). `render.yaml` creates:
   - `vb-api` (web service, Docker, health check `/v1/health`)
   - `vb-worker` (background worker, Docker)
   - `vb-scheduler` (background worker, Docker)
   - `vb-postgres` (PostgreSQL 16) and `vb-cache` (Key Value / Redis)
3. Fill the `sync: false` env vars when prompted:

   | Variable | Where to get it |
   | --- | --- |
   | `LIVEKIT_HOST` | LiveKit Cloud project URL, `wss://<project>.livekit.cloud` |
   | `LIVEKIT_API_KEY` / `LIVEKIT_API_SECRET` | LiveKit Cloud → Settings → Keys |
   | `PALABRA_CLIENT_ID` / `PALABRA_CLIENT_SECRET` | Your S2S provider dashboard |
   | `CORS_ALLOWED_ORIGINS` | `https://<your-app>.vercel.app` (comma-separate for multiple) |
   | `PUBLIC_BASE_URL` | `https://<your-app>.vercel.app` (used for invite links) |
   | `PARTICIPANT_TOKEN_SECRET` (worker) | **Copy the auto-generated value from vb-api** so both match |

4. **Run the DB migration once**: Dashboard → `vb-postgres` → shell / psql:
   ```bash
   psql "$DATABASE_URL" -f backend-go/migrations/0001_init.sql
   ```
   (or run it from your machine with the connection string from Render).

### Option B — Manual services (no Blueprint)

Create the five resources by hand. Use the **same region** for all of them so
internal URLs work.

1. **Postgres** — New → Postgres → name `vb-postgres`, plan `basic-256mb`
   (or Starter). Copy the **Internal Database URL** from the Connect panel.
2. **Key Value** — New → Key Value → name `vb-cache`, plan `starter`,
   maxmemory policy `noeviction`, **internal connections only** (no public
   allow list). Copy the **Internal Connection String**.
3. **API** — New → Web Service → build from Git → repo, **Root Directory =
   `backend-go`**, Runtime `Docker`, Dockerfile `./deploy/Dockerfile.api`,
   health check `/v1/health`. Env vars:
   `APP_ENV=production`, `HTTP_ADDR=:8080`, `LOG_FORMAT=json`,
   `DATABASE_URL` (Postgres internal URL), `REDIS_URL` (Key Value string),
   `LIVEKIT_HOST` / `LIVEKIT_API_KEY` / `LIVEKIT_API_SECRET`,
   `PARTICIPANT_TOKEN_SECRET` (generate a long random value — worker and
   scheduler must reuse the **same** value),
   `CORS_ALLOWED_ORIGINS=https://<app>.vercel.app`,
   `PUBLIC_BASE_URL=https://<app>.vercel.app`,
   `MAX_ROOM_SIZE=8`, `RATE_LIMIT_RPS=2`, `RATE_LIMIT_BURST=10`.
4. **Media worker** — New → Background Worker → same root, Dockerfile
   `./deploy/Dockerfile.worker`, plan **Standard** (audio pipeline wants
   ≥ 1 vCPU / 2 GB). Same `DATABASE_URL` / `REDIS_URL` / `LIVEKIT_*` /
   `PARTICIPANT_TOKEN_SECRET`, plus `PALABRA_CLIENT_ID` /
   `PALABRA_CLIENT_SECRET`, `PALABRA_WS_ENDPOINT=wss://wss.palabra.ai/ws/`,
   `PALABRA_AUTH_ENDPOINT=https://auth.palabra.ai/access-token`,
   `PALABRA_SESSION_LIMIT=64`, `WORKER_ID=worker-render-1`,
   `MAX_ROOM_SIZE=8`.
5. **Scheduler** — New → Background Worker → same root, Dockerfile
   `./deploy/Dockerfile.scheduler`, plan `starter`. Needs `DATABASE_URL`,
   `REDIS_URL`, `WORKER_HEARTBEAT_TTL=30s` only.
6. **Migrate once** from your machine:
   ```bash
   psql "<postgres-external-url>" -f backend-go/migrations/0001_init.sql
   ```
7. After creation, every push to `main` auto-deploys; force one from the
   dashboard via service → **Manual Deploy → Deploy latest commit**.

### Verify the backend

```bash
curl https://vb-api.onrender.com/v1/health   # {"status":"ok"}
curl https://vb-api.onrender.com/v1/ready    # {"status":"ready"}  (DB+Redis pinged)
curl https://vb-api.onrender.com/v1/languages
curl https://vb-api.onrender.com/v1/metrics  # Prometheus
```

> **Protocol verification (do this once before go-live):** open
> `backend-go/internal/palabra/protocol.go` and check every message type /
> field name against your provider's CURRENT Speech-to-Speech docs. All wire
> strings are overridable via `PALABRA_MSG_TYPE_*` env vars — no code change
> needed for naming drift. Then run one real session and watch
> `vb-worker` logs + `palabra_session_init_seconds` / `first_translated_audio_seconds` metrics.

---

## Deploy: frontend on Vercel

1. Vercel → **Add New → Project** → import the repo (framework auto-detects Next.js).
2. Environment variable:
   - `NEXT_PUBLIC_API_URL = https://vb-api.onrender.com` (your Render API URL)
3. Deploy. Open the app → **Create a room** → invite link `https://<app>.vercel.app/?join=<CODE>`.
4. Back on Render, ensure `CORS_ALLOWED_ORIGINS` contains exactly this Vercel
   domain (plus any preview domains you use).

### End-to-end test (2 browsers / devices)

1. Browser A: create room (e.g. speaks English, hears Tamil).
2. Browser B: join with the code (speaks Tamil, hears English).
3. Both allow the microphone → header shows **Connected**.
4. Translation panel: A sees "B · Tamil → English · live" once the worker
   finishes session init (1–3 s typical).
5. Speak: each side hears the other in their own language; nobody hears their
   own translated voice.

---

## Local development

Backend (with the in-process **fake** translation engine — no provider creds):

```bash
cd backend-go
docker compose -f deploy/compose.yaml up --build
# API http://localhost:8080 · Postgres :5432 · Redis :6379
```

For a REAL translation locally, put credentials in `backend-go/.env`
(see `.env.example`) and remove the `PALABRA_WS_ENDPOINT=fake://` override.

Frontend:

```bash
npm install
echo 'NEXT_PUBLIC_API_URL=http://localhost:8080' > .env.local
npm run dev     # http://localhost:3000
```

---

## API (blueprint §10)

| Method | Endpoint | Auth | Purpose |
| --- | --- | --- | --- |
| POST | `/v1/rooms` | — | Create room (+ owner admission) |
| POST | `/v1/rooms/{id}/join` | — | Join with code |
| GET | `/v1/rooms/by-code/{code}` | — | Resolve invite code → room id |
| GET | `/v1/rooms/{id}` | Bearer | Room snapshot (participants, routes, my grants) |
| POST | `/v1/rooms/{id}/leave` | Bearer | Leave |
| PATCH | `/v1/rooms/{id}/me/preferences` | Bearer | Change listening/source language, mic |
| POST | `/v1/rooms/{id}/invite` | Bearer | Invite code + link |
| DELETE | `/v1/rooms/{id}` | Bearer (owner) | End room |
| GET | `/v1/languages` | — | Supported language allowlist |
| GET | `/v1/health` · `/v1/ready` · `/v1/metrics` | — | Ops |

## Track naming (single source of truth)

| Track | Name | Published by |
| --- | --- | --- |
| Original mic | `mic_{participantId}` | Browser |
| Translation | `tr_{speakerId}_{targetLang}` | Media worker (permissions enforced) |

## Security & privacy

- Provider credentials and LiveKit API secret are **server-side only**.
- Participants get short-lived HMAC tokens; LiveKit tokens are join-only with
  publish/subscribe grants.
- Translated tracks are restricted at the publisher (worker) to exactly the
  intended recipients — self-translation is impossible by construction.
- No audio or transcripts are ever persisted; only metering numbers land in
  the `usage` table.
- Rate limiting per IP; CORS restricted to your Vercel origin in production.

## Typography & design language

**Visual language — "Bridge Connect":** restrained cool neutrals and one
deep-teal brand accent (`#0E7568`) spent carefully — primary actions,
selected states and live indicators only. Semantic colors (success green,
warning amber, destructive red) report status and nothing else. The app is
light-first (`#F5F6F8` canvas, white cards, 10px radius, 1px hairlines) and
the live room is the product's one dark surface: a media-led meeting canvas
(`canvas-*` tokens, `#0F1216`) where chrome stays quiet so voices, routes
and captions lead. No gradients, no glow, no decorative color, no filler —
honest empty states, real status pills, data in mono. Canonical UI
primitives (`PageHeader`, `SectionCard`, `StatTile`, `EmptyState`,
`StatusPill`, `LiveDot`, `CopyButton`, `Reveal`) live in
`src/components/app/ui-bits.tsx`; all tokens live in `src/app/globals.css`.

Motion is part of the system, not decoration: a single expo-out easing
(`EASE = cubic-bezier(0.16, 1, 0.3, 1)`), Framer Motion entrances at 0.45s
(staggered scroll reveals, a living stage preview where speakers take turns
and a route ticker cycles live translation paths), Tailwind 150ms
transitions for interactive feedback, transform + opacity only, and
`prefers-reduced-motion` honored globally (JS motion falls back to a 0.2s
opacity-only fade).

| Role | Font |
| --- | --- |
| Body / all UI | **Space Grotesk** (`font-sans`) — the everyday voice |
| Editorial accents | **Instrument Serif** (`font-display`) — one moment per view |
| Data · codes · timers · labels | **Geist Mono** (`font-mono`) — room codes get `0.2em`+ tracking |

Self-hosted at build time via `next/font` (no runtime request to Google, no
layout shift). Change the pairing in `src/app/layout.tsx` and the theme
mapping in `src/app/globals.css`.

---

## ✅ What you need to go live (checklist)

**Accounts & services**

1. **LiveKit Cloud** account → create a project → copy
   `LIVEKIT_HOST` (`wss://<project>.livekit.cloud`) and one API key pair
   (`LIVEKIT_API_KEY`, `LIVEKIT_API_SECRET`). Free tier is enough to start.
2. **Speech-to-speech translation provider** (used server-side only; its name
   never appears in the UI) → copy `PALABRA_CLIENT_ID` and
   `PALABRA_CLIENT_SECRET`. Verify the wire protocol once against
   `backend-go/internal/palabra/protocol.go` before go-live.
3. **Render** account → deploy via Blueprint (`render.yaml`): it creates the
   API web service, media worker, scheduler, **PostgreSQL 16** and
   **Key Value (Redis)**.
4. **Vercel** account → import the repo → deploy the Next.js frontend.
5. **GitHub** repo to connect Render + Vercel to.

**Environment variables**

| Where | Variable | Notes |
| --- | --- | --- |
| Render `vb-api` + `vb-worker` + `vb-scheduler` | `LIVEKIT_HOST`, `LIVEKIT_API_KEY`, `LIVEKIT_API_SECRET` | from LiveKit Cloud |
| same | `PALABRA_CLIENT_ID`, `PALABRA_CLIENT_SECRET` | **never expose to the frontend** |
| same | `DATABASE_URL`, `REDIS_URL` | auto-injected by Render blueprint |
| `vb-api` (then copy to workers) | `PARTICIPANT_TOKEN_SECRET` | must match across api/worker |
| all Render services | `CORS_ALLOWED_ORIGINS` | your `https://<app>.vercel.app` |
| Vercel frontend | `NEXT_PUBLIC_API_URL` | your `https://vb-api.onrender.com` |

**One-time setup steps**

- [ ] Run `backend-go/migrations/0001_init.sql` against the Render Postgres.
- [ ] Copy `PARTICIPANT_TOKEN_SECRET` from `vb-api` into both workers.
- [ ] Set `CORS_ALLOWED_ORIGINS` to your exact Vercel domain.
- [ ] Health-check: `GET /v1/health` and `GET /v1/ready` return ok.
- [ ] Two-device end-to-end call test (see *End-to-end test* above).

**Costs (starting point)**

- LiveKit Cloud: free tier, then usage-based.
- Render: API + 2 workers + Postgres + Key Value ≈ $25–35/mo on starter
  plans (workers can run on the cheapest instance).
- Vercel: hobby free tier is fine for the frontend.
- S2S provider: per-minute audio pricing depends on the provider.
