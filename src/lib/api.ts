// Typed client for the Go control-plane API (backend-go/internal/httpapi).
// The base URL comes from NEXT_PUBLIC_API_URL (the Render deployment).
// All errors are normalized to ApiError with the API's stable error codes so
// the UI can react precisely (blueprint §11.2.G).
//
// Contract (backend-go/internal/rooms/service.go):
//   POST   /v1/rooms                      → JoinResult
//   POST   /v1/rooms/{id}/join            → JoinResult
//   GET    /v1/rooms/{id}                 → Snapshot (Bearer)
//   POST   /v1/rooms/{id}/leave           → {left}
//   PATCH  /v1/rooms/{id}/me/preferences  → Snapshot (Bearer)
//   POST   /v1/rooms/{id}/invite          → InviteResult (Bearer)
//   DELETE /v1/rooms/{id}                 → {ended} (Bearer, owner)
//   GET    /v1/languages                  → language allowlist

export const API_URL = (process.env.NEXT_PUBLIC_API_URL ?? "").replace(/\/+$/, "");

export interface RoomDTO {
  id: string;
  code: string;
  name: string;
  owner_id: string;
  capacity: number;
  status: string; // active | ended
  translation_mode: string;
  livekit_room: string;
  created_at: string;
  expires_at: string;
}

export interface ParticipantDTO {
  id: string;
  display_name: string;
  source_lang: string; // "" = auto-detect (capability-gated)
  listen_lang: string;
  mic_enabled: boolean;
  joined_at: string;
  left_at?: string;
}

export interface ParticipantViewDTO {
  id: string;
  name: string;
  source_lang: string;
  listen_lang: string;
  mic_enabled: boolean;
  connection_state: string;
  is_owner: boolean;
  is_self: boolean;
  joined_at: string;
}

export interface RouteSummaryDTO {
  ready: number;
  starting: number;
  error: number;
  total: number;
}

export interface MyViewDTO {
  listen_lang: string;
  source_lang: string;
  original_speakers: string[]; // participant IDs audible in original audio
  translated_tracks: string[]; // expected translated track names for me
  listen_ready: boolean;
}

export interface SnapshotDTO {
  room: RoomDTO;
  participants: ParticipantViewDTO[];
  routes: RouteSummaryDTO;
  me: MyViewDTO | null;
}

export interface JoinResult {
  room: RoomDTO;
  participant: ParticipantDTO;
  participant_token: string;
  livekit_url: string;
  livekit_token: string;
}

export class ApiError extends Error {
  code: string;
  status: number;
  constructor(code: string, message: string, status: number) {
    super(message);
    this.code = code;
    this.status = status;
  }
}

async function request<T>(path: string, init?: RequestInit & { token?: string }): Promise<T> {
  if (!API_URL) {
    throw new ApiError(
      "not_configured",
      "The server address is not configured. Set NEXT_PUBLIC_API_URL to your Render API deployment, then redeploy.",
      0
    );
  }
  const headers: Record<string, string> = { "Content-Type": "application/json" };
  if (init?.token) headers["Authorization"] = `Bearer ${init.token}`;

  let res: Response;
  try {
    res = await fetch(`${API_URL}${path}`, {
      ...init,
      headers,
      cache: "no-store",
    });
  } catch {
    throw new ApiError("network_error", "Cannot reach the server. Check your connection and try again.", 0);
  }

  const body = (await res.json().catch(() => ({}))) as {
    error?: string;
    detail?: string;
    [k: string]: unknown;
  };
  if (!res.ok) {
    // API error shape: {"error": "<code>", "detail": "<message>"}
    throw new ApiError(body.error ?? "internal", body.detail ?? res.statusText, res.status);
  }
  return body as T;
}

// ── Endpoints (blueprint §10) ───────────────────────────────────────────────

export function createRoom(input: {
  name: string;
  capacity: number;
  display_name: string;
  source_lang: string;
  listen_lang: string;
}) {
  return request<JoinResult>("/v1/rooms", { method: "POST", body: JSON.stringify(input) });
}

export function joinRoom(
  roomId: string,
  input: { code: string; display_name: string; source_lang: string; listen_lang: string }
) {
  return request<JoinResult>(`/v1/rooms/${roomId}/join`, {
    method: "POST",
    body: JSON.stringify(input),
  });
}

// Join with an invitation code. The deep link may also carry the room id
// (/room/{id}?code=XXX) which skips the by-code lookup round trip.
export async function joinByCode(
  code: string,
  input: { display_name: string; source_lang: string; listen_lang: string },
  roomId?: string
): Promise<JoinResult> {
  const target =
    roomId ??
    (await request<{ room_id: string }>(`/v1/rooms/by-code/${encodeURIComponent(code)}`, {
      method: "GET",
    })).room_id;
  return joinRoom(target, {
    code,
    display_name: input.display_name,
    source_lang: input.source_lang,
    listen_lang: input.listen_lang,
  });
}

export function getRoomState(roomId: string, token: string) {
  return request<SnapshotDTO>(`/v1/rooms/${roomId}`, { method: "GET", token });
}

export function updatePreferences(
  roomId: string,
  token: string,
  input: { listen_lang?: string; source_lang?: string; mic_enabled?: boolean }
) {
  return request<SnapshotDTO>(`/v1/rooms/${roomId}/me/preferences`, {
    method: "PATCH",
    body: JSON.stringify(input),
    token,
  });
}

export function leaveRoom(roomId: string, token: string) {
  return request<{ left: boolean }>(`/v1/rooms/${roomId}/leave`, { method: "POST", token });
}

export function createInvite(roomId: string, token: string) {
  return request<{ code: string; link?: string }>(`/v1/rooms/${roomId}/invite`, {
    method: "POST",
    token,
  });
}

export function endRoom(roomId: string, token: string) {
  return request<{ ended: boolean }>(`/v1/rooms/${roomId}`, { method: "DELETE", token });
}

export function fetchLanguages() {
  return request<{ languages: { code: string; name: string; native_name: string }[]; auto_detect_source: boolean }>(
    "/v1/languages",
    { method: "GET" }
  );
}

export function health() {
  return request<{ status: string }>("/v1/health", { method: "GET" });
}
