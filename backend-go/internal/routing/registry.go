package routing

import "sync"

// routeKey identifies one active translation route inside a room.
type routeKey struct {
	SpeakerID string
	Source    string
	Target    string
}

// activeRoute binds a planned route to its runtime resources: the Palabra
// session feeding it and the LiveKit output track that carries its audio.
// Recipients always come from the latest Plan (never cached here) so
// permission refreshes are cheap and race-free.
type activeRoute struct {
	SpeakerID     string
	SourceLang    string
	TargetLang    string
	SessionID     string // palabra session correlation id ("" until ready)
	OutputTrackID string // livekit published track SID ("" until published)
}

// Registry is the worker-side runtime index of active routes per room. It is
// safe for concurrent use; the worker's room actor calls it while applying
// plan diffs. The mutex is held only for map access — never during network
// I/O (blueprint §6.3).
type Registry struct {
	mu     sync.Mutex
	byRoom map[string]map[routeKey]*activeRoute // roomID -> routeKey -> route
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{byRoom: make(map[string]map[routeKey]*activeRoute)}
}

// Apply starts and stops routes so the registry matches plan, returning the
// route keys that were started and stopped. It does NOT itself create Palabra
// sessions or publish tracks — the worker interprets the returned keys; the
// registry only tracks state.
func (r *Registry) Apply(roomID string, plan Plan) (started, stopped []Route) {
	r.mu.Lock()
	defer r.mu.Unlock()

	room := r.byRoom[roomID]
	if room == nil {
		room = make(map[routeKey]*activeRoute)
		r.byRoom[roomID] = room
	}

	want := make(map[routeKey]Route, len(plan.Routes))
	for _, rt := range plan.Routes {
		k := routeKey{rt.SpeakerID, rt.SourceLang, rt.TargetLang}
		want[k] = rt
	}

	// Stop routes that vanished.
	for k := range room {
		if _, ok := want[k]; !ok {
			delete(room, k)
			stopped = append(stopped, Route{SpeakerID: k.SpeakerID, SourceLang: k.Source, TargetLang: k.Target})
		}
	}
	// Start routes that appeared; keep existing ones untouched.
	for k, rt := range want {
		if _, ok := room[k]; !ok {
			room[k] = &activeRoute{SpeakerID: rt.SpeakerID, SourceLang: rt.SourceLang, TargetLang: rt.TargetLang}
			started = append(started, rt)
		}
	}
	return started, stopped
}

// SetSession records the palabra session id for a route.
func (r *Registry) SetSession(roomID string, rt Route, sessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if room := r.byRoom[roomID]; room != nil {
		if a := room[routeKey{rt.SpeakerID, rt.SourceLang, rt.TargetLang}]; a != nil {
			a.SessionID = sessionID
		}
	}
}

// SetOutputTrack records the published LiveKit track SID for a route.
func (r *Registry) SetOutputTrack(roomID string, rt Route, trackSID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if room := r.byRoom[roomID]; room != nil {
		if a := room[routeKey{rt.SpeakerID, rt.SourceLang, rt.TargetLang}]; a != nil {
			a.OutputTrackID = trackSID
		}
	}
}

// Get returns the runtime route, if active.
func (r *Registry) Get(roomID string, rt Route) (activeRoute, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if room := r.byRoom[roomID]; room != nil {
		if a, ok := room[routeKey{rt.SpeakerID, rt.SourceLang, rt.TargetLang}]; ok {
			return *a, true
		}
	}
	return activeRoute{}, false
}

// Count returns the number of active routes in a room.
func (r *Registry) Count(roomID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.byRoom[roomID])
}

// DropRoom clears all runtime state for a room (worker shutdown / migration).
func (r *Registry) DropRoom(roomID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.byRoom, roomID)
}
