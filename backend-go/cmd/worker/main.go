// worker: the media-plane service (blueprint §6.1).
//
// Responsibilities: join LiveKit rooms as a server-side participant, subscribe
// to participant microphones, run Palabra S2S translation sessions, publish
// translated tracks and enforce recipient permissions.
//
// SECURITY: Palabra client credentials live HERE and in no other service.
package main

import (
        "context"
        "os"
        "os/signal"
        "syscall"
        "time"

        "github.com/jackc/pgx/v5/pgxpool"
        "github.com/rs/zerolog"

        "github.com/palabra/palabra-group-translator/internal/config"
        "github.com/palabra/palabra-group-translator/internal/languages"
        "github.com/palabra/palabra-group-translator/internal/observability"
        "github.com/palabra/palabra-group-translator/internal/palabra"
        "github.com/palabra/palabra-group-translator/internal/participants"
        "github.com/palabra/palabra-group-translator/internal/store"
        "github.com/palabra/palabra-group-translator/internal/worker"
)

func main() {
        cfg := config.Load()
        log := observability.NewLogger(cfg.LogLevel, cfg.LogFormat)

        if err := languages.ConfigureExtra(cfg.ExtraLanguages); err != nil {
                log.Warn().Err(err).Msg("extra languages parse failed")
        }

        if err := run(cfg, log); err != nil {
                log.Fatal().Err(err).Msg("worker exited")
        }
}

func run(cfg config.Config, log zerolog.Logger) error {
        ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
        defer stop()

        if cfg.IsProduction() {
                if err := cfg.Validate(); err != nil {
                        return err
                }
        }

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

        metrics := observability.NewMetrics()

        // Palabra S2S session manager (the ONLY holder of provider credentials).
        pm := palabra.NewManager(palabra.ManagerConfig{
                WSEndpoint:   cfg.PalabraWSEndpoint,
                AuthEndpoint: cfg.PalabraAuthEndpoint,
                ClientID:     cfg.PalabraClientID,
                ClientSecret: cfg.PalabraClientSecret,
                MsgTypes:     cfg.PalabraMsgTypes,
                SessionLimit: cfg.PalabraSessionLimit,
                SkipAuth:     cfg.PalabraSkipAuth,
                InputRate:    16000,
                OutputRate:   16000,
        }, log)

        deps := worker.RuntimeDeps{
                Parts:   participants.New(pool),
                Palabra: pm,
                RDB:     rdb,
                Metrics: metrics,
                Usage:   worker.NewUsageWriter(pool),
                Log:     log,
                // The runner resolves the LiveKit room name when claiming a room.
                LiveKitRoomName: func(ctx context.Context, roomID string) (string, error) {
                        return resolveLiveKitRoomName(ctx, pool, roomID)
                },
        }

        runner := worker.NewRunner(worker.RunnerConfig{
                WorkerID:       cfg.WorkerID,
                WorkerIdentity: "media_" + cfg.WorkerID,
                LKHost:         cfg.LiveKitHost,
                LKAPIKey:       cfg.LiveKitAPIKey,
                LKAPISecret:    cfg.LiveKitAPISecret,
                LKTokenTTL:     cfg.LiveKitTokenTTL,
                HeartbeatTTL:   cfg.WorkerHeartbeatTTL,
                AssignPoll:     3 * time.Second,
        }, deps, rdb, metrics, log)

        log.Info().Str("worker", cfg.WorkerID).Msg("media worker starting")
        return runner.Run(ctx)
}

// resolveLiveKitRoomName reads the LiveKit room name for a room ID.
func resolveLiveKitRoomName(ctx context.Context, pool *pgxpool.Pool, roomID string) (string, error) {
        var name string
        err := pool.QueryRow(ctx,
                `SELECT livekit_room FROM rooms WHERE id = $1::uuid AND status = 'active'`,
                roomID).Scan(&name)
        return name, err
}
