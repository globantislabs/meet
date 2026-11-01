package routing

import (
	"reflect"
	"testing"
)

// participants4 is the blueprint §5 example: four participants, four different
// languages, nobody shares a listening language.
func participants4() []Participant {
	return []Participant{
		{ID: "A", Name: "Alice", SourceLang: "en", ListenLang: "en"},
		{ID: "B", Name: "Bhanu", SourceLang: "ta", ListenLang: "ta"},
		{ID: "C", Name: "Chika", SourceLang: "ja", ListenLang: "ja"},
		{ID: "D", Name: "Dev", SourceLang: "hi", ListenLang: "hi"},
	}
}

func findRoute(p Plan, speaker, target string) (Route, bool) {
	for _, r := range p.Routes {
		if r.SpeakerID == speaker && r.TargetLang == target {
			return r, true
		}
	}
	return Route{}, false
}

// TestBlueprintFourParticipants verifies the exact §5 example: 12 unique
// source-target streams, no self-translation.
func TestBlueprintFourParticipants(t *testing.T) {
	plan := PlanRoutes(participants4())

	if got := plan.SessionCount(); got != 12 {
		t.Fatalf("SessionCount = %d, want 12", got)
	}
	// Speaker A (en): targets ta, ja, hi.
	for _, tgt := range []string{"ta", "ja", "hi"} {
		if _, ok := findRoute(plan, "A", tgt); !ok {
			t.Errorf("missing route A(en) -> %s", tgt)
		}
	}
	// Speaker B (ta): targets en, ja, hi.
	for _, tgt := range []string{"en", "ja", "hi"} {
		if _, ok := findRoute(plan, "B", tgt); !ok {
			t.Errorf("missing route B(ta) -> %s", tgt)
		}
	}
	// No self-target routes at all.
	for _, r := range plan.Routes {
		if r.SourceLang == r.TargetLang {
			t.Errorf("self-language route leaked: %+v", r)
		}
		if r.SpeakerID == "" || r.TargetLang == "" {
			t.Errorf("incomplete route: %+v", r)
		}
	}
	// Every route to a participant's own language has exactly the other 3 as recipients.
	for _, sp := range []struct{ speaker, target string }{{"A", "ta"}, {"B", "en"}} {
		r, _ := findRoute(plan, sp.speaker, sp.target)
		want := []string{"A", "B", "C", "D"}
		var recipients []string
		for _, id := range want {
			if id != sp.speaker {
				recipients = append(recipients, id)
			}
		}
		if !reflect.DeepEqual(r.RecipientIDs, recipients) {
			t.Errorf("route %s->%s recipients = %v, want %v", sp.speaker, sp.target, r.RecipientIDs, recipients)
		}
	}
}

// TestSharedLanguageDedup verifies blueprint §5 "Shared target-language
// example": 10 participants, 5 of whom prefer Tamil. A given speaker's Tamil
// output is ONE route with 5 recipients — never 5 routes.
func TestSharedLanguageDedup(t *testing.T) {
	// 10 participants: 5 English-listeners, 5 Tamil-listeners. Speaker S0 speaks English.
	ids := []string{"S0", "E1", "E2", "E3", "E4", "T1", "T2", "T3", "T4", "T5"}
	room := make([]Participant, 0, 10)
	for i, id := range ids {
		listen := "en"
		if i >= 5 {
			listen = "ta"
		}
		src := "en"
		if listen == "ta" {
			src = "ta"
		}
		room = append(room, Participant{ID: id, Name: id, SourceLang: src, ListenLang: listen})
	}
	plan := PlanRoutes(room)

	// S0 (en speaker): only one target language needed — ta. ONE route, 5 recipients.
	ta, ok := findRoute(plan, "S0", "ta")
	if !ok {
		t.Fatal("missing route S0(en) -> ta")
	}
	if len(ta.RecipientIDs) != 5 {
		t.Fatalf("S0->ta recipients = %d, want 5 (shared fan-out)", len(ta.RecipientIDs))
	}
	// English listeners hear S0 directly: there must be NO S0->en route.
	if _, ok := findRoute(plan, "S0", "en"); ok {
		t.Fatal("S0->en route should not exist (original audio is shared)")
	}
	// Each Tamil speaker needs exactly 1 route (en). English speakers need 1 each (ta).
	// Total = 5 speakers needing en->ta + 5 speakers needing ta->en = 10 routes.
	if got := plan.SessionCount(); got != 10 {
		t.Fatalf("SessionCount = %d, want 10", got)
	}
}

// TestSelfTranslationExcluded verifies rule 1 even in degenerate setups.
func TestSelfTranslationExcluded(t *testing.T) {
	room := []Participant{
		{ID: "solo", SourceLang: "en", ListenLang: "ta"},
	}
	plan := PlanRoutes(room)
	if len(plan.Routes) != 0 {
		t.Fatalf("expected zero routes for a lone participant, got %d", len(plan.Routes))
	}

	room = []Participant{
		{ID: "x", SourceLang: "en", ListenLang: "ta"},
		{ID: "y", SourceLang: "en", ListenLang: "en"},
	}
	plan = PlanRoutes(room)
	// x speaks en; y wants en => hears original. No routes needed.
	if len(plan.Routes) != 0 {
		t.Fatalf("expected zero routes, got %d: %+v", len(plan.Routes), plan.Routes)
	}
}

// TestSameLanguageRoom verifies a fully homogeneous room costs zero sessions.
func TestSameLanguageRoom(t *testing.T) {
	room := []Participant{
		{ID: "a", SourceLang: "hi", ListenLang: "hi"},
		{ID: "b", SourceLang: "hi", ListenLang: "hi"},
		{ID: "c", SourceLang: "hi", ListenLang: "hi"},
	}
	plan := PlanRoutes(room)
	if plan.SessionCount() != 0 {
		t.Fatalf("same-language room should need 0 sessions, got %d", plan.SessionCount())
	}
}

// TestAutoDetectSpeaker verifies that an empty SourceLang still produces
// routes (detection is delegated to the provider session), and that the
// self-language skip cannot apply (unknown source).
func TestAutoDetectSpeaker(t *testing.T) {
	room := []Participant{
		{ID: "a", SourceLang: "", ListenLang: "en"}, // auto-detect speaker
		{ID: "b", SourceLang: "ta", ListenLang: "ta"},
	}
	plan := PlanRoutes(room)
	if _, ok := findRoute(plan, "a", "ta"); !ok {
		t.Fatal("missing route a(auto) -> ta")
	}
	// b speaks ta, a listens en: b->en required.
	if _, ok := findRoute(plan, "b", "en"); !ok {
		t.Fatal("missing route b(ta) -> en")
	}
	if got := plan.SessionCount(); got != 2 {
		t.Fatalf("SessionCount = %d, want 2", got)
	}
}

// TestPlanDiff verifies join/leave/language-change deltas (blueprint §6.4):
// only missing paths start, only unused paths stop.
func TestPlanDiff(t *testing.T) {
	before := PlanRoutes(participants4())

	// E joins and listens in English -> B/C/D now need en targets; A->en not needed (A speaks en, E hears original).
	after := PlanRoutes(append(participants4(), Participant{ID: "E", SourceLang: "en", ListenLang: "en"}))

	toStart, toStop := PlanDiff(before, after)
	wantStart := map[string]bool{
		"B|ta|en": true, "C|ja|en": true, "D|hi|en": true,
	}
	if len(toStart) != len(wantStart) {
		t.Fatalf("toStart = %d routes, want %d: %+v", len(toStart), len(wantStart), toStart)
	}
	for _, r := range toStart {
		if !wantStart[r.Key()] {
			t.Errorf("unexpected start route %s", r.Key())
		}
	}
	if len(toStop) != 0 {
		t.Fatalf("toStop should be empty on join, got %+v", toStop)
	}

	// Reverse: leaving returns to the before plan.
	start2, stop2 := PlanDiff(after, before)
	if len(start2) != 0 || len(stop2) != 3 {
		t.Fatalf("leave diff: start=%d stop=%d, want 0/3", len(start2), len(stop2))
	}
}

// TestPlanChangedRecipientRefresh verifies that a shared route whose recipient
// set grew (new member shares the listening language) is detected as a
// permission refresh, not a new session.
func TestPlanChangedRecipientRefresh(t *testing.T) {
	base := []Participant{
		{ID: "a", SourceLang: "en", ListenLang: "en"},
		{ID: "b", SourceLang: "ta", ListenLang: "ta"},
		{ID: "c", SourceLang: "ja", ListenLang: "ja"},
	}
	old := PlanRoutes(base)

	// d joins and also listens in Tamil: a->ta gains a recipient, session unchanged.
	newer := PlanRoutes(append(base, Participant{ID: "d", SourceLang: "hi", ListenLang: "ta"}))
	if !PlanChanged(old, newer) {
		t.Fatal("recipient-set change should be reported by PlanChanged")
	}

	// Join of d with a brand-new language also changes routes (start diff), but
	// PlanChanged only tracks shared-route recipient churn for EXISTING keys.
	toStart, _ := PlanDiff(old, newer)
	if len(toStart) == 0 {
		t.Fatal("new listener language should require new routes")
	}
}
