// Session store — room, participant and device state (blueprint §3: Zustand
// for client state). Runtime media state flows in through the LiveKit engine
// callbacks; control state through the Go API client.
"use client";

import { create } from "zustand";
import type { ConnState } from "@/lib/livekit-engine";
import type {
  JoinResult,
  ParticipantViewDTO,
  MyViewDTO,
  RouteSummaryDTO,
  RoomDTO,
  SnapshotDTO,
} from "@/lib/api";

export type View = "landing" | "create" | "join" | "room" | "ended";

export interface UiError {
  code: string;
  message: string;
}

interface SessionState {
  view: View;
  pendingJoinCode: string;
  pendingRoomId: string; // deep link /room/{id}?code=

  // Identity / preferences
  displayName: string;
  sourceLang: string; // "" = auto-detect (provider capability)
  listenLang: string;
  micDeviceId: string;
  speakerDeviceId: string;

  // Session (from JoinResult)
  room: RoomDTO | null;
  participantId: string;
  apiToken: string;
  livekitUrl: string;
  livekitToken: string;

  // Runtime (from snapshots + LiveKit)
  participants: ParticipantViewDTO[];
  me: MyViewDTO | null;
  routes: RouteSummaryDTO | null;
  connState: ConnState;
  speaking: string[]; // livekit identities currently speaking
  muted: boolean;
  micLevel: number;
  subscribedTracks: string[]; // live remote audio track names
  error: UiError | null;
  endedReason: string;
  joinedAtMs: number;

  setView: (v: View) => void;
  setPendingJoinCode: (c: string) => void;
  setPendingRoomId: (id: string) => void;
  setDisplayName: (n: string) => void;
  setSourceLang: (c: string) => void;
  setListenLang: (c: string) => void;
  setMicDevice: (id: string) => void;
  setSpeakerDevice: (id: string) => void;

  applyJoin: (res: JoinResult) => void;
  applySnapshot: (s: SnapshotDTO) => void;
  setConnState: (s: ConnState) => void;
  setSpeaking: (ids: string[]) => void;
  setMuted: (m: boolean) => void;
  setMicLevel: (l: number) => void;
  setSubscribedTracks: (t: string[]) => void;
  setError: (e: UiError | null) => void;
  endSession: (reason: string) => void;
  reset: () => void;
}

const initial = {
  view: "landing" as View,
  pendingJoinCode: "",
  pendingRoomId: "",
  displayName: "",
  sourceLang: "en",
  listenLang: "ta",
  micDeviceId: "",
  speakerDeviceId: "",
  room: null,
  participantId: "",
  apiToken: "",
  livekitUrl: "",
  livekitToken: "",
  participants: [] as ParticipantViewDTO[],
  me: null as MyViewDTO | null,
  routes: null as RouteSummaryDTO | null,
  connState: "idle" as ConnState,
  speaking: [] as string[],
  muted: false,
  micLevel: 0,
  subscribedTracks: [] as string[],
  error: null,
  endedReason: "",
  joinedAtMs: 0,
};

export const useSession = create<SessionState>((set) => ({
  ...initial,
  setView: (view) => set({ view }),
  setPendingJoinCode: (pendingJoinCode) => set({ pendingJoinCode }),
  setPendingRoomId: (pendingRoomId) => set({ pendingRoomId }),
  setDisplayName: (displayName) => set({ displayName }),
  setSourceLang: (sourceLang) => set({ sourceLang }),
  setListenLang: (listenLang) => set({ listenLang }),
  setMicDevice: (micDeviceId) => set({ micDeviceId }),
  setSpeakerDevice: (speakerDeviceId) => set({ speakerDeviceId }),

  applyJoin: (res) =>
    set({
      view: "room",
      room: res.room,
      participantId: res.participant.id,
      apiToken: res.participant_token,
      livekitUrl: res.livekit_url,
      livekitToken: res.livekit_token,
      sourceLang: res.participant.source_lang,
      listenLang: res.participant.listen_lang,
      displayName: res.participant.display_name,
      participants: [],
      me: null,
      routes: null,
      joinedAtMs: Date.now(),
      error: null,
      endedReason: "",
    }),

  applySnapshot: (snap) =>
    set((cur) => ({
      room: snap.room,
      participants: snap.participants,
      me: snap.me,
      routes: snap.routes,
      listenLang: snap.me?.listen_lang ?? cur.listenLang,
      sourceLang: snap.me?.source_lang ?? cur.sourceLang,
    })),

  setConnState: (connState) => set({ connState }),
  setSpeaking: (speaking) => set({ speaking }),
  setMuted: (muted) => set({ muted }),
  setMicLevel: (micLevel) => set({ micLevel }),
  setSubscribedTracks: (subscribedTracks) => set({ subscribedTracks }),

  setError: (error) => set({ error }),
  endSession: (endedReason) => set({ view: "ended", endedReason, connState: "disconnected" }),
  reset: () => set({ ...initial, displayName: "", micDeviceId: "", speakerDeviceId: "" }),
}));

// Helpers ────────────────────────────────────────────────────────────────────

export function myParticipant(s: SessionState): ParticipantViewDTO | undefined {
  return s.participants.find((p) => p.id === s.participantId);
}

// Routes addressed to me, with live status derived from real subscribed
// tracks (translated track names are `tr_{speaker}_{lang}`).
export function routesForMe(
  s: SessionState
): { speaker: string; from: string; to: string; live: boolean }[] {
  if (!s.me) return [];
  const byId = new Map(s.participants.map((p) => [p.id, p.name]));
  const live = new Set(s.subscribedTracks);
  const others = s.participants.filter((p) => p.id !== s.participantId);
  return others
    .map((p) => {
      if (p.source_lang && p.source_lang === s.listenLang) {
        // Original audio (same language) — live when the mic track is on.
        return {
          speaker: p.name,
          from: p.source_lang,
          to: s.listenLang,
          live: live.has(`mic_${p.id}`),
          original: true,
        };
      }
      const name = `tr_${p.id}_${s.listenLang}`;
      return {
        speaker: p.name,
        from: p.source_lang || "auto",
        to: s.listenLang,
        live: live.has(name),
        original: false,
      };
    })
    .map(({ speaker, from, to, live: isLive }) => ({ speaker, from, to, live: isLive }));
}
