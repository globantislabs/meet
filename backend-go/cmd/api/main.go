// api: the control-plane HTTP service (blueprint §6.1).
//
// Responsibilities: room CRUD and invitations, participant admission, language
// preferences, LiveKit token issuance, health/metrics. It owns NO media path —
// Palabra credentials are not even loaded here.
package main

import (
        "context"
        "errors"
        "net/http"
        "os"
        "os/signal"
        "syscall"
        "time"

        "github.com/redis/go-redis/v9"
        "github.com/rs/zerolog"

        "github.com/palabra/palabra-group-translator/internal/auth"
        "github.com/palabra/palabra-group-translator/internal/config"
        "github.com/palabra/palabra-group-translator/internal/httpapi"
        "github.com/palabra/palabra-group-translator/internal/languages"
        lk "github.com/palabra/palabra-group-translator/internal/livekit"
        "github.com/palabra/palabra-group-translator/internal/observability"
        "github.com/palabra/palabra-group-translator/internal/participants"
        "github.com/palabra/palabra-group-translator/internal/rooms"
        "github.com/palabra/palabra-group-translator/internal/store"
)

func main() {
        cfg := config.Load()
        log := observability.NewLogger(cfg.LogLevel, cfg.LogFormat)

        if err := languages.ConfigureExtra(cfg.ExtraLanguages); err != nil {
                log.Warn().Err(err).Msg("extra languages parse failed")
        }

        if err := run(cfg, log); err != nil {
                log.Fatal().Err(err).Msg("api exited")
        }
}

func run(cfg config.Config, log zerolog.Logger) error {
        ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
        defer stop()

        if cfg.IsProduction() {
                if err := cfg.Validate(); err != nil {
                        return err
                }
                log.Info().Msg("configuration validated for production")
        }

        // Backing services.
        pool, err := store.NewPostgres(ctx, cfg.DatabaseURL)
        if err != nil {
                return err
        }
        defer pool.Close()

        rdb, err := store.NewRedis(ctx, cfg.RedisAddr, cfg.RedisPassword, cfg.RedisTLS)
        if err != nil {
                return err
        }
        defer rdb.Close()

        // Domain wiring.
        tokenizer := auth.NewTokenizer(cfg.ParticipantTokenSecret)
        roomStore := rooms.New(pool)
        partStore := participants.New(pool)
        lkClient := lk.NewClient(cfg.LiveKitHost, cfg.LiveKitAPIKey, cfg.LiveKitAPISecret)

        svc := rooms.NewService(roomStore, partStore, tokenizer, rdb, rooms.ServiceConfig{
                LiveKitURL:          cfg.LiveKitHost,
                LiveKitAPIKey:       cfg.LiveKitAPIKey,
                LiveKitAPISecret:    cfg.LiveKitAPISecret,
                LiveKitTokenTTL:     cfg.LiveKitTokenTTL,
                RoomTTL:             cfg.RoomTTL,
                MaxRoomSize:         cfg.MaxRoomSize,
                ParticipantTokenTTL: cfg.ParticipantTokenTTL,
                PublicBaseURL:       cfg.PublicBaseURL,
        }, log)

        handlers := httpapi.NewRoomHandlers(svc, tokenizer, lkClient, log)
        httpapi.SetLogger(log)
        httpapi.AddReadinessProbe(postgresPinger{pool: pool})
        httpapi.AddReadinessProbe(redisPinger{rdb: rdb})
        httpapi.ConfigureRateLimit(cfg.RateLimitRPS, cfg.RateLimitBurst)

        router := httpapi.NewRouter(httpapi.Deps{
                Rooms:   handlers,
                Origins: httpapi.SplitOrigins(cfg.CORSAllowedOrigins),
        })

        srv := &http.Server{
                Addr:              cfg.HTTPAddr,
                Handler:           router,
                ReadHeaderTimeout: 10 * time.Second,
                ReadTimeout:       15 * time.Second,
                WriteTimeout:      30 * time.Second,
                IdleTimeout:       60 * time.Second,
        }

        errCh := make(chan error, 1)
        go func() {
                log.Info().Str("addr", cfg.HTTPAddr).Msg("api listening")
                errCh <- srv.ListenAndServe()
        }()

        select {
        case err := <-errCh:
                if errors.Is(err, http.ErrServerClosed) {
                        return nil
                }
                return err
        case <-ctx.Done():
                log.Info().Msg("shutdown signal received")
                shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
                defer cancel()
                return srv.Shutdown(shutCtx)
        }
}

// postgresPinger adapts pgxpool to the readiness probe interface.
type postgresPinger struct{ pool interface{ Ping(context.Context) error } }

func (p postgresPinger) Ping(ctx context.Context) error { return p.pool.Ping(ctx) }

// redisPinger adapts go-redis to the readiness probe interface.
type redisPinger struct{ rdb *redis.Client }

func (p redisPinger) Ping(ctx context.Context) error { return p.rdb.Ping(ctx).Err() }
