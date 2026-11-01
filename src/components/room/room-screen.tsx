"use client";

// RoomScreen — the active group room (blueprint §11.2.D). This view IS the
// product's dark meeting canvas: a media-led stage (canvas-* tokens) where
// chrome stays quiet so voices, routes and captions lead. LIGHT APP, DARK
// STAGE — the canvas is the one dark surface in the product.
//   header (brand · room code · timer · connection) +
//   participant grid (name · speaks · hears · REAL speaking indicator · mic) +
//   translation status (your listening language + live routes from the worker) +
//   controls bar (mute · devices · language · invite · leave).
//
// Truth sources: a 2.5 s room-state poll of the Go API (participants,
// route states, my grants) + LiveKit events (speakers, subscribed tracks).
// The subscription whitelist is recomputed on every snapshot.

import { useEffect, useMemo, useRef, useState } from "react";
import { motion } from "framer-motion";
import {
  Copy,
  Ear,
  Globe2,
  Loader2,
  Mic,
  MicOff,
  PhoneOff,
  Settings2,
  Users,
  Volume2,
  Check,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { cn } from "@/lib/utils";
import { EASE, LiveDot } from "@/components/app/ui-bits";
import { langDisplay, ListeningLanguageSelect } from "./controls";
import { AudioLevelMeter, ConnectionBadge, MicOffIndicator, ReconnectPanel, SpeakingIndicator } from "./states";
import { useSession, routesForMe } from "@/stores/session-store";
import { livekitEngine } from "@/lib/livekit-engine";
import { audioEngine } from "@/lib/audio-engine";
import { updatePreferences, leaveRoom, getRoomState, createInvite, ApiError } from "@/lib/api";
import { useToast } from "@/hooks/use-toast";

function useElapsed(sinceMs: number) {
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, []);
  const s = Math.max(0, Math.floor((now - sinceMs) / 1000));
  const mm = String(Math.floor(s / 60)).padStart(2, "0");
  const ss = String(s % 60).padStart(2, "0");
  return `${mm}:${ss}`;
}

export function RoomScreen({
  onLeave,
  onEnded,
  onOpenSettings,
}: {
  onLeave: () => void;
  onEnded: () => void;
  onOpenSettings: () => void;
}) {
  const s = useSession();
  const { toast } = useToast();
  const timer = useElapsed(s.joinedAtMs);
  const [inviteOpen, setInviteOpen] = useState(false);
  const [langOpen, setLangOpen] = useState(false);
  const [pendingLang, setPendingLang] = useState(s.listenLang);
  const [switching, setSwitching] = useState(false);
  const [copied, setCopied] = useState(false);
  const leavingRef = useRef(false);

  // ── Snapshot poll: participants, route states and my grants ───────────────
  useEffect(() => {
    let stopped = false;
    const poll = async () => {
      const cur = useSession.getState();
      if (!cur.room || !cur.apiToken || stopped) return;
      try {
        const snap = await getRoomState(cur.room.id, cur.apiToken);
        if (stopped) return;
        cur.applySnapshot(snap);
        // Recompute the subscription whitelist from server grants.
        if (snap.me) {
          const allowed = [
            ...snap.me.original_speakers.map((id) => `mic_${id}`),
            ...snap.me.translated_tracks,
          ];
          await livekitEngine.setAllowedTracks(allowed);
        }
        useSession.getState().setSubscribedTracks(livekitEngine.subscribedAudioTrackNames());
        // The room was ended remotely → leave gracefully.
        if (snap.room.status !== "active") onEnded();
      } catch (e) {
        if (e instanceof ApiError && e.status === 401) onEnded();
        /* transient poll errors are ignored; next tick retries */
      }
    };
    void poll();
    const t = setInterval(poll, 2500);
    return () => {
      stopped = true;
      clearInterval(t);
    };

  }, []);

  const identityOf = useMemo(() => {
    const m = new Map<string, string>(); // participantId -> livekit identity (= participant id server-side)
    s.participants.forEach((p) => m.set(p.id, p.id));
    return m;
  }, [s.participants]);

  const speakingIds = new Set(s.speaking);
  const amISpeaking = speakingIds.has(livekitEngine.localIdentity() ?? "");

  const myRoutes = useMemo(() => routesForMe(useSession.getState()), [s.me, s.participants, s.subscribedTracks, s.listenLang]);

  const copyInvite = async () => {
    try {
      let link: string | undefined;
      if (s.room && s.apiToken) {
        const res = await createInvite(s.room.id, s.apiToken).catch(() => null);
        link = res?.link ?? undefined;
      }
      const url = link ?? `${window.location.origin}/?join=${s.room?.code ?? ""}`;
      await navigator.clipboard.writeText(url);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      toast({ title: "Could not copy", description: "Copy the room code manually: " + s.room?.code });
    }
  };

  const changeLanguage = async () => {
    if (!s.room || pendingLang === s.listenLang) {
      setLangOpen(false);
      return;
    }
    setSwitching(true);
    try {
      // Explicit session reconfiguration (blueprint §2 rule 10): the API
      // re-plans; the worker starts new routes and switches only when ready.
      const snap = await updatePreferences(s.room.id, s.apiToken, { listen_lang: pendingLang });
      useSession.getState().applySnapshot(snap);
      setLangOpen(false);
      toast({
        title: `Switching to ${langDisplay(pendingLang)}`,
        description: "Translation path is starting — audio will switch once it is ready.",
      });
    } catch (e) {
      const api = e as ApiError;
      toast({ title: "Could not switch language", description: api.message, variant: "destructive" });
    } finally {
      setSwitching(false);
    }
  };

  const doLeave = async () => {
    if (leavingRef.current) return;
    leavingRef.current = true;
    try {
      if (s.room) await leaveRoom(s.room.id, s.apiToken).catch(() => undefined);
    } finally {
      await livekitEngine.disconnect();
      audioEngine.stopMic();
      onLeave();
    }
  };

  // Auto-exit when disconnected (room closed remotely, network drop, 401).
  useEffect(() => {
    if (s.connState === "reconnecting") return;
    if (s.connState === "disconnected" && s.room) {
      const t = setTimeout(() => onEnded(), 1500);
      return () => clearTimeout(t);
    }

  }, [s.connState]);

  return (
    <div className="flex min-h-dvh flex-col bg-canvas text-canvas-foreground" data-testid="room-screen">
      {/* Header — quiet stage chrome: data in mono, state in a pill */}
      <header className="border-b border-canvas-border">
        <div className="mx-auto flex max-w-4xl items-center justify-between gap-3 px-4 py-3 sm:px-6">
          <div className="flex min-w-0 items-center gap-3">
            <span className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-canvas-accent/15 text-canvas-accent">
              <Globe2 className="h-4 w-4" aria-hidden />
            </span>
            <div className="min-w-0">
              <p className="truncate text-sm font-semibold leading-tight text-canvas-foreground">
                {s.room?.name || "Global conversation"}
              </p>
              <p className="font-mono text-xs tracking-[0.25em] text-canvas-muted">{s.room?.code}</p>
            </div>
          </div>
          <div className="flex shrink-0 items-center gap-3">
            <span className="font-mono text-sm text-canvas-foreground" data-testid="room-timer">{timer}</span>
            <ConnectionBadge state={s.connState} tone="canvas" />
          </div>
        </div>
      </header>

      {/* Body */}
      <main className="mx-auto w-full max-w-4xl flex-1 space-y-6 px-4 py-6 sm:px-6">
        {/* Participants */}
        <section aria-label="Participants">
          <h2 className="mb-3 flex items-center gap-2 font-mono text-[11px] font-medium uppercase tracking-[0.15em] text-canvas-muted">
            <Users className="h-3.5 w-3.5" aria-hidden /> Participants · {s.participants.length || 1}/{s.room?.capacity ?? "?"}
          </h2>
          <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
            {/* Self card */}
            <ParticipantCard
              name={s.displayName || "You"}
              isSelf
              speaking={amISpeaking && !s.muted}
              muted={s.muted}
              speaks={s.sourceLang}
              hears={s.listenLang}
              level={<AudioLevelMeter level={s.muted ? 0 : s.micLevel} tone="canvas" />}
            />
            {s.participants
              .filter((p) => p.id !== s.participantId)
              .map((p) => (
                <ParticipantCard
                  key={p.id}
                  name={p.name}
                  speaking={speakingIds.has(identityOf.get(p.id) ?? "")}
                  muted={!p.mic_enabled}
                  speaks={p.source_lang}
                  hears={p.listen_lang}
                />
              ))}
          </div>
        </section>

        {/* Translation status */}
        <TranslationPanel routes={myRoutes} />
      </main>

      {/* Controls — stage chrome, ≥44px touch targets */}
      <footer className="sticky bottom-0 border-t border-canvas-border bg-canvas/90 backdrop-blur">
        <div className="mx-auto flex max-w-4xl flex-wrap items-center justify-center gap-2 px-4 py-3 sm:gap-3 sm:px-6">
          <Button
            size="lg"
            aria-pressed={s.muted}
            className={cn(
              "h-12 flex-1 rounded-lg border sm:flex-none sm:px-6",
              s.muted
                ? "border-canvas-danger/40 bg-canvas-danger/15 text-canvas-danger hover:bg-canvas-danger/25"
                : "border-transparent bg-primary text-primary-foreground hover:bg-primary/90"
            )}
            onClick={() => {
              const m = !s.muted;
              livekitEngine.setMicMuted(m);
              s.setMuted(m);
            }}
          >
            {s.muted ? <MicOff className="mr-2 h-4 w-4" /> : <Mic className="mr-2 h-4 w-4" />}
            {s.muted ? "Unmute" : "Mute"}
          </Button>

          <Button
            size="lg"
            variant="outline"
            className="h-12 rounded-lg border-canvas-border bg-canvas-card text-canvas-foreground hover:bg-white/10 hover:text-canvas-foreground"
            onClick={onOpenSettings}
          >
            <Settings2 className="mr-2 h-4 w-4" /> Devices
          </Button>

          <Button
            size="lg"
            variant="outline"
            className="h-12 rounded-lg border-canvas-border bg-canvas-card text-canvas-foreground hover:bg-white/10 hover:text-canvas-foreground"
            onClick={() => {
              setPendingLang(s.listenLang);
              setLangOpen(true);
            }}
          >
            <Ear className="mr-2 h-4 w-4" />
            <span className="hidden sm:inline">Hear</span>&nbsp;{langDisplay(s.listenLang)}
          </Button>

          <Button
            size="lg"
            variant="outline"
            className="h-12 rounded-lg border-canvas-border bg-canvas-card text-canvas-foreground hover:bg-white/10 hover:text-canvas-foreground"
            onClick={() => setInviteOpen(true)}
          >
            <Users className="mr-2 h-4 w-4" />
            <span className="hidden sm:inline">Invite</span>
          </Button>

          <Button
            size="lg"
            className="h-12 rounded-lg bg-destructive text-white hover:bg-destructive/90"
            onClick={doLeave}
          >
            <PhoneOff className="mr-2 h-4 w-4" />
            <span className="hidden sm:inline">Leave</span>
          </Button>
        </div>
      </footer>

      {/* Reconnecting overlay */}
      {s.connState === "reconnecting" && <ReconnectPanel onLeave={doLeave} />}

      {/* Invite dialog */}
      <Dialog open={inviteOpen} onOpenChange={setInviteOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Invite to the conversation</DialogTitle>
            <DialogDescription>
              Share this link or the code. Everyone picks the language they want to hear.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-4">
            <div className="rounded-lg border bg-muted/50 p-4 text-center">
              <p className="font-mono text-[10px] font-medium uppercase tracking-[0.2em] text-muted-foreground">Room code</p>
              <p className="mt-1.5 font-mono text-3xl tracking-[0.3em] text-foreground">{s.room?.code}</p>
            </div>
            <Button className="w-full" onClick={copyInvite}>
              {copied ? <Check className="mr-2 h-4 w-4" /> : <Copy className="mr-2 h-4 w-4" />}
              {copied ? "Link copied" : "Copy invitation link"}
            </Button>
          </div>
        </DialogContent>
      </Dialog>

      {/* Language change dialog — explicit reconfiguration (blueprint §6.4) */}
      <Dialog open={langOpen} onOpenChange={setLangOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Change what you hear</DialogTitle>
            <DialogDescription>
              This is a live reconfiguration: a new translation path starts first, then your audio switches. Nothing changes silently.
            </DialogDescription>
          </DialogHeader>
          <ListeningLanguageSelect value={pendingLang} onChange={setPendingLang} />
          <Button className="w-full" disabled={switching} onClick={changeLanguage}>
            {switching && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
            Switch to {langDisplay(pendingLang)}
          </Button>
        </DialogContent>
      </Dialog>
    </div>
  );
}

function ParticipantCard({
  name,
  isSelf,
  speaking,
  muted,
  speaks,
  hears,
  level,
}: {
  name: string;
  isSelf?: boolean;
  speaking: boolean;
  muted: boolean;
  speaks: string;
  hears: string;
  level?: React.ReactNode;
}) {
  const initial = (name || "?").trim().charAt(0).toUpperCase();
  return (
    <motion.div
      initial={{ opacity: 0, y: 10 }}
      animate={{ opacity: 1, y: 0 }}
      transition={{ duration: 0.45, ease: [...EASE] }}
      className={cn(
        "rounded-lg border p-4 transition-colors duration-300",
        speaking ? "border-canvas-accent/40 bg-canvas-accent/10" : "border-canvas-border bg-canvas-card"
      )}
      data-testid="participant-card"
    >
      <div className="flex items-center justify-between gap-3">
        <div className="flex min-w-0 items-center gap-3">
          <span
            className={cn(
              "flex h-10 w-10 shrink-0 items-center justify-center rounded-full font-mono text-sm transition-all duration-300",
              speaking
                ? "bg-canvas-accent/20 text-canvas-accent ring-2 ring-canvas-accent/50"
                : "bg-white/10 text-canvas-foreground"
            )}
          >
            {initial}
          </span>
          <div className="min-w-0">
            <p className="truncate text-sm font-medium leading-tight text-canvas-foreground">
              {name} {isSelf && <span className="text-xs text-canvas-muted">(you)</span>}
            </p>
            <div className="mt-0.5 flex items-center gap-1.5 truncate text-xs text-canvas-muted">
              <span className="truncate">speaks {langDisplay(speaks)}</span>
              <span aria-hidden>·</span>
              <span className="inline-flex shrink-0 items-center gap-1">
                <Ear className="h-3 w-3" aria-hidden /> {langDisplay(hears)}
              </span>
            </div>
          </div>
        </div>
        <div className="flex shrink-0 flex-col items-end gap-1.5">
          {muted ? <MicOffIndicator /> : <SpeakingIndicator speaking={speaking} tone="canvas" />}
          {level}
        </div>
      </div>
    </motion.div>
  );
}

// TranslationPanel — my listening language + per-speaker route states from
// the REAL worker (starting / live / error) + route summary counters.
function TranslationPanel({ routes }: { routes: { speaker: string; from: string; to: string; live: boolean }[] }) {
  const s = useSession();
  return (
    <section aria-label="Translation status" className="rounded-xl border border-canvas-border">
      <div className="flex flex-wrap items-center justify-between gap-2 border-b border-canvas-border px-4 py-3">
        <h2 className="flex items-center gap-2 font-mono text-[11px] font-medium uppercase tracking-[0.15em] text-canvas-muted">
          <Volume2 className="h-3.5 w-3.5" aria-hidden /> Translation
        </h2>
        <div className="flex flex-wrap items-center gap-2">
          {s.routes && s.routes.total > 0 && (
            <span className="inline-flex items-center gap-1.5 rounded-full border border-canvas-border bg-canvas-card px-2.5 py-1 font-mono text-[11px] text-canvas-foreground">
              {s.routes.ready}/{s.routes.total} live
              {s.routes.error > 0 && <span className="text-canvas-danger">· {s.routes.error} failed</span>}
            </span>
          )}
          <span className="inline-flex items-center gap-1.5 rounded-full border border-canvas-accent/30 bg-canvas-accent/10 px-2.5 py-1 text-[11px] font-medium text-canvas-accent">
            <Ear className="h-3 w-3" aria-hidden /> You hear {langDisplay(s.listenLang)}
          </span>
        </div>
      </div>
      <div className="px-4 py-4">
        {s.me && !s.me.listen_ready && (
          <p className="mb-3 rounded-lg border border-canvas-warning/25 bg-canvas-warning/10 px-3 py-2 text-sm text-canvas-warning">
            Translation paths are still starting or unavailable — you will hear speakers as soon as they are ready.
          </p>
        )}
        {routes.length === 0 ? (
          <p className="text-sm leading-relaxed text-canvas-muted">
            No translation paths yet — when other participants speak a different language, their audio is translated here.
          </p>
        ) : (
          <ul className="space-y-2" data-testid="translation-routes">
            {routes.map((r, i) => (
              <motion.li
                key={i}
                initial={{ opacity: 0, y: 8 }}
                animate={{ opacity: 1, y: 0 }}
                transition={{ duration: 0.45, ease: [...EASE] }}
                className="flex items-center justify-between gap-3 rounded-lg border border-canvas-border bg-canvas-card px-3 py-2 text-sm"
              >
                <span className="flex min-w-0 items-center gap-2">
                  <span className="truncate font-medium text-canvas-foreground">{r.speaker}</span>
                  <span className="shrink-0 font-mono text-xs uppercase tracking-[0.12em] text-canvas-muted">
                    {langDisplay(r.from)} → {langDisplay(r.to)}
                  </span>
                </span>
                {r.live ? (
                  <span className="flex shrink-0 items-center gap-1.5 font-mono text-[11px] uppercase tracking-[0.12em] text-canvas-accent">
                    <LiveDot /> live
                  </span>
                ) : (
                  <span className="flex shrink-0 items-center gap-1.5 text-xs text-canvas-warning">
                    <Loader2 className="h-3 w-3 animate-spin" aria-hidden /> starting…
                  </span>
                )}
              </motion.li>
            ))}
          </ul>
        )}
      </div>
    </section>
  );
}
