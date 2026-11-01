// AudioEngine — Web Audio API layer (blueprint §7.4):
//   - microphone permission + device enumeration
//   - REAL input level metering via AnalyserNode (no fake waveforms, §11.1)
//   - playback unlock handling (browser autoplay policy)
// Everything here runs in the browser only.

export type MicPermission = "unknown" | "granted" | "denied" | "unavailable";

export class AudioEngine {
  private ctx: AudioContext | null = null;
  private analyser: AnalyserNode | null = null;
  private micStream: MediaStream | null = null;
  private buf: Float32Array<ArrayBuffer> | null = null;
  private raf = 0;

  // List of input devices (after permission is granted we get labels).
  async listDevices(): Promise<MediaDeviceInfo[]> {
    if (!navigator.mediaDevices?.enumerateDevices) return [];
    return navigator.mediaDevices.enumerateDevices();
  }

  async listMics(): Promise<MediaDeviceInfo[]> {
    const devs = await this.listDevices();
    return devs.filter((d) => d.kind === "audioinput");
  }

  async listSpeakers(): Promise<MediaDeviceInfo[]> {
    const devs = await this.listDevices();
    return devs.filter((d) => d.kind === "audiooutput");
  }

  get permission(): MicPermission {
    if (!navigator.mediaDevices?.getUserMedia) return "unavailable";
    return "unknown"; // decided by the first getUserMedia call
  }

  // requestMic opens the microphone and starts real RMS level metering.
  async requestMic(deviceId?: string): Promise<MediaStream> {
    if (!navigator.mediaDevices?.getUserMedia) {
      throw new Error("This browser does not support microphone capture.");
    }
    this.stopMeter();
    this.micStream?.getTracks().forEach((t) => t.stop());

    const constraints: MediaStreamConstraints = {
      audio: {
        deviceId: deviceId ? { exact: deviceId } : undefined,
        echoCancellation: true,
        noiseSuppression: true,
        autoGainControl: true,
      },
      video: false,
    };
    try {
      this.micStream = await navigator.mediaDevices.getUserMedia(constraints);
    } catch (e) {
      const err = e as DOMException;
      if (err.name === "NotAllowedError" || err.name === "SecurityError") {
        throw new Error("Microphone permission was denied. Allow mic access in your browser settings and try again.");
      }
      if (err.name === "NotFoundError" || err.name === "OverconstrainedError") {
        throw new Error("No microphone was found. Connect a microphone and try again.");
      }
      throw new Error(`Could not open the microphone (${err.name}).`);
    }
    return this.micStream;
  }

  // startMeter computes RMS levels from the current mic stream.
  startMeter(onLevel: (rms: number) => void) {
    if (!this.micStream) return;
    this.stopMeter();
    this.ctx = this.ctx ?? new AudioContext();
    void this.ctx.resume();
    const src = this.ctx.createMediaStreamSource(this.micStream);
    const an = this.ctx.createAnalyser();
    an.fftSize = 1024;
    src.connect(an);
    this.analyser = an;
    this.buf = new Float32Array(an.fftSize);

    const loop = () => {
      if (!this.analyser || !this.buf) return;
      this.analyser.getFloatTimeDomainData(this.buf);
      let sum = 0;
      for (let i = 0; i < this.buf.length; i++) sum += this.buf[i] * this.buf[i];
      const rms = Math.sqrt(sum / this.buf.length);
      onLevel(rms);
      this.raf = requestAnimationFrame(loop);
    };
    this.raf = requestAnimationFrame(loop);
  }

  stopMeter() {
    if (this.raf) cancelAnimationFrame(this.raf);
    this.raf = 0;
  }

  currentMicStream(): MediaStream | null {
    return this.micStream;
  }

  // stopMic releases the microphone.
  stopMic() {
    this.stopMeter();
    this.micStream?.getTracks().forEach((t) => t.stop());
    this.micStream = null;
  }

  // unlockPlayback resumes the shared AudioContext after a user gesture —
  // required by autoplay policies before remote audio can play.
  async unlockPlayback() {
    this.ctx = this.ctx ?? new AudioContext();
    if (this.ctx.state === "suspended") await this.ctx.resume();
  }

  playbackBlocked(): boolean {
    return this.ctx?.state === "suspended";
  }
}

export const audioEngine = new AudioEngine();

// AudioLevelBar renders a real level meter (used by mic test + self card).
export function levelToBars(rms: number): number {
  // Map RMS (typ. 0.005..0.35 for speech) onto 5 bars.
  const db = 20 * Math.log10(Math.max(rms, 1e-6));
  // -60dB -> 0 bars, -10dB -> 5 bars
  const norm = Math.min(1, Math.max(0, (db + 60) / 50));
  return Math.round(norm * 5);
}
