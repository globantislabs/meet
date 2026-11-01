// scheduler: assigns active rooms to healthy media workers (blueprint §6.1
// optional control service). It never touches media: it only maintains the
// Redis assignment sets, room leases and worker heartbeats.
//
// Algorithm (every tick):
//  1. List active, non-expired rooms from Postgres.
//  2. Collect live workers (heartbeat key present).
//  3. For each unassigned room (no room:worker:{id} lease): pick the least
//     loaded live worker (fewest rooms in its set), claim the lease on its
//     behalf and add the room to its assignment set.
//  4. Reap ended/expired rooms from all assignment sets and leases.
package main

import (
        "context"
        "os"
        "os/signal"
        "syscall"
        "time"

        "github.com/jackc/pgx/v5/pgxpool"
        "github.com/redis/go-redis/v9"
        "github.com/rs/zerolog"

        "github.com/palabra/palabra-group-translator/internal/config"
        "github.com/palabra/palabra-group-translator/internal/observability"
        "github.com/palabra/palabra-group-translator/internal/store"
)

func main() {
        cfg := config.Load()
        log := observability.NewLogger(cfg.LogLevel, cfg.LogFormat)

        if err := run(cfg, log); err != nil {
                log.Fatal().Err(err).Msg("scheduler exited")
        }
}

func run(cfg config.Config, log zerolog.Logger) error {
        ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
        defer stop()

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

        tick := time.NewTicker(5 * time.Second)
        defer tick.Stop()

        log.Info().Msg("scheduler running")
        for {
                select {
                case <-ctx.Done():
                        return nil
                case <-tick.C:
                        if err := schedule(ctx, pool, rdb, cfg, log); err != nil {
                                log.Warn().Err(err).Msg("scheduler tick failed")
                        }
                }
        }
}

type roomRow struct {
        ID       string
        LiveKit  string
        WorkerID string // current lease owner ("" = unassigned)
}

func schedule(ctx context.Context, pool *pgxpool.Pool, rdb *redis.Client, cfg config.Config, log zerolog.Logger) error {
        // Active, non-expired rooms.
        rows, err := pool.Query(ctx, `
                SELECT id::text, livekit_room FROM rooms
                WHERE status = 'active' AND expires_at > now()`)
        if err != nil {
                return err
        }
        type activeRoom struct{ ID, LiveKit string }
        var actives []activeRoom
        for rows.Next() {
                var id, lk string
                if err := rows.Scan(&id, &lk); err != nil {
                        rows.Close()
                        return err
                }
                actives = append(actives, activeRoom{ID: id, LiveKit: lk})
        }
        rows.Close()

        // Live workers: heartbeat key present.
        workers := map[string]int{} // workerID -> assigned room count
        var cursor uint64
        for {
                keys, next, err := rdb.Scan(ctx, cursor, "worker:*:hb", 100).Result()
                if err != nil {
                        return err
                }
                for _, k := range keys {
                        id := trimWorkerKey(k)
                        if n, err := rdb.SCard(ctx, "worker:"+id+":rooms").Result(); err == nil {
                                workers[id] = int(n)
                        }
                }
                cursor = next
                if cursor == 0 {
                        break
                }
        }

        for _, room := range actives {
                leaseKey := store.KeyRoomWorker + room.ID
                owner, err := rdb.Get(ctx, leaseKey).Result()
                if err != nil && err != redis.Nil {
                        return err
                }
                if owner != "" {
                        continue // assigned
                }
                if len(workers) == 0 {
                        continue // no live workers; try next tick
                }
                // Least-loaded live worker.
                best, bestN := "", 1<<30
                for id, n := range workers {
                        if n < bestN {
                                best, bestN = id, n
                        }
                }
                ok, err := rdb.SetNX(ctx, leaseKey, best, cfg.WorkerHeartbeatTTL).Result()
                if err != nil {
                        return err
                }
                if !ok {
                        continue
                }
                if err := rdb.SAdd(ctx, "worker:"+best+":rooms", room.ID).Err(); err != nil {
                        return err
                }
                workers[best]++
                if err := store.PublishRoomEvent(ctx, rdb, store.RoomEvent{
                        Type: store.EventPlanChanged, RoomID: room.ID,
                }); err != nil {
                        log.Warn().Err(err).Str("room", room.ID).Msg("assign event publish failed")
                }
                log.Info().Str("room", room.ID).Str("worker", best).Msg("room assigned")
        }

        // Reap leases for rooms that are no longer active.
        cursor = 0
        for {
                keys, next, err := rdb.Scan(ctx, cursor, store.KeyRoomWorker+"*", 100).Result()
                if err != nil {
                        return err
                }
                for _, k := range keys {
                        roomID := k[len(store.KeyRoomWorker):]
                        var status string
                        err := pool.QueryRow(ctx,
                                `SELECT status FROM rooms WHERE id = $1::uuid`, roomID).Scan(&status)
                        if err != nil {
                                // Room row gone: release lease and membership.
                                _ = rdb.Del(ctx, k).Err()
                        } else if status != "active" {
                                owner, _ := rdb.Get(ctx, k).Result()
                                _ = rdb.Del(ctx, k).Err()
                                if owner != "" {
                                        _ = rdb.SRem(ctx, "worker:"+owner+":rooms", roomID).Err()
                                }
                        }
                }
                cursor = next
                if cursor == 0 {
                        break
                }
        }
        return nil
}

func trimWorkerKey(k string) string {
        // "worker:{id}:hb" -> "{id}"
        const prefix, suffix = "worker:", ":hb"
        if len(k) > len(prefix)+len(suffix) && k[:len(prefix)] == prefix {
                body := k[len(prefix):]
                if body[len(body)-len(suffix):] == suffix {
                        return body[:len(body)-len(suffix)]
                }
        }
        return k
}
