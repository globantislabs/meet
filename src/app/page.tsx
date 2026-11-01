"use client";

// VoiceBridge — Multilingual Group Voice Translator (blueprint §11.2.A).
// Single-page flow: landing → create → join → live room → ended. LIGHT THEME.
// All realtime work goes through the real Go API (Render) + LiveKit Cloud +
// the server-side translation worker. NO demo/simulation mode: without a
// configured backend the UI shows the honest "not configured" error states.
//
// Design language: "Bridge Connect" — restrained cool neutrals, one deep-teal
// brand accent spent carefully (primary actions / selected / live only),
// a three-font system (Space Grotesk voice, Instrument Serif for the single
// editorial hero moment, Geist Mono for every piece of data), and a dark
// media-led canvas for the live stage. No gradients, no glow, no decorative
// color. The delight comes from choreographed motion: one expo-out easing,
// staggered scroll reveals, a living stage preview and honest live indicators.

import { useCallback, useEffect, useRef, useState, Suspense } from "react";
import { useSearchParams } from "next/navigation";
import { motion, useReducedMotion } from "framer-motion";
import {
  AudioLines,
  AudioWaveform,
  Ear,
  EarOff,
  Globe2,
  KeyRound,
  Lock,
  Mic,
  MonitorSmartphone,
  Route,
  ShieldCheck,
  Timer,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { LANGUAGES, findLanguage } from "@/lib/languages";
import { API_URL, health, ApiError } from "@/lib/api";
import { audioEngine } from "@/lib/audio-engine";
import { livekitEngine } from "@/lib/livekit-engine";
import { useSession } from "@/stores/session-store";
import { CreateRoomForm, JoinRoomForm } from "@/components/room/forms";
import { RoomScreen } from "@/components/room/room-screen";
import { SettingsDialog } from "@/components/room/settings-dialog";
import { CallEndedScreen, ConnectionBadge, ErrorBanner, Loading, RoomFullState } from "@/components/room/states";
import { EASE, Reveal, SectionCard, StatTile } from "@/components/app/ui-bits";

type ApiStatus = "checking" | "ok" | "unconfigured" | "unreachable";

// Suspense wrapper: useSearchParams requires it during static prerender.
export default function Home() {
  return (
    <Suspense fallback={<div className="min-h-dvh bg-background" />}>
      <HomeInner />
    </Suspense>
  );
}

function HomeInner() {
  const s = useSession();
  const search = useSearchParams();
  const [apiStatus, setApiStatus] = useState<ApiStatus>("checking");
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [joining, setJoining] = useState(false); // between applyJoin and LiveKit connected
  const [joinError, setJoinError] = useState<string | null>(null);
  const [scrolled, setScrolled] = useState(false);
  const connectingRef = useRef(false);

  // Backend reachability probe (honest status on the landing screen).
  useEffect(() => {
    if (!API_URL) {
      setApiStatus("unconfigured");
      return;
    }
    health()
      .then(() => setApiStatus("ok"))
      .catch((e) => setApiStatus(e instanceof ApiError && e.code === "not_configured" ? "unconfigured" : "unreachable"));
  }, []);

  // Nav settles its hairline border once the page starts moving.
  useEffect(() => {
    const onScroll = () => setScrolled(window.scrollY > 10);
    onScroll();
    window.addEventListener("scroll", onScroll, { passive: true });
    return () => window.removeEventListener("scroll", onScroll);
  }, []);

  // Deep link: /?join=CODE (optionally &room={id})
  useEffect(() => {
    const code = search.get("join");
    const roomId = search.get("room");
    if ((code || roomId) && s.view === "landing") {
      if (code) s.setPendingJoinCode(code.toUpperCase());
      if (roomId) s.setPendingRoomId(roomId);
      s.setView("join");
    }

  }, [search]);

  // Unlock audio playback on the first user gesture (autoplay policy, §7.4).
  useEffect(() => {
    const unlock = () => void audioEngine.unlockPlayback();
    window.addEventListener("pointerdown", unlock, { once: true });
    return () => window.removeEventListener("pointerdown", unlock);
  }, []);

  // LiveKit lifecycle: connect once a join result exists.
  const connectRoom = useCallback(async () => {
    if (connectingRef.current) return;
    connectingRef.current = true;
    setJoining(true);
    setJoinError(null);
    const st = useSession.getState();
    try {
      await audioEngine.unlockPlayback();
      let micStream: MediaStream | undefined;
      try {
        micStream = st.micDeviceId
          ? await audioEngine.requestMic(st.micDeviceId)
          : await audioEngine.requestMic();
        audioEngine.startMeter((rms) => useSession.getState().setMicLevel(rms));
      } catch (micErr) {
        // Permission errors must be actionable, not silent (§11.2.G).
        connectingRef.current = false;
        setJoining(false);
        setJoinError(
          micErr instanceof Error ? micErr.message : "Microphone unavailable."
        );
        return;
      }

      livekitEngine.setCallbacks({
        onStateChanged: (st2) => useSession.getState().setConnState(st2),
        onActiveSpeakers: (ids) => useSession.getState().setSpeaking(ids),
        onParticipantsChanged: () => {
          useSession.getState().setSubscribedTracks(livekitEngine.subscribedAudioTrackNames());
        },
        onRemoteAudioAdded: () => {
          useSession.getState().setSubscribedTracks(livekitEngine.subscribedAudioTrackNames());
        },
        onPlaybackBlocked: () => {
          useSession.getState().setError({
            code: "playback_blocked",
            message: "Your browser blocked audio playback. Tap anywhere to start hearing the room.",
          });
        },
        onError: (msg) => useSession.getState().setError({ code: "livekit", message: msg }),
      });

      await livekitEngine.connect(st.livekitUrl, st.livekitToken, micStream, st.participantId);
      useSession.getState().setConnState("connected");
    } catch (e) {
      setJoinError(e instanceof Error ? e.message : "Could not connect to the room.");
      useSession.getState().setConnState("disconnected");
    } finally {
      connectingRef.current = false;
      setJoining(false);
    }
  }, []);

  // Trigger connect whenever the view enters the room.
  useEffect(() => {
    if (s.view === "room" && s.connState === "idle") void connectRoom();

  }, [s.view]);

  const leaveToHome = () => {
    useSession.getState().reset();
    connectingRef.current = false;
  };

  return (
    <div className="min-h-dvh bg-background text-foreground">
      {/* ── Live room ─────────────────────────────────────────────────── */}
      {s.view === "room" && s.room && (
        <>
          <RoomScreen
            onLeave={() => useSession.getState().endSession("left")}
            onEnded={() => useSession.getState().endSession("ended")}
            onOpenSettings={() => setSettingsOpen(true)}
          />
          <SettingsDialog open={settingsOpen} onOpenChange={setSettingsOpen} />
          {joinError && (
            <div className="fixed bottom-24 left-1/2 z-50 w-[92%] max-w-md -translate-x-1/2">
              <ErrorBanner title="Connection problem" message={joinError} onRetry={connectRoom} onDismiss={() => setJoinError(null)} />
            </div>
          )}
          {s.error && (
            <div className="fixed bottom-40 left-1/2 z-50 w-[92%] max-w-md -translate-x-1/2">
              <ErrorBanner title="Room notice" message={s.error.message} onDismiss={() => useSession.getState().setError(null)} />
            </div>
          )}
          {joining && (
            <div className="fixed inset-0 z-50 flex items-center justify-center bg-background/80 backdrop-blur-sm">
              <Loading label="Connecting to the room…" />
            </div>
          )}
        </>
      )}

      {s.view === "room" && !s.room && <RoomFullState onBack={leaveToHome} />}

      {/* ── Ended ─────────────────────────────────────────────────────── */}
      {s.view === "ended" && (
        <div className="flex min-h-dvh items-center justify-center px-4">
          <CallEndedScreen
            reason={s.endedReason || "left"}
            roomCode={s.room?.code}
            onHome={leaveToHome}
          />
        </div>
      )}

      {/* ── Create / Join ─────────────────────────────────────────────── */}
      {(s.view === "create" || s.view === "join") && (
        <div className="mx-auto flex min-h-dvh max-w-lg flex-col justify-center px-4 py-10">
          <motion.div
            initial={{ opacity: 0, y: 12 }}
            animate={{ opacity: 1, y: 0 }}
            transition={{ duration: 0.45, ease: [...EASE] }}
          >
            <BrandHeader compact />
            {apiStatus !== "ok" && (
              <div className="mb-4">
                <ErrorBanner
                  title={apiStatus === "unconfigured" ? "Server not configured" : "Server unreachable"}
                  message={
                    apiStatus === "unconfigured"
                      ? "Set NEXT_PUBLIC_API_URL to your Render API URL, then redeploy the frontend. See the README deployment checklist."
                      : "The control-plane API did not answer. Once your Render service is live, reload this page."
                  }
                />
              </div>
            )}
            {s.view === "create" ? (
              <CreateRoomForm onBack={() => s.setView("landing")} />
            ) : (
              <JoinRoomForm code={s.pendingJoinCode} onBack={() => s.setView("landing")} />
            )}
          </motion.div>
        </div>
      )}

      {/* ── Landing — Bridge Connect ──────────────────────────────────── */}
      {s.view === "landing" && (
        <main className="relative flex min-h-dvh flex-col">
          {/* Sticky nav — quiet chrome, one hairline that settles on scroll */}
          <header
            className={cn(
              "sticky top-0 z-40 border-b bg-background/80 backdrop-blur transition-[border-color,box-shadow] duration-300",
              scrolled ? "border-border shadow-[0_1px_2px_rgba(23,26,31,0.04)]" : "border-transparent"
            )}
          >
            <div className="mx-auto flex h-14 max-w-6xl items-center justify-between px-4 sm:px-6">
              <div className="flex items-center gap-2.5">
                <BrandMark />
                <span className="text-base font-semibold tracking-[-0.01em]">VoiceBridge</span>
              </div>
              <nav aria-label="Sections" className="hidden items-center gap-7 text-sm font-medium text-muted-foreground md:flex">
                <a href="#how" className="transition-colors duration-150 hover:text-foreground">How it works</a>
                <a href="#features" className="transition-colors duration-150 hover:text-foreground">Features</a>
                <a href="#security" className="transition-colors duration-150 hover:text-foreground">Security</a>
              </nav>
              <div className="flex items-center gap-3">
                <span className="hidden sm:inline-flex">
                  <ConnectionBadge state={apiStatus === "ok" ? "idle" : "disconnected"} />
                </span>
                <Button size="sm" className="font-medium" onClick={() => s.setView("create")}>
                  <Mic className="mr-1.5 h-3.5 w-3.5" /> New room
                </Button>
              </div>
            </div>
          </header>

          {/* Hero — whitespace, type and one serif moment. No decoration. */}
          <section className="mx-auto w-full max-w-6xl px-4 pt-16 text-center sm:px-6 sm:pt-24">
            <motion.div
              initial={{ opacity: 0, y: 10 }}
              animate={{ opacity: 1, y: 0 }}
              transition={{ duration: 0.45, ease: [...EASE] }}
            >
              <p className="font-mono text-[11px] font-medium uppercase tracking-[0.2em] text-muted-foreground">
                Live group voice translation
              </p>
            </motion.div>
            <motion.h1
              initial={{ opacity: 0, y: 14 }}
              animate={{ opacity: 1, y: 0 }}
              transition={{ duration: 0.45, delay: 0.06, ease: [...EASE] }}
              className="mx-auto mt-5 max-w-3xl text-4xl leading-[1.05] tracking-tight sm:text-6xl"
            >
              <span className="font-semibold">Speak your language.</span>
              <span className="mt-1 block font-display font-normal italic text-primary">
                Hear everyone in yours.
              </span>
            </motion.h1>
            <motion.p
              initial={{ opacity: 0, y: 14 }}
              animate={{ opacity: 1, y: 0 }}
              transition={{ duration: 0.45, delay: 0.12, ease: [...EASE] }}
              className="mx-auto mt-6 max-w-2xl text-base leading-relaxed text-muted-foreground sm:text-lg"
            >
              One room in the browser where an English, Tamil and Japanese speaker hold a
              natural conversation — every voice arrives translated, live, into the language
              each listener picked. No downloads. No host. No typing.
            </motion.p>
            <motion.div
              initial={{ opacity: 0, y: 14 }}
              animate={{ opacity: 1, y: 0 }}
              transition={{ duration: 0.45, delay: 0.18, ease: [...EASE] }}
              className="mt-8 flex flex-wrap items-center justify-center gap-3"
            >
              <Button
                size="lg"
                className="h-11 px-6 text-sm font-medium transition-transform duration-150 hover:-translate-y-px active:translate-y-0 active:scale-[0.98]"
                onClick={() => s.setView("create")}
              >
                <Mic className="mr-2 h-4 w-4" /> Create a room
              </Button>
              <Button
                size="lg"
                variant="outline"
                className="h-11 bg-card px-6 text-sm font-medium transition-transform duration-150 hover:-translate-y-px active:translate-y-0 active:scale-[0.98]"
                onClick={() => s.setView("join")}
              >
                <AudioLines className="mr-2 h-4 w-4" /> Join with a code
              </Button>
            </motion.div>
            {apiStatus !== "ok" && (
              <p className="mt-4 text-sm text-warning">
                {apiStatus === "unconfigured"
                  ? "Set NEXT_PUBLIC_API_URL to your Render API URL, then redeploy — see the README checklist."
                  : "The control-plane API did not answer. Once Render is live, reload this page."}
              </p>
            )}

            <RoomPreview />
          </section>

          {/* Stats — data, set in mono, counted up on first view */}
          <section className="mx-auto w-full max-w-6xl px-4 sm:px-6">
            <Reveal>
              <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
                <StatTile label="Listening languages" value={<CountUp to={LANGUAGES.length} />} hint="picked per participant" />
                <StatTile label="Seats per room" value={<CountUp to={12} />} hint="one shared code" />
                <StatTile label="Stream per pair" value={<CountUp to={1} />} hint="shared by all listeners" />
                <StatTile label="Recordings" value={<CountUp to={0} />} hint="none, ever" />
              </div>
            </Reveal>
          </section>

          {/* How it works */}
          <section id="how" className="mx-auto w-full max-w-6xl scroll-mt-20 px-4 py-16 sm:px-6 sm:py-24">
            <Reveal>
              <div className="max-w-2xl">
                <p className="font-mono text-[11px] font-medium uppercase tracking-[0.2em] text-primary">
                  How it works
                </p>
                <h2 className="mt-2 text-2xl font-semibold tracking-[-0.01em] sm:text-3xl">
                  Three steps. Zero setup.
                </h2>
              </div>
            </Reveal>
            <div className="mt-8 grid gap-3 sm:grid-cols-3">
              {[
                {
                  n: "01",
                  title: "Create a room",
                  body: "Name it, set the size and pick the language you want to hear. That is the entire setup.",
                },
                {
                  n: "02",
                  title: "Share six characters",
                  body: "Everyone joins from their browser with the room code — nothing to install — and picks their own listening language.",
                },
                {
                  n: "03",
                  title: "Just talk",
                  body: "Speak naturally. Every other voice arrives in your language — and yours arrives in theirs.",
                },
              ].map((step, i) => (
                <Reveal key={step.n} delay={i * 0.08}>
                  <SectionCard className="h-full transition-colors duration-150 hover:border-input">
                    <span className="font-mono text-xs font-medium tracking-[0.2em] text-primary">{step.n}</span>
                    <p className="mt-3 text-sm font-semibold">{step.title}</p>
                    <p className="mt-1.5 text-sm leading-relaxed text-muted-foreground">{step.body}</p>
                  </SectionCard>
                </Reveal>
              ))}
            </div>
          </section>

          {/* Feature bento — flat SectionCards, teal only on icon chips */}
          <section id="features" className="scroll-mt-20 border-y border-border bg-card/40">
            <div className="mx-auto max-w-6xl px-4 py-16 sm:px-6 sm:py-24">
              <Reveal>
                <div className="max-w-2xl">
                  <p className="font-mono text-[11px] font-medium uppercase tracking-[0.2em] text-primary">
                    Features
                  </p>
                  <h2 className="mt-2 text-2xl font-semibold tracking-[-0.01em] sm:text-3xl">
                    Built for real conversations
                  </h2>
                  <p className="mt-3 text-sm leading-relaxed text-muted-foreground sm:text-base">
                    No room-wide language, no push-to-talk, no captions to read. Just voices —
                    each one delivered in the listener&apos;s own language.
                  </p>
                </div>
              </Reveal>

              <div className="mt-8 grid gap-3 text-left sm:grid-cols-3">
                {/* Wide card: personal language + real pairing examples */}
                <Reveal className="sm:col-span-2">
                  <SectionCard className="h-full">
                    <span className="flex h-9 w-9 items-center justify-center rounded-lg bg-primary/10 text-primary">
                      <Ear className="h-4.5 w-4.5" />
                    </span>
                    <p className="mt-4 text-sm font-semibold">Personal by default</p>
                    <p className="mt-1.5 max-w-md text-sm leading-relaxed text-muted-foreground">
                      Each participant selects exactly one listening language. There is no global
                      room language — every pairing is served individually.
                    </p>
                    <div className="mt-5 flex flex-wrap items-center gap-2">
                      {["es", "ja", "hi"].map((code) => {
                        const l = findLanguage(code);
                        const en = findLanguage("en");
                        return (
                          <span
                            key={code}
                            className="inline-flex items-center gap-1.5 rounded-full border bg-background px-3 py-1.5 text-xs font-medium text-foreground transition-colors duration-150 hover:border-ring"
                          >
                            <span aria-hidden>{l?.flag}</span> {l?.name}
                            <span className="font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground">{l?.code}</span>
                            <Route className="h-3 w-3 text-primary" aria-hidden />
                            <span>{en?.name ?? "English"}</span>
                          </span>
                        );
                      })}
                    </div>
                  </SectionCard>
                </Reveal>
                <Reveal delay={0.06}>
                  <FeatureCard
                    icon={<Route className="h-4.5 w-4.5" />}
                    title="Shared streams"
                    body="One translation stream per language pair — shared by everyone listening in that language."
                  />
                </Reveal>
                <Reveal delay={0.04}>
                  <FeatureCard
                    icon={<EarOff className="h-4.5 w-4.5" />}
                    title="You never hear yourself"
                    body="Your own voice is never translated back at you. The conversation stays echo-free, naturally."
                  />
                </Reveal>
                <Reveal delay={0.08}>
                  <FeatureCard
                    icon={<Timer className="h-4.5 w-4.5" />}
                    title="Built for real time"
                    body="WebRTC transport, server-side speech-to-speech translation and bounded audio queues keep voices in sync."
                  />
                </Reveal>
                <Reveal delay={0.04}>
                  <FeatureCard
                    icon={<MonitorSmartphone className="h-4.5 w-4.5" />}
                    title="Right in the browser"
                    body="Nothing to install. The room works on laptops and phones, straight from a link."
                  />
                </Reveal>
              </div>
            </div>
          </section>

          {/* Security — light, factual, no dark theater */}
          <section id="security" className="mx-auto w-full max-w-6xl scroll-mt-20 px-4 py-16 sm:px-6 sm:py-24">
            <Reveal>
              <div className="max-w-2xl">
                <p className="font-mono text-[11px] font-medium uppercase tracking-[0.2em] text-primary">
                  Security &amp; privacy
                </p>
                <h2 className="mt-2 text-2xl font-semibold tracking-[-0.01em] sm:text-3xl">
                  Your voice stays yours.
                </h2>
              </div>
            </Reveal>
            <div className="mt-8 grid gap-3 sm:grid-cols-3">
              {[
                {
                  icon: <KeyRound className="h-4.5 w-4.5" />,
                  title: "Join-only access",
                  body: "Signed, single-purpose tokens. A participant token can join one room and nothing else.",
                },
                {
                  icon: <Lock className="h-4.5 w-4.5" />,
                  title: "Encrypted transport",
                  body: "Audio moves over encrypted WebRTC (SRTP/DTLS); the control plane runs over TLS.",
                },
                {
                  icon: <Ear className="h-4.5 w-4.5" />,
                  title: "Whitelisted audio",
                  body: "You receive only the tracks the server grants — original voices and your translations, nothing else.",
                },
              ].map((f, i) => (
                <Reveal key={f.title} delay={i * 0.08}>
                  <FeatureCard icon={f.icon} title={f.title} body={f.body} />
                </Reveal>
              ))}
            </div>
          </section>

          {/* Languages — a calm, honest registry. No marquee. */}
          <section aria-label="Supported languages" className="border-t border-border bg-card/40">
            <div className="mx-auto max-w-6xl px-4 py-12 sm:px-6">
              <Reveal>
                <div className="flex items-center justify-between gap-3">
                  <p className="font-mono text-[11px] font-medium uppercase tracking-[0.2em] text-muted-foreground">
                    Supported listening languages
                  </p>
                  <p className="font-mono text-[11px] tracking-wide text-muted-foreground">
                    {LANGUAGES.length} total
                  </p>
                </div>
                <div className="mt-5 flex flex-wrap gap-1.5">
                  {LANGUAGES.map((l) => (
                    <span
                      key={l.code}
                      className="inline-flex items-center gap-1.5 rounded-full border bg-card px-3 py-1.5 text-xs font-medium text-foreground transition-colors duration-150 hover:border-ring"
                    >
                      <span aria-hidden>{l.flag}</span>
                      {l.name}
                      <span className="font-mono text-[10px] uppercase tracking-[0.12em] text-muted-foreground">
                        {l.code}
                      </span>
                    </span>
                  ))}
                </div>
              </Reveal>
            </div>
          </section>

          {/* CTA */}
          <section className="mx-auto w-full max-w-6xl px-4 py-16 sm:px-6 sm:py-24">
            <Reveal>
              <div className="rounded-xl border bg-card px-6 py-14 text-center sm:px-12">
                <p className="font-mono text-[11px] font-medium uppercase tracking-[0.2em] text-primary">
                  Ready in seconds
                </p>
                <h2 className="mx-auto mt-3 max-w-xl text-2xl font-semibold tracking-[-0.01em] sm:text-3xl">
                  No account. No download.
                  <span className="mt-1 block font-display font-normal italic text-primary">
                    Just a room and your voice.
                  </span>
                </h2>
                <div className="mt-8 flex flex-wrap items-center justify-center gap-3">
                  <Button
                    size="lg"
                    className="h-11 px-6 text-sm font-medium transition-transform duration-150 hover:-translate-y-px active:translate-y-0 active:scale-[0.98]"
                    onClick={() => s.setView("create")}
                  >
                    <Mic className="mr-2 h-4 w-4" /> Create a room
                  </Button>
                  <Button
                    size="lg"
                    variant="outline"
                    className="h-11 bg-card px-6 text-sm font-medium transition-transform duration-150 hover:-translate-y-px active:translate-y-0 active:scale-[0.98]"
                    onClick={() => s.setView("join")}
                  >
                    <AudioLines className="mr-2 h-4 w-4" /> Join with a code
                  </Button>
                </div>
              </div>
            </Reveal>
          </section>

          {/* Footer — privacy notice (blueprint §11.2.A) */}
          <footer className="mt-auto border-t border-border bg-card/40">
            <div className="mx-auto max-w-6xl px-4 py-8 text-xs leading-relaxed text-muted-foreground sm:px-6">
              <p className="flex items-start gap-2">
                <ShieldCheck className="mt-0.5 h-4 w-4 shrink-0 text-primary" aria-hidden />
                <span>
                  Privacy: your voice is streamed over an encrypted connection and processed by the
                  speech-to-speech translation service to produce translated audio. Conversations
                  are not recorded. Microphone access is only used while you are in a room.
                  {apiStatus !== "ok" && (
                    <span className="mt-1 block text-warning">
                      Live connection requires the backend (Render) to be configured and running.
                    </span>
                  )}
                </span>
              </p>
              <div className="mt-5 flex flex-wrap items-center justify-between gap-3 border-t border-border pt-5">
                <p className="flex items-center gap-2">
                  <Globe2 className="h-3.5 w-3.5 text-primary" aria-hidden /> VoiceBridge — group voice translator
                </p>
                <p className="font-mono text-[11px] tracking-wide">© {new Date().getFullYear()} VoiceBridge</p>
              </div>
            </div>
          </footer>
        </main>
      )}
    </div>
  );
}

/* ── Building blocks ───────────────────────────────────────────────────── */

// Brand mark: the one teal tile in the chrome — a waveform glyph.
function BrandMark() {
  return (
    <span className="flex h-7 w-7 items-center justify-center rounded-lg bg-primary text-primary-foreground transition-transform duration-150 hover:scale-105">
      <AudioWaveform className="h-4 w-4" aria-hidden />
    </span>
  );
}

// FeatureCard — a flat SectionCard with a muted→teal icon chip.
// The chip is the card's single accent; hover lifts nothing, only the
// hairline warms up (restrained, Bridge Connect style).
function FeatureCard({ icon, title, body }: { icon: React.ReactNode; title: string; body: string }) {
  return (
    <SectionCard className="group h-full transition-colors duration-150 hover:border-input">
      <span className="flex h-9 w-9 items-center justify-center rounded-lg bg-primary/10 text-primary transition-transform duration-200 group-hover:scale-105">
        {icon}
      </span>
      <p className="mt-4 text-sm font-semibold">{title}</p>
      <p className="mt-1.5 text-sm leading-relaxed text-muted-foreground">{body}</p>
    </SectionCard>
  );
}

// CountUp — a number that counts up when it first enters the viewport.
// Data displayed in mono; skips straight to the value under reduced motion.
function CountUp({ to, duration = 1100 }: { to: number; duration?: number }) {
  const [val, setVal] = useState(0);
  const ref = useRef<HTMLSpanElement>(null);
  const reduce = useReducedMotion();

  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    if (reduce || to === 0) {
      const raf = requestAnimationFrame(() => setVal(to));
      return () => cancelAnimationFrame(raf);
    }
    let raf = 0;
    const io = new IntersectionObserver(
      (entries) => {
        if (!entries[0].isIntersecting) return;
        io.disconnect();
        const t0 = performance.now();
        const tick = (t: number) => {
          const p = Math.min(1, (t - t0) / duration);
          const eased = 1 - Math.pow(1 - p, 3); // expo-out, the app's one easing
          setVal(Math.round(eased * to));
          if (p < 1) raf = requestAnimationFrame(tick);
        };
        raf = requestAnimationFrame(tick);
      },
      { threshold: 0.4 }
    );
    io.observe(el);
    return () => {
      io.disconnect();
      cancelAnimationFrame(raf);
    };
  }, [to, duration, reduce]);

  return <span ref={ref}>{val}</span>;
}

// RoomPreview — the hero visual: the live room's dark canvas, faithfully
// miniaturized. Speakers take turns (driven by a timer here, by REAL
// active-speaker events in the room), the equalizer follows, and a ticker
// cycles the live translation routes. This is the only dark surface on the
// landing page — the product's stage, shown honestly.
function RoomPreview() {
  const rows = [
    { name: "Mariana", code: "es" },
    { name: "Kenji", code: "ja" },
    { name: "Priya", code: "hi" },
  ];
  const [active, setActive] = useState(0);
  const [tick, setTick] = useState(0);
  const reduce = useReducedMotion();
  const en = findLanguage("en");
  const hearName = en?.name ?? "English";

  // Speakers take turns; the route ticker runs on its own cadence.
  useEffect(() => {
    if (reduce) return; // static preview under reduced motion
    const a = setInterval(() => setActive((v) => (v + 1) % rows.length), 2200);
    const t = setInterval(() => setTick((v) => v + 1), 2600);
    return () => {
      clearInterval(a);
      clearInterval(t);
    };
  }, [reduce]);

  const tickerRow = rows[tick % rows.length];
  const tickerLang = findLanguage(tickerRow.code);

  return (
    <motion.div
      initial={reduce ? { opacity: 0 } : { opacity: 0, y: 22 }}
      animate={{ opacity: 1, y: 0 }}
      transition={{ duration: reduce ? 0.2 : 0.45, delay: reduce ? 0 : 0.3, ease: [...EASE] }}
      className="relative mx-auto mt-14 w-fit pb-2 sm:mt-16"
      aria-hidden
    >
      {/* The stage — canvas tokens, quiet chrome, data in mono */}
      <div className="w-[min(92vw,560px)] rounded-xl border border-canvas-border bg-canvas p-4 text-left shadow-[0_24px_60px_-30px_rgba(15,18,22,0.5)] sm:p-5">
        {/* stage header */}
        <div className="flex items-center justify-between gap-3">
          <div className="flex min-w-0 items-center gap-2.5">
            <span className="relative inline-flex h-2 w-2">
              <span className="animate-live-halo absolute inline-flex h-full w-full rounded-full bg-canvas-accent" />
              <span className="relative inline-flex h-2 w-2 rounded-full bg-canvas-accent" />
            </span>
            <span className="truncate text-sm font-semibold text-canvas-foreground">Global standup</span>
            <span className="hidden rounded-full border border-canvas-accent/30 bg-canvas-accent/10 px-2 py-0.5 text-[11px] font-medium text-canvas-accent sm:inline-flex">
              Live
            </span>
          </div>
          <span className="shrink-0 rounded-md border border-canvas-border bg-canvas-card px-2 py-1 font-mono text-xs tracking-[0.25em] text-canvas-foreground">
            K7Q2MX
          </span>
        </div>

        {/* participant rows — the active speaker's equalizer dances */}
        <div className="mt-4 space-y-2">
          {rows.map((r, i) => {
            const l = findLanguage(r.code);
            const isActive = i === active;
            return (
              <div
                key={r.name}
                className={cn(
                  "flex items-center justify-between gap-3 rounded-lg border px-3 py-2.5 transition-colors duration-300",
                  isActive ? "border-canvas-accent/40 bg-canvas-accent/10" : "border-canvas-border bg-canvas-card"
                )}
              >
                <div className="flex min-w-0 items-center gap-3">
                  <span
                    className={cn(
                      "flex h-8 w-8 shrink-0 items-center justify-center rounded-full font-mono text-xs transition-shadow duration-300",
                      isActive ? "bg-canvas-accent/20 text-canvas-accent ring-2 ring-canvas-accent/50" : "bg-white/10 text-canvas-foreground"
                    )}
                  >
                    {r.name.charAt(0)}
                  </span>
                  <div className="min-w-0">
                    <p className="truncate text-sm font-medium leading-tight text-canvas-foreground">{r.name}</p>
                    <p className="mt-0.5 truncate text-xs text-canvas-muted">
                      speaks {l?.name} · hears <span className="text-canvas-foreground">{hearName}</span>
                    </p>
                  </div>
                </div>
                <EqBars live={isActive} />
              </div>
            );
          })}
        </div>

        {/* stage footer — live route with travelling packets */}
        <div className="mt-4 flex items-center justify-between gap-3 border-t border-canvas-border pt-3">
          <div className="flex min-w-0 flex-1 items-center gap-2.5 text-xs text-canvas-muted">
            <span key={tick} className="animate-ticker inline-flex min-w-0 items-center gap-1.5 truncate font-mono uppercase tracking-[0.12em]">
              <span aria-hidden>{tickerLang?.flag}</span>
              {tickerRow.code} → en
            </span>
            <span className="flow-line h-0.5 w-10 shrink-0 rounded-full opacity-70 sm:w-14" aria-hidden />
            <span className="shrink-0 font-mono uppercase tracking-[0.12em] text-canvas-accent">live</span>
          </div>
          <span className="hidden shrink-0 font-mono text-[11px] tracking-wide text-canvas-muted sm:inline">
            {LANGUAGES.length} languages
          </span>
        </div>
      </div>

      {/* caption — the stage explains itself, in a micro-label */}
      <p className="mt-3 text-center font-mono text-[11px] uppercase tracking-[0.2em] text-muted-foreground">
        The live room — every voice routed to the listener&apos;s language
      </p>
    </motion.div>
  );
}

// Tiny equalizer used inside the preview rows. Transform-only animation;
// a real speaking indicator in the room, a faithful miniature here.
function EqBars({ live }: { live: boolean }) {
  return (
    <span className={cn("flex h-4 items-end gap-[2.5px]", live ? "text-canvas-accent" : "text-white/25")}>
      {[7, 12, 9, 14, 6].map((h, i) => (
        <span
          key={i}
          className={cn("w-[3px] rounded-full bg-current transition-all duration-300", live && "animate-eq")}
          style={{ height: `${h}px`, animationDelay: `${i * 120}ms` }}
        />
      ))}
    </span>
  );
}

function BrandHeader({ compact }: { compact?: boolean }) {
  return (
    <div className="mb-6 text-center">
      <span className="mx-auto flex h-10 w-10 items-center justify-center rounded-lg bg-primary text-primary-foreground">
        <AudioWaveform className="h-5 w-5" aria-hidden />
      </span>
      <h1 className="mt-3 text-xl font-semibold tracking-[-0.01em] sm:text-2xl">
        {compact ? "VoiceBridge" : "VoiceBridge — group voice translator"}
      </h1>
      <p className="mt-1 font-mono text-[11px] uppercase tracking-[0.2em] text-muted-foreground">
        Speak your language · Hear yours
      </p>
    </div>
  );
}
