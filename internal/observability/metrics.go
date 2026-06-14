package observability

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// Metrics holds every Prometheus instrument Noema exposes. Instruments are
// created once here so that names are visible in one place and no package
// registers globals.
type Metrics struct {
	Registry *prometheus.Registry

	HTTPRequests  *prometheus.CounterVec
	HTTPDuration  *prometheus.HistogramVec
	AuthFailures  prometheus.Counter
	Events        *prometheus.CounterVec
	Candidates    prometheus.Counter
	Admissions    prometheus.Counter
	Retrievals    prometheus.Counter
	Beliefs       prometheus.Gauge
	Predictions   prometheus.Counter
	PredictionErr prometheus.Counter
	LLMRequests   *prometheus.CounterVec
	LLMTokens     *prometheus.CounterVec
	LLMFailures   *prometheus.CounterVec
	ToolRequests  *prometheus.CounterVec
	ToolDenied    *prometheus.CounterVec
	QueueDepth    *prometheus.GaugeVec
	Workers       *prometheus.GaugeVec
}

// NewMetrics creates and registers all instruments on a fresh registry.
func NewMetrics() *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{
		Registry:      reg,
		HTTPRequests:  prometheus.NewCounterVec(prometheus.CounterOpts{Name: "noema_http_requests_total", Help: "HTTP requests by route and status."}, []string{"route", "status"}),
		HTTPDuration:  prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "noema_http_request_duration_seconds", Help: "HTTP request latency.", Buckets: prometheus.DefBuckets}, []string{"route"}),
		AuthFailures:  prometheus.NewCounter(prometheus.CounterOpts{Name: "noema_auth_failures_total", Help: "Failed authentication attempts."}),
		Events:        prometheus.NewCounterVec(prometheus.CounterOpts{Name: "noema_events_total", Help: "Cognitive events appended by type."}, []string{"type"}),
		Candidates:    prometheus.NewCounter(prometheus.CounterOpts{Name: "noema_workspace_candidates_total", Help: "Thought candidates scored."}),
		Admissions:    prometheus.NewCounter(prometheus.CounterOpts{Name: "noema_workspace_admissions_total", Help: "Thoughts admitted to the workspace."}),
		Retrievals:    prometheus.NewCounter(prometheus.CounterOpts{Name: "noema_memory_retrievals_total", Help: "Memory retrievals."}),
		Beliefs:       prometheus.NewGauge(prometheus.GaugeOpts{Name: "noema_beliefs_total", Help: "Beliefs currently held across minds."}),
		Predictions:   prometheus.NewCounter(prometheus.CounterOpts{Name: "noema_predictions_total", Help: "Predictions created."}),
		PredictionErr: prometheus.NewCounter(prometheus.CounterOpts{Name: "noema_prediction_errors_total", Help: "Prediction error events."}),
		LLMRequests:   prometheus.NewCounterVec(prometheus.CounterOpts{Name: "noema_llm_requests_total", Help: "LLM requests by provider and role."}, []string{"provider", "role"}),
		LLMTokens:     prometheus.NewCounterVec(prometheus.CounterOpts{Name: "noema_llm_tokens_total", Help: "LLM tokens by provider and direction."}, []string{"provider", "direction"}),
		LLMFailures:   prometheus.NewCounterVec(prometheus.CounterOpts{Name: "noema_llm_failures_total", Help: "LLM failures by provider."}, []string{"provider"}),
		ToolRequests:  prometheus.NewCounterVec(prometheus.CounterOpts{Name: "noema_tool_requests_total", Help: "Capability invocations by capability."}, []string{"capability"}),
		ToolDenied:    prometheus.NewCounterVec(prometheus.CounterOpts{Name: "noema_tool_denied_total", Help: "Denied capability invocations."}, []string{"capability"}),
		QueueDepth:    prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "noema_cognitive_queue_depth", Help: "Pending work per queue."}, []string{"queue"}),
		Workers:       prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "noema_worker_count", Help: "Busy workers per pool."}, []string{"pool"}),
	}
	reg.MustRegister(
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.HTTPRequests, m.HTTPDuration, m.AuthFailures, m.Events, m.Candidates, m.Admissions, m.Retrievals,
		m.Beliefs, m.Predictions, m.PredictionErr, m.LLMRequests, m.LLMTokens, m.LLMFailures, m.ToolRequests,
		m.ToolDenied, m.QueueDepth, m.Workers,
	)
	return m
}
