// Package fake provides a wire-protocol fake Palabra S2S server: it speaks
// the same WebSocket message protocol as internal/palabra/session.go, so the
// REAL session implementation can be tested end-to-end without provider
// credentials (blueprint §13: "Use a fake Palabra server for deterministic
// protocol tests").
//
// It is test infrastructure only — never deployed.
package fake

import (
        "context"
        "encoding/json"
        "math"
        "net"
        "net/http"
        "sync"
        "time"

        "github.com/coder/websocket"

        "github.com/palabra/palabra-group-translator/internal/audio"
        "github.com/palabra/palabra-group-translator/internal/palabra"
)

// Server is a minimal fake S2S endpoint.
type Server struct {
        srv      *http.Server
        addr     string
        mt       palabra.MsgTypes
        mu       sync.Mutex
        sessions int
}

// New builds a fake server bound to addr (e.g. "127.0.0.1:0" to auto-assign).
func New(addr string, mt palabra.MsgTypes) *Server {
        s := &Server{addr: addr, mt: mt}
        mux := http.NewServeMux()
        mux.HandleFunc("/ws", s.handleWS)
        mux.HandleFunc("/access-token", s.handleAuth)
        s.srv = &http.Server{Addr: addr, Handler: mux}
        return s
}

// Start serves in the background; returns the listen address (use for :0).
func (s *Server) Start() error {
        ln, err := net.Listen("tcp", s.addr)
        if err != nil {
                return err
        }
        s.addr = ln.Addr().String()
        go func() { _ = s.srv.Serve(ln) }()
        return nil
}

// Addr returns the bound address after Start.
func (s *Server) Addr() string { return s.addr }

// Close shuts the server down.
func (s *Server) Close() error { return s.srv.Close() }

// Sessions reports how many WebSocket sessions were opened.
func (s *Server) Sessions() int {
        s.mu.Lock()
        defer s.mu.Unlock()
        return s.sessions
}

func (s *Server) handleAuth(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "application/json")
        _ = json.NewEncoder(w).Encode(map[string]any{
                "access_token": "fake-access-token",
                "expires_in":   3600,
        })
}

// handleWS accepts one session, acks init with session_ready, then converts
// every input_audio chunk into a delayed output_audio sine response.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
        conn, err := websocket.Accept(w, r, nil)
        if err != nil {
                return
        }
        defer conn.Close(websocket.StatusNormalClosure, "fake server closing")
        s.mu.Lock()
        s.sessions++
        s.mu.Unlock()

        ctx := r.Context()
        var srcRate, outRate int

        for {
                _, raw, err := conn.Read(ctx)
                if err != nil {
                        return
                }
                var env struct{ Type string `json:"type"` }
                if json.Unmarshal(raw, &env) != nil {
                        continue
                }
                switch env.Type {
                case s.mt.ClientInit:
                        var init palabra.OutAuthInit
                        _ = json.Unmarshal(raw, &init)
                        srcRate = init.InputSampleRate
                        outRate = init.OutputSampleRate
                        if outRate == 0 {
                                outRate = 16000
                        }
                        ready, _ := json.Marshal(palabra.InSessionReady{
                                Type: s.mt.ServerSessionReady, SessionID: init.SessionID,
                                InputSampleRate: srcRate, OutputSampleRate: outRate,
                        })
                        if conn.Write(ctx, websocket.MessageText, ready) != nil {
                                return
                        }
                case s.mt.ClientInputAudio:
                        var msg palabra.OutInputAudio
                        if json.Unmarshal(raw, &msg) != nil {
                                continue
                        }
                        data, err := palabra.DecodeAudioB64(msg.Data)
                        if err != nil || len(data) == 0 {
                                continue
                        }
                        // Emit one 20 ms sine chunk per received chunk (after 80 ms
                        // simulated translation latency).
                        n := len(data) / 2
                        samples := make([]int16, n)
                        for i := range samples {
                                samples[i] = int16(9000 * math.Sin(2*math.Pi*440*float64(i)/float64(outRate)))
                        }
                        out := palabra.InOutputAudio{
                                Type: s.mt.ServerOutputAudio, Seq: msg.Seq,
                                SampleRate: outRate, Data: palabra.EncodeAudioB64(audio.SamplesToBytes(samples)),
                        }
                        go func(out palabra.InOutputAudio) {
                                time.Sleep(80 * time.Millisecond)
                                b, _ := json.Marshal(out)
                                cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
                                defer cancel()
                                _ = conn.Write(cctx, websocket.MessageText, b)
                        }(out)
                case s.mt.ClientPing:
                        // WebSocket-level pongs are automatic; message pings are ignored.
                }
        }
}
