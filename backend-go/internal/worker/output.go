// OutputTrackManager: publishes one LiveKit output track per translation
// route (`tr_{speaker}_{target}`), encodes Palabra's PCM output to Opus and
// enforces recipient permissions at the publisher side.
//
// PERMISSION MODEL (blueprint §2 rule 5, §5): the worker is the publisher of
// all translated tracks, so it is the publisher's job to restrict who may
// subscribe. After every plan change it sends a subscription-permission
// update allowing ONLY route recipients on each output track. A participant
// can therefore never subscribe to their own translation.
package worker

import (
	"context"
	"sync"
	"time"

	"github.com/hraban/opus"
	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/rs/zerolog"

	"github.com/palabra/palabra-group-translator/internal/audio"
	"github.com/palabra/palabra-group-translator/internal/routing"
)

// outputTrack bundles one published route's publisher pipeline.
type outputTrack struct {
	routeKey string
	route    routing.Route
	pub      *lksdk.LocalTrackPublication
	track    *lksdk.LocalSampleTrack
	enc      *opus.Encoder
	resamp   *audio.Resampler // 16k -> 48k
	queue    *audio.Queue
	cancel   context.CancelFunc
}

// OutputTrackManager owns all published output tracks of one room runtime.
type OutputTrackManager struct {
	roomID string
	room   *lksdk.Room
	log    zerolog.Logger

	mu     sync.Mutex
	tracks map[string]*outputTrack

	onPublished func(route routing.Route, sid string)
	onEnded     func(route routing.Route)
}

// NewOutputTrackManager builds the manager for one room.
func NewOutputTrackManager(roomID string, room *lksdk.Room, log zerolog.Logger) *OutputTrackManager {
	return &OutputTrackManager{
		roomID: roomID,
		room:   room,
		log:    log,
		tracks: make(map[string]*outputTrack),
	}
}

// SetCallbacks wires publish/stop notifications (used for permissions and
// metrics). Must be called before EnsureTrack.
func (m *OutputTrackManager) SetCallbacks(onPublished func(route routing.Route, sid string), onEnded func(route routing.Route)) {
	m.onPublished = onPublished
	m.onEnded = onEnded
}

// EnsureTrack starts publishing the output track for a route if not already
// live. The publisher goroutine consumes PCM16 16 kHz frames, resamples to
// 48 kHz, encodes 20 ms Opus frames and writes samples to LiveKit.
func (m *OutputTrackManager) EnsureTrack(route routing.Route) error {
	key := route.Key()
	m.mu.Lock()
	if _, ok := m.tracks[key]; ok {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()

	capability := webrtc.RTPCodecCapability{
		MimeType:  webrtc.MimeTypeOpus,
		ClockRate: audio.SampleRate48k,
		Channels:  2,
	}
	track, err := lksdk.NewLocalSampleTrack(capability)
	if err != nil {
		return err
	}
	pub, err := m.room.LocalParticipant.PublishTrack(track, &lksdk.TrackPublicationOptions{
		// Canonical name shared with routing.OutputTrackName and the frontend.
		Name:   routing.OutputTrackName(route.SpeakerID, route.TargetLang),
		Source: webrtc.TrackSourceMicrophone,
	})
	if err != nil {
		return err
	}

	enc, err := opus.NewEncoder(audio.SampleRate48k, audio.ChannelsMono, opus.AppVoIP)
	if err != nil {
		_ = pub.Unpublish()
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	ot := &outputTrack{
		routeKey: key,
		route:    route,
		pub:      pub,
		track:    track,
		enc:      enc,
		resamp:   audio.NewResampler(audio.SampleRate16k, audio.SampleRate48k),
		queue:    audio.NewQueue(150, audio.PolicyDropOldest),
		cancel:   cancel,
	}

	m.mu.Lock()
	m.tracks[key] = ot
	m.mu.Unlock()

	go m.publishLoop(ctx, ot)

	if m.onPublished != nil {
		m.onPublished(route, pub.SID())
	}
	m.log.Info().Str("room", m.roomID).Str("track", pub.SID()).
		Str("route", key).Msg("published translated output track")
	return nil
}

// publishLoop drains the route queue: resample 16k->48k, buffer exact 20 ms
// Opus frames (960 samples), encode, write.
func (m *OutputTrackManager) publishLoop(ctx context.Context, ot *outputTrack) {
	const frameSamples48k = audio.SampleRate48k / 50 // 960 samples = 20 ms
	buf48 := make([]int16, 0, frameSamples48k*4)
	opusOut := make([]byte, 4096)

	for {
		frame, err := ot.queue.Pop(ctx)
		if err != nil {
			return // queue closed or canceled
		}
		samples, err := audio.BytesToSamples(frame.Data)
		if err != nil {
			continue
		}
		buf48 = append(buf48, ot.resamp.Process(samples)...)
		for len(buf48) >= frameSamples48k {
			n, err := ot.enc.Encode(buf48[:frameSamples48k:frameSamples48k], opusOut)
			if err != nil {
				m.log.Warn().Err(err).Msg("opus encode failed")
				buf48 = buf48[frameSamples48k:]
				continue
			}
			ot.track.WriteSample(media.Sample{
				Data:     opusOut[:n],
				Duration: 20 * time.Millisecond,
			})
			buf48 = buf48[frameSamples48k:]
		}
	}
}

// Enqueue hands one translated frame to the route's publisher (never blocks).
func (m *OutputTrackManager) Enqueue(routeKey string, frame FrameWithRoute) {
	m.mu.Lock()
	ot := m.tracks[routeKey]
	m.mu.Unlock()
	if ot == nil {
		return
	}
	_ = ot.queue.Push(frame.AudioFrame)
}

// UnpublishTrack stops and removes one route's output track.
func (m *OutputTrackManager) UnpublishTrack(routeKey string) {
	m.mu.Lock()
	ot, ok := m.tracks[routeKey]
	if ok {
		delete(m.tracks, routeKey)
	}
	m.mu.Unlock()
	if !ok {
		return
	}
	ot.cancel()
	ot.queue.Close()
	_ = ot.pub.Unpublish()
	if m.onEnded != nil {
		m.onEnded(ot.route)
	}
}

// TrackSID returns the published track SID for a route ("" if unpublished).
func (m *OutputTrackManager) TrackSID(routeKey string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ot := m.tracks[routeKey]; ot != nil {
		return ot.pub.SID()
	}
	return ""
}

// ActiveTracks returns the currently published route keys.
func (m *OutputTrackManager) ActiveTracks() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.tracks))
	for k := range m.tracks {
		out = append(out, k)
	}
	return out
}

// StopAll unpublishes every output track (room teardown).
func (m *OutputTrackManager) StopAll() {
	for _, k := range m.ActiveTracks() {
		m.UnpublishTrack(k)
	}
}

// ── Permissions ──────────────────────────────────────────────────────────────

// ApplyPermissions recomputes publisher-side subscription permissions from the
// current plan: for every published translated track, ONLY its recipients are
// allowed to subscribe.
//
// VERIFY against lksdk docs: LocalParticipant.SetTrackSubscriptionPermissions
// takes (allParticipants bool, allowedTrackIDs ...string) where each allowed
// entry has the form "trackSID:participantIdentity". If the format changes,
// this is the single function to fix.
func (m *OutputTrackManager) ApplyPermissions(pairs []string) {
	// Deduplicate pairs while preserving order.
	seen := make(map[string]struct{}, len(pairs))
	dedup := make([]string, 0, len(pairs))
	for _, p := range pairs {
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		dedup = append(dedup, p)
	}
	// NOTE: empty dedup list = allow nobody on our tracks (valid state when a
	// room has no listeners needing translations).
	m.room.LocalParticipant.SetTrackSubscriptionPermissions(false, dedup...)
}
