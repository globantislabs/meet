// Package httpapi wires the REST control-plane API (blueprint §10) on chi:
// room CRUD, join/leave, language preferences, invites, languages list and
// health/metrics endpoints, with CORS for the browser frontend and
// bearer-token authentication for participant-scoped operations.
package httpapi

import (
        "net/http"
        "strings"

        "github.com/go-chi/chi/v5"
        "github.com/prometheus/client_golang/prometheus/promhttp"
)

// Deps is the full handler dependency set (assembled in cmd/api).
type Deps struct {
        Rooms   *RoomHandlers
        Origins []string // allowed CORS origins; ["*"] allows all
}

// NewRouter builds the v1 API router.
func NewRouter(d Deps) chi.Router {
        r := chi.NewRouter()

        r.Use(requestID)
        r.Use(recoverer)
        r.Use(cors(d.Origins))
        r.Use(rateLimit)

        // Unauthenticated endpoints.
        r.Get("/v1/health", d.Rooms.health)
        r.Get("/v1/ready", d.Rooms.ready)
        r.Get("/v1/languages", d.Rooms.languages)
        r.Get("/v1/metrics", promhttp.HandlerFor(promRegistry, promhttp.HandlerOpts{}).ServeHTTP)

        // Room endpoints. Some require no participant token (create, join);
        // the rest authenticate with the HMAC participant token.
        r.Post("/v1/rooms", d.Rooms.create)
        r.Post("/v1/rooms/{roomID}/join", d.Rooms.join)
        r.Get("/v1/rooms/by-code/{code}", d.Rooms.byCode)

        r.Group(func(pr chi.Router) {
                pr.Use(d.Rooms.authenticate)
                pr.Get("/v1/rooms/{roomID}", d.Rooms.getRoom)
                pr.Get("/v1/rooms/{roomID}/participants", d.Rooms.listParticipants)
                pr.Post("/v1/rooms/{roomID}/leave", d.Rooms.leave)
                pr.Patch("/v1/rooms/{roomID}/me/preferences", d.Rooms.updatePreferences)
                pr.Post("/v1/rooms/{roomID}/invite", d.Rooms.invite)
                pr.Delete("/v1/rooms/{roomID}", d.Rooms.endRoom)
        })

        return r
}

// SplitOrigins parses the CSV CORS origin list (exported for cmd/api).
func SplitOrigins(csv string) []string {
        if strings.TrimSpace(csv) == "" {
                return []string{"*"}
        }
        parts := strings.Split(csv, ",")
        out := make([]string, 0, len(parts))
        for _, p := range parts {
                if p = strings.TrimSpace(p); p != "" {
                        out = append(out, p)
                }
        }
        return out
}

// originAllowed reports whether the request origin is allowed. "*" allows
// every origin (dev only; production configures the Vercel domain).
func originAllowed(allowed []string, origin string) bool {
        for _, a := range allowed {
                if a == "*" || strings.EqualFold(a, origin) {
                        return true
                }
        }
        return false
}

// cors is a minimal, dependency-free CORS middleware for the allowed origins.
func cors(allowed []string) func(http.Handler) http.Handler {
        return func(next http.Handler) http.Handler {
                return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
                        origin := req.Header.Get("Origin")
                        if origin != "" && originAllowed(allowed, origin) {
                                h := w.Header()
                                h.Set("Access-Control-Allow-Origin", origin)
                                h.Set("Vary", "Origin")
                                h.Set("Access-Control-Allow-Credentials", "true")
                                h.Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
                                h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
                                h.Set("Access-Control-Max-Age", "600")
                        }
                        if req.Method == http.MethodOptions {
                                w.WriteHeader(http.StatusNoContent)
                                return
                        }
                        next.ServeHTTP(w, req)
                })
        }
}
