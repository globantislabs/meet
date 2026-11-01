// Package participants persists room membership and per-participant language
// configuration. No audio or transcript content is ever stored (blueprint §12).
package participants

import (
        "context"
        "errors"
        "time"

        "github.com/jackc/pgx/v5"
        "github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when the requested participant row does not exist.
var ErrNotFound = errors.New("participants: not found")

// Participant is the persisted participant row.
type Participant struct {
        ID              string     `json:"id"`
        RoomID          string     `json:"room_id"`
        DisplayName     string     `json:"display_name"`
        SourceLang      string     `json:"source_lang"` // "" = auto-detect (capability-gated)
        ListenLang      string     `json:"listen_lang"`
        LiveKitIdentity string     `json:"livekit_identity"`
        MicEnabled      bool       `json:"mic_enabled"`
        ConnectionState string     `json:"connection_state"`
        JoinedAt        time.Time  `json:"joined_at"`
        LeftAt          *time.Time `json:"left_at,omitempty"`
}

// Store provides participant persistence on PostgreSQL.
type Store struct {
        pool *pgxpool.Pool
}

// New builds a participant store.
func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

const participantColumns = `id, room_id, display_name, source_lang, listen_lang,
        livekit_identity, mic_enabled, connection_state, joined_at, left_at`

func scanParticipant(row pgx.Row) (Participant, error) {
        var p Participant
        err := row.Scan(&p.ID, &p.RoomID, &p.DisplayName, &p.SourceLang, &p.ListenLang,
                &p.LiveKitIdentity, &p.MicEnabled, &p.ConnectionState, &p.JoinedAt, &p.LeftAt)
        if errors.Is(err, pgx.ErrNoRows) {
                return Participant{}, ErrNotFound
        }
        return p, err
}

// Create inserts a new active participant.
func (s *Store) Create(ctx context.Context, p Participant) error {
        _, err := s.pool.Exec(ctx, `
                INSERT INTO participants (id, room_id, display_name, source_lang, listen_lang,
                        livekit_identity, mic_enabled, connection_state)
                VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
                p.ID, p.RoomID, p.DisplayName, p.SourceLang, p.ListenLang,
                p.LiveKitIdentity, p.MicEnabled, "connected")
        if err != nil {
                return err
        }
        return nil
}

// Get returns one participant of a room.
func (s *Store) Get(ctx context.Context, roomID, id string) (Participant, error) {
        return scanParticipant(s.pool.QueryRow(ctx,
                `SELECT `+participantColumns+` FROM participants WHERE room_id = $1 AND id = $2`,
                roomID, id))
}

// GetByIdentity resolves a LiveKit identity to a participant (worker join path).
func (s *Store) GetByIdentity(ctx context.Context, roomID, identity string) (Participant, error) {
        return scanParticipant(s.pool.QueryRow(ctx,
                `SELECT `+participantColumns+` FROM participants
                 WHERE room_id = $1 AND livekit_identity = $2 AND left_at IS NULL
                 ORDER BY joined_at DESC LIMIT 1`,
                roomID, identity))
}

// ListActive returns every currently-connected participant of a room, oldest
// join first (stable ordering for the routing planner and the UI).
func (s *Store) ListActive(ctx context.Context, roomID string) ([]Participant, error) {
        rows, err := s.pool.Query(ctx,
                `SELECT `+participantColumns+` FROM participants
                 WHERE room_id = $1 AND left_at IS NULL ORDER BY joined_at ASC, id ASC`,
                roomID)
        if err != nil {
                return nil, err
        }
        defer rows.Close()

        var out []Participant
        for rows.Next() {
                p, err := scanParticipant(rows)
                if err != nil {
                        return nil, err
                }
                out = append(out, p)
        }
        return out, rows.Err()
}

// UpdatePreferences updates the mutable per-participant settings.
func (s *Store) UpdatePreferences(ctx context.Context, roomID, id string, listenLang, sourceLang string, micEnabled *bool) (Participant, error) {
        row := s.pool.QueryRow(ctx, `
                UPDATE participants SET
                        listen_lang = $3,
                        source_lang = $4,
                        mic_enabled = COALESCE($5, mic_enabled)
                WHERE room_id = $1 AND id = $2 AND left_at IS NULL
                RETURNING `+participantColumns,
                roomID, id, listenLang, sourceLang, micEnabled)
        return scanParticipant(row)
}

// MarkLeft marks the participant as disconnected (leave or disconnect).
func (s *Store) MarkLeft(ctx context.Context, roomID, id string) error {
        tag, err := s.pool.Exec(ctx,
                `UPDATE participants SET left_at = now(), connection_state = 'disconnected'
                 WHERE room_id = $1 AND id = $2 AND left_at IS NULL`,
                roomID, id)
        if err != nil {
                return err
        }
        if tag.RowsAffected() == 0 {
                return ErrNotFound
        }
        return nil
}

// MarkConnectionState records the live connection health reported by the worker.
func (s *Store) MarkConnectionState(ctx context.Context, roomID, id, state string) error {
        _, err := s.pool.Exec(ctx,
                `UPDATE participants SET connection_state = $3 WHERE room_id = $1 AND id = $2`,
                roomID, id, state)
        return err
}

// CountActive returns the number of connected participants (capacity checks).
func (s *Store) CountActive(ctx context.Context, roomID string) (int, error) {
        var n int
        err := s.pool.QueryRow(ctx,
                `SELECT COUNT(*) FROM participants WHERE room_id = $1 AND left_at IS NULL`,
                roomID).Scan(&n)
        return n, err
}
