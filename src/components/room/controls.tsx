"use client";

// Language selectors (blueprint §11.3: LanguagePreferenceSelector,
// SourceLanguageSelector) + MicTestPanel with a REAL level meter.
// Bridge Connect: token classes only; language data may carry the flag,
// names stay in the UI voice; the meter is status color, nothing else.

import { useCallback, useEffect, useRef, useState } from "react";
import { Check, ChevronsUpDown, Mic, Square } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from "@/components/ui/command";
import { LANGUAGES, findLanguage } from "@/lib/languages";
import { audioEngine } from "@/lib/audio-engine";
import { cn } from "@/lib/utils";
import { AudioLevelMeter, PermissionHelp } from "./states";

function LangFlag({ code }: { code: string }) {
  const l = findLanguage(code);
  return <span aria-hidden>{l ? `${l.flag} ` : ""}</span>;
}

// Preferred listening language — exactly one per participant (blueprint §2).
export function ListeningLanguageSelect({
  value,
  onChange,
  id,
}: {
  value: string;
  onChange: (code: string) => void;
  id?: string;
}) {
  const [open, setOpen] = useState(false);
  const selected = findLanguage(value);
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button
          id={id}
          variant="outline"
          role="combobox"
          aria-expanded={open}
          className="w-full justify-between bg-card font-normal"
        >
          <span>
            {selected ? (
              <>
                <LangFlag code={value} />
                {selected.name} <span className="text-muted-foreground">· {selected.native}</span>
              </>
            ) : (
              "Select your language"
            )}
          </span>
          <ChevronsUpDown className="ml-2 h-4 w-4 shrink-0 opacity-50" aria-hidden />
        </Button>
      </PopoverTrigger>
      <PopoverContent className="w-[320px] p-0" align="start">
        <Command>
          <CommandInput placeholder="Search languages…" />
          <CommandList className="max-h-64">
            <CommandEmpty>No language found.</CommandEmpty>
            <CommandGroup>
              {LANGUAGES.map((l) => (
                <CommandItem
                  key={l.code}
                  value={`${l.name} ${l.native}`}
                  onSelect={() => {
                    onChange(l.code);
                    setOpen(false);
                  }}
                >
                  <span className="mr-2" aria-hidden>{l.flag}</span>
                  {l.name} <span className="ml-1 text-muted-foreground">{l.native}</span>
                  <Check className={cn("ml-auto h-4 w-4", value === l.code ? "opacity-100" : "opacity-0")} aria-hidden />
                </CommandItem>
              ))}
            </CommandGroup>
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  );
}

// Spoken/source language. Empty value = auto-detect, a provider capability
// that must be enabled in deployment (blueprint §1 "Important").
export function SourceLanguageSelect({
  value,
  onChange,
}: {
  value: string;
  onChange: (code: string) => void;
}) {
  return (
    <Select value={value} onValueChange={onChange}>
      <SelectTrigger className="w-full bg-card">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value="__auto__">
          Auto-detect <span className="text-muted-foreground">(if supported)</span>
        </SelectItem>
        {LANGUAGES.map((l) => (
          <SelectItem key={l.code} value={l.code}>
            <span aria-hidden>{l.flag}</span> {l.name} <span className="text-muted-foreground">· {l.native}</span>
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}

export function sourceValueToLang(v: string): string {
  return v === "__auto__" ? "" : v;
}

export function langDisplay(code: string): string {
  if (code === "" || code === "__auto__") return "Auto-detect";
  const l = findLanguage(code);
  return l ? l.name : code;
}

export function langFlag(code: string): string {
  return findLanguage(code)?.flag ?? "";
}

// MicTestPanel — permission flow + device pick + REAL level meter.
export function MicTestPanel({
  micDeviceId,
  onMicDeviceChange,
  compact,
}: {
  micDeviceId: string;
  onMicDeviceChange: (id: string) => void;
  compact?: boolean;
}) {
  const [testing, setTesting] = useState(false);
  const [level, setLevel] = useState(0);
  const [mics, setMics] = useState<MediaDeviceInfo[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const levelRef = useRef<number>(0);

  const refreshMics = useCallback(async () => {
    try {
      setMics(await audioEngine.listMics());
    } catch {
      /* enumeration may fail pre-permission; not fatal */
    }
  }, []);

  useEffect(() => {
    // Deferred: device labels are only populated after permission is granted,
    // so refresh asynchronously instead of synchronizing state in the effect.
    const t = setTimeout(() => {
      void refreshMics();
    }, 0);
    return () => clearTimeout(t);
  }, [refreshMics]);

  const startTest = async () => {
    setErr(null);
    try {
      await audioEngine.requestMic(micDeviceId || undefined);
      await audioEngine.unlockPlayback();
      audioEngine.startMeter((rms) => {
        levelRef.current = rms;
        setLevel(rms);
      });
      setTesting(true);
      void refreshMics();
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Microphone test failed");
    }
  };

  const stopTest = () => {
    audioEngine.stopMic();
    setTesting(false);
    setLevel(0);
  };

  useEffect(() => () => audioEngine.stopMic(), []);

  return (
    <div className="space-y-3">
      <div className="flex items-center gap-3">
        <Button
          type="button"
          variant={testing ? "outline" : "secondary"}
          size="sm"
          className={cn(
            testing && "border-destructive/30 text-destructive hover:bg-destructive/10 hover:text-destructive"
          )}
          onClick={testing ? stopTest : startTest}
        >
          {testing ? <Square className="mr-2 h-3.5 w-3.5" /> : <Mic className="mr-2 h-3.5 w-3.5" />}
          {testing ? "Stop test" : "Test microphone"}
        </Button>
        {testing ? <AudioLevelMeter level={level} /> : <span className="text-xs text-muted-foreground">Levels shown are live from your mic</span>}
      </div>

      <Select value={micDeviceId || "__default__"} onValueChange={(v) => onMicDeviceChange(v === "__default__" ? "" : v)}>
        <SelectTrigger className="w-full bg-card" aria-label="Microphone device">
          <SelectValue placeholder="Default microphone" />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="__default__">Default microphone</SelectItem>
          {mics.map((m) => (
            <SelectItem key={m.deviceId} value={m.deviceId}>
              {m.label || `Microphone ${m.deviceId.slice(0, 5)}`}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>

      {!compact && err && (
        err.toLowerCase().includes("denied") || err.toLowerCase().includes("blocked") ? (
          <PermissionHelp />
        ) : (
          <p className="text-sm text-destructive">{err}</p>
        )
      )}
    </div>
  );
}
