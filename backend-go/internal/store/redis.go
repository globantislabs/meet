package store

import (
        "context"
        "crypto/tls"
        "encoding/json"
        "fmt"
        "net"
        "time"

        "github.com/redis/go-redis/v9"
)

// NewRedis opens a Redis client and verifies connectivity. useTLS must be
// true for rediss:// endpoints (e.g. Render Key Value).
func NewRedis(ctx context.Context, addr, password string, useTLS bool) (*redis.Client, error) {
        cli := redis.NewClient(&redis.Options{
                Addr:         addr,
                Password:     password,
                TLSConfig:    tlsCfgFor(useTLS, addr),
                DialTimeout:  3 * time.Second,
                ReadTimeout:  2 * time.Second,
                WriteTimeout: 2 * time.Second,
                PoolSize:     32,
        })
        pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
        defer cancel()
        if err := cli.Ping(pingCtx).Err(); err != nil {
                _ = cli.Close()
                return nil, fmt.Errorf("store: ping redis: %w", err)
        }
        return cli, nil
}

// Redis key conventions used across api / worker / scheduler:
//
//      room:worker:{roomID}      -> worker lease (SET NX EX) proving ownership
//      room:meta:{roomID}        -> hash: status, capacity, livekit_room
//      room:events               -> pub/sub channel for plan_changed / room_ended
//      worker:{workerID}:rooms   -> set of room IDs claimed by a worker
//      worker:{workerID}:hb      -> heartbeat key with TTL for liveness
const (
        ChanRoomEvents = "room:events"
        KeyRoomWorker  = "room:worker:"  // + roomID
        KeyRoomMeta    = "room:meta:"    // + roomID
        KeyRoomRoutes  = "room:routes:"  // + roomID (hash: routeKey -> state)
        KeyWorkerRooms = "worker:%s:rooms"
        KeyWorkerHB    = "worker:%s:hb"
)

// Route lifecycle states written by the media worker and read by the API so
// the UI can show real translation status (route starting / ready / stalled).
const (
        RouteStateStarting = "starting"
        RouteStateReady    = "ready"
        RouteStateError    = "error"
)

// SetRouteStates writes the worker's current route states for a room and
// refreshes the key TTL. Workers call this after every plan change and on a
// periodic refresh; a room whose worker dies simply lets the key expire.
func SetRouteStates(ctx context.Context, rdb *redis.Client, roomID string, states map[string]string, ttl time.Duration) error {
        if len(states) == 0 {
                return nil
        }
        fields := make([]any, 0, len(states)*2)
        for k, v := range states {
                fields = append(fields, k, v)
        }
        key := KeyRoomRoutes + roomID
        if err := rdb.HSet(ctx, key, fields...).Err(); err != nil {
                return fmt.Errorf("store: set route states: %w", err)
        }
        return rdb.Expire(ctx, key, ttl).Err()
}

// GetRouteStates reads the current route states for a room. A missing key
// (expired or no worker yet) returns an empty map, not an error.
func GetRouteStates(ctx context.Context, rdb *redis.Client, roomID string) (map[string]string, error) {
        m, err := rdb.HGetAll(ctx, KeyRoomRoutes+roomID).Result()
        if err != nil {
                return nil, fmt.Errorf("store: get route states: %w", err)
        }
        return m, nil
}

// RoomEvent is the payload published on ChanRoomEvents. Consumers (workers)
// recompute routing plans on PlanChanged; workers and api stop work on
// RoomEnded.
type RoomEvent struct {
        Type     string `json:"type"` // plan_changed | room_ended
        RoomID   string `json:"room_id"`
        WorkerID string `json:"worker_id,omitempty"`
}

// Event types for RoomEvent.Type.
const (
        EventPlanChanged = "plan_changed"
        EventRoomEnded   = "room_ended"
)

// PublishRoomEvent marshals and publishes a room event, returning any error.
func PublishRoomEvent(ctx context.Context, rdb *redis.Client, ev RoomEvent) error {
        b, err := marshalJSON(ev)
        if err != nil {
                return fmt.Errorf("store: marshal room event: %w", err)
        }
        return rdb.Publish(ctx, ChanRoomEvents, b).Err()
}

// ParseRoomEvent decodes a pub/sub payload into a RoomEvent.
func ParseRoomEvent(payload string) (RoomEvent, error) {
        var ev RoomEvent
        if err := json.Unmarshal([]byte(payload), &ev); err != nil {
                return ev, fmt.Errorf("store: parse room event: %w", err)
        }
        return ev, nil
}

// tlsCfgFor returns a TLS config for rediss:// endpoints. Server names of
// managed providers are already correct in addr, so the default config with
// system roots is sufficient.
func tlsCfgFor(useTLS bool, addr string) *tls.Config {
        if !useTLS {
                return nil
        }
        host := addr
        if h, _, err := net.SplitHostPort(addr); err == nil {
                host = h
        }
        return &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
}

// marshalJSON is a local indirection kept for symmetry with other store files
// (and a single place to switch encoding libraries later).
func marshalJSON(v any) ([]byte, error) { return json.Marshal(v) }
