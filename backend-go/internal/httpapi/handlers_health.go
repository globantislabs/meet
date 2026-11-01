// Readiness probe: verifies the backing services (Postgres, Redis) before the
// API declares itself ready. Liveness (/v1/health) never checks dependencies.
package httpapi

import (
	"context"
	"net/http"
	"time"
)

// readinessDeps is satisfied by the concrete stores (pgxpool has Ping, so does
// the redis client). Declared as small interfaces so the package stays decoupled.
type Pinger interface {
	Ping(ctx context.Context) error
}

// SetReadinessProbes registers the dependency probes (called from cmd/api).
var readiness []Pinger

// AddReadinessProbe adds one dependency probe.
func AddReadinessProbe(p Pinger) { readiness = append(readiness, p) }

func (h *RoomHandlers) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	for _, p := range readiness {
		if err := p.Ping(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"status": "unavailable", "detail": "a backing service is unreachable",
			})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready"})
}
