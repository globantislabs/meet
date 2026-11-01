"use client";

// SettingsDialog (blueprint §11.2.F), LIGHT THEME: microphone/speaker
// selection with a REAL input level meter, noise suppression/echo
// cancellation preferences and network info. Token classes only.

import { useEffect, useState } from "react";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Switch } from "@/components/ui/switch";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Separator } from "@/components/ui/separator";
import { audioEngine } from "@/lib/audio-engine";
import { livekitEngine } from "@/lib/livekit-engine";
import { useSession } from "@/stores/session-store";
import { AudioLevelMeter } from "./states";

export function SettingsDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const s = useSession();
  const [mics, setMics] = useState<MediaDeviceInfo[]>([]);
  const [speakers, setSpeakers] = useState<MediaDeviceInfo[]>([]);
  const [level, setLevel] = useState(0);
  const [testing, setTesting] = useState(false);
  const [echo, setEcho] = useState(true);
  const [ns, setNs] = useState(true);

  useEffect(() => {
    if (!open) return;
    void (async () => {
      setMics(await audioEngine.listMics());
      setSpeakers(await audioEngine.listSpeakers());
    })();
  }, [open]);

  const testMic = async () => {
    if (testing) {
      audioEngine.stopMic();
      setTesting(false);
      setLevel(0);
      return;
    }
    try {
      await audioEngine.requestMic(s.micDeviceId || undefined);
      audioEngine.startMeter(setLevel);
      setTesting(true);
    } catch {
      /* MicTestPanel surfaces permission issues during join */
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Audio settings</DialogTitle>
          <DialogDescription>
            Devices and audio processing for this room.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-5">
          <div className="space-y-2">
            <Label>Microphone</Label>
            <div className="flex items-center gap-3">
              <Select
                value={s.micDeviceId || "__default__"}
                onValueChange={async (v) => {
                  const id = v === "__default__" ? "" : v;
                  s.setMicDevice(id);
                  if (livekitEngine.getState() === "connected" && id) {
                    await livekitEngine.changeMicDevice(id).catch(() => undefined);
                  }
                }}
              >
                <SelectTrigger className="flex-1 bg-card">
                  <SelectValue placeholder="Default microphone" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="__default__">Default microphone</SelectItem>
                  {mics.map((m) => (
                    <SelectItem key={m.deviceId} value={m.deviceId}>
                      {m.label || "Microphone"}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <button
                onClick={testMic}
                className="whitespace-nowrap rounded-md border bg-card px-3.5 py-2 text-xs font-medium transition-colors duration-150 hover:bg-accent focus-visible:ring-[3px] focus-visible:ring-ring/50 focus-visible:outline-none"
              >
                {testing ? "Stop" : "Test"}
              </button>
            </div>
            {testing && <AudioLevelMeter level={level} />}
          </div>

          <div className="space-y-2">
            <Label>Speaker</Label>
            <Select
              value={s.speakerDeviceId || "__default__"}
              onValueChange={(v) => s.setSpeakerDevice(v === "__default__" ? "" : v)}
            >
              <SelectTrigger className="w-full bg-card">
                <SelectValue placeholder="Default speaker" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="__default__">Default speaker</SelectItem>
                {speakers.map((d) => (
                  <SelectItem key={d.deviceId} value={d.deviceId}>
                    {d.label || "Speaker"}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <p className="text-xs leading-relaxed text-muted-foreground">
              Output device switching uses your browser/OS default where the browser does not allow per-element selection.
            </p>
          </div>

          <Separator />

          <div className="flex items-center justify-between">
            <div>
              <Label>Echo cancellation</Label>
              <p className="text-xs text-muted-foreground">Prevents translated audio feeding back into your mic</p>
            </div>
            <Switch checked={echo} onCheckedChange={setEcho} />
          </div>
          <div className="flex items-center justify-between">
            <div>
              <Label>Noise suppression</Label>
              <p className="text-xs text-muted-foreground">Cleaner input for the translation engine</p>
            </div>
            <Switch checked={ns} onCheckedChange={setNs} />
          </div>

          <Separator />

          <div className="text-sm text-muted-foreground">
            <p className="font-medium text-foreground">Network</p>
            <p className="mt-1 leading-relaxed">
              Connection: <span className="font-mono text-xs">{livekitEngine.getState()}</span> · WebRTC media (UDP where possible, TURN fallback).
              Translation runs server-side close to the realtime network.
            </p>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}
