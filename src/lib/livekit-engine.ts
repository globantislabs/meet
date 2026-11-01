// LiveKitEngine — browser realtime transport (blueprint §4, §7):
//   - connects with the short-lived token issued by the Go API
//   - publishes exactly ONE microphone track named `mic_{participantId}`
//   - subscribes ONLY to allowed tracks: original mic audio of speakers in
//     your listening language + translated outputs `tr_{speaker}_{lang}`
//     addressed to you (the worker enforces this at the publisher side too)
//   - attaches remote audio to persistent <audio> elements (no per-chunk
//     element churn, blueprint §7.4)
//   - reports REAL speaking activity via ActiveSpeakersChanged (§11.1:
//     no fake waveforms)
"use client";

import {
  ConnectionState,
  DisconnectReason,
  LocalAudioTrack,
  Participant,
  Room,
  RoomEvent,
  Track,
  createLocalAudioTrack,
  type RemoteTrack,
  type RemoteTrackPublication,
} from "livekit-client";

export type ConnState = "idle" | "connecting" | "connected" | "reconnecting" | "disconnected";

export interface LiveKitCallbacks {
  onStateChanged?: (s: ConnState) => void;
  onActiveSpeakers?: (identities: string[]) => void;
  onParticipantsChanged?: () => void;
  onRemoteAudioAdded?: (identity: string, trackName: string) => void;
  onPlaybackBlocked?: () => void;
  onError?: (message: string) => void;
}

// Track-name conventions shared with backend-go/internal/routing and worker:
//   original mic  → `mic_{participantId}`
//   translated    → `tr_{speakerId}_{targetLang}`
export function micTrackName(participantId: string) {
  return `mic_${participantId}`;
}
export function translatedTrackName(speakerId: string, targetLang: string) {
  return `tr_${speakerId}_${targetLang}`;
}

export class LiveKitEngine {
  private room: Room | null = null;
  private mic: LocalAudioTrack | null = null;
  private audioEls = new Map<string, HTMLAudioElement>();
  private cbs: LiveKitCallbacks = {};
  private state: ConnState = "idle";
  // Track names this participant may subscribe to (recomputed from the
  // server-provided grants on every snapshot refresh).
  private allowed = new Set<string>();

  setCallbacks(cbs: LiveKitCallbacks) {
    this.cbs = cbs;
  }

  private setState(s: ConnState) {
    this.state = s;
    this.cbs.onStateChanged?.(s);
  }

  getState(): ConnState {
    return this.state;
  }

  localIdentity(): string | undefined {
    return this.room?.localParticipant.identity;
  }

  // Remote participants currently connected (excludes self).
  remoteParticipants(): Participant[] {
    if (!this.room) return [];
    return Array.from(this.room.remoteParticipants.values());
  }

  async connect(url: string, token: string, micStream?: MediaStream, myParticipantId?: string) {
    this.setState("connecting");
    const room = new Room({
      adaptiveStream: false,
      dynacast: false,
    });
    this.room = room;

    room.on(RoomEvent.Connected, () => this.setState("connected"));
    room.on(RoomEvent.Reconnecting, () => this.setState("reconnecting"));
    room.on(RoomEvent.Reconnected, () => this.setState("connected"));
    room.on(RoomEvent.Disconnected, (reason) => {
      this.setState("disconnected");
      if (reason !== undefined && reason !== DisconnectReason.CLIENT_INITIATED) {
        this.cbs.onError?.("You were disconnected from the room.");
      }
    });
    room.on(RoomEvent.ActiveSpeakersChanged, (speakers: Participant[]) => {
      this.cbs.onActiveSpeakers?.(speakers.map((s) => s.identity));
    });
    room.on(RoomEvent.ParticipantConnected, () => this.cbs.onParticipantsChanged?.());
    room.on(RoomEvent.ParticipantDisconnected, () => this.cbs.onParticipantsChanged?.());

    // A track was published: subscribe iff it is on the allowed list.
    room.on(RoomEvent.TrackPublished, (pub: RemoteTrackPublication) => {
      if (pub.kind !== Track.Kind.Audio) return;
      const name = pub.trackName || pub.trackSid;
      try {
        pub.setSubscribed(this.allowed.has(name));
      } catch {
        /* publication may have been unpublished concurrently */
      }
    });

    room.on(
      RoomEvent.TrackSubscribed,
      (track: RemoteTrack, pub: RemoteTrackPublication, participant: Participant) => {
        if (track.kind === Track.Kind.Audio) {
          this.attachRemoteAudio(participant.identity, pub.trackName ?? pub.trackSid, track);
          this.cbs.onRemoteAudioAdded?.(participant.identity, pub.trackName ?? "");
          this.cbs.onParticipantsChanged?.();
        }
      }
    );
    room.on(RoomEvent.TrackUnsubscribed, (_t: RemoteTrack, pub: RemoteTrackPublication, p: Participant) => {
      const key = `${p.identity}:${pub.trackName ?? pub.trackSid}`;
      const el = this.audioEls.get(key);
      if (el) {
        el.remove();
        this.audioEls.delete(key);
      }
      this.cbs.onParticipantsChanged?.();
    });

    try {
      // Subscription discipline (blueprint §7.4): nothing is subscribed
      // automatically; the engine subscribes explicitly by track name.
      await room.connect(url, token, { autoSubscribe: false });
    } catch (e) {
      this.setState("disconnected");
      const msg = e instanceof Error ? e.message : String(e);
      throw new Error(`Could not join the room: ${msg}`);
    }

    // Publish exactly one microphone track (blueprint §2 rule 3).
    try {
      this.mic =
        (micStream ? new LocalAudioTrack(micStream.getAudioTracks()[0]) : null) ??
        (await createLocalAudioTrack({
          echoCancellation: true,
          noiseSuppression: true,
          autoGainControl: true,
        }));
      await room.localParticipant.publishTrack(this.mic, {
        source: Track.Source.Microphone,
        name: myParticipantId ? micTrackName(myParticipantId) : undefined,
      });
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e);
      throw new Error(`Microphone publish failed: ${msg}`);
    }

    // Subscribe to anything already published that we are allowed to hear.
    this.applyAllowedToPublications();
  }

  // attachRemoteAudio plays a subscribed (translated or original) track via a
  // persistent audio element; blocked autoplay is surfaced to the UI.
  private attachRemoteAudio(identity: string, trackName: string, track: RemoteTrack) {
    const key = `${identity}:${trackName}`;
    if (this.audioEls.has(key)) return;
    const el = track.attach() as HTMLAudioElement;
    el.autoplay = true;
    el.dataset.identity = identity;
    el.dataset.trackName = trackName;
    el.style.display = "none";
    document.body.appendChild(el);
    this.audioEls.set(key, el);
    el.play().catch(() => this.cbs.onPlaybackBlocked?.());
  }

  // setAllowedTracks updates the subscription whitelist (called on every room
  // snapshot from the Go API): original speakers + translated outputs.
  async setAllowedTracks(trackNames: string[]) {
    this.allowed = new Set(trackNames);
    await this.applyAllowedToPublications();
  }

  private async applyAllowedToPublications() {
    if (!this.room) return;
    for (const p of this.room.remoteParticipants.values()) {
      p.trackPublications.forEach((pub) => {
        if (pub.kind !== Track.Kind.Audio) return;
        const name = pub.trackName || pub.trackSid;
        const want = this.allowed.has(name);
        try {
          if (want && !pub.isSubscribed) {
            pub.setSubscribed(true);
          } else if (!want && pub.isSubscribed) {
            pub.setSubscribed(false);
          }
        } catch {
          /* transient publication state race; next snapshot retries */
        }
      });
    }
  }

  setMicMuted(muted: boolean) {
    if (!this.mic) return;
    if (muted) {
      void this.mic.mute();
    } else {
      void this.mic.unmute();
    }
  }

  isMicMuted(): boolean {
    return this.mic?.isMuted ?? false;
  }

  async changeMicDevice(deviceId: string) {
    if (!this.mic) return;
    await this.mic.setDeviceId(deviceId);
  }

  // subscribedAudioTrackNames lists currently subscribed remote audio names
  // (the UI uses this to show which translation streams are live).
  subscribedAudioTrackNames(): string[] {
    const names: string[] = [];
    for (const p of this.remoteParticipants()) {
      p.trackPublications.forEach((pub) => {
        if (pub.kind === Track.Kind.Audio && pub.isSubscribed) {
          names.push(pub.trackName || pub.trackSid);
        }
      });
    }
    return names;
  }

  async disconnect() {
    try {
      this.audioEls.forEach((el) => el.remove());
      this.audioEls.clear();
      if (this.mic) {
        this.mic.stop();
        this.mic = null;
      }
      await this.room?.disconnect();
    } finally {
      this.room = null;
      this.allowed = new Set();
      this.setState("disconnected");
    }
  }
}

export const livekitEngine = new LiveKitEngine();

// Re-export so screens can import ConnectionState without a second import.
export { ConnectionState };
