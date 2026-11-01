-- 0001_init.sql — initial schema for the Palabra group translator control plane.
-- Apply with:  psql "$DATABASE_URL" -f migrations/0001_init.sql
--
-- Retention: metadata rows are operational data. rooms/participants are safe to
-- prune after their expires_at / left_at + retention window (e.g. 30 days).
-- No audio or transcript content is ever stored here.

CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE IF NOT EXISTS rooms (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code          VARCHAR(8)  NOT NULL UNIQUE,
    name          VARCHAR(120) NOT NULL DEFAULT '',
    owner_id      VARCHAR(64) NOT NULL DEFAULT '',
    capacity      INT         NOT NULL DEFAULT 8 CHECK (capacity BETWEEN 2 AND 64),
    status        VARCHAR(16) NOT NULL DEFAULT 'active',  -- active | ended
    translation_mode VARCHAR(16) NOT NULL DEFAULT 'personal',
    livekit_room  VARCHAR(128) NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at    TIMESTAMPTZ NOT NULL DEFAULT now() + INTERVAL '4 hours',
    ended_at      TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS participants (
    id                VARCHAR(64) PRIMARY KEY,           -- server-issued ID
    room_id           UUID        NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
    display_name      VARCHAR(80) NOT NULL DEFAULT '',
    source_lang       VARCHAR(12) NOT NULL DEFAULT '',  -- '' = auto-detect (if supported)
    listen_lang       VARCHAR(12) NOT NULL,
    livekit_identity  VARCHAR(128) NOT NULL DEFAULT '',
    mic_enabled       BOOLEAN     NOT NULL DEFAULT TRUE,
    connection_state  VARCHAR(16) NOT NULL DEFAULT 'connected',
    joined_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    left_at           TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_participants_room ON participants(room_id) WHERE left_at IS NULL;

CREATE TABLE IF NOT EXISTS usage (
    id                 BIGSERIAL PRIMARY KEY,
    room_id            UUID        NOT NULL,
    participant_id     VARCHAR(64) NOT NULL DEFAULT '',
    session_id         VARCHAR(64) NOT NULL DEFAULT '',   -- palabra session id
    source_lang        VARCHAR(12) NOT NULL DEFAULT '',
    target_lang        VARCHAR(12) NOT NULL DEFAULT '',
    session_seconds    DOUBLE PRECISION NOT NULL DEFAULT 0,
    translated_minutes DOUBLE PRECISION NOT NULL DEFAULT 0,
    ts                 TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_usage_room_ts ON usage(room_id, ts DESC);
