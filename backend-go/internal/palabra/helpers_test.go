package palabra

import "testing"

// TestInErrorIsRateLimit covers the heuristic throttle classifier.
func TestInErrorIsRateLimit(t *testing.T) {
        cases := []struct {
                in   InError
                want bool
        }{
                {InError{Code: "rate_limited"}, true},
                {InError{Message: "Too many requests"}, true},
                {InError{Code: "quota_exceeded"}, true},
                {InError{Message: "invalid source language"}, false},
                {InError{}, false},
        }
        for i, c := range cases {
                if got := c.in.IsRateLimit(); got != c.want {
                        t.Errorf("case %d: IsRateLimit() = %v, want %v", i, got, c.want)
                }
        }
}

// TestAudioB64RoundTrip covers the tolerant base64 codec.
func TestAudioB64RoundTrip(t *testing.T) {
        raw := []byte{0, 1, 2, 250, 251, 255}
        enc := EncodeAudioB64(raw)
        got, err := DecodeAudioB64(enc)
        if err != nil || string(got) != string(raw) {
                t.Fatalf("round trip failed: %v", err)
        }
        if _, err := DecodeAudioB64(""); err == nil {
                t.Fatal("empty payload must error")
        }
}

// TestParseInboundUnknownTypeIsForwardCompatible ensures unknown inbound
// message types do not fail parsing (blueprint §8 requirement).
func TestParseInboundUnknownType(t *testing.T) {
        mt := DefaultMsgTypes()
        in, err := ParseInbound(mt, []byte(`{"type":"brand_new_future_message","x":1}`))
        if err != nil {
                t.Fatalf("unknown type must not error: %v", err)
        }
        if in.Kind != InboundUnknown {
                t.Fatalf("kind = %v, want InboundUnknown", in.Kind)
        }
}
