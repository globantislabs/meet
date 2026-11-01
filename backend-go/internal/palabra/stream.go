// Session manager: reference-counted Palabra S2S session lifecycle for the
// media worker. One session exists per unique (room, speaker, source, target)
// tuple — speakers' source streams are NEVER mixed (blueprint §4), and
// recipients sharing a target language share one session (blueprint §5).
//
// The manager enforces the deployment's session budget (PALABRA_SESSION_LIMIT)
// so a single misbehaving room cannot exhaust the provider concurrency quota.
package palabra

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
)

// ErrSessionBudgetExceeded is returned when creating a session would exceed
// the configured concurrency budget.
var ErrSessionBudgetExceeded = errors.New("palabra: session budget exceeded")

// ManagerConfig is the process-wide manager configuration.
type ManagerConfig struct {
	WSEndpoint    string
	AuthEndpoint  string
	ClientID      string
	ClientSecret  string
	MsgTypes      MsgTypes
	SessionLimit  int
	SkipAuth      bool // fake/dev only
	Voice         string
	InputRate     int
	OutputRate    int
	DialTimeout   time.Duration
	ReadyTimeout  time.Duration
	SessionOptsFn func() SessionOptions // optional per-session overrides
}

// Manager owns all S2S sessions of one worker process.
type Manager struct {
	cfg ManagerConfig
	log zerolog.Logger

	mu      sync.Mutex
	rooms   map[string]map[string]*refCountedSession // roomID -> key -> session
	total   atomic.Int64
	auth    *AuthClient
	factory func(ctx context.Context, cfg SessionConfig) (SpeechToSpeechSession, error)
}

type refCountedSession struct {
	session SpeechToSpeechSession
	refs    int
}

// NewManager builds a manager. The default factory opens real WebSocket
// sessions; tests can inject their own factory.
func NewManager(cfg ManagerConfig, log zerolog.Logger) *Manager {
	if cfg.SessionLimit <= 0 {
		cfg.SessionLimit = 64
	}
	m := &Manager{
		cfg:   cfg,
		log:   log,
		rooms: make(map[string]map[string]*refCountedSession),
	}
	if !cfg.SkipAuth {
		m.auth = NewAuthClient(cfg.AuthEndpoint, cfg.ClientID, cfg.ClientSecret)
	}
	m.factory = m.defaultFactory
	return m
}

// sessionKey identifies a session inside a room. The speaker ID MUST be part
// of the key: two speakers with identical language pairs need separate
// sessions because their source streams are independent.
type sessionKey struct {
	speaker string
	src     string
	tgt     string
}

func (k sessionKey) String() string { return k.speaker + "|" + k.src + "|" + k.tgt }

// GetOrCreate returns the existing session for (room, speaker, src->tgt) or
// creates, starts and awaits session-ready for a new one. The returned session
// carries one reference; the caller must Release it exactly once when done.
func (m *Manager) GetOrCreate(ctx context.Context, roomID, speakerID string, cfg SessionConfig) (SpeechToSpeechSession, error) {
	key := sessionKey{speakerID, cfg.SourceLang, cfg.TargetLang}

	m.mu.Lock()
	room := m.rooms[roomID]
	if room == nil {
		room = make(map[string]*refCountedSession)
		m.rooms[roomID] = room
	}
	if ref, ok := room[key.String()]; ok {
		ref.refs++
		s := ref.session
		m.mu.Unlock()
		return s, nil
	}
	if int(m.total.Load()) >= m.cfg.SessionLimit {
		m.mu.Unlock()
		return nil, fmt.Errorf("%w: limit %d", ErrSessionBudgetExceeded, m.cfg.SessionLimit)
	}
	m.mu.Unlock() // network I/O NEVER happens under the manager lock (§6.3)

	// Fill format defaults from the manager config.
	if cfg.InputSampleRate == 0 {
		cfg.InputSampleRate = m.cfg.InputRate
	}
	if cfg.OutputSampleRate == 0 {
		cfg.OutputSampleRate = m.cfg.OutputRate
	}
	if cfg.Voice == "" {
		cfg.Voice = m.cfg.Voice
	}
	if cfg.InputEncoding == "" {
		cfg.InputEncoding = EncodingPCM16
	}
	if cfg.OutputEncoding == "" {
		cfg.OutputEncoding = EncodingPCM16
	}

	s, err := m.factory(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := s.Start(ctx, cfg); err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("palabra: start session %s: %w", key.String(), err)
	}

	m.mu.Lock()
	// Re-check: a concurrent GetOrCreate may have created the same session
	// while we were dialing. Keep ours alive (it is started) but prefer the
	// existing entry and close our duplicate.
	if ref, ok := room[key.String()]; ok {
		ref.refs++
		existing := ref.session
		m.mu.Unlock()
		go func() {
			cctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = s.Close()
			_ = cctx
		}()
		return existing, nil
	}
	room[key.String()] = &refCountedSession{session: s, refs: 1}
	m.mu.Unlock()

	m.total.Add(1)
	return s, nil
}

// Release drops one reference acquired by GetOrCreate; when the last
// reference for a key is gone the session is closed asynchronously.
func (m *Manager) Release(roomID, speakerID string, src, tgt string) {
	key := sessionKey{speakerID, src, tgt}
	m.mu.Lock()
	room := m.rooms[roomID]
	if room == nil {
		m.mu.Unlock()
		return
	}
	ref, ok := room[key.String()]
	if !ok {
		m.mu.Unlock()
		return
	}
	ref.refs--
	var closeIt SpeechToSpeechSession
	if ref.refs <= 0 {
		closeIt = ref.session
		delete(room, key.String())
	}
	m.mu.Unlock()

	if closeIt != nil {
		m.total.Add(-1)
		go func() {
			cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = closeIt.Close()
			_ = cctx
		}()
	}
}

// CloseRoom closes and forgets every session of a room (room end, worker
// migration, panic recovery).
func (m *Manager) CloseRoom(roomID string) {
	m.mu.Lock()
	room := m.rooms[roomID]
	delete(m.rooms, roomID)
	var toClose []SpeechToSpeechSession
	for _, ref := range room {
		toClose = append(toClose, ref.session)
	}
	m.mu.Unlock()

	m.total.Add(-int64(len(toClose)))
	for _, s := range toClose {
		go func(s SpeechToSpeechSession) {
			cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = s.Close()
			_ = cctx
		}(s)
	}
}

// ActiveSessions returns the process-wide number of live sessions.
func (m *Manager) ActiveSessions() int { return int(m.total.Load()) }

// defaultFactory builds a real WebSocket session (or an in-process fake when
// the endpoint is the reserved fake:// scheme — used by integration tests and
// offline development, never in production).
func (m *Manager) defaultFactory(ctx context.Context, cfg SessionConfig) (SpeechToSpeechSession, error) {
	_ = ctx
	opts := DefaultSessionOptions()
	opts.MsgTypes = m.cfg.MsgTypes
	opts.WSEndpoint = m.cfg.WSEndpoint
	opts.SkipAuth = m.cfg.SkipAuth
	if !m.cfg.SkipAuth {
		opts.Auth = m.auth
	}
	if m.cfg.DialTimeout > 0 {
		opts.DialTimeout = m.cfg.DialTimeout
	}
	if m.cfg.ReadyTimeout > 0 {
		opts.ReadyTimeout = m.cfg.ReadyTimeout
	}
	opts.Log = m.log.With().Str("pair", strings.Join([]string{cfg.SourceLang, cfg.TargetLang}, "->")).Logger()

	if strings.HasPrefix(m.cfg.WSEndpoint, "fake://") {
		return NewFakeSession(cfg), nil
	}
	return NewWSSession(opts), nil
}
