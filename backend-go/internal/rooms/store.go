// Package rooms implements room persistence plus the room business rules:
// creation, admission (capacity + join code), leaving, preference updates and
// ending. It is the control-plane core consumed by the HTTP API.
package rooms

import (
        "context"
        "errors"
        "time"

        "github.com/jackc/pgx/v5"
        "github.com/jackc/pgx/v5/pgxpool"
)

// Domain errors surfaced to the API layer (mapped to HTTP status codes there).
var (
        ErrNotFound     = errors.New("rooms: not found")
        ErrRoomEnded    = errors.New("rooms: room already ended")
        ErrRoomFull     = errors.New("rooms: room is full")
        ErrInvalidCode  = errors.New("rooms: invalid join code")
        ErrStillExpired = errors.New("rooms: room expired")
)

// Room is the persisted room row.
type Room struct {
        ID              string     `json:"id"`
        Code            string     `json:"code"`
        Name            string     `json:"name"`
        OwnerID         string     `json:"owner_id"`
        Capacity        int        `json:"capacity"`
        Status          string     `json:"status"`            // active | ended
        TranslationMode string     `json:"translation_mode"`  // personal
        LiveKitRoom     string     `json:"livekit_room"`
        CreatedAt       time.Time  `json:"created_at"`
        ExpiresAt       time.Time  `json:"expires_at"`
        EndedAt         *time.Time `json:"ended_at,omitempty"`
}

// Store provides room persistence on PostgreSQL.
type Store struct {
        pool *pgxpool.Pool
}

// New builds a room store.
func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

const roomColumns = `id, code, name, owner_id, capacity, status, translation_mode,
        livekit_room, created_at, expires_at, ended_at`

func scanRoom(row pgx.Row) (Room, error) {
        var r Room
        err := row.Scan(&r.ID, &r.Code, &r.Name, &r.OwnerID, &r.Capacity, &r.Status,
                &r.TranslationMode, &r.LiveKitRoom, &r.CreatedAt, &r.ExpiresAt, &r.EndedAt)
        if errors.Is(err, pgx.ErrNoRows) {
                return Room{}, ErrNotFound
        }
        return r, err
}

// Create inserts a room row. ID and code are assigned by the service so the
// caller knows them before any token is issued.
func (s *Store) Create(ctx context.Context, r Room) error {
        _, err := s.pool.Exec(ctx, `
                INSERT INTO rooms (id, code, name, owner_id, capacity, status,
                        translation_mode, livekit_room, expires_at)
                VALUES ($1, $2, $3, $4, $5, 'active', 'personal', $6, $7)`,
                r.ID, r.Code, r.Name, r.OwnerID, r.Capacity, r.LiveKitRoom, r.ExpiresAt)
        return err
}

// Get returns a room by ID.
func (s *Store) Get(ctx context.Context, id string) (Room, error) {
        return scanRoom(s.pool.QueryRow(ctx,
                `SELECT `+roomColumns+` FROM rooms WHERE id = $1`, id))
}

// GetByCode returns a room by join code.
func (s *Store) GetByCode(ctx context.Context, code string) (Room, error) {
        return scanRoom(s.pool.QueryRow(ctx,
                `SELECT `+roomColumns+` FROM rooms WHERE code = $1 AND status = 'active'`, code))
}

// End marks a room ended.
func (s *Store) End(ctx context.Context, id string) error {
        tag, err := s.pool.Exec(ctx,
                `UPDATE rooms SET status = 'ended', ended_at = now()
                 WHERE id = $1 AND status = 'active'`, id)
        if err != nil {
                return err
        }
        if tag.RowsAffected() == 0 {
                return ErrNotFound
        }
        return nil
}
