package observability

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics is the shared metric registry for the api and worker binaries.
// Labels use ONLY server-issued IDs (room, participant, session, route) —
// never personal names, transcripts or other sensitive data.
type Metrics struct {
	Registry *prometheus.Registry

	ActiveRooms          prometheus.Gauge
	ActiveParticipants   prometheus.Gauge
	UniqueListenLangs    prometheus.Gauge
	ActiveSourceTracks   prometheus.Gauge
	ActivePalabraSess    prometheus.Gauge
	TranslationRoutes    prometheus.GaugeVec // labels: room
	AudioQueueDepth      prometheus.GaugeVec // labels: room, route
	QueueDroppedFrames   prometheus.CounterVec // labels: room, route
	PalabraErrors        prometheus.CounterVec // labels: kind (rate_limit, error, closed)
	SessionInitLatency   prometheus.Histogram  // seconds to session-ready
	FirstTranslatedAudio prometheus.Histogram  // T2->T3 seconds
	EndToEndLatency      prometheus.Histogram  // T0->T6 seconds
	StageLatency         *prometheus.HistogramVec // labels: stage (T0..T6)
	WorkerRoomCount      prometheus.Gauge
}

// NewMetrics registers all collectors on a fresh registry.
func NewMetrics() *Metrics {
	reg := prometheus.NewRegistry()
	f := promauto.With(reg)
	m := &Metrics{
		Registry: reg,
		ActiveRooms: f.NewGauge(prometheus.GaugeOpts{
			Namespace: "palabra", Name: "active_rooms",
			Help: "Rooms currently handled by this process.",
		}),
		ActiveParticipants: f.NewGauge(prometheus.GaugeOpts{
			Namespace: "palabra", Name: "active_participants",
			Help: "Participants currently connected in handled rooms.",
		}),
		UniqueListenLangs: f.NewGauge(prometheus.GaugeOpts{
			Namespace: "palabra", Name: "unique_listen_languages",
			Help: "Sum of distinct listening languages across handled rooms.",
		}),
		ActiveSourceTracks: f.NewGauge(prometheus.GaugeOpts{
			Namespace: "palabra", Name: "active_source_tracks",
			Help: "Participant microphone tracks currently subscribed by this worker.",
		}),
		ActivePalabraSess: f.NewGauge(prometheus.GaugeOpts{
			Namespace: "palabra", Name: "active_palabra_sessions",
			Help: "Live Palabra S2S WebSocket sessions.",
		}),
		TranslationRoutes: f.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "palabra", Name: "translation_routes",
			Help: "Active translation routes per room.",
		}, []string{"room"}),
		AudioQueueDepth: f.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "palabra", Name: "audio_queue_depth",
			Help: "Current depth of per-route audio queues.",
		}, []string{"room", "route"}),
		QueueDroppedFrames: f.NewCounterVec(prometheus.CounterOpts{
			Namespace: "palabra", Name: "queue_dropped_frames_total",
			Help: "Audio frames dropped by the bounded-queue overflow policy.",
		}, []string{"room", "route"}),
		PalabraErrors: f.NewCounterVec(prometheus.CounterOpts{
			Namespace: "palabra", Name: "palabra_errors_total",
			Help: "Palabra session errors by kind.",
		}, []string{"kind"}),
		SessionInitLatency: f.NewHistogram(prometheus.HistogramOpts{
			Namespace: "palabra", Name: "session_init_seconds",
			Help: "Time from session start request to session-ready ack.",
			Buckets: prometheus.ExponentialBuckets(0.05, 2, 10), // 50ms .. ~25s
		}),
		FirstTranslatedAudio: f.NewHistogram(prometheus.HistogramOpts{
			Namespace: "palabra", Name: "first_translated_audio_seconds",
			Help: "T2->T3: worker sent audio to Palabra until first translated audio returned.",
			Buckets: []float64{0.05, 0.1, 0.2, 0.35, 0.5, 0.75, 1, 1.5, 2, 3, 5, 10},
		}),
		EndToEndLatency: f.NewHistogram(prometheus.HistogramOpts{
			Namespace: "palabra", Name: "end_to_end_seconds",
			Help: "T0->T6: browser capture until recipient playback starts.",
			Buckets: []float64{0.1, 0.2, 0.35, 0.5, 0.75, 1, 1.5, 2, 3, 5, 8, 12},
		}),
		StageLatency: f.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "palabra", Name: "stage_seconds",
			Help: "Per-stage latency (T0 capture, T1 worker recv, T2 send to palabra, T3 first output, T4 publish, T5 recipient recv, T6 playback).",
			Buckets: []float64{0.01, 0.025, 0.05, 0.1, 0.2, 0.35, 0.5, 1, 2, 5},
		}, []string{"stage"}),
		WorkerRoomCount: f.NewGauge(prometheus.GaugeOpts{
			Namespace: "palabra", Name: "worker_rooms",
			Help: "Rooms currently run by this media worker.",
		}),
	}
	return m
}
