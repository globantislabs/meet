// Package palabra implements the server-side adapter for the Palabra
// Speech-to-Speech (S2S) translation service: one WebSocket session translates
// one source->target language pair of streamed PCM audio.
//
// SECURITY: Palabra client credentials and short-lived access tokens are
// server-side only. They are never sent to browsers and never logged.
package palabra

// ┌──────────────────────────────────────────────────────────────────────────┐
// │ !!! VERIFY AGAINST CURRENT PALABRA S2S DOCS BEFORE DEPLOYMENT !!!        │
// │                                                                          │
// │ Every message *type string* and *field name* below is a best-effort      │
// │ reconstruction. The TTS-only Palabra WebSocket protocol DIFFERS from the  │
// │ S2S protocol; do not copy names from TTS examples.                       │
// │                                                                          │
// │ All type strings live in MsgTypes (this file) and are overridable via    │
// │ env (PALABRA_MSG_TYPE_*) so a protocol drift needs NO code change.       │
// │ All field names live in the struct json tags in THIS FILE ONLY — if the  │
// │ real wire format differs, edit these structs and nothing else.           │
// │                                                                          │
// │ Specifically confirm: message type names, auth exchange shape, audio     │
// │ format (sample rate / encoding / chunk duration), session-ready ack,     │
// │ error & end-of-stream messages, usage event fields, and whether one      │
// │ session can emit multiple target languages (this code assumes NOT: one   │
// │ session per source->target pair).                                        │
// └──────────────────────────────────────────────────────────────────────────┘

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// MsgTypes holds the wire message type strings. Defaults are best-effort and
// configurable (see the file-level verification notice).
type MsgTypes struct {
	ClientInit         string `json:"client_init"`
	ClientInputAudio   string `json:"client_input_audio"`
	ClientPing         string `json:"client_ping"`
	ServerSessionReady string `json:"server_session_ready"`
	ServerOutputAudio  string `json:"server_output_audio"`
	ServerStatus       string `json:"server_status"`
	ServerError        string `json:"server_error"`
	ServerUsage        string `json:"server_usage"`
}

// DefaultMsgTypes returns the default (best-effort) message type names.
func DefaultMsgTypes() MsgTypes {
	return MsgTypes{
		ClientInit:         "init",
		ClientInputAudio:   "input_audio",
		ClientPing:         "ping",
		ServerSessionReady: "session_ready",
		ServerOutputAudio:  "output_audio",
		ServerStatus:       "status",
		ServerError:        "error",
		ServerUsage:        "usage",
	}
}

// Validate reports any unset message type (a mis-typed override would silently
// break the protocol handshake otherwise).
func (m MsgTypes) Validate() error {
	fields := map[string]*string{
		"client_init":          &m.ClientInit,
		"client_input_audio":   &m.ClientInputAudio,
		"client_ping":          &m.ClientPing,
		"server_session_ready": &m.ServerSessionReady,
		"server_output_audio":  &m.ServerOutputAudio,
		"server_status":        &m.ServerStatus,
		"server_error":         &m.ServerError,
		"server_usage":         &m.ServerUsage,
	}
	var missing []string
	for name, p := range fields {
		if strings.TrimSpace(*p) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("palabra: unset message types: %s", strings.Join(missing, ", "))
	}
	return nil
}

// Audio encodings. VERIFY the accepted values against current docs; the
// pipeline sends PCM16 little-endian mono.
const (
	EncodingPCM16 = "pcm16"
)

// ── Outbound (client -> server) ─────────────────────────────────────────────

// OutAuthInit is the first message after dialing: session authentication plus
// the translation parameters and audio format. VERIFY whether auth is carried
// here, in an Authorization header, or both (this code sends both).
type OutAuthInit struct {
	Type             string   `json:"type"` // MsgTypes.ClientInit
	AccessToken      string   `json:"access_token,omitempty"`
	ClientID         string   `json:"client_id,omitempty"`
	SessionID        string   `json:"session_id,omitempty"` // correlation/metering only
	SourceLang       string   `json:"source_lang,omitempty"`
	TargetLang       string   `json:"target_lang,omitempty"`
	InputSampleRate  int      `json:"input_sample_rate,omitempty"`
	OutputSampleRate int      `json:"output_sample_rate,omitempty"`
	InputEncoding    string   `json:"input_encoding,omitempty"`
	OutputEncoding   string   `json:"output_encoding,omitempty"`
	Voice            string   `json:"voice,omitempty"`
	Extra            []KVPair `json:"extra,omitempty"`
}

// KVPair carries free-form session parameters without hardcoding unknown
// provider fields.
type KVPair struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// OutInputAudio carries one chunk of PCM16 LE mono audio, base64 encoded.
// VERIFY: max chunk duration/size, whether a sample-rate field is expected,
// and whether frames should carry timestamps.
type OutInputAudio struct {
	Type       string `json:"type"` // MsgTypes.ClientInputAudio
	Data       string `json:"data"` // base64(PCM16 LE mono)
	Seq        uint64 `json:"seq,omitempty"`
	DurationMs int    `json:"duration_ms,omitempty"`
}

// OutPing is the client keepalive. The server may also rely on WebSocket-level
// ping/pong control frames (coder/websocket answers those automatically).
type OutPing struct {
	Type          string `json:"type"` // MsgTypes.ClientPing
	TimestampUnixMs int64 `json:"ts_unix_ms,omitempty"`
}

// ── Inbound (server -> client) ──────────────────────────────────────────────

// InSessionReady is the ack for OutAuthInit. Start blocks until it arrives
// (or times out). VERIFY the field names and whether extra negotiation data
// (e.g. negotiated rates) is returned.
type InSessionReady struct {
	Type             string `json:"type"` // MsgTypes.ServerSessionReady
	SessionID        string `json:"session_id,omitempty"`
	InputSampleRate  int    `json:"input_sample_rate,omitempty"`
	OutputSampleRate int    `json:"output_sample_rate,omitempty"`
}

// InOutputAudio carries one chunk of translated PCM audio, base64 encoded.
type InOutputAudio struct {
	Type       string `json:"type"` // MsgTypes.ServerOutputAudio
	Data       string `json:"data"` // base64(PCM16 LE mono)
	Seq        uint64 `json:"seq,omitempty"`
	SampleRate int    `json:"sample_rate,omitempty"`
}

// InStatus is an informational status update (e.g. "listening", degraded
// quality, backpressure hints). Rate-limit hints are detected heuristically.
type InStatus struct {
	Type   string `json:"type"` // MsgTypes.ServerStatus
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// InError reports a session-level failure. IsRateLimit distinguishes throttle
// errors so the worker can back off instead of hard-restarting.
type InError struct {
	Type      string `json:"type"` // MsgTypes.ServerError
	Code      string `json:"code,omitempty"`
	Message   string `json:"message,omitempty"`
	Retryable bool   `json:"retryable,omitempty"`
}

// IsRateLimit heuristically classifies throttle errors. VERIFY the real error
// taxonomy and tighten this.
func (e InError) IsRateLimit() bool {
	joined := strings.ToLower(e.Code + " " + e.Message)
	return strings.Contains(joined, "rate") || strings.Contains(joined, "quota") ||
		strings.Contains(joined, "throttle") || strings.Contains(joined, "too many")
}

// InUsage reports metered usage (billing/observability). VERIFY field names.
type InUsage struct {
	Type              string  `json:"type"` // MsgTypes.ServerUsage
	SessionSeconds    float64 `json:"session_seconds,omitempty"`
	TranslatedMinutes float64 `json:"translated_minutes,omitempty"`
	AudioSecondsIn    float64 `json:"audio_seconds_in,omitempty"`
	AudioSecondsOut   float64 `json:"audio_seconds_out,omitempty"`
}

// InboundKind classifies a parsed inbound message.
type InboundKind int

const (
	InboundUnknown InboundKind = iota
	InboundSessionReady
	InboundOutputAudio
	InboundStatus
	InboundError
	InboundUsage
)

// Inbound is the discriminated union returned by ParseInbound.
type Inbound struct {
	Kind   InboundKind
	Ready  *InSessionReady
	Audio  *InOutputAudio
	Status *InStatus
	Error  *InError
	Usage  *InUsage
}

// envelope is the minimal shape every inbound message shares.
type envelope struct {
	Type string `json:"type"`
}

// ParseInbound decodes a raw inbound text frame against the configured message
// type names. Unknown types return InboundUnknown (callers skip them) rather
// than an error, so forward-compatible additions don't break sessions.
func ParseInbound(mt MsgTypes, raw []byte) (*Inbound, error) {
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("palabra: parse envelope: %w", err)
	}
	in := &Inbound{}
	switch env.Type {
	case mt.ServerSessionReady:
		in.Kind = InboundSessionReady
		in.Ready = &InSessionReady{}
		if err := json.Unmarshal(raw, in.Ready); err != nil {
			return nil, fmt.Errorf("palabra: parse session_ready: %w", err)
		}
	case mt.ServerOutputAudio:
		in.Kind = InboundOutputAudio
		in.Audio = &InOutputAudio{}
		if err := json.Unmarshal(raw, in.Audio); err != nil {
			return nil, fmt.Errorf("palabra: parse output_audio: %w", err)
		}
	case mt.ServerStatus:
		in.Kind = InboundStatus
		in.Status = &InStatus{}
		if err := json.Unmarshal(raw, in.Status); err != nil {
			return nil, fmt.Errorf("palabra: parse status: %w", err)
		}
	case mt.ServerError:
		in.Kind = InboundError
		in.Error = &InError{}
		if err := json.Unmarshal(raw, in.Error); err != nil {
			return nil, fmt.Errorf("palabra: parse error: %w", err)
		}
	case mt.ServerUsage:
		in.Kind = InboundUsage
		in.Usage = &InUsage{}
		if err := json.Unmarshal(raw, in.Usage); err != nil {
			return nil, fmt.Errorf("palabra: parse usage: %w", err)
		}
	default:
		in.Kind = InboundUnknown
	}
	return in, nil
}

// EncodeJSON marshals an outbound message. Kept as a single point so message
// encoding (e.g. switching to a binary framing later) changes in one place.
func EncodeJSON(v any) ([]byte, error) {
	return json.Marshal(v)
}

// EncodeAudioB64 encodes a PCM byte chunk for the wire. VERIFY whether Palabra
// expects standard base64 (assumed here) or URL-safe base64.
func EncodeAudioB64(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}

// DecodeAudioB64 decodes inbound audio, tolerating both standard and URL-safe
// alphabets with or without padding.
func DecodeAudioB64(s string) ([]byte, error) {
	if s == "" {
		return nil, fmt.Errorf("palabra: empty audio payload")
	}
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	if b, err := base64.RawStdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	if b, err := base64.URLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("palabra: decode base64 audio: %w", err)
	}
	return b, nil
}
