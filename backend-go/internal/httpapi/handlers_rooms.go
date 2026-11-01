// Room HTTP handlers (blueprint §10). Handlers are thin: parse/validate the
// request, delegate to the rooms service, map domain errors to status codes.
package httpapi

import (
        "encoding/json"
        "errors"
        "net/http"
        "time"

        "github.com/go-chi/chi/v5"
        "github.com/rs/zerolog"

        "github.com/palabra/palabra-group-translator/internal/auth"
        "github.com/palabra/palabra-group-translator/internal/languages"
        lk "github.com/palabra/palabra-group-translator/internal/livekit"
        "github.com/palabra/palabra-group-translator/internal/participants"
        "github.com/palabra/palabra-group-translator/internal/rooms"
)

// RoomHandlers carries the handler dependencies.
type RoomHandlers struct {
        rooms     *rooms.Service
        tokenizer *auth.Tokenizer
        livekit   *lk.Client
        log       zerolog.Logger
        startedAt time.Time
}

// NewRoomHandlers assembles the room handlers.
func NewRoomHandlers(rooms *rooms.Service, tokenizer *auth.Tokenizer, livekit *lk.Client, log zerolog.Logger) *RoomHandlers {
        return &RoomHandlers{rooms: rooms, tokenizer: tokenizer, livekit: livekit, log: log, startedAt: time.Now()}
}

// ── helpers ──────────────────────────────────────────────────────────────────

type apiError struct {
        Error string `json:"error"`
        Detail string `json:"detail,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
        w.Header().Set("Content-Type", "application/json")
        w.WriteHeader(status)
        _ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, detail string) {
        writeJSON(w, status, apiError{Error: code, Detail: detail})
}

// mapServiceError converts domain errors into (status, code, message).
func mapServiceError(err error) (int, string, string) {
        switch {
        case errors.Is(err, rooms.ErrNotFound), errors.Is(err, participants.ErrNotFound):
                return http.StatusNotFound, "not_found", "room or participant not found"
        case errors.Is(err, rooms.ErrRoomEnded):
                return http.StatusConflict, "room_ended", "this room has ended"
        case errors.Is(err, rooms.ErrRoomFull):
                return http.StatusConflict, "room_full", "the room is at capacity"
        case errors.Is(err, rooms.ErrInvalidCode):
                return http.StatusForbidden, "invalid_code", "wrong join code"
        case errors.Is(err, rooms.ErrStillExpired):
                return http.StatusConflict, "room_expired", "this room has expired"
        default:
                return http.StatusBadRequest, "bad_request", err.Error()
        }
}

// requireMembership verifies the caller owns the token for this room.
func (h *RoomHandlers) requireMember(w http.ResponseWriter, r *http.Request, roomID string) (auth.Claims, bool) {
        claims, ok := claimsOf(r.Context())
        if !ok || claims.RoomID != roomID {
                writeError(w, http.StatusForbidden, "forbidden", "token does not grant access to this room")
                return auth.Claims{}, false
        }
        return claims, true
}

func decodeBody(r *http.Request, dst any) error {
        defer r.Body.Close()
        dec := json.NewDecoder(r.Body)
        return dec.Decode(dst)
}

// ── handlers ─────────────────────────────────────────────────────────────────

func (h *RoomHandlers) health(w http.ResponseWriter, r *http.Request) {
        writeJSON(w, http.StatusOK, map[string]any{
                "status": "ok", "uptime_seconds": time.Since(h.startedAt).Seconds(),
        })
}

// create handles POST /v1/rooms.
func (h *RoomHandlers) create(w http.ResponseWriter, r *http.Request) {
        var in struct {
                Name        string `json:"name"`
                Capacity    int    `json:"capacity"`
                DisplayName string `json:"display_name"`
                SourceLang  string `json:"source_lang"`
                ListenLang  string `json:"listen_lang"`
        }
        if err := decodeBody(r, &in); err != nil {
                writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
                return
        }
        res, err := h.rooms.CreateRoom(r.Context(), rooms.CreateInput{
                Name: in.Name, Capacity: in.Capacity, DisplayName: in.DisplayName,
                SourceLang: in.SourceLang, ListenLang: in.ListenLang,
        })
        if err != nil {
                status, code, msg := mapServiceError(err)
                writeError(w, status, code, msg)
                return
        }
        writeJSON(w, http.StatusCreated, res)
}

// join handles POST /v1/rooms/{roomID}/join.
func (h *RoomHandlers) join(w http.ResponseWriter, r *http.Request) {
        roomID := chi.URLParam(r, "roomID")
        var in struct {
                Code        string `json:"code"`
                DisplayName string `json:"display_name"`
                SourceLang  string `json:"source_lang"`
                ListenLang  string `json:"listen_lang"`
        }
        if err := decodeBody(r, &in); err != nil {
                writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
                return
        }
        res, err := h.rooms.JoinRoom(r.Context(), roomID, rooms.JoinInput{
                Code: in.Code, DisplayName: in.DisplayName,
                SourceLang: in.SourceLang, ListenLang: in.ListenLang,
        })
        if err != nil {
                status, code, msg := mapServiceError(err)
                writeError(w, status, code, msg)
                return
        }
        writeJSON(w, http.StatusOK, res)
}

// getRoom handles GET /v1/rooms/{roomID}.
func (h *RoomHandlers) getRoom(w http.ResponseWriter, r *http.Request) {
        roomID := chi.URLParam(r, "roomID")
        claims, ok := h.requireMember(w, r, roomID)
        if !ok {
                return
        }
        snap, err := h.rooms.GetRoom(r.Context(), roomID, claims.ParticipantID)
        if err != nil {
                status, code, msg := mapServiceError(err)
                writeError(w, status, code, msg)
                return
        }
        writeJSON(w, http.StatusOK, snap)
}

// listParticipants handles GET /v1/rooms/{roomID}/participants.
func (h *RoomHandlers) listParticipants(w http.ResponseWriter, r *http.Request) {
        roomID := chi.URLParam(r, "roomID")
        if _, ok := h.requireMember(w, r, roomID); !ok {
                return
        }
        snap, err := h.rooms.GetRoom(r.Context(), roomID, "")
        if err != nil {
                status, code, msg := mapServiceError(err)
                writeError(w, status, code, msg)
                return
        }
        writeJSON(w, http.StatusOK, map[string]any{"participants": snap.Participants})
}

// leave handles POST /v1/rooms/{roomID}/leave.
func (h *RoomHandlers) leave(w http.ResponseWriter, r *http.Request) {
        roomID := chi.URLParam(r, "roomID")
        claims, ok := h.requireMember(w, r, roomID)
        if !ok {
                return
        }
        if err := h.rooms.Leave(r.Context(), roomID, claims.ParticipantID); err != nil {
                status, code, msg := mapServiceError(err)
                writeError(w, status, code, msg)
                return
        }
        writeJSON(w, http.StatusOK, map[string]any{"left": true})
}

// updatePreferences handles PATCH /v1/rooms/{roomID}/me/preferences.
func (h *RoomHandlers) updatePreferences(w http.ResponseWriter, r *http.Request) {
        roomID := chi.URLParam(r, "roomID")
        claims, ok := h.requireMember(w, r, roomID)
        if !ok {
                return
        }
        var in struct {
                ListenLang string `json:"listen_lang"`
                SourceLang string `json:"source_lang"`
                MicEnabled *bool  `json:"mic_enabled"`
        }
        if err := decodeBody(r, &in); err != nil {
                writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
                return
        }
        snap, err := h.rooms.UpdatePreferences(r.Context(), roomID, claims.ParticipantID,
                in.ListenLang, in.SourceLang, in.MicEnabled)
        if err != nil {
                status, code, msg := mapServiceError(err)
                writeError(w, status, code, msg)
                return
        }
        writeJSON(w, http.StatusOK, snap)
}

// invite handles POST /v1/rooms/{roomID}/invite.
func (h *RoomHandlers) invite(w http.ResponseWriter, r *http.Request) {
        roomID := chi.URLParam(r, "roomID")
        if _, ok := h.requireMember(w, r, roomID); !ok {
                return
        }
        res, err := h.rooms.Invite(r.Context(), roomID)
        if err != nil {
                status, code, msg := mapServiceError(err)
                writeError(w, status, code, msg)
                return
        }
        writeJSON(w, http.StatusOK, res)
}

// endRoom handles DELETE /v1/rooms/{roomID} (owner only).
func (h *RoomHandlers) endRoom(w http.ResponseWriter, r *http.Request) {
        roomID := chi.URLParam(r, "roomID")
        claims, ok := h.requireMember(w, r, roomID)
        if !ok {
                return
        }
        if err := h.rooms.EndRoom(r.Context(), roomID, claims.ParticipantID, h.livekit); err != nil {
                if err.Error() == "only the room owner can end the room" {
                        writeError(w, http.StatusForbidden, "forbidden", "only the room owner can end the room")
                        return
                }
                status, code, msg := mapServiceError(err)
                writeError(w, status, code, msg)
                return
        }
        writeJSON(w, http.StatusOK, map[string]any{"ended": true})
}

// byCode resolves a join code to its room ID (used by the invite deep link
// when only the code is known). It leaks nothing else.
func (h *RoomHandlers) byCode(w http.ResponseWriter, r *http.Request) {
        code := chi.URLParam(r, "code")
        room, err := h.rooms.GetByCode(r.Context(), code)
        if err != nil {
                status, c, msg := mapServiceError(err)
                writeError(w, status, c, msg)
                return
        }
        writeJSON(w, http.StatusOK, map[string]any{"room_id": room.ID})
}

// languages handles GET /v1/languages.
func (h *RoomHandlers) languages(w http.ResponseWriter, r *http.Request) {
        list := languages.List()
        writeJSON(w, http.StatusOK, map[string]any{
                "languages":          list,
                "auto_detect_source": false, // capability-gated: see languages package notice
        })
}
