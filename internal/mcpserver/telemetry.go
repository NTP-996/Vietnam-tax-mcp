package mcpserver

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"
)

// latencyWindow is how many recent latencies per tool the percentiles read.
const latencyWindow = 1024

// Stats are the in-memory counters since start: calls per tool and
// code, illustrative answers per tool, latency p50 / p95 per tool. They hold
// no argument, no result and no caller detail.
type Stats struct {
	mu           sync.Mutex
	started      time.Time
	calls        map[string]map[string]int
	illustrative map[string]int
	latencies    map[string][]float64
}

// NewStats starts the counters.
func NewStats() *Stats {
	return &Stats{
		started: time.Now().UTC(), calls: map[string]map[string]int{},
		illustrative: map[string]int{}, latencies: map[string][]float64{},
	}
}

func (st *Stats) add(tool, code string, ms float64, illustrative bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.calls[tool] == nil {
		st.calls[tool] = map[string]int{}
	}
	st.calls[tool][code]++
	if illustrative {
		st.illustrative[tool]++
	}
	l := append(st.latencies[tool], ms)
	if len(l) > latencyWindow {
		l = l[len(l)-latencyWindow:]
	}
	st.latencies[tool] = l
}

// ToolStats is one tool's line of the snapshot.
type ToolStats struct {
	Calls        map[string]int `json:"calls"`
	Illustrative int            `json:"illustrative"`
	P50Ms        float64        `json:"p50_ms"`
	P95Ms        float64        `json:"p95_ms"`
}

// Snapshot is GET /internal/stats.
type Snapshot struct {
	Since string               `json:"since"`
	Tools map[string]ToolStats `json:"tools"`
}

// Snapshot copies the counters.
func (st *Stats) Snapshot() Snapshot {
	st.mu.Lock()
	defer st.mu.Unlock()
	out := Snapshot{Since: st.started.Format(time.RFC3339), Tools: map[string]ToolStats{}}
	for tool, codes := range st.calls {
		ts := ToolStats{Calls: map[string]int{}, Illustrative: st.illustrative[tool]}
		for code, n := range codes {
			ts.Calls[code] = n
		}
		l := append([]float64(nil), st.latencies[tool]...)
		sort.Float64s(l)
		ts.P50Ms, ts.P95Ms = percentile(l, 0.50), percentile(l, 0.95)
		out.Tools[tool] = ts
	}
	return out
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(p * float64(len(sorted)-1))
	return sorted[i]
}

// record writes the one tool_call line and counts the call. It names the
// tool, the code, the latency, the verification flags and the versions —
// never an argument, a result body, the user agent or the client IP.
func (s *Service) record(requestID, tool, code string, d time.Duration, env *Envelope) {
	ms := float64(d.Microseconds()) / 1000
	verified, illustrative := false, false
	packs := []string{}
	if env != nil {
		verified, illustrative = env.Verified, env.Illustrative
		packs = env.Versions.Packs
	}
	s.stats.add(tool, code, ms, illustrative)
	s.log.LogAttrs(context.Background(), slog.LevelInfo, "tool_call",
		slog.String("request_id", requestID),
		slog.String("tool", tool),
		slog.String("code", code),
		slog.Float64("latency_ms", ms),
		slog.Bool("verified", verified),
		slog.Bool("illustrative", illustrative),
		slog.Any("packs", packs),
		slog.String("corpus_version", s.corpus.Version),
	)
}
