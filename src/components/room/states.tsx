"use client";

// Shared state components (blueprint §11.2.G, §11.3):
// ConnectionBadge, SpeakingIndicator, AudioLevelMeter, ErrorBanner,
// PermissionHelp, ReconnectPanel, RoomFullState, CallEndedScreen, Loading.
// Bridge Connect rules: semantic color = status only; tint recipe
// bg-<token>/10 text-<token>; animate-ping reserved for live dots;
// animate-spin only for real loading. Motion = transform/opacity on EASE.

import { motion, useReducedMotion } from "framer-motion";
import { Loader2, MicOff, ShieldAlert, Signal, WifiOff, LogOut, PhoneOff } from "lucide-react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { EASE, LiveDot, StatusPill, type PillTone } from "@/components/app/ui-bits";
import type { ConnState } from "@/lib/livekit-engine";
import { levelToBars } from "@/lib/audio-engine";

// ConnectionBadge — real connection state only, as a StatusPill.
// tone="canvas" remaps to the dark-stage status colors (canvas-* tokens).
export function ConnectionBadge({ state, tone = "default" }: { state: ConnState; tone?: "default" | "canvas" }) {
  const map: Record<ConnState, { label: string; tone: PillTone; spin?: boolean; live?: boolean }> = {
    idle: { label: "Idle", tone: "neutral" },
    connecting: { label: "Connecting…", tone: "warn", spin: true },
    connected: { label: "Connected", tone: "live", live: true },
    reconnecting: { label: "Reconnecting…", tone: "warn", spin: true },
    disconnected: { label: "Disconnected", tone: "error" },
  };
  const m = map[state];
  const canvasCls: Record<ConnState, string> = {
    idle: "border-canvas-border bg-canvas-card text-canvas-muted",
    connecting: "border-canvas-warning/30 bg-canvas-warning/10 text-canvas-warning",
    connected: "border-canvas-accent/30 bg-canvas-accent/10 text-canvas-accent",
    reconnecting: "border-canvas-warning/30 bg-canvas-warning/10 text-canvas-warning",
    disconnected: "border-canvas-danger/30 bg-canvas-danger/10 text-canvas-danger",
  };
  return (
    <StatusPill tone={m.tone} className={tone === "canvas" ? canvasCls[state] : undefined} data-testid="connection-badge">
      {m.live ? (
        <LiveDot className={tone === "canvas" ? "[&>span]:bg-canvas-accent" : undefined} />
      ) : m.spin ? (
        <Loader2 className="h-3 w-3 animate-spin" aria-hidden />
      ) : (
        <span className="h-1.5 w-1.5 rounded-full bg-current opacity-60" aria-hidden />
      )}
      {m.label}
    </StatusPill>
  );
}

// SpeakingIndicator — driven by REAL LiveKit active-speaker events.
// Bars rise only when the speaker is live; collapsed to a quiet baseline
// otherwise. Transform-only animation. tone="canvas" for the dark stage.
export function SpeakingIndicator({ speaking, tone = "default" }: { speaking: boolean; tone?: "default" | "canvas" }) {
  return (
    <span
      className={cn(
        "flex h-4 items-end gap-[2px]",
        tone === "canvas"
          ? speaking ? "text-canvas-accent" : "text-white/25"
          : speaking ? "text-primary" : "text-muted-foreground/40"
      )}
      aria-label={speaking ? "Speaking" : "Silent"}
    >
      {[8, 14, 10, 16, 7].map((h, i) => (
        <span
          key={i}
          className={cn("w-[3px] rounded-full bg-current transition-transform duration-150", speaking && "animate-eq")}
          style={{ height: speaking ? `${h}px` : "3px", animationDelay: `${i * 90}ms` }}
        />
      ))}
    </span>
  );
}

// AudioLevelMeter — REAL microphone RMS, 5 bars. Colors are status, not
// decoration: ok (green) → warn (amber) → error (red) as the input clips.
// tone="canvas" uses the dark-stage status colors.
export function AudioLevelMeter({ level, tone = "default" }: { level: number; tone?: "default" | "canvas" }) {
  const bars = levelToBars(level);
  return (
    <div className="flex h-5 items-end gap-1" aria-label="Microphone level">
      {[0, 1, 2, 3, 4].map((i) => (
        <span
          key={i}
          className={cn(
            "w-1.5 rounded-sm transition-all duration-100",
            i < bars
              ? i < 2
                ? tone === "canvas" ? "bg-canvas-success" : "bg-success"
                : i < 4
                  ? tone === "canvas" ? "bg-canvas-warning" : "bg-warning"
                  : tone === "canvas" ? "bg-canvas-danger" : "bg-destructive"
              : tone === "canvas" ? "bg-white/15" : "bg-border"
          )}
          style={{ height: `${6 + i * 3}px` }}
        />
      ))}
    </div>
  );
}

export function MicOffIndicator() {
  return (
    <span className="inline-flex h-6 w-6 items-center justify-center rounded-full bg-destructive/10 text-destructive">
      <MicOff className="h-3.5 w-3.5" aria-hidden />
    </span>
  );
}

export function ErrorBanner({
  title,
  message,
  onRetry,
  onDismiss,
}: {
  title: string;
  message: string;
  onRetry?: () => void;
  onDismiss?: () => void;
}) {
  const reduce = useReducedMotion();
  return (
    <motion.div
      initial={reduce ? { opacity: 0 } : { opacity: 0, y: 8 }}
      animate={{ opacity: 1, y: 0 }}
      transition={reduce ? { duration: 0.2 } : { duration: 0.45, ease: [...EASE] }}
      className="rounded-lg border border-destructive/20 bg-destructive/10 p-4 text-sm"
      role="alert"
    >
      <div className="flex items-start gap-3">
        <ShieldAlert className="mt-0.5 h-4 w-4 shrink-0 text-destructive" aria-hidden />
        <div className="flex-1">
          <p className="font-medium text-foreground">{title}</p>
          <p className="mt-1 leading-relaxed text-muted-foreground">{message}</p>
          {(onRetry || onDismiss) && (
            <div className="mt-3 flex gap-2">
              {onRetry && (
                <Button size="sm" variant="outline" className="border-destructive/30 text-destructive hover:bg-destructive/10" onClick={onRetry}>
                  Try again
                </Button>
              )}
              {onDismiss && (
                <Button size="sm" variant="ghost" className="text-muted-foreground" onClick={onDismiss}>
                  Dismiss
                </Button>
              )}
            </div>
          )}
        </div>
      </div>
    </motion.div>
  );
}

export function PermissionHelp() {
  return (
    <div className="rounded-lg border border-warning/20 bg-warning/10 p-4 text-sm">
      <p className="flex items-center gap-2 font-medium text-foreground">
        <MicOff className="h-4 w-4 text-warning" aria-hidden /> Microphone blocked
      </p>
      <ol className="mt-2 list-decimal space-y-1 pl-5 text-muted-foreground">
        <li>Click the lock / camera icon in your browser address bar.</li>
        <li>Set <em>Microphone</em> to <em>Allow</em> for this site.</li>
        <li>Reload the page and test again.</li>
      </ol>
    </div>
  );
}

export function ReconnectPanel({ onLeave }: { onLeave: () => void }) {
  const reduce = useReducedMotion();
  return (
    <motion.div
      initial={{ opacity: 0 }}
      animate={{ opacity: 1 }}
      transition={{ duration: reduce ? 0.2 : 0.45, ease: [...EASE] }}
      className="fixed inset-0 z-50 flex items-center justify-center bg-background/70 backdrop-blur-sm"
    >
      <motion.div
        initial={reduce ? { opacity: 0 } : { opacity: 0, y: 10 }}
        animate={{ opacity: 1, y: 0 }}
        transition={reduce ? { duration: 0.2 } : { duration: 0.45, ease: [...EASE] }}
        className="mx-4 max-w-sm rounded-xl border bg-card p-6 text-center shadow-sm"
      >
        <span className="mx-auto flex h-9 w-9 items-center justify-center rounded-lg bg-warning/10 text-warning">
          <WifiOff className="h-4.5 w-4.5" aria-hidden />
        </span>
        <h2 className="mt-3 text-base font-semibold">Reconnecting…</h2>
        <p className="mt-1 text-sm leading-relaxed text-muted-foreground">
          The connection dropped. Translation pauses and resumes automatically.
        </p>
        <Button variant="outline" className="mt-4" onClick={onLeave}>
          <PhoneOff className="mr-2 h-4 w-4" /> Leave room
        </Button>
      </motion.div>
    </motion.div>
  );
}

export function RoomFullState({ onBack }: { onBack: () => void }) {
  return (
    <div className="mx-auto max-w-sm rounded-xl border bg-card p-6 text-center shadow-sm">
      <span className="mx-auto flex h-9 w-9 items-center justify-center rounded-lg bg-warning/10 text-warning">
        <Signal className="h-4.5 w-4.5" aria-hidden />
      </span>
      <h2 className="mt-3 text-base font-semibold">Room is full</h2>
      <p className="mt-1 text-sm leading-relaxed text-muted-foreground">
        This room has reached its participant limit. Ask the host for space or start a new room.
      </p>
      <Button className="mt-4" onClick={onBack}>
        Back
      </Button>
    </div>
  );
}

export function CallEndedScreen({ reason, roomCode, onHome }: { reason: string; roomCode?: string; onHome: () => void }) {
  const reduce = useReducedMotion();
  return (
    <motion.div
      initial={reduce ? { opacity: 0 } : { opacity: 0, y: 12 }}
      animate={{ opacity: 1, y: 0 }}
      transition={reduce ? { duration: 0.2 } : { duration: 0.45, ease: [...EASE] }}
      className="mx-auto w-full max-w-md rounded-xl border bg-card p-8 text-center shadow-sm"
    >
      <span className="mx-auto flex h-9 w-9 items-center justify-center rounded-lg bg-primary/10 text-primary">
        <LogOut className="h-4.5 w-4.5" aria-hidden />
      </span>
      <h2 className="mt-4 text-xl font-semibold tracking-[-0.01em]">
        {reason === "ended" ? "The room was closed" : reason === "left" ? "You left the room" : "Call ended"}
      </h2>
      {roomCode && (
        <p className="mt-2 font-mono text-xs tracking-[0.25em] text-muted-foreground">{roomCode}</p>
      )}
      <p className="mt-2 text-sm leading-relaxed text-muted-foreground">
        Translated audio has stopped. Thanks for talking with VoiceBridge.
      </p>
      <Button className="mt-6 w-full" onClick={onHome}>
        Back to home
      </Button>
    </motion.div>
  );
}

export function Loading({ label }: { label: string }) {
  return (
    <div className="flex items-center justify-center gap-3 p-8 text-muted-foreground" role="status">
      <Loader2 className="h-5 w-5 animate-spin text-primary" aria-hidden />
      <span className="text-sm">{label}</span>
    </div>
  );
}
