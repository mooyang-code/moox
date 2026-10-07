package observability

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const (
	// Bound the subjects tracked per process so a malformed or runaway View
	// cannot grow the tracker without limit.
	maxViewSubjectObservations     = 20000
	maxViewDatasetMetricLabelBytes = 256
	// ViewLaggingSubjectsReported caps the named lagging subjects per View;
	// the count metric still covers all of them.
	ViewLaggingSubjectsReported = 20
	// viewSubjectLagTolerance is how far past one bar a subject may trail its
	// View's latest bar before it counts as lagging. It matches Monitor's
	// K-line stale_after, so a lagging subject is one Monitor would call stale
	// while the View itself is fresh.
	viewSubjectLagTolerance = 5 * time.Minute
)

// ViewDatasetObservation identifies the data watermark observed after a View
// successfully applies rows. Storage records this generic contract; Monitor
// decides whether a given View represents a K-line business stream.
type ViewDatasetObservation struct {
	SpaceID   string
	ViewID    string
	DatasetID string
	SubjectID string
	Frequency string
	SeriesTag string
	DataTime  time.Time
}

type viewSubjectScope struct {
	spaceID, viewID, datasetID, freq string
}

type viewSubjectState struct {
	bar time.Duration
	// trackedSince is the View's first observed bar in this process. Until
	// the View moves a bar past it, a subject not seen yet may simply not have
	// written since the restart.
	trackedSince int64
	latest       int64
	subjects     map[string]int64
}

// viewSubjectTracker keeps each View subject's latest output bar in memory and
// reports a per-View summary when metrics are gathered: the View's latest bar,
// how many subjects it tracks, how many trail that bar, and the most lagging
// ones by name. Updating it on the write path is a map write, not a metric
// series.
type viewSubjectTracker struct {
	mu       sync.Mutex
	scopes   map[viewSubjectScope]*viewSubjectState
	observed int

	latestDesc         *prometheus.Desc
	trackedSinceDesc   *prometheus.Desc
	subjectsDesc       *prometheus.Desc
	laggingDesc        *prometheus.Desc
	laggingSubjectDesc *prometheus.Desc
}

func newViewSubjectTracker() *viewSubjectTracker {
	labels := []string{"space_id", "view_id", "dataset_id", "freq"}
	return &viewSubjectTracker{
		scopes: make(map[viewSubjectScope]*viewSubjectState),
		latestDesc: prometheus.NewDesc("moox_storage_view_output_latest_data_time_seconds",
			"Latest output bar committed to an active View by any subject.", labels, nil),
		trackedSinceDesc: prometheus.NewDesc("moox_storage_view_output_tracked_since_data_time_seconds",
			"The View's latest output bar when this process started tracking its subjects.", labels, nil),
		subjectsDesc: prometheus.NewDesc("moox_storage_view_output_subjects",
			"Subjects with committed output in an active View since the process started.", labels, nil),
		laggingDesc: prometheus.NewDesc("moox_storage_view_output_lagging_subjects",
			"Subjects whose latest output trails the View's latest bar by more than one bar plus the lag tolerance.", labels, nil),
		laggingSubjectDesc: prometheus.NewDesc("moox_storage_view_output_lagging_subject_data_time_seconds",
			"Latest output bar of the most lagging subjects of a View.", append(labels, "subject_id"), nil),
	}
}

func (t *viewSubjectTracker) observe(observation ViewDatasetObservation) error {
	scope, subjectID, bar, err := canonicalViewSubject(observation)
	if err != nil {
		return err
	}
	dataTime := observation.DataTime.UTC().Unix()
	t.mu.Lock()
	defer t.mu.Unlock()
	state := t.scopes[scope]
	if state == nil {
		state = &viewSubjectState{bar: bar, trackedSince: dataTime, subjects: make(map[string]int64)}
		t.scopes[scope] = state
	}
	previous, known := state.subjects[subjectID]
	if !known {
		if t.observed >= maxViewSubjectObservations {
			return fmt.Errorf("storage view subject observation limit exceeded: %d", maxViewSubjectObservations)
		}
		t.observed++
	}
	if !known || dataTime > previous {
		state.subjects[subjectID] = dataTime
	}
	if dataTime > state.latest {
		state.latest = dataTime
	}
	return nil
}

func (t *viewSubjectTracker) Describe(ch chan<- *prometheus.Desc) {
	ch <- t.latestDesc
	ch <- t.trackedSinceDesc
	ch <- t.subjectsDesc
	ch <- t.laggingDesc
	ch <- t.laggingSubjectDesc
}

func (t *viewSubjectTracker) Collect(ch chan<- prometheus.Metric) {
	type lagging struct {
		subjectID string
		dataTime  int64
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for scope, state := range t.scopes {
		labels := []string{scope.spaceID, scope.viewID, scope.datasetID, scope.freq}
		cutoff := state.latest - int64((state.bar+viewSubjectLagTolerance)/time.Second)
		var behind []lagging
		for subjectID, dataTime := range state.subjects {
			if dataTime < cutoff {
				behind = append(behind, lagging{subjectID: subjectID, dataTime: dataTime})
			}
		}
		ch <- prometheus.MustNewConstMetric(t.latestDesc, prometheus.GaugeValue, float64(state.latest), labels...)
		ch <- prometheus.MustNewConstMetric(t.trackedSinceDesc, prometheus.GaugeValue, float64(state.trackedSince), labels...)
		ch <- prometheus.MustNewConstMetric(t.subjectsDesc, prometheus.GaugeValue, float64(len(state.subjects)), labels...)
		ch <- prometheus.MustNewConstMetric(t.laggingDesc, prometheus.GaugeValue, float64(len(behind)), labels...)
		sort.Slice(behind, func(i, j int) bool {
			if behind[i].dataTime != behind[j].dataTime {
				return behind[i].dataTime < behind[j].dataTime
			}
			return behind[i].subjectID < behind[j].subjectID
		})
		if len(behind) > ViewLaggingSubjectsReported {
			behind = behind[:ViewLaggingSubjectsReported]
		}
		for _, item := range behind {
			ch <- prometheus.MustNewConstMetric(t.laggingSubjectDesc, prometheus.GaugeValue, float64(item.dataTime), append(labels, item.subjectID)...)
		}
	}
}

func canonicalViewSubject(observation ViewDatasetObservation) (viewSubjectScope, string, time.Duration, error) {
	scope := viewSubjectScope{
		spaceID: strings.TrimSpace(observation.SpaceID), viewID: strings.TrimSpace(observation.ViewID),
		datasetID: strings.TrimSpace(observation.DatasetID), freq: strings.TrimSpace(observation.Frequency),
	}
	subjectID := strings.TrimSpace(observation.SubjectID)
	for _, value := range []string{scope.spaceID, scope.viewID, scope.datasetID, scope.freq, subjectID} {
		if value == "" {
			return viewSubjectScope{}, "", 0, fmt.Errorf("storage view dataset observation requires space_id, view_id, dataset_id, subject_id, and freq")
		}
		if len(value) > maxViewDatasetMetricLabelBytes {
			return viewSubjectScope{}, "", 0, fmt.Errorf("storage view dataset observation label exceeds %d bytes", maxViewDatasetMetricLabelBytes)
		}
	}
	bar, err := parseDatasetFrequency(scope.freq)
	if err != nil {
		return viewSubjectScope{}, "", 0, fmt.Errorf("storage view dataset observation freq %q is invalid", scope.freq)
	}
	if observation.DataTime.IsZero() {
		return viewSubjectScope{}, "", 0, fmt.Errorf("storage view dataset observation data_time is required")
	}
	return scope, subjectID, bar, nil
}
