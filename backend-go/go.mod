module github.com/palabra/palabra-group-translator

go 1.22

// NOTE: run `go mod tidy` after checkout to resolve the full transitive
// dependency graph (indirect requirements are omitted here on purpose).
// Versions below are known-good stable majors; minor/patch versions are
// approximate and will be adjusted by tidy.
require (
        github.com/coder/websocket v1.8.12
        github.com/go-chi/chi/v5 v5.1.0
        github.com/hraban/opus v0.0.0-20230925102934-8ff7778f7cee
        github.com/jackc/pgx/v5 v5.6.0
        github.com/livekit/protocol v1.30.0
        github.com/livekit/server-sdk-go/v2 v2.6.1
        github.com/pion/rtp v1.8.9
        github.com/pion/samplebuilder v1.1.1
        github.com/pion/webrtc/v3 v3.2.42
        github.com/prometheus/client_golang v1.19.1
        github.com/redis/go-redis/v9 v9.5.3
        github.com/rs/zerolog v1.33.0
        golang.org/x/time v0.5.0
        google.golang.org/grpc v1.65.0
        google.golang.org/protobuf v1.34.2
)
