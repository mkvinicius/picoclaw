// Package metrics provides lightweight, zero-dependency observability for
// PicoClaw. It collects counters, gauges, and latency histograms using atomic
// operations and exposes them in two formats:
//
//   - Prometheus text format  → GET /metrics
//   - JSON summary            → GET /api/metrics/summary
//
// Design goals:
//   - No external dependencies (pure stdlib + sync/atomic)
//   - < 1 µs overhead per observation
//   - Safe for concurrent use from many goroutines
//   - Suitable for devices with as little as 10 MB of RAM
package metrics

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Counter
// ─────────────────────────────────────────────────────────────────────────────

// Counter is a monotonically increasing integer metric.
type Counter struct{ v atomic.Int64 }

func (c *Counter) Inc()              { c.v.Add(1) }
func (c *Counter) Add(n int64)       { c.v.Add(n) }
func (c *Counter) Value() int64      { return c.v.Load() }

// ─────────────────────────────────────────────────────────────────────────────
// Gauge
// ─────────────────────────────────────────────────────────────────────────────

// Gauge is a value that can go up and down.
type Gauge struct{ v atomic.Int64 }

func (g *Gauge) Inc()           { g.v.Add(1) }
func (g *Gauge) Dec()           { g.v.Add(-1) }
func (g *Gauge) Set(n int64)    { g.v.Store(n) }
func (g *Gauge) Value() int64   { return g.v.Load() }

// ─────────────────────────────────────────────────────────────────────────────
// Histogram — fixed buckets, latency-oriented (milliseconds)
// ─────────────────────────────────────────────────────────────────────────────

// histBuckets defines upper bounds in milliseconds.
// Covers 1ms → 30s, matching typical LLM and tool latencies.
var histBuckets = []float64{1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000, 30000}

// Histogram tracks count + sum + per-bucket counts for a duration metric.
type Histogram struct {
	mu      sync.Mutex
	count   int64
	sum     float64             // milliseconds
	buckets [len(histBuckets)]int64 // cumulative counts per bucket
}

// Observe records one duration measurement.
func (h *Histogram) Observe(d time.Duration) {
	ms := float64(d.Nanoseconds()) / 1e6
	h.mu.Lock()
	h.count++
	h.sum += ms
	for i, bound := range histBuckets {
		if ms <= bound {
			h.buckets[i]++
		}
	}
	h.mu.Unlock()
}

// Snapshot returns a point-in-time view of the histogram.
type HistogramSnapshot struct {
	Count   int64              `json:"count"`
	SumMs   float64            `json:"sum_ms"`
	MeanMs  float64            `json:"mean_ms"`
	P50Ms   float64            `json:"p50_ms"`
	P95Ms   float64            `json:"p95_ms"`
	P99Ms   float64            `json:"p99_ms"`
	Buckets map[string]int64   `json:"buckets"`
}

func (h *Histogram) Snapshot() HistogramSnapshot {
	h.mu.Lock()
	count := h.count
	sum := h.sum
	buckets := h.buckets
	h.mu.Unlock()

	snap := HistogramSnapshot{
		Count:   count,
		SumMs:   sum,
		Buckets: make(map[string]int64, len(histBuckets)),
	}
	if count > 0 {
		snap.MeanMs = sum / float64(count)
		snap.P50Ms = percentileFromBuckets(buckets[:], count, 0.50)
		snap.P95Ms = percentileFromBuckets(buckets[:], count, 0.95)
		snap.P99Ms = percentileFromBuckets(buckets[:], count, 0.99)
	}
	for i, bound := range histBuckets {
		snap.Buckets[fmt.Sprintf("le_%gms", bound)] = buckets[i]
	}
	return snap
}

// percentileFromBuckets approximates a percentile from cumulative bucket counts.
func percentileFromBuckets(buckets []int64, total int64, pct float64) float64 {
	target := int64(math.Ceil(float64(total) * pct))
	for i, b := range histBuckets {
		if buckets[i] >= target {
			return b
		}
	}
	return histBuckets[len(histBuckets)-1]
}

// ─────────────────────────────────────────────────────────────────────────────
// PerLabel counter map — e.g. tool calls by tool name
// ─────────────────────────────────────────────────────────────────────────────

// LabelCounter tracks a Counter per string label (e.g., tool name).
type LabelCounter struct {
	mu sync.RWMutex
	m  map[string]*Counter
}

func (lc *LabelCounter) Inc(label string) {
	lc.mu.RLock()
	c, ok := lc.m[label]
	lc.mu.RUnlock()
	if !ok {
		lc.mu.Lock()
		if c, ok = lc.m[label]; !ok {
			if lc.m == nil {
				lc.m = make(map[string]*Counter)
			}
			c = &Counter{}
			lc.m[label] = c
		}
		lc.mu.Unlock()
	}
	c.Inc()
}

func (lc *LabelCounter) Snapshot() map[string]int64 {
	lc.mu.RLock()
	defer lc.mu.RUnlock()
	out := make(map[string]int64, len(lc.m))
	for k, v := range lc.m {
		out[k] = v.Value()
	}
	return out
}

// ─────────────────────────────────────────────────────────────────────────────
// Collector — the single global metrics object
// ─────────────────────────────────────────────────────────────────────────────

// Collector holds all metrics for a PicoClaw instance.
type Collector struct {
	// Message pipeline
	MessagesIn    Counter
	MessagesOut   Counter
	MessageErrors Counter

	// LLM
	LLMCalls    Counter
	LLMErrors   Counter
	TokensIn    Counter
	TokensOut   Counter
	LLMLatency  Histogram

	// Tools
	ToolCalls  LabelCounter
	ToolErrors LabelCounter

	// Security
	SecurityBlocked Counter

	// Active sessions (gauge)
	ActiveSessions Gauge

	// Message processing latency (end-to-end)
	MessageLatency Histogram

	startTime time.Time
}

// New creates and returns a new Collector.
func New() *Collector {
	return &Collector{startTime: time.Now()}
}

// UptimeSeconds returns how long the collector has been running.
func (c *Collector) UptimeSeconds() float64 {
	return time.Since(c.startTime).Seconds()
}

// ─────────────────────────────────────────────────────────────────────────────
// Snapshot & export
// ─────────────────────────────────────────────────────────────────────────────

// Summary is a JSON-serialisable point-in-time snapshot.
type Summary struct {
	UptimeSeconds  float64                `json:"uptime_seconds"`
	MessagesIn     int64                  `json:"messages_in"`
	MessagesOut    int64                  `json:"messages_out"`
	MessageErrors  int64                  `json:"message_errors"`
	LLMCalls       int64                  `json:"llm_calls"`
	LLMErrors      int64                  `json:"llm_errors"`
	TokensIn       int64                  `json:"tokens_in"`
	TokensOut      int64                  `json:"tokens_out"`
	SecurityBlocked int64                 `json:"security_blocked"`
	ActiveSessions int64                  `json:"active_sessions"`
	ToolCalls      map[string]int64       `json:"tool_calls"`
	ToolErrors     map[string]int64       `json:"tool_errors"`
	MessageLatency HistogramSnapshot      `json:"message_latency"`
	LLMLatency     HistogramSnapshot      `json:"llm_latency"`
}

// Snapshot returns a point-in-time Summary.
func (c *Collector) Snapshot() Summary {
	return Summary{
		UptimeSeconds:   c.UptimeSeconds(),
		MessagesIn:      c.MessagesIn.Value(),
		MessagesOut:     c.MessagesOut.Value(),
		MessageErrors:   c.MessageErrors.Value(),
		LLMCalls:        c.LLMCalls.Value(),
		LLMErrors:       c.LLMErrors.Value(),
		TokensIn:        c.TokensIn.Value(),
		TokensOut:       c.TokensOut.Value(),
		SecurityBlocked: c.SecurityBlocked.Value(),
		ActiveSessions:  c.ActiveSessions.Value(),
		ToolCalls:       c.ToolCalls.Snapshot(),
		ToolErrors:      c.ToolErrors.Snapshot(),
		MessageLatency:  c.MessageLatency.Snapshot(),
		LLMLatency:      c.LLMLatency.Snapshot(),
	}
}

// PrometheusText renders the metrics in Prometheus exposition format.
func (c *Collector) PrometheusText() string {
	var sb strings.Builder
	uptime := c.UptimeSeconds()

	writeCounter := func(name, help string, val int64) {
		fmt.Fprintf(&sb, "# HELP %s %s\n# TYPE %s counter\n%s %d\n", name, help, name, name, val)
	}
	writeGauge := func(name, help string, val int64) {
		fmt.Fprintf(&sb, "# HELP %s %s\n# TYPE %s gauge\n%s %d\n", name, help, name, name, val)
	}
	writeGaugeFloat := func(name, help string, val float64) {
		fmt.Fprintf(&sb, "# HELP %s %s\n# TYPE %s gauge\n%s %g\n", name, help, name, name, val)
	}

	writeGaugeFloat("picoclaw_uptime_seconds", "Seconds since the agent started.", uptime)
	writeCounter("picoclaw_messages_in_total", "Total inbound messages processed.", c.MessagesIn.Value())
	writeCounter("picoclaw_messages_out_total", "Total outbound messages sent.", c.MessagesOut.Value())
	writeCounter("picoclaw_message_errors_total", "Total message processing errors.", c.MessageErrors.Value())
	writeCounter("picoclaw_llm_calls_total", "Total LLM API calls.", c.LLMCalls.Value())
	writeCounter("picoclaw_llm_errors_total", "Total LLM API errors.", c.LLMErrors.Value())
	writeCounter("picoclaw_tokens_in_total", "Total input tokens sent to LLM.", c.TokensIn.Value())
	writeCounter("picoclaw_tokens_out_total", "Total output tokens received from LLM.", c.TokensOut.Value())
	writeCounter("picoclaw_security_blocked_total", "Total messages blocked by the security stack.", c.SecurityBlocked.Value())
	writeGauge("picoclaw_active_sessions", "Currently active sessions.", c.ActiveSessions.Value())

	// Tool calls per label
	sb.WriteString("# HELP picoclaw_tool_calls_total Total calls per tool.\n")
	sb.WriteString("# TYPE picoclaw_tool_calls_total counter\n")
	toolSnap := c.ToolCalls.Snapshot()
	keys := sortedKeys(toolSnap)
	for _, k := range keys {
		fmt.Fprintf(&sb, "picoclaw_tool_calls_total{tool=%q} %d\n", k, toolSnap[k])
	}

	sb.WriteString("# HELP picoclaw_tool_errors_total Total errors per tool.\n")
	sb.WriteString("# TYPE picoclaw_tool_errors_total counter\n")
	errSnap := c.ToolErrors.Snapshot()
	for _, k := range sortedKeys(errSnap) {
		fmt.Fprintf(&sb, "picoclaw_tool_errors_total{tool=%q} %d\n", k, errSnap[k])
	}

	// Histograms
	writeHistogram(&sb, "picoclaw_message_latency_ms", "End-to-end message processing latency in milliseconds.", &c.MessageLatency)
	writeHistogram(&sb, "picoclaw_llm_latency_ms", "LLM API call latency in milliseconds.", &c.LLMLatency)

	return sb.String()
}

func writeHistogram(sb *strings.Builder, name, help string, h *Histogram) {
	snap := h.Snapshot()
	fmt.Fprintf(sb, "# HELP %s %s\n# TYPE %s histogram\n", name, help, name)
	for _, bound := range histBuckets {
		k := fmt.Sprintf("le_%gms", bound)
		fmt.Fprintf(sb, "%s_bucket{le=%q} %d\n", name, fmt.Sprintf("%gms", bound), snap.Buckets[k])
	}
	fmt.Fprintf(sb, "%s_count %d\n", name, snap.Count)
	fmt.Fprintf(sb, "%s_sum %g\n", name, snap.SumMs)
}

func sortedKeys(m map[string]int64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ─────────────────────────────────────────────────────────────────────────────
// HTTP Handlers
// ─────────────────────────────────────────────────────────────────────────────

// RegisterOnMux mounts the metrics endpoints.
//
//	GET /metrics              → Prometheus text format
//	GET /api/metrics/summary  → JSON summary
func (c *Collector) RegisterOnMux(mux *http.ServeMux) {
	mux.HandleFunc("/metrics", c.handlePrometheus)
	mux.HandleFunc("/api/metrics/summary", c.handleSummary)
}

func (c *Collector) handlePrometheus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	fmt.Fprint(w, c.PrometheusText())
}

func (c *Collector) handleSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	snap := c.Snapshot()
	fmt.Fprintf(w, `{"uptime_seconds":%g,"messages_in":%d,"messages_out":%d,"message_errors":%d,"llm_calls":%d,"llm_errors":%d,"tokens_in":%d,"tokens_out":%d,"security_blocked":%d,"active_sessions":%d}`,
		snap.UptimeSeconds, snap.MessagesIn, snap.MessagesOut, snap.MessageErrors,
		snap.LLMCalls, snap.LLMErrors, snap.TokensIn, snap.TokensOut,
		snap.SecurityBlocked, snap.ActiveSessions)
}
