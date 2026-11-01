// Middleware: request IDs, panic recovery, per-IP rate limiting and
// bearer-token authentication for participant-scoped endpoints.
package httpapi

import (
        "context"
        "net/http"
        "sync"
        "time"

        "github.com/go-chi/chi/v5/middleware"
        "github.com/prometheus/client_golang/prometheus"
        "github.com/rs/zerolog"
        "golang.org/x/time/rate"

        "github.com/palabra/palabra-group-translator/internal/auth"
)

// ctxKey is the private context key type for request-scoped values.
type ctxKey int

const (
        ctxKeyClaims ctxKey = iota
        ctxKeyReqID
)

// promRegistry backs /v1/metrics (assembled in cmd/api via SetPromRegistry).
var promRegistry prometheus.Gatherer = prometheus.DefaultGatherer

// SetPromRegistry points /v1/metrics at a custom registry (called from cmd/api
// before serving starts).
func SetPromRegistry(g prometheus.Gatherer) { promRegistry = g }

var requestID = middleware.RequestID

// processLog is the package logger (installed by SetLogger from cmd/api).
var processLog = zerolog.Nop()

// SetLogger installs the API process logger.
func SetLogger(l zerolog.Logger) { processLog = l }

// recoverer converts panics into 500s without leaking stack traces to clients.
func recoverer(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
                defer func() {
                        if rec := recover(); rec != nil {
                                processLog.Error().Str("request_id", middleware.GetReqID(r.Context())).
                                        Msg("http: panic recovered")
                                writeError(w, http.StatusInternalServerError, "internal_error", "unexpected server error")
                        }
                }()
                next.ServeHTTP(w, r)
        })
}

// ipRateLimiter keeps an in-memory token bucket per client IP. Single-replica
// scope (documented): a multi-replica API would move this to Redis.
type ipRateLimiter struct {
        mu       sync.Mutex
        visitors map[string]*visitor
        rps      rate.Limit
        burst    int
}

type visitor struct {
        lim  *rate.Limiter
        seen time.Time
}

func newIPRateLimiter(rps float64, burst int) *ipRateLimiter {
        if rps <= 0 {
                rps = 2
        }
        if burst <= 0 {
                burst = 10
        }
        l := &ipRateLimiter{
                visitors: make(map[string]*visitor),
                rps:      rate.Limit(rps),
                burst:    burst,
        }
        go l.reaper()
        return l
}

func (l *ipRateLimiter) allow(ip string) bool {
        l.mu.Lock()
        defer l.mu.Unlock()
        v, ok := l.visitors[ip]
        if !ok {
                v = &visitor{lim: rate.NewLimiter(l.rps, l.burst)}
                l.visitors[ip] = v
        }
        v.seen = time.Now()
        return v.lim.Allow()
}

// reaper evicts idle visitors so the map cannot grow unbounded.
func (l *ipRateLimiter) reaper() {
        for range time.Tick(5 * time.Minute) {
                l.mu.Lock()
                for ip, v := range l.visitors {
                        if time.Since(v.seen) > 30*time.Minute {
                                delete(l.visitors, ip)
                        }
                }
                l.mu.Unlock()
        }
}

var lastLimiter *ipRateLimiter

// rateLimit installs the process-wide per-IP limiter (configured via
// ConfigureRateLimit from cmd/api before serving).
func rateLimit(next http.Handler) http.Handler {
        if lastLimiter == nil {
                lastLimiter = newIPRateLimiter(2, 10)
        }
        lim := lastLimiter
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
                if !lim.allow(clientIP(r)) {
                        writeError(w, http.StatusTooManyRequests, "rate_limited", "too many requests; slow down and retry")
                        return
                }
                next.ServeHTTP(w, r)
        })
}

// ConfigureRateLimit sets the process limiter parameters before serving.
func ConfigureRateLimit(rps float64, burst int) {
        lastLimiter = newIPRateLimiter(rps, burst)
}

func clientIP(r *http.Request) string {
        // Render terminates TLS and proxies; trust the standard proxy header.
        if xf := r.Header.Get("X-Forwarded-For"); xf != "" {
                for i := 0; i < len(xf); i++ {
                        if xf[i] == ',' {
                                return trimSpace(xf[:i])
                        }
                }
                return trimSpace(xf)
        }
        return r.RemoteAddr
}

func trimSpace(s string) string {
        start, end := 0, len(s)
        for start < end && (s[start] == ' ' || s[start] == '\t') {
                start++
        }
        for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
                end--
        }
        return s[start:end]
}

// authenticate verifies the HMAC participant token and stores the claims in
// the request context. Handlers then enforce room membership against the DB.
func (h *RoomHandlers) authenticate(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
                authz := r.Header.Get("Authorization")
                if len(authz) < 8 || (authz[:7] != "Bearer " && authz[:7] != "bearer ") {
                        writeError(w, http.StatusUnauthorized, "unauthorized", "missing bearer token")
                        return
                }
                claims, err := h.tokenizer.Verify(trimSpace(authz[7:]))
                if err != nil {
                        writeError(w, http.StatusUnauthorized, "unauthorized", "invalid or expired token; rejoin the room")
                        return
                }
                ctx := context.WithValue(r.Context(), ctxKeyClaims, claims)
                next.ServeHTTP(w, r.WithContext(ctx))
        })
}

// claimsOf extracts the authenticated participant claims.
func claimsOf(ctx context.Context) (auth.Claims, bool) {
        c, ok := ctx.Value(ctxKeyClaims).(auth.Claims)
        return c, ok
}
