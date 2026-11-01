// Room business service: admission, membership, language preferences and room
// lifecycle. Everything the HTTP API needs lives here so handlers stay thin.
//
// Event flow: any membership or preference change bumps the routing inputs and
// publishes a Redis `plan_changed` event; the media worker owning the room
// re-reads participants and applies the plan diff (blueprint §6.4).
package rooms

import (
        "context"
        "errors"
        "fmt"
        "time"

        "github.com/redis/go-redis/v9"
        "github.com/rs/zerolog"

        "github.com/palabra/palabra-group-translator/internal/auth"
        "github.com/palabra/palabra-group-translator/internal/languages"
        lk "github.com/palabra/palabra-group-translator/internal/livekit"
        "github.com/palabra/palabra-group-translator/internal/participants"
        "github.com/palabra/palabra-group-translator/internal/routing"
        "github.com/palabra/palabra-group-translator/internal/store"
)

// ServiceConfig carries the service dependencies' static settings.
type ServiceConfig struct {
        LiveKitURL      string // wss URL handed to browsers (LiveKit Cloud endpoint)
        LiveKitAPIKey   string
        LiveKitAPISecret string
        LiveKitTokenTTL time.Duration
        RoomTTL         time.Duration
        MaxRoomSize     int
        ParticipantTokenTTL time.Duration
        PublicBaseURL   string
}

// Service implements the room use cases.
type Service struct {
        rooms  *Store
        parts  *participants.Store
        tok    *auth.Tokenizer
        redis  *redis.Client
        cfg    ServiceConfig
        log    zerolog.Logger
}

// NewService wires the room service.
func NewService(rooms *Store, parts *participants.Store, tok *auth.Tokenizer,
        rdb *redis.Client, cfg ServiceConfig, log zerolog.Logger) *Service {
        if cfg.MaxRoomSize <= 0 {
                cfg.MaxRoomSize = 8
        }
        if cfg.RoomTTL <= 0 {
                cfg.RoomTTL = 4 * time.Hour
        }
        return &Service{rooms: rooms, parts: parts, tok: tok, redis: rdb, cfg: cfg, log: log}
}

// CreateInput is the create-room request payload.
type CreateInput struct {
        Name        string
        Capacity    int
        DisplayName string
        SourceLang  string // optional: "" = auto-detect
        ListenLang  string // required
}

// JoinInput is the join-room request payload.
type JoinInput struct {
        Code        string
        DisplayName string
        SourceLang  string
        ListenLang  string // required
}

// JoinResult is returned by Create and Join: everything a browser needs to
// enter the room in one round trip.
type JoinResult struct {
        Room             Room                `json:"room"`
        Participant      participants.Participant `json:"participant"`
        ParticipantToken string              `json:"participant_token"`
        LiveKitURL       string              `json:"livekit_url"`
        LiveKitToken     string              `json:"livekit_token"`
}

// ParticipantView is the API's projection of a participant row.
type ParticipantView struct {
        ID              string    `json:"id"`
        Name            string    `json:"name"`
        SourceLang      string    `json:"source_lang"`
        ListenLang      string    `json:"listen_lang"`
        MicEnabled      bool      `json:"mic_enabled"`
        ConnectionState string    `json:"connection_state"`
        IsOwner         bool      `json:"is_owner"`
        IsSelf          bool      `json:"is_self"`
        JoinedAt        time.Time `json:"joined_at"`
}

// RouteSummary aggregates real worker-reported route states for the UI.
type RouteSummary struct {
        Ready    int `json:"ready"`
        Starting int `json:"starting"`
        Error    int `json:"error"`
        Total    int `json:"total"`
}

// MyView is the requesting participant's personalized room view: what they
// should subscribe to (computed from the current plan with blueprint §5
// rules) and whether their listening languages are live.
type MyView struct {
        ListenLang       string   `json:"listen_lang"`
        SourceLang       string   `json:"source_lang"`
        OriginalSpeakers []string `json:"original_speakers"`
        TranslatedTracks []string `json:"translated_tracks"`
        ListenReady      bool     `json:"listen_ready"`
}

// Snapshot is the GET room-state response.
type Snapshot struct {
        Room         Room              `json:"room"`
        Participants []ParticipantView `json:"participants"`
        Routes       RouteSummary      `json:"routes"`
        Me           *MyView           `json:"me,omitempty"`
}

// InviteResult is the invite endpoint response.
type InviteResult struct {
        Code string `json:"code"`
        Link string `json:"link,omitempty"`
}

// CreateRoom creates a room, admits the owner as first participant and
// returns the join payload.
func (s *Service) CreateRoom(ctx context.Context, in CreateInput) (JoinResult, error) {
        if len(in.Name) > 120 {
                return JoinResult{}, errors.New("room name too long (max 120)")
        }
        if in.ListenLang == "" {
                return JoinResult{}, errors.New("listen_lang is required")
        }
        if !languages.IsValid(in.ListenLang) {
                return JoinResult{}, fmt.Errorf("unsupported listen language %q", in.ListenLang)
        }
        if in.SourceLang != "" && !languages.IsValid(in.SourceLang) {
                return JoinResult{}, fmt.Errorf("unsupported source language %q", in.SourceLang)
        }
        capacity := in.Capacity
        if capacity <= 0 {
                capacity = s.cfg.MaxRoomSize
        }
        if capacity > s.cfg.MaxRoomSize {
                capacity = s.cfg.MaxRoomSize
        }
        if len(in.DisplayName) > 80 {
                in.DisplayName = in.DisplayName[:80]
        }

        id, err := auth.NewID()
        if err != nil {
                return JoinResult{}, fmt.Errorf("room id: %w", err)
        }
        code, err := auth.NewJoinCode()
        if err != nil {
                return JoinResult{}, fmt.Errorf("join code: %w", err)
        }
        pid, err := auth.NewID()
        if err != nil {
                return JoinResult{}, fmt.Errorf("participant id: %w", err)
        }

        room := Room{
                ID:          id,
                Code:        code,
                Name:        in.Name,
                OwnerID:     pid,
                Capacity:    capacity,
                LiveKitRoom: "vb_" + id,
                ExpiresAt:   time.Now().Add(s.cfg.RoomTTL),
        }
        if err := s.rooms.Create(ctx, room); err != nil {
                return JoinResult{}, fmt.Errorf("create room: %w", err)
        }

        p := participants.Participant{
                ID:              pid,
                RoomID:          id,
                DisplayName:     in.DisplayName,
                SourceLang:      in.SourceLang,
                ListenLang:      in.ListenLang,
                LiveKitIdentity: pid,
                MicEnabled:      true,
        }
        if err := s.parts.Create(ctx, p); err != nil {
                return JoinResult{}, fmt.Errorf("create owner participant: %w", err)
        }

        res, err := s.issueJoin(ctx, room, p)
        if err != nil {
                return JoinResult{}, err
        }
        s.publishPlanChanged(room.ID)
        return res, nil
}

// JoinRoom admits a new participant after verifying the join code, room
// status/expiry and capacity (blueprint §6.4 join sequence).
func (s *Service) JoinRoom(ctx context.Context, roomID string, in JoinInput) (JoinResult, error) {
        room, err := s.rooms.Get(ctx, roomID)
        if err != nil {
                return JoinResult{}, err
        }
        if room.Status != "active" {
                return JoinResult{}, ErrRoomEnded
        }
        if time.Now().After(room.ExpiresAt) {
                return JoinResult{}, ErrStillExpired
        }
        if in.Code == "" || in.Code != room.Code {
                return JoinResult{}, ErrInvalidCode
        }
        if in.ListenLang == "" {
                return JoinResult{}, errors.New("listen_lang is required")
        }
        if !languages.IsValid(in.ListenLang) {
                return JoinResult{}, fmt.Errorf("unsupported listen language %q", in.ListenLang)
        }
        if in.SourceLang != "" && !languages.IsValid(in.SourceLang) {
                return JoinResult{}, fmt.Errorf("unsupported source language %q", in.SourceLang)
        }

        n, err := s.parts.CountActive(ctx, roomID)
        if err != nil {
                return JoinResult{}, err
        }
        if n >= room.Capacity {
                return JoinResult{}, ErrRoomFull
        }
        if len(in.DisplayName) > 80 {
                in.DisplayName = in.DisplayName[:80]
        }

        pid, err := auth.NewID()
        if err != nil {
                return JoinResult{}, fmt.Errorf("participant id: %w", err)
        }
        p := participants.Participant{
                ID:              pid,
                RoomID:          roomID,
                DisplayName:     in.DisplayName,
                SourceLang:      in.SourceLang,
                ListenLang:      in.ListenLang,
                LiveKitIdentity: pid,
                MicEnabled:      true,
        }
        if err := s.parts.Create(ctx, p); err != nil {
                return JoinResult{}, fmt.Errorf("create participant: %w", err)
        }

        res, err := s.issueJoin(ctx, room, p)
        if err != nil {
                return JoinResult{}, err
        }
        s.publishPlanChanged(roomID)
        return res, nil
}

// issueJoin mints both tokens (API participant token + LiveKit join token).
func (s *Service) issueJoin(ctx context.Context, room Room, p participants.Participant) (JoinResult, error) {
        apiToken, err := s.tok.Issue(auth.Claims{
                ParticipantID: p.ID,
                RoomID:        room.ID,
                LiveKitID:     p.LiveKitIdentity,
                DisplayName:   p.DisplayName,
                CanPublish:    true,
        }, s.cfg.ParticipantTokenTTL)
        if err != nil {
                return JoinResult{}, fmt.Errorf("issue participant token: %w", err)
        }
        lkToken, err := lk.IssueJoinToken(s.cfg.LiveKitAPIKey, s.cfg.LiveKitAPISecret, lk.TokenGrants{
                RoomName:     room.LiveKitRoom,
                Identity:     p.LiveKitIdentity,
                DisplayName:  p.DisplayName,
                CanPublish:   true,
                CanSubscribe: true,
                TTL:          s.cfg.LiveKitTokenTTL,
        })
        if err != nil {
                return JoinResult{}, err
        }
        return JoinResult{
                Room:             room,
                Participant:      p,
                ParticipantToken: apiToken,
                LiveKitURL:       s.cfg.LiveKitURL,
                LiveKitToken:     lkToken,
        }, nil
}

// Leave marks the participant as left and triggers a replan.
func (s *Service) Leave(ctx context.Context, roomID, participantID string) error {
        if err := s.parts.MarkLeft(ctx, roomID, participantID); err != nil {
                return err
        }
        s.publishPlanChanged(roomID)
        return nil
}

// UpdatePreferences changes the participant's listening (and optional source)
// language or mic state, then triggers a replan. Language changes are an
// explicit reconfiguration (blueprint §2 rule 10): the worker stops feeding
// the old routes only after new ones are session-ready.
func (s *Service) UpdatePreferences(ctx context.Context, roomID, participantID string, listenLang, sourceLang string, micEnabled *bool) (Snapshot, error) {
        if listenLang == "" {
                cur, err := s.parts.Get(ctx, roomID, participantID)
                if err != nil {
                        return Snapshot{}, err
                }
                listenLang = cur.ListenLang
        }
        if !languages.IsValid(listenLang) {
                return Snapshot{}, fmt.Errorf("unsupported listen language %q", listenLang)
        }
        if sourceLang != "" && !languages.IsValid(sourceLang) {
                return Snapshot{}, fmt.Errorf("unsupported source language %q", sourceLang)
        }
        // Empty source_lang means "auto-detect"; the API keeps it as configured.
        if _, err := s.parts.UpdatePreferences(ctx, roomID, participantID, listenLang, sourceLang, micEnabled); err != nil {
                return Snapshot{}, err
        }
        s.publishPlanChanged(roomID)
        return s.GetRoom(ctx, roomID, participantID)
}

// EndRoom lets the owner end the room: persistence, event and LiveKit teardown.
func (s *Service) EndRoom(ctx context.Context, roomID, requesterID string, lkClient *lk.Client) error {
        room, err := s.rooms.Get(ctx, roomID)
        if err != nil {
                return err
        }
        if room.OwnerID != requesterID {
                return errors.New("only the room owner can end the room")
        }
        if err := s.rooms.End(ctx, roomID); err != nil {
                return err
        }
        if err := store.PublishRoomEvent(ctx, s.redis, store.RoomEvent{
                Type: store.EventRoomEnded, RoomID: roomID,
        }); err != nil {
                s.log.Warn().Err(err).Str("room", roomID).Msg("publish room_ended failed")
        }
        if lkClient != nil {
                if err := lkClient.DeleteRoom(ctx, room.LiveKitRoom); err != nil {
                        s.log.Warn().Err(err).Str("room", roomID).Msg("livekit delete room failed")
                }
        }
        return nil
}

// GetByCode resolves an invitation code to its room (invite deep links).
func (s *Service) GetByCode(ctx context.Context, code string) (Room, error) {
        return s.rooms.GetByCode(ctx, code)
}

// GetRoom builds the full room snapshot, including the requester's
// subscription grants and real route states.
func (s *Service) GetRoom(ctx context.Context, roomID, viewerID string) (Snapshot, error) {
        room, err := s.rooms.Get(ctx, roomID)
        if err != nil {
                return Snapshot{}, err
        }
        rows, err := s.parts.ListActive(ctx, roomID)
        if err != nil {
                return Snapshot{}, err
        }

        parts := make([]routing.Participant, 0, len(rows))
        views := make([]ParticipantView, 0, len(rows))
        for _, p := range rows {
                parts = append(parts, routing.Participant{
                        ID: p.ID, Name: p.DisplayName, SourceLang: p.SourceLang, ListenLang: p.ListenLang,
                })
                views = append(views, ParticipantView{
                        ID: p.ID, Name: p.DisplayName, SourceLang: p.SourceLang,
                        ListenLang: p.ListenLang, MicEnabled: p.MicEnabled,
                        ConnectionState: p.ConnectionState, IsOwner: p.ID == room.OwnerID,
                        IsSelf: p.ID == viewerID, JoinedAt: p.JoinedAt,
                })
        }

        states, err := store.GetRouteStates(ctx, s.redis, roomID)
        if err != nil {
                s.log.Warn().Err(err).Str("room", roomID).Msg("read route states failed")
                states = map[string]string{}
        }
        summary := summarizeRoutes(states)

        snap := Snapshot{Room: room, Participants: views, Routes: summary}

        if viewerID != "" {
                plan := routing.PlanRoutes(parts)
                originals, translated := routing.SubscriptionGrants(viewerID, plan, parts)
                me := &MyView{
                        OriginalSpeakers: sortedKeys(originals),
                        TranslatedTracks: sortedKeys(translated),
                        ListenReady:      true,
                }
                for _, p := range rows {
                        if p.ID == viewerID {
                                me.ListenLang = p.ListenLang
                                me.SourceLang = p.SourceLang
                        }
                }
                // Listen-ready: every other speaker is either directly audible
                // (source == my listen language) or has a ready translation route.
                for _, p := range rows {
                        if p.ID == viewerID {
                                continue
                        }
                        if p.SourceLang != "" && p.SourceLang == me.ListenLang {
                                continue
                        }
                        st, ok := states[p.ID+"|"+p.SourceLang+"|"+me.ListenLang]
                        if ok && st == store.RouteStateError {
                                me.ListenReady = false
                                break
                        }
                        if ok && st != store.RouteStateReady {
                                me.ListenReady = false
                                break
                        }
                        if !ok {
                                // Worker has not reported this route (starting up) — not ready.
                                me.ListenReady = false
                                break
                        }
                }
                snap.Me = me
        }
        return snap, nil
}

// Invite returns the join code and an absolute invite link when the public
// base URL is configured.
func (s *Service) Invite(ctx context.Context, roomID string) (InviteResult, error) {
        room, err := s.rooms.Get(ctx, roomID)
        if err != nil {
                return InviteResult{}, err
        }
        out := InviteResult{Code: room.Code}
        if s.cfg.PublicBaseURL != "" {
                out.Link = fmt.Sprintf("%s/room/%s?code=%s", s.cfg.PublicBaseURL, room.ID, room.Code)
        }
        return out, nil
}

func summarizeRoutes(states map[string]string) RouteSummary {
        var rs RouteSummary
        rs.Total = len(states)
        for _, v := range states {
                switch v {
                case store.RouteStateReady:
                        rs.Ready++
                case store.RouteStateError:
                        rs.Error++
                default:
                        rs.Starting++
                }
        }
        return rs
}

func sortedKeys(m map[string]bool) []string {
        out := make([]string, 0, len(m))
        for k := range m {
                out = append(out, k)
        }
        for i := 1; i < len(out); i++ {
                for j := i; j > 0 && out[j] < out[j-1]; j-- {
                        out[j], out[j-1] = out[j-1], out[j]
                }
        }
        return out
}

func (s *Service) publishPlanChanged(roomID string) {
        err := store.PublishRoomEvent(context.Background(), s.redis, store.RoomEvent{
                Type: store.EventPlanChanged, RoomID: roomID,
        })
        if err != nil {
                s.log.Warn().Err(err).Str("room", roomID).Msg("publish plan_changed failed")
        }
}
