"use client";

// CreateRoomForm + JoinRoomForm (blueprint §11.2 B, C). LIGHT THEME.
// Both forms talk to the REAL Go API; error codes map to actionable copy.
// Bridge Connect: token classes only, data (room code) in mono with wide
// tracking, one primary action per form.

import { useState } from "react";
import { Users, Loader2, ArrowLeft, KeyRound } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Slider } from "@/components/ui/slider";
import { createRoom, joinByCode, ApiError } from "@/lib/api";
import { ListeningLanguageSelect, MicTestPanel, SourceLanguageSelect, sourceValueToLang, langDisplay } from "./controls";
import { ErrorBanner } from "./states";
import { useSession } from "@/stores/session-store";

export function CreateRoomForm({ onBack }: { onBack: () => void }) {
  const store = useSession();
  const [name, setName] = useState("");
  const [capacity, setCapacity] = useState(6);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<{ title: string; message: string } | null>(null);

  const submit = async () => {
    setErr(null);
    if (!store.displayName.trim()) {
      setErr({ title: "Name required", message: "Enter the name other participants will see." });
      return;
    }
    if (!name.trim()) {
      setErr({ title: "Room name required", message: "Give your conversation a short name." });
      return;
    }
    setBusy(true);
    try {
      const res = await createRoom({
        name: name.trim(),
        capacity,
        display_name: store.displayName.trim(),
        source_lang: store.sourceLang,
        listen_lang: store.listenLang,
      });
      store.applyJoin(res);
    } catch (e) {
      const api = e as ApiError;
      if (api.code === "not_configured") {
        setErr({ title: "Server not configured", message: api.message });
      } else if (api.code === "bad_request" && api.message.includes("language")) {
        setErr({ title: "Language not supported", message: `${langDisplay(store.listenLang)} is not enabled on this deployment. Pick another language or ask the operator to enable it.` });
      } else if (api.code === "network_error") {
        setErr({ title: "Server unreachable", message: api.message });
      } else {
        setErr({ title: "Could not create the room", message: api.message });
      }
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card className="shadow-sm">
      <CardHeader>
        <Button variant="ghost" size="sm" className="mb-1 w-fit px-2 text-muted-foreground" onClick={onBack}>
          <ArrowLeft className="mr-1.5 h-4 w-4" /> Back
        </Button>
        <CardTitle>Create a room</CardTitle>
        <CardDescription>
          Everyone picks ONE listening language and hears every other speaker in it.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-5">
        {err && <ErrorBanner title={err.title} message={err.message} onDismiss={() => setErr(null)} />}

        <div className="space-y-2">
          <Label htmlFor="room-name">Room name</Label>
          <Input
            id="room-name"
            placeholder="e.g. Project kickoff"
            value={name}
            onChange={(e) => setName(e.target.value)}
            maxLength={120}
          />
        </div>

        <div className="space-y-2">
          <Label htmlFor="display-name">Your name</Label>
          <Input
            id="display-name"
            placeholder="How others see you"
            value={store.displayName}
            onChange={(e) => store.setDisplayName(e.target.value)}
            maxLength={80}
          />
        </div>

        <div className="grid gap-4 sm:grid-cols-2">
          <div className="space-y-2">
            <Label>You speak</Label>
            <SourceLanguageSelect value={store.sourceLang || "__auto__"} onChange={(v) => store.setSourceLang(sourceValueToLang(v))} />
          </div>
          <div className="space-y-2">
            <Label>You want to hear</Label>
            <ListeningLanguageSelect value={store.listenLang} onChange={store.setListenLang} />
          </div>
        </div>

        <div className="space-y-2">
          <div className="flex items-center justify-between">
            <Label>
              <Users className="mr-1.5 inline h-4 w-4" /> Max participants
            </Label>
            <span className="font-mono text-sm font-medium text-primary">{capacity}</span>
          </div>
          <Slider value={[capacity]} min={2} max={12} step={1} onValueChange={([v]) => setCapacity(v)} />
          <p className="text-xs leading-relaxed text-muted-foreground">
            Each extra language pair needs its own translation stream — smaller rooms keep latency and cost low.
          </p>
        </div>

        <div className="rounded-lg border bg-accent/50 p-4">
          <p className="mb-3 text-sm font-medium">Microphone check</p>
          <MicTestPanel micDeviceId={store.micDeviceId} onMicDeviceChange={store.setMicDevice} />
        </div>

        <Button
          className="h-12 w-full text-base font-medium transition-transform duration-150 active:scale-[0.99]"
          disabled={busy}
          onClick={submit}
        >
          {busy && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
          Create room
        </Button>
      </CardContent>
    </Card>
  );
}

export function JoinRoomForm({ code, onBack }: { code: string; onBack: () => void }) {
  const store = useSession();
  const [joinCode, setJoinCode] = useState(code);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<{ title: string; message: string } | null>(null);

  const submit = async () => {
    setErr(null);
    if (!store.displayName.trim()) {
      setErr({ title: "Name required", message: "Enter the name other participants will see." });
      return;
    }
    if (!joinCode.trim()) {
      setErr({ title: "Code required", message: "Enter the room code from your invitation." });
      return;
    }
    setBusy(true);
    try {
      const res = await joinByCode(
        joinCode.trim().toUpperCase(),
        {
          display_name: store.displayName.trim(),
          source_lang: store.sourceLang,
          listen_lang: store.listenLang,
        },
        store.pendingRoomId || undefined
      );
      store.applyJoin(res);
    } catch (e) {
      const api = e as ApiError;
      const map: Record<string, { title: string }> = {
        not_found: { title: "Room not found" },
        room_ended: { title: "Room has ended" },
        room_expired: { title: "Room has expired" },
        room_full: { title: "Room is full" },
        invalid_code: { title: "Wrong code" },
        bad_request: { title: "Could not join" },
        network_error: { title: "Server unreachable" },
        not_configured: { title: "Server not configured" },
        rate_limited: { title: "Too many attempts" },
      };
      setErr({ title: map[api.code]?.title ?? "Could not join", message: api.message });
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card className="shadow-sm">
      <CardHeader>
        <Button variant="ghost" size="sm" className="mb-1 w-fit px-2 text-muted-foreground" onClick={onBack}>
          <ArrowLeft className="mr-1.5 h-4 w-4" /> Back
        </Button>
        <CardTitle>Join a room</CardTitle>
        <CardDescription>Pick the language you want to HEAR. You always speak your own.</CardDescription>
      </CardHeader>
      <CardContent className="space-y-5">
        {err && <ErrorBanner title={err.title} message={err.message} onDismiss={() => setErr(null)} />}

        <div className="space-y-2">
          <Label htmlFor="join-code">
            <KeyRound className="mr-1.5 inline h-4 w-4" /> Room code
          </Label>
          <Input
            id="join-code"
            placeholder="e.g. K7Q2MX"
            value={joinCode}
            onChange={(e) => setJoinCode(e.target.value.toUpperCase())}
            maxLength={8}
            className="text-center font-mono text-lg tracking-[0.3em]"
          />
        </div>

        <div className="space-y-2">
          <Label htmlFor="join-name">Your name</Label>
          <Input
            id="join-name"
            placeholder="How others see you"
            value={store.displayName}
            onChange={(e) => store.setDisplayName(e.target.value)}
            maxLength={80}
          />
        </div>

        <div className="space-y-2">
          <Label>You speak</Label>
          <SourceLanguageSelect value={store.sourceLang || "__auto__"} onChange={(v) => store.setSourceLang(sourceValueToLang(v))} />
        </div>

        <div className="space-y-2">
          <Label>You want to hear</Label>
          <ListeningLanguageSelect value={store.listenLang} onChange={store.setListenLang} />
        </div>

        <div className="rounded-lg border bg-accent/50 p-4">
          <p className="mb-3 text-sm font-medium">Microphone check</p>
          <MicTestPanel micDeviceId={store.micDeviceId} onMicDeviceChange={store.setMicDevice} />
        </div>

        <Button
          className="h-12 w-full text-base font-medium transition-transform duration-150 active:scale-[0.99]"
          disabled={busy}
          onClick={submit}
        >
          {busy && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
          Join room
        </Button>
      </CardContent>
    </Card>
  );
}
