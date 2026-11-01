// Package routing implements the scalable translation-routing model:
//
//	Targets(j) = unique { L(i) | i != j }
//
// For each speaker j, one translation route is created per unique listening
// language among the OTHER participants. Recipients who share a listening
// language share the same route and the same translated output stream (safe
// fan-out), so the session count is:
//
//	SessionCount = sum over speakers of distinct recipient target languages
//
// Worst case (every participant a different language, one session per
// source->target pair): N * (N - 1). The planner is pure and deterministic so
// it is fully unit-testable and identical on every worker.
//
// Terminology:
//   - SourceLang S(j): the language the participant SPEAKS. Empty string
//     means "auto-detect", which is a CONFIGURABLE product capability: it is
//     only offered when the Palabra S2S API reliably supports it for the
//     chosen model/languages. Do not assume detection is available.
//   - ListenLang L(i): the ONE language the participant wants to hear.
package routing

import "sort"

// Participant is the routing view of a room member.
type Participant struct {
	ID         string
	Name       string
	SourceLang string // spoken language; "" = auto-detect (capability-gated)
	ListenLang string // preferred listening language (exactly one)
}

// Route is one unique source->target translation path for one speaker.
// RecipientIDs lists every participant (excluding the speaker) whose
// ListenLang == TargetLang; they all share this single output stream.
type Route struct {
	SpeakerID    string
	SourceLang   string
	TargetLang   string
	RecipientIDs []string // deduplicated fan-out set, sorted for determinism
}

// Key returns the stable identity of the route within a room.
func (r Route) Key() string { return r.SpeakerID + "|" + r.SourceLang + "|" + r.TargetLang }

// Plan is the complete set of routes for a room snapshot.
type Plan struct {
	Routes []Route
}

// SessionCount returns the number of Palabra sessions the plan implies
// (one session per unique source->target pair, assuming the provider does NOT
// support multi-target output in one session — verify this capability; if a
// multi-target session IS supported, one speaker's routes can be merged).
func (p Plan) SessionCount() int { return len(p.Routes) }

// PlanRoutes computes the routing plan for a room snapshot.
//
// Rules implemented (blueprint §2, §5):
//  1. A speaker never receives their own translation.
//  2. One route per unique (speaker, target) — recipients sharing a target
//     language share one route.
//  3. No route is created when TargetLang == SourceLang: listeners who prefer
//     the speaker's own language hear the original audio directly (the
//     subscription permission for the ORIGINAL track covers them).
//  4. Participants with an empty ListenLang are skipped defensively (the API
//     layer enforces exactly one listening language; the planner must not
//     crash on a bad snapshot).
//  5. Deterministic output: routes sorted by speaker then target; recipients
//     sorted by ID — identical inputs always yield identical plans on every
//     worker.
func PlanRoutes(participants []Participant) Plan {
	plan := Plan{Routes: []Route{}}

	// Listening-language buckets, excluding the speaker, built once.
	buckets := make(map[string][]string, len(participants)) // lang -> IDs
	for _, p := range participants {
		if p.ListenLang == "" {
			continue
		}
		buckets[p.ListenLang] = append(buckets[p.ListenLang], p.ID)
	}

	for _, speaker := range participants {
		for target, recipientIDs := range buckets {
			if speaker.ListenLang == "" {
				continue
			}
			// Rule 3: nobody needs a translation into the speaker's own source
			// language — they hear the original.
			if speaker.SourceLang != "" && target == speaker.SourceLang {
				continue
			}
			recipients := excludeID(recipientIDs, speaker.ID)
			if len(recipients) == 0 {
				continue // only the speaker himself would listen to it
			}
			sort.Strings(recipients)
			plan.Routes = append(plan.Routes, Route{
				SpeakerID:    speaker.ID,
				SourceLang:   speaker.SourceLang,
				TargetLang:   target,
				RecipientIDs: recipients,
			})
		}
	}
	sort.Slice(plan.Routes, func(i, j int) bool {
		a, b := plan.Routes[i], plan.Routes[j]
		if a.SpeakerID != b.SpeakerID {
			return a.SpeakerID < b.SpeakerID
		}
		return a.TargetLang < b.TargetLang
	})
	return plan
}

// PlanDiff compares two plans and returns which routes must be started and
// which must be stopped to move from old to new. A route is "kept" when an
// identical route (same key) exists in both plans — even if its recipient set
// changed; callers should refresh permissions for kept routes. Diff is used on
// join / leave / language-change so only missing paths are created and unused
// ones are torn down (blueprint §6.4).
func PlanDiff(old, new Plan) (toStart []Route, toStop []Route) {
	oldByKey := make(map[string]Route, len(old.Routes))
	for _, r := range old.Routes {
		oldByKey[r.Key()] = r
	}
	newByKey := make(map[string]Route, len(new.Routes))
	for _, r := range new.Routes {
		newByKey[r.Key()] = r
	}

	for _, r := range new.Routes {
		if _, ok := oldByKey[r.Key()]; !ok {
			toStart = append(toStart, r)
		}
	}
	for _, r := range old.Routes {
		if _, ok := newByKey[r.Key()]; !ok {
			toStop = append(toStop, r)
		}
	}
	sortRoutes(toStart)
	sortRoutes(toStop)
	return toStart, toStop
}

// PlanChanged reports whether recipient sets changed for routes that exist in
// both plans (permissions need refreshing but sessions keep running).
func PlanChanged(old, new Plan) bool {
	oldByKey := make(map[string]Route, len(old.Routes))
	for _, r := range old.Routes {
		oldByKey[r.Key()] = r
	}
	for _, r := range new.Routes {
		if o, ok := oldByKey[r.Key()]; ok {
			if !equalStrings(o.RecipientIDs, r.RecipientIDs) {
				return true
			}
		}
	}
	return false
}

func excludeID(ids []string, id string) []string {
	out := ids[:0:0]
	for _, v := range ids {
		if v != id {
			out = append(out, v)
		}
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sortRoutes(rs []Route) {
	sort.Slice(rs, func(i, j int) bool { return rs[i].Key() < rs[j].Key() })
}
