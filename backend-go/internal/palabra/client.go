// Auth client for the Palabra access-token exchange. The client ID/secret are
// server-side credentials (blueprint §12) and are exchanged for a short-lived
// access token, which is cached until shortly before expiry so S2S session
// setup never blocks on a fresh auth round-trip.
//
// !!! VERIFY the exchange shape and response fields against the current
// provider docs. This implementation POSTs JSON credentials and tolerates the
// common response field names (access_token | token | accessToken) plus
// expires_in seconds or an absolute expiry timestamp.
package palabra

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// AuthClient exchanges client credentials for access tokens and caches the
// current one. Safe for concurrent use.
type AuthClient struct {
	Endpoint   string // e.g. https://auth.palabra.ai/access-token
	ClientID   string
	ClientSecret string
	HTTPClient *http.Client

	mu      sync.Mutex
	token   string
	expires time.Time
}

// NewAuthClient builds an auth client with sane HTTP defaults.
func NewAuthClient(endpoint, clientID, clientSecret string) *AuthClient {
	return &AuthClient{
		Endpoint:     endpoint,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		HTTPClient:   &http.Client{Timeout: 10 * time.Second},
	}
}

type authRequest struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

// authResponse tolerates the common token-response field spellings.
type authResponse struct {
	AccessToken string  `json:"access_token"`
	Token       string  `json:"token"`
	AccessToken2 string `json:"accessToken"`
	ExpiresIn   float64 `json:"expires_in"`          // seconds (typical)
	ExpiresAt   float64 `json:"expires_at_unix_ms"`  // absolute ms (alternative)
	TokenType   string  `json:"token_type"`
}

func (a *authResponse) token() string {
	switch {
	case a.AccessToken != "":
		return a.AccessToken
	case a.Token != "":
		return a.Token
	default:
		return a.AccessToken2
	}
}

func (a *authResponse) ttl() time.Duration {
	if a.ExpiresAt > 0 {
		ms := a.ExpiresAt - float64(time.Now().UnixMilli())
		if ms <= 0 {
			return time.Minute
		}
		return time.Duration(ms) * time.Millisecond
	}
	if a.ExpiresIn > 0 {
		return time.Duration(a.ExpiresIn * float64(time.Second))
	}
	// Conservative fallback when the response carries no expiry.
	return 10 * time.Minute
}

// AccessToken returns a cached token if still fresh, otherwise performs the
// credential exchange.
func (a *AuthClient) AccessToken(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.token != "" && time.Now().Before(a.expires) {
		return a.token, nil
	}

	body, err := json.Marshal(authRequest{ClientID: a.ClientID, ClientSecret: a.ClientSecret})
	if err != nil {
		return "", fmt.Errorf("palabra: marshal auth request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.Endpoint, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("palabra: auth request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("palabra: auth exchange: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("palabra: read auth response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Body is not logged (may echo secrets); status only.
		return "", fmt.Errorf("palabra: auth exchange status %d", resp.StatusCode)
	}

	var out authResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("palabra: parse auth response: %w", err)
	}
	tok := strings.TrimSpace(out.token())
	if tok == "" {
		return "", fmt.Errorf("palabra: auth response carried no token")
	}
	a.token = tok
	a.expires = time.Now().Add(out.ttl() - 60*time.Second) // refresh 60 s early
	if a.expires.Before(time.Now()) {
		a.expires = time.Now().Add(time.Minute)
	}
	return a.token, nil
}
