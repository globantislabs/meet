"use client";

// Bridge Connect canonical primitives (design system §4).
// Import from here before writing a bespoke one; if a new primitive is
// genuinely needed, add it HERE so it stays canonical.
//
// Motion contract (design system §6): one easing — EASE (expo-out) — the
// only easing in the app. Framer Motion for entrance/hover, Tailwind
// transitions (150ms) for interactive feedback. Transform + opacity only.
// `prefers-reduced-motion` is honored globally in globals.css and again
// here for JS-driven motion (opacity-only 0.2s fallback).

import * as React from "react";
import { motion, useReducedMotion } from "framer-motion";
import { Check, Copy } from "lucide-react";
import { cn } from "@/lib/utils";

// ── Motion ───────────────────────────────────────────────────────────────
// The only easing in the app (mirrors --ease-expo in globals.css).
export const EASE = [0.16, 1, 0.3, 1] as const;

// Thin-scrollbar utility string for long lists — pair with
// `max-h-96 overflow-y-auto`.
export const SCROLL_AREA = "thin-scroll";

// Reveal — scroll-entrance fade+rise. Entrance 0.45s on EASE; reduced
// motion falls back to a 0.2s opacity-only fade. `delay` in seconds.
export function Reveal({
  children,
  className,
  delay = 0,
  y = 14,
}: {
  children: React.ReactNode;
  className?: string;
  delay?: number;
  y?: number;
}) {
  const reduce = useReducedMotion();
  return (
    <motion.div
      className={className}
      initial={reduce ? { opacity: 0 } : { opacity: 0, y }}
      whileInView={{ opacity: 1, y: 0 }}
      viewport={{ once: true, margin: "0px 0px -40px 0px" }}
      transition={
        reduce
          ? { duration: 0.2, delay }
          : { duration: 0.45, delay, ease: [...EASE] }
      }
    >
      {children}
    </motion.div>
  );
}

// ── Page furniture ───────────────────────────────────────────────────────
// PageHeader — exactly one per view: icon chip + h1 + description +
// trailing actions. The h1 of the view.
export function PageHeader({
  icon,
  title,
  description,
  actions,
  className,
}: {
  icon?: React.ReactNode;
  title: React.ReactNode;
  description?: React.ReactNode;
  actions?: React.ReactNode;
  className?: string;
}) {
  return (
    <div className={cn("flex flex-wrap items-start justify-between gap-4", className)}>
      <div className="flex items-start gap-3">
        {icon && (
          <span className="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">
            <span className="[&_svg]:h-4.5 [&_svg]:w-4.5">{icon}</span>
          </span>
        )}
        <div className="min-w-0">
          <h1 className="text-xl font-semibold tracking-[-0.01em] sm:text-2xl">{title}</h1>
          {description && (
            <p className="mt-1 max-w-2xl text-sm leading-relaxed text-muted-foreground">
              {description}
            </p>
          )}
        </div>
      </div>
      {actions && <div className="flex shrink-0 items-center gap-2">{actions}</div>}
    </div>
  );
}

// SectionCard — tonal grouping with an optional header row. One per
// concern, flat inside: never nest boxes inside a SectionCard.
export function SectionCard({
  title,
  meta,
  actions,
  children,
  className,
  bodyClassName,
}: {
  title?: React.ReactNode;
  meta?: React.ReactNode; // mono — counts, ids, timestamps
  actions?: React.ReactNode;
  children: React.ReactNode;
  className?: string;
  bodyClassName?: string;
}) {
  return (
    <section className={cn("overflow-hidden rounded-xl border bg-card", className)}>
      {(title || actions || meta) && (
        <div className="flex items-center justify-between gap-3 border-b px-4 py-3 sm:px-5">
          <div className="flex min-w-0 items-center gap-2.5">
            {title && (
              <h2 className="truncate text-sm font-semibold">{title}</h2>
            )}
            {meta && (
              <span className="font-mono text-[11px] tracking-wide text-muted-foreground">
                {meta}
              </span>
            )}
          </div>
          {actions && <div className="flex shrink-0 items-center gap-2">{actions}</div>}
        </div>
      )}
      <div className={cn("px-4 py-4 sm:px-5 sm:py-5", bodyClassName)}>{children}</div>
    </section>
  );
}

// StatTile — uppercase micro-label + mono value (+ optional hint).
// The value is data: font-mono, truncates, never a sentence.
export function StatTile({
  label,
  value,
  hint,
  className,
}: {
  label: React.ReactNode;
  value: React.ReactNode;
  hint?: React.ReactNode;
  className?: string;
}) {
  return (
    <div className={cn("rounded-xl border bg-card px-4 py-4", className)}>
      <p className="text-[10px] font-medium uppercase tracking-[0.12em] text-muted-foreground">
        {label}
      </p>
      <p className="mt-1.5 truncate font-mono text-lg font-medium tracking-tight sm:text-xl">
        {value}
      </p>
      {hint && <p className="mt-1 text-xs text-muted-foreground">{hint}</p>}
    </div>
  );
}

// EmptyState — honest empty: icon, title, hint, ONE real next action.
// Dashed border; never placeholder rows or fake data.
export function EmptyState({
  icon,
  title,
  hint,
  action,
  className,
}: {
  icon?: React.ReactNode;
  title: React.ReactNode;
  hint?: React.ReactNode;
  action?: React.ReactNode;
  className?: string;
}) {
  return (
    <div
      className={cn(
        "flex flex-col items-center rounded-xl border border-dashed px-6 py-10 text-center",
        className
      )}
    >
      {icon && (
        <span className="flex h-9 w-9 items-center justify-center rounded-lg bg-muted text-muted-foreground">
          <span className="[&_svg]:h-4.5 [&_svg]:w-4.5">{icon}</span>
        </span>
      )}
      <p className="mt-3 text-sm font-semibold">{title}</p>
      {hint && (
        <p className="mt-1 max-w-sm text-sm leading-relaxed text-muted-foreground">{hint}</p>
      )}
      {action && <div className="mt-4">{action}</div>}
    </div>
  );
}

// ── Status ───────────────────────────────────────────────────────────────
export type PillTone = "live" | "ok" | "warn" | "error" | "neutral" | "info";

const PILL_TONES: Record<PillTone, string> = {
  // live uses the brand teal (selected/live state); success green is OK-state.
  live: "border-primary/20 bg-primary/10 text-primary",
  ok: "border-success/20 bg-success/10 text-success",
  warn: "border-warning/20 bg-warning/10 text-warning",
  error: "border-destructive/20 bg-destructive/10 text-destructive",
  neutral: "border-border bg-muted text-muted-foreground",
  info: "border-border bg-accent text-foreground",
};

// StatusPill — real state only, tint recipe bg-<tone>/10 text-<tone>.
export function StatusPill({
  tone = "neutral",
  children,
  className,
  ...props
}: React.ComponentProps<"span"> & { tone?: PillTone }) {
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 rounded-full border px-2.5 py-1 text-[11px] font-medium",
        PILL_TONES[tone],
        className
      )}
      {...props}
    >
      {children}
    </span>
  );
}

// LiveDot — pulsing brand dot for genuinely-live things ONLY (a connected
// room, a live route). Never decorative. Ping halo + solid core.
export function LiveDot({ className }: { className?: string }) {
  return (
    <span className={cn("relative inline-flex h-2 w-2", className)} aria-hidden>
      <span className="animate-live-halo absolute inline-flex h-full w-full rounded-full bg-primary" />
      <span className="relative inline-flex h-2 w-2 rounded-full bg-primary" />
    </span>
  );
}

// ── Actions ──────────────────────────────────────────────────────────────
// CopyButton — copy affordance with inline "Copied" confirmation (no toast
// noise). Falls back to window.prompt when clipboard is unavailable.
export function CopyButton({
  value,
  children,
  copiedLabel = "Copied",
  className,
  ...props
}: {
  value: string;
  children?: React.ReactNode;
  copiedLabel?: string;
} & React.ComponentProps<"button">) {
  const [copied, setCopied] = React.useState(false);

  const onCopy = async () => {
    try {
      await navigator.clipboard.writeText(value);
    } catch {
      window.prompt("Copy to clipboard:", value);
    }
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  };

  return (
    <button
      type="button"
      onClick={onCopy}
      className={cn(
        "inline-flex h-8 items-center gap-1.5 rounded-md border bg-card px-2.5 text-xs font-medium text-foreground transition-colors duration-150 hover:bg-accent focus-visible:ring-[3px] focus-visible:ring-ring/50 focus-visible:outline-none",
        className
      )}
      {...props}
    >
      {copied ? (
        <Check className="h-3.5 w-3.5 text-success" aria-hidden />
      ) : (
        <Copy className="h-3.5 w-3.5 text-muted-foreground" aria-hidden />
      )}
      {children ? (
        <span>{copied ? copiedLabel : children}</span>
      ) : (
        <span>{copied ? copiedLabel : "Copy"}</span>
      )}
    </button>
  );
}
