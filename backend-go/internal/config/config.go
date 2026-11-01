// Package config loads service configuration from environment variables with
// sane development defaults and a production validation checklist.
//
// Configuration is intentionally dependency-light: it only imports the palabra
// package to embed the configurable S2S message-type names so that operators
// can remap protocol strings without a code change.
package config

import (
        "errors"
        "fmt"
        "net/url"
        "os"
        "strconv"
        "strings"
        "time"

        "github.com/palabra/palabra-group-translator/internal/palabra"
)

// devTokenSecret is used only when APP_ENV != "production". Production requires
// PARTICIPANT_TOKEN_SECRET to be set explicitly.
const devTokenSecret = "dev-only-insecure-secret-change-me"

// Config carries every runtime knob for the api, worker and scheduler
// binaries. All fields are populated by Load.
type Config struct {
        // Env selects relaxed ("dev") vs strict ("production") validation.
        Env string

        // HTTP API.
        HTTPAddr      string
        PublicBaseURL string

        // Persistence.
        DatabaseURL   string
        RedisAddr     string
        RedisPassword string
        RedisTLS      bool

        // CORS: comma-separated list of browser origins allowed to call the
        // API (e.g. "https://voicebridge.vercel.app,https://voicebridge.app").
        // "*" allows every origin (dev only).
        CORSAllowedOrigins string

        // LiveKit transport (server-side credentials only).
        LiveKitHost      string
        LiveKitAPIKey    string
        LiveKitAPISecret string
        LiveKitTokenTTL  time.Duration

        // Palabra Speech-to-Speech credentials and endpoints. The client
        // secret never leaves the server processes.
        PalabraClientID     string
        PalabraClientSecret string
        PalabraWSEndpoint   string
        PalabraAuthEndpoint string
        PalabraSessionLimit int
        PalabraMsgTypes     palabra.MsgTypes

        // Dev-only helpers for running against the fake Palabra server.
        PalabraFakeAddr string // when non-empty, worker starts an in-process fake
        PalabraSkipAuth bool   // skip the auth exchange (fake server only)

        // Rooms & participants.
        RoomTTL                time.Duration
        MaxRoomSize            int
        ParticipantTokenSecret string
        ParticipantTokenTTL    time.Duration
        RateLimitRPS           float64
        RateLimitBurst         int

        // Worker identity / lease TTL.
        WorkerID           string
        WorkerHeartbeatTTL time.Duration

        // Extra language codes appended to the built-in allowlist (CSV).
        ExtraLanguages string

        // Observability.
        LogLevel  string
        LogFormat string
}

// Load reads configuration from the process environment, applying defaults for
// anything unset. It never fails; use Validate for production readiness checks.
func Load() Config {
        c := Config{
                Env:            getEnv("APP_ENV", "dev"),
                HTTPAddr:       getEnv("HTTP_ADDR", ":8080"),
                PublicBaseURL:  getEnv("PUBLIC_BASE_URL", ""),
                DatabaseURL:    getEnv("DATABASE_URL", ""),
                RedisAddr:      getEnv("REDIS_ADDR", "localhost:6379"),
                RedisPassword:  getEnv("REDIS_PASSWORD", ""),

                CORSAllowedOrigins: getEnv("CORS_ALLOWED_ORIGINS", "*"),
                LiveKitHost:    getEnv("LIVEKIT_HOST", ""),
                LiveKitAPIKey:  getEnv("LIVEKIT_API_KEY", ""),
                LiveKitAPISecret: getEnv("LIVEKIT_API_SECRET", ""),
                LiveKitTokenTTL:  getEnvDuration("LIVEKIT_TOKEN_TTL", 12*time.Hour),

                PalabraClientID:     getEnv("PALABRA_CLIENT_ID", ""),
                PalabraClientSecret: getEnv("PALABRA_CLIENT_SECRET", ""),
                PalabraWSEndpoint:   getEnv("PALABRA_WS_ENDPOINT", "wss://wss.palabra.ai/ws/"),
                PalabraAuthEndpoint: getEnv("PALABRA_AUTH_ENDPOINT", "https://auth.palabra.ai/access-token"),
                PalabraSessionLimit: getEnvInt("PALABRA_SESSION_LIMIT", 64),
                PalabraMsgTypes:     loadMsgTypes(),
                PalabraFakeAddr:     getEnv("PALABRA_FAKE_ADDR", ""),
                PalabraSkipAuth:     getEnvBool("PALABRA_SKIP_AUTH", false),

                RoomTTL:                getEnvDuration("ROOM_TTL", 4*time.Hour),
                MaxRoomSize:            getEnvInt("MAX_ROOM_SIZE", 8),
                ParticipantTokenSecret: getEnv("PARTICIPANT_TOKEN_SECRET", devTokenSecret),
                ParticipantTokenTTL:    getEnvDuration("PARTICIPANT_TOKEN_TTL", 12*time.Hour),
                RateLimitRPS:           getEnvFloat("RATE_LIMIT_RPS", 2.0),
                RateLimitBurst:         getEnvInt("RATE_LIMIT_BURST", 10),

                WorkerID:           getEnv("WORKER_ID", defaultWorkerID()),
                WorkerHeartbeatTTL: getEnvDuration("WORKER_HEARTBEAT_TTL", 30*time.Second),

                ExtraLanguages: getEnv("EXTRA_LANGUAGES", ""),

                LogLevel:  getEnv("LOG_LEVEL", "info"),
                LogFormat: getEnv("LOG_FORMAT", "console"),
        }

        // Render Key Value and most managed Redis providers expose REDIS_URL
        // (redis://[user:pass@]host:port/db or rediss:// for TLS). When set it
        // overrides REDIS_ADDR / REDIS_PASSWORD.
        if ru := getEnv("REDIS_URL", ""); ru != "" {
                if addr, pass, tls, err := parseRedisURL(ru); err == nil {
                        c.RedisAddr, c.RedisPassword, c.RedisTLS = addr, pass, tls
                }
        }
        return c
}

// parseRedisURL splits a redis:// or rediss:// URL into host:port, password
// and a TLS flag. A missing port falls back to 6379.
func parseRedisURL(raw string) (addr, password string, useTLS bool, err error) {
        u, err := url.Parse(raw)
        if err != nil {
                return "", "", false, fmt.Errorf("config: parse REDIS_URL: %w", err)
        }
        host := u.Hostname()
        if host == "" {
                return "", "", false, fmt.Errorf("config: REDIS_URL has no host")
        }
        if p := u.Port(); p != "" {
                addr = host + ":" + p
        } else {
                addr = host + ":6379"
        }
        if u.User != nil {
                password, _ = u.User.Password()
        }
        useTLS = strings.EqualFold(u.Scheme, "rediss")
        return addr, password, useTLS, nil
}

// IsProduction reports whether strict production validation applies.
func (c Config) IsProduction() bool {
        return strings.EqualFold(c.Env, "production")
}

// Validate returns an error listing every configuration problem that must be
// fixed before running in production. Dev environments are allowed to run with
// partial configuration (e.g. against the fake Palabra server).
func (c Config) Validate() error {
        var missing []string
        req := func(key, val string) {
                if strings.TrimSpace(val) == "" {
                        missing = append(missing, key)
                }
        }
        req("DATABASE_URL", c.DatabaseURL)
        req("LIVEKIT_HOST", c.LiveKitHost)
        req("LIVEKIT_API_KEY", c.LiveKitAPIKey)
        req("LIVEKIT_API_SECRET", c.LiveKitAPISecret)
        req("PALABRA_CLIENT_ID", c.PalabraClientID)
        req("PALABRA_CLIENT_SECRET", c.PalabraClientSecret)
        if c.ParticipantTokenSecret == "" || c.ParticipantTokenSecret == devTokenSecret {
                missing = append(missing, "PARTICIPANT_TOKEN_SECRET (must not be the dev default)")
        }
        if len(missing) == 0 {
                return nil
        }
        return fmt.Errorf("missing required configuration for production: %s", strings.Join(missing, ", "))
}

// loadMsgTypes builds the Palabra protocol message-type table from env
// overrides, falling back to the documented defaults. See internal/palabra
// protocol.go for why every name is configurable.
func loadMsgTypes() palabra.MsgTypes {
        mt := palabra.DefaultMsgTypes()
        override := func(dst *string, key string) {
                if v := os.Getenv(key); v != "" {
                        *dst = v
                }
        }
        override(&mt.ClientInit, "PALABRA_MSG_TYPE_CLIENT_INIT")
        override(&mt.ClientInputAudio, "PALABRA_MSG_TYPE_CLIENT_INPUT_AUDIO")
        override(&mt.ClientPing, "PALABRA_MSG_TYPE_CLIENT_PING")
        override(&mt.ServerSessionReady, "PALABRA_MSG_TYPE_SERVER_SESSION_READY")
        override(&mt.ServerOutputAudio, "PALABRA_MSG_TYPE_SERVER_OUTPUT_AUDIO")
        override(&mt.ServerStatus, "PALABRA_MSG_TYPE_SERVER_STATUS")
        override(&mt.ServerError, "PALABRA_MSG_TYPE_SERVER_ERROR")
        override(&mt.ServerUsage, "PALABRA_MSG_TYPE_SERVER_USAGE")
        return mt
}

func defaultWorkerID() string {
        host, err := os.Hostname()
        if err != nil || host == "" {
                return "worker-1"
        }
        return "worker-" + host
}

func getEnv(key, def string) string {
        if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
                return strings.TrimSpace(v)
        }
        return def
}

func getEnvInt(key string, def int) int {
        if v := os.Getenv(key); v != "" {
                if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
                        return n
                }
        }
        return def
}

func getEnvFloat(key string, def float64) float64 {
        if v := os.Getenv(key); v != "" {
                if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
                        return f
                }
        }
        return def
}

func getEnvBool(key string, def bool) bool {
        if v := os.Getenv(key); v != "" {
                if b, err := strconv.ParseBool(strings.TrimSpace(v)); err == nil {
                        return b
                }
        }
        return def
}

func getEnvDuration(key string, def time.Duration) time.Duration {
        if v := os.Getenv(key); v != "" {
                if d, err := time.ParseDuration(strings.TrimSpace(v)); err == nil {
                        return d
                }
        }
        return def
}

// ErrValidation is returned by Validate when production configuration is
// incomplete. It exists so callers can errors.Is without string matching,
// though the wrapped message lists every missing variable.
var ErrValidation = errors.New("configuration validation failed")
