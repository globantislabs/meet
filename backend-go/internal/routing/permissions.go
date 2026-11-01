package routing

// SubscriptionGrants computes, for one participant, which LiveKit tracks it
// may subscribe to. Two categories:
//
//  1. ORIGINAL audio: speakers whose SourceLang equals the participant's
//     ListenLang hear the original microphone track directly (no translation
//     needed — blueprint rule 3 in the planner).
//  2. TRANSLATED audio: every published output track `tr_{speaker}_{target}`
//     where target == the participant's ListenLang, produced by any speaker
//     other than the participant.
//
// The returned map is track-name -> allowed for the translated outputs plus a
// boolean for original mic audio, which the worker converts into LiveKit
// TrackSubscriptionPermissions (livekit_protocol models.ParticipantPermission)
// via the LiveKit server API (internal/livekit UpdateParticipant).
//
// SELF-EXCLUSION GUARANTEE: a participant is never granted their own
// translated output — enforced here (blueprint §2 rule 5) and again at
// publish time in the worker's OutputTrackManager.
func SubscriptionGrants(participantID string, plan Plan, participants []Participant) (originalSpeakers map[string]bool, translatedTracks map[string]bool) {
	originalSpeakers = make(map[string]bool)
	translatedTracks = make(map[string]bool)

	var self *Participant
	for i := range participants {
		if participants[i].ID == participantID {
			self = &participants[i]
			break
		}
	}
	if self == nil {
		return originalSpeakers, translatedTracks
	}

	// Original audio from speakers talking in the listener's language.
	for _, p := range participants {
		if p.ID == participantID || p.ListenLang == "" {
			continue
		}
		if p.SourceLang != "" && p.SourceLang == self.ListenLang {
			originalSpeakers[p.ID] = true
		}
	}

	// Translated outputs addressed to this participant's listening language.
	for _, r := range plan.Routes {
		if r.TargetLang != self.ListenLang {
			continue
		}
		for _, rec := range r.RecipientIDs {
			if rec == participantID {
				// Track name convention MUST match internal/worker/output.go.
				translatedTracks[OutputTrackName(r.SpeakerID, r.TargetLang)] = true
				break
			}
		}
	}
	return originalSpeakers, translatedTracks
}

// OutputTrackName is the canonical track-name convention for translated
// outputs. Keep in ONE place: routing (used by permissions) and worker output
// manager must agree.
func OutputTrackName(speakerID, targetLang string) string {
	return "tr_" + speakerID + "_" + targetLang
}
