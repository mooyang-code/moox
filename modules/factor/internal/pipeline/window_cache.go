package pipeline

import (
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
)

// liveWindowCache keeps the source window each live set loaded for its last
// period. Consecutive live periods share all but one bar of their lookback, so
// the next period reads only the bars from its target period on and takes the
// rest from here instead of re-reading the whole lookback for every subject.
type liveWindowCache struct {
	mu   sync.Mutex
	sets map[string]*cachedSetWindow
}

type cachedSetWindow struct {
	source   string
	subjects map[string]cachedSubjectWindow
}

// cachedSubjectWindow holds the rows of [start, end) for one subject.
type cachedSubjectWindow struct {
	start time.Time
	end   time.Time
	rows  [][]any
}

func newLiveWindowCache() *liveWindowCache {
	return &liveWindowCache{sets: make(map[string]*cachedSetWindow)}
}

func windowSource(plan Plan) string {
	return plan.Set.SpaceID + "|" + plan.Set.SourceDatasetID + "|" + plan.Set.Freq + "|" + strings.Join(plan.CarryColumns, ",")
}

// readStarts returns where each subject's read begins and the cached rows that
// precede it. A subject whose cached window does not cover the plan's start is
// read in full; a covered one re-reads from the target period, so a retried
// period still picks up a late target bar.
func (c *liveWindowCache) readStarts(plan Plan, subjects []string) (map[string]time.Time, map[string][][]any) {
	starts := make(map[string]time.Time, len(subjects))
	prefixes := make(map[string][][]any, len(subjects))
	c.mu.Lock()
	defer c.mu.Unlock()
	set := c.sets[plan.Set.SetID]
	for _, subject := range subjects {
		starts[subject] = plan.TargetStart
		if set == nil || set.source != windowSource(plan) {
			continue
		}
		cached, ok := set.subjects[subject]
		if !ok || cached.start.After(plan.TargetStart) || !cached.end.After(plan.TargetStart) {
			continue
		}
		start := cached.end
		if plan.PeriodTime.Before(start) {
			start = plan.PeriodTime
		}
		starts[subject] = start
		prefixes[subject] = rowsBetween(cached.rows, plan.TargetStart, start)
	}
	return starts, prefixes
}

// store records the loaded windows. Subjects that failed keep their previous
// window, subjects that left the plan are dropped, and since periods may finish
// out of order an older period never replaces a newer window.
func (c *liveWindowCache) store(plan Plan, frames map[string]*storageio.Frame, failed map[string]struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	source := windowSource(plan)
	previous := map[string]cachedSubjectWindow{}
	if set := c.sets[plan.Set.SetID]; set != nil && set.source == source {
		previous = set.subjects
	}
	next := &cachedSetWindow{source: source, subjects: make(map[string]cachedSubjectWindow, len(frames)+len(failed))}
	for subject := range failed {
		if cached, ok := previous[subject]; ok {
			next.subjects[subject] = cached
		}
	}
	for subject, frame := range frames {
		if cached, ok := previous[subject]; ok && cached.end.After(plan.TargetEnd) {
			next.subjects[subject] = cached
			continue
		}
		next.subjects[subject] = cachedSubjectWindow{
			start: plan.TargetStart, end: plan.TargetEnd,
			rows: rowsBetween(frame.Rows, plan.TargetStart, plan.TargetEnd),
		}
	}
	c.sets[plan.Set.SetID] = next
}

// rowsBetween copies the rows with start <= data_time < end; rows are ordered
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
