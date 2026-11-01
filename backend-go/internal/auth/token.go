// Package auth issues and verifies short-lived, HMAC-signed participant
// tokens used by the browser to call the control-plane API.
//
// SECURITY MODEL
//
//   - Tokens are issued by the API service after room admission and carry the
//     participant + room binding, an expiry, and the granted permissions.
//   - Tokens are stateless (HMAC-SHA256 over a compact JSON claim set) so the
//     API can verify them without a round-trip; revocation is achieved by
//     short TTLs plus room-ended checks.
//   - In production this should be replaced by (or federated with) a proper
//     OIDC provider; the interface below is intentionally small so the swap is
//     local to this package.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Claims is the signed payload carried by every participant token.
type Claims struct {
	ParticipantID string `json:"pid"`
	RoomID        string `json:"rid"`
	LiveKitID     string `json:"lid,omitempty"`
	DisplayName   string `json:"name,omitempty"`
	CanPublish    bool   `json:"pub"`
	IssuedAt      int64  `json:"iat"`
	ExpiresAt     int64  `json:"exp"`
}

// ErrInvalidToken is returned for any malformed, tampered or expired token.
var ErrInvalidToken = errors.New("auth: invalid or expired token")

// Tokenizer signs and verifies participant tokens with a server-side secret.
// The secret must be at least 32 bytes of entropy in production.
type Tokenizer struct {
	secret []byte
	nowFn  func() time.Time
}

// NewTokenizer builds a Tokenizer from a raw secret.
func NewTokenizer(secret string) *Tokenizer {
	return &Tokenizer{secret: []byte(secret), nowFn: time.Now}
}

// Issue mints a signed token for the given claims with the supplied TTL.
func (t *Tokenizer) Issue(c Claims, ttl time.Duration) (string, error) {
	now := t.nowFn().UTC()
	c.IssuedAt = now.Unix()
	c.ExpiresAt = now.Add(ttl).Unix()
	if c.ParticipantID == "" {
		// Participants are anonymous; identity is the server-issued ID.
		id, err := NewID()
		if err != nil {
			return "", fmt.Errorf("auth: participant id: %w", err)
		}
		c.ParticipantID = id
	}
	payload, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("auth: marshal claims: %w", err)
	}
	mac := hmac.New(sha256.New, t.secret)
	mac.Write(payload)
	sig := mac.Sum(nil)

	body := base64.RawURLEncoding.EncodeToString(payload)
	stamp := base64.RawURLEncoding.EncodeToString(sig)
	return body + "." + stamp, nil
}

// Verify parses, authenticates and validates expiry of a token.
func (t *Tokenizer) Verify(token string) (Claims, error) {
	var c Claims
	body, stamp, ok := split2(token, ".")
	if !ok {
		return c, ErrInvalidToken
	}
	payload, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return c, ErrInvalidToken
	}
	sig, err := base64.RawURLEncoding.DecodeString(stamp)
	if err != nil {
		return c, ErrInvalidToken
	}
	mac := hmac.New(sha256.New, t.secret)
	mac.Write(payload)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return c, ErrInvalidToken
	}
	if err := json.Unmarshal(payload, &c); err != nil {
		return c, ErrInvalidToken
	}
	if t.nowFn().Unix() > c.ExpiresAt {
		return c, ErrInvalidToken
	}
	return c, nil
}

// NewID returns a 128-bit random identifier encoded as 22 base64url chars.
func NewID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// NewJoinCode returns a short human-shareable room code (e.g. "K7Q2MX").
// Ambiguous characters (0/O, 1/I) are excluded.
func NewJoinCode() (string, error) {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	out := make([]byte, len(b))
	for i, v := range b {
		out[i] = alphabet[int(v)%len(alphabet)]
	}
	return string(out), nil
}

// split2 splits s on the first sep into exactly two non-empty parts.
func split2(s, sep string) (string, string, bool) {
	for i := 0; i+len(sep) <= len(s); i++ {
		if s[i:i+len(sep)] == sep {
			a, b := s[:i], s[i+len(sep):]
			if a != "" && b != "" {
				return a, b, true
			}
			return "", "", false
		}
	}
	return "", "", false
}
