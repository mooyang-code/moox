package routeprobe

import (
	"errors"
	"math"
	"sort"
	"time"
)

var ErrNoHealthyRoute = errors.New("routeprobe: no healthy route")

type ScoreOptions struct {
	EWMAAlpha          float64
	FailurePenalty     time.Duration
	RemoteErrorPenalty time.Duration
	UnprobedPenalty    time.Duration
}

func (options ScoreOptions) normalized() ScoreOptions {
	if options.EWMAAlpha <= 0 || options.EWMAAlpha > 1 {
		options.EWMAAlpha = 0.3
	}
	if options.FailurePenalty <= 0 {
		options.FailurePenalty = 2 * time.Second
	}
	if options.RemoteErrorPenalty <= 0 {
		options.RemoteErrorPenalty = 500 * time.Millisecond
	}
	if options.UnprobedPenalty <= 0 {
		options.UnprobedPenalty = 10 * time.Second
	}
	return options
}

// RouteStats stores observations used by the selector. Latencies are kept as
// integer durations so snapshots can be serialized without a custom
// histogram dependency; p95 is recomputed from the probe samples.
type RouteStats struct {
	Attempts            int             `json:"attempts"`
	Successes           int             `json:"successes"`
	Failures            int             `json:"failures"`
	RemoteErrors        int             `json:"remote_errors"`
	ConsecutiveFailures int             `json:"consecutive_failures"`
	EWMA                time.Duration   `json:"ewma,omitempty"`
	P95                 time.Duration   `json:"p95,omitempty"`
	Latencies           []time.Duration `json:"latencies,omitempty"`
	LastObservedAt      time.Time       `json:"last_observed_at,omitempty"`
}

func (stats RouteStats) SuccessRate() float64 {
	if stats.Attempts <= 0 {
		return 0
	}
	return float64(stats.Successes) / float64(stats.Attempts)
}

func (stats *RouteStats) Observe(result ProbeResult, options ScoreOptions) {
	if stats == nil {
		return
	}
	options = options.normalized()
	stats.Attempts++
	stats.LastObservedAt = result.ObservedAt
	if result.Success {
		stats.Successes++
		stats.ConsecutiveFailures = 0
		latency := result.FirstResponseLatency
		if latency <= 0 {
			latency = result.Latency
		}
		if latency > 0 {
			stats.Latencies = append(stats.Latencies, latency)
			if stats.EWMA <= 0 {
				stats.EWMA = latency
			} else {
				stats.EWMA = time.Duration(options.EWMAAlpha*float64(latency) + (1-options.EWMAAlpha)*float64(stats.EWMA))
			}
			stats.P95 = percentile95(stats.Latencies)
		}
		return
	}
	stats.Failures++
	stats.ConsecutiveFailures++
	if result.RemoteError || result.ErrorKind == ErrorRemote {
		stats.RemoteErrors++
	}
}

func (stats RouteStats) Score(options ScoreOptions) float64 {
	options = options.normalized()
	if stats.Attempts == 0 {
		return math.Inf(1)
	}
	latency := stats.EWMA
	if stats.P95 > 0 && latency > 0 {
		latency = time.Duration((float64(stats.P95) + float64(latency)) / 2)
	} else if stats.P95 > 0 {
		latency = stats.P95
	}
	if latency <= 0 {
		latency = options.FailurePenalty
	}
	return float64(latency) +
		float64(options.FailurePenalty)*float64(stats.Failures)/float64(stats.Attempts) +
		float64(options.RemoteErrorPenalty)*float64(stats.RemoteErrors)/float64(stats.Attempts)
}

func BuildRouteStats(results []ProbeResult, options ScoreOptions) map[string]RouteStats {
	stats := make(map[string]RouteStats)
	for _, result := range results {
		identity := result.Candidate.Identity()
		current := stats[identity]
		current.Observe(result, options)
		stats[identity] = current
	}
	return stats
}

func RankCandidates(candidates []Candidate, stats map[string]RouteStats, options ScoreOptions) []ScoredCandidate {
	options = options.normalized()
	normalized, err := NormalizeCandidates(candidates)
	if err != nil {
		return nil
	}
	ranked := make([]ScoredCandidate, 0, len(normalized))
	for _, candidate := range normalized {
		current, observed := stats[candidate.Identity()]
		score := current.Score(options)
		if !observed || current.Attempts == 0 {
			score = float64(options.UnprobedPenalty)
			if candidate.HintLatency > 0 {
				score += float64(candidate.HintLatency)
			}
		}
		healthy := observed && current.Successes > 0
		status := StatusUnavailable
		if healthy {
			status = StatusDegraded
			if current.SuccessRate() >= 0.8 && current.ConsecutiveFailures == 0 {
				status = StatusHealthy
			}
		}
		ranked = append(ranked, ScoredCandidate{Candidate: candidate, Stats: current, Score: score, Healthy: healthy, Status: status})
	}
	sort.SliceStable(ranked, func(left, right int) bool {
		if ranked[left].Score != ranked[right].Score {
			return ranked[left].Score < ranked[right].Score
		}
		return ranked[left].Candidate.Identity() < ranked[right].Candidate.Identity()
	})
	return ranked
}

func percentile95(values []time.Duration) time.Duration {
	if len(values) == 0 {
		return 0
	}
	ordered := append([]time.Duration(nil), values...)
	sort.Slice(ordered, func(left, right int) bool { return ordered[left] < ordered[right] })
	index := int(math.Ceil(float64(len(ordered))*0.95)) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(ordered) {
		index = len(ordered) - 1
	}
	return ordered[index]
}

type SelectionRequest struct {
	Key        RouteKey
	Candidates []ScoredCandidate
	Snapshot   *Snapshot
}

type RouteSelection struct {
	Key           RouteKey
	Routes        []Candidate
	Ranked        []ScoredCandidate
	Status        HealthStatus
	FromSnapshot  bool
	StaleSnapshot bool
}

// RouteSelector returns one primary and at most MaxFallback already-probed
// routes. An expired snapshot is never used, and unprobed candidates are never
// silently promoted to healthy routes.
type RouteSelector struct {
	MaxFallback int
	Clock       func() time.Time
}

func (selector RouteSelector) Select(request SelectionRequest) (RouteSelection, error) {
	if err := request.Key.Validate(); err != nil {
		return RouteSelection{}, err
	}
	if selector.MaxFallback < 0 {
		return RouteSelection{}, errors.New("max fallback must not be negative")
	}
	clock := selector.Clock
	if clock == nil {
		clock = time.Now
	}
	var ranked []ScoredCandidate
	fromSnapshot := false
	staleSnapshot := false
	if request.Snapshot != nil {
		if err := request.Snapshot.Validate(); err != nil {
			return RouteSelection{}, err
		}
		if request.Snapshot.Key != request.Key {
			return RouteSelection{}, errors.New("snapshot route key does not match selection key")
		}
		if request.Snapshot.FreshAt(clock()) {
			ranked = cloneScoredCandidates(request.Snapshot.Routes)
			fromSnapshot = true
		} else {
			staleSnapshot = true
		}
	}
	if !fromSnapshot {
		ranked = cloneScoredCandidates(request.Candidates)
		sort.SliceStable(ranked, func(left, right int) bool {
			if ranked[left].Score != ranked[right].Score {
				return ranked[left].Score < ranked[right].Score
			}
			return ranked[left].Candidate.Identity() < ranked[right].Candidate.Identity()
		})
	}
	selectionLimit := selector.MaxFallback + 1
	if selectionLimit < 1 || selectionLimit > len(ranked) {
		selectionLimit = len(ranked)
	}
	selected := make([]ScoredCandidate, 0, selectionLimit)
	seen := make(map[string]struct{}, len(ranked))
	for _, route := range ranked {
		if !route.Healthy || route.Stats.Attempts == 0 || route.Stats.Successes == 0 || route.Candidate.RouteKey() != request.Key {
			continue
		}
		identity := route.Candidate.Identity()
		if _, exists := seen[identity]; exists {
			continue
		}
		seen[identity] = struct{}{}
		selected = append(selected, route)
		if len(selected) >= selectionLimit {
			break
		}
	}
	if len(selected) == 0 {
		return RouteSelection{}, ErrNoHealthyRoute
	}
	routes := make([]Candidate, 0, len(selected))
	for _, route := range selected {
		routes = append(routes, cloneCandidate(route.Candidate))
	}
	status := StatusDegraded
	if selected[0].Status != StatusDegraded {
		status = StatusHealthy
	}
	return RouteSelection{Key: request.Key, Routes: routes, Ranked: cloneScoredCandidates(selected), Status: status, FromSnapshot: fromSnapshot, StaleSnapshot: staleSnapshot}, nil
}
