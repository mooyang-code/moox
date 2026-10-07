package pipeline

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
)

// Warm-up states of a live factor set.
const (
	WarmupWarming = "warming"
	WarmupReady   = "ready"
)

// liveCatchUpBars bounds how many missed bars a warm subject may read inline;
// a subject further behind its cached window is warmed up again in the
// background instead of slowing the live period down.
const liveCatchUpBars = 5

// WarmupStatus reports how much of a live set's universe has its lookback
// window loaded. Results of warming subjects are not formal: they are reported
// as failed subjects, so strategies holding them do not evaluate the period.
type WarmupStatus struct {
	SetID            string
	State            string
	WarmSubjects     int
	ExpectedSubjects int
}

// liveWindowCache keeps the source window each live set loaded. Consecutive
// live periods share all but one bar of their lookback, so a warm subject reads
// only the bars from its target period on and takes the rest from here.
type liveWindowCache struct {
	mu   sync.Mutex
	sets map[string]*cachedSetWindow
	// wake signals RunWarmup and is nil without background warm-up, in which
	// case a subject that is not warm is read in full inside the period.
	wake chan struct{}
}

type cachedSetWindow struct {
	source   string
	subjects map[string]cachedSubjectWindow

	// warmup is the latest live plan that found subjects not warm; pending
	// subjects wait for the warm-up worker and inflight ones are being read.
	warmup   Plan
	pending  map[string]struct{}
	inflight map[string]struct{}
	warm     int
	expected int
}

// cachedSubjectWindow holds the rows of [start, end) for one subject, ordered
// by data_time.
type cachedSubjectWindow struct {
	start time.Time
	end   time.Time
	rows  [][]any
}

// subjectRead is where one subject's live read begins and the cached rows
// that precede it.
type subjectRead struct {
	start  time.Time
	prefix [][]any
}

func newLiveWindowCache(backgroundWarmup bool) *liveWindowCache {
	cache := &liveWindowCache{sets: make(map[string]*cachedSetWindow)}
	if backgroundWarmup {
		cache.wake = make(chan struct{}, 1)
	}
	return cache
}

func windowSource(plan Plan) string {
	return plan.Set.SpaceID + "|" + plan.Set.SourceDatasetID + "|" + plan.Set.Freq + "|" + strings.Join(plan.CarryColumns, ",")
}

func (c *liveWindowCache) setFor(plan Plan) *cachedSetWindow {
	source := windowSource(plan)
	set := c.sets[plan.Set.SetID]
	if set == nil || set.source != source {
		set = &cachedSetWindow{
			source: source, subjects: make(map[string]cachedSubjectWindow),
			pending: make(map[string]struct{}), inflight: make(map[string]struct{}),
		}
		c.sets[plan.Set.SetID] = set
	}
	return set
}

// plan splits a live period's subjects into reads and warming subjects. A warm
// subject re-reads from its target period, so a retried period still picks up
// a late target bar. With background warm-up a subject that is not warm is
// queued for it and left out of the period; otherwise it is read in full.
func (c *liveWindowCache) plan(plan Plan, subjects []string) (map[string]subjectRead, []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	set := c.setFor(plan)
	period := plan.TargetEnd.Sub(plan.PeriodTime)
	reads := make(map[string]subjectRead, len(subjects))
	var warming []string
	for _, subject := range subjects {
		cached, ok := set.subjects[subject]
		covered := ok && !cached.start.After(plan.TargetStart) && cached.end.After(plan.TargetStart)
		if covered && c.wake != nil && period > 0 && plan.PeriodTime.Sub(cached.end) > liveCatchUpBars*period {
			covered = false
		}
		if covered {
			start := cached.end
			if plan.PeriodTime.Before(start) {
				start = plan.PeriodTime
			}
			reads[subject] = subjectRead{start: start, prefix: rowsBetween(cached.rows, plan.TargetStart, start)}
			continue
		}
		if c.wake == nil {
			reads[subject] = subjectRead{start: plan.TargetStart}
			continue
		}
		warming = append(warming, subject)
		if _, busy := set.inflight[subject]; !busy {
			set.pending[subject] = struct{}{}
		}
	}
	set.expected, set.warm = len(subjects), len(subjects)-len(warming)
	if len(warming) > 0 {
		if set.warmup.TargetEnd.Before(plan.TargetEnd) {
			set.warmup = plan
		}
		select {
		case c.wake <- struct{}{}:
		default:
		}
	}
	return reads, warming
}

// store merges loaded windows into the cache; subjects without a frame keep
// their previous window. Periods may finish out of order, so a window only
// extends the cached one it overlaps and never replaces a newer disjoint one.
func (c *liveWindowCache) store(plan Plan, frames map[string]*storageio.Frame) {
	c.mu.Lock()
	defer c.mu.Unlock()
	set := c.setFor(plan)
	retain := 2 * plan.TargetEnd.Sub(plan.TargetStart)
	for subject, frame := range frames {
		loaded := cachedSubjectWindow{
			start: plan.TargetStart, end: plan.TargetEnd,
			rows: rowsBetween(frame.Rows, plan.TargetStart, plan.TargetEnd),
		}
		cached, ok := set.subjects[subject]
		set.subjects[subject] = mergeWindow(cached, ok, loaded, retain)
	}
}

func mergeWindow(cached cachedSubjectWindow, ok bool, loaded cachedSubjectWindow, retain time.Duration) cachedSubjectWindow {
	if !ok || loaded.start.After(cached.end) || cached.start.After(loaded.end) {
		if ok && cached.end.After(loaded.end) {
			return cached
		}
		return loaded
	}
	merged := cachedSubjectWindow{start: cached.start, end: cached.end}
	if loaded.start.Before(merged.start) {
		merged.start = loaded.start
	}
	if loaded.end.After(merged.end) {
		merged.end = loaded.end
	}
	merged.rows = append(merged.rows, rowsBetween(cached.rows, merged.start, loaded.start)...)
	merged.rows = append(merged.rows, loaded.rows...)
	merged.rows = append(merged.rows, rowsBetween(cached.rows, loaded.end, merged.end)...)
	if retain > 0 && merged.end.Sub(merged.start) > retain {
		merged.start = merged.end.Add(-retain)
		merged.rows = rowsBetween(merged.rows, merged.start, merged.end)
	}
	return merged
}

// nextWarmup hands the warm-up worker the pending subjects of one set and the
// latest window they were requested for.
func (c *liveWindowCache) nextWarmup() (Plan, []string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	setIDs := make([]string, 0, len(c.sets))
	for setID := range c.sets {
		setIDs = append(setIDs, setID)
	}
	sort.Strings(setIDs)
	for _, setID := range setIDs {
		set := c.sets[setID]
		if len(set.pending) == 0 {
			continue
		}
		subjects := make([]string, 0, len(set.pending))
		for subject := range set.pending {
			subjects = append(subjects, subject)
			set.inflight[subject] = struct{}{}
		}
		set.pending = make(map[string]struct{})
		sort.Strings(subjects)
		return set.warmup, subjects, true
	}
	return Plan{}, nil, false
}

func (c *liveWindowCache) finishWarmup(plan Plan, subjects []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if set := c.sets[plan.Set.SetID]; set != nil {
		for _, subject := range subjects {
			delete(set.inflight, subject)
		}
	}
}

func (c *liveWindowCache) statuses() []WarmupStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]WarmupStatus, 0, len(c.sets))
	for setID, set := range c.sets {
		state := WarmupReady
		if set.warm < set.expected || len(set.pending) > 0 || len(set.inflight) > 0 {
			state = WarmupWarming
		}
		out = append(out, WarmupStatus{SetID: setID, State: state, WarmSubjects: set.warm, ExpectedSubjects: set.expected})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SetID < out[j].SetID })
	return out
}

// rowsBetween returns the rows with start <= data_time < end; rows are ordered
// by data_time and their values are never mutated, so they are shared.
func rowsBetween(rows [][]any, start, end time.Time) [][]any {
	out := make([][]any, 0, len(rows))
	for _, row := range rows {
		at, ok := row[0].(time.Time)
		if !ok || at.Before(start) || !at.Before(end) {
			continue
		}
		out = append(out, row)
	}
	return out
}
