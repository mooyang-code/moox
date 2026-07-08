package trigger

import (
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/registry"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

// Task is a debounced event-derived scheduler request.
type Task struct {
	SpaceID       string
	SourceDataset string
	TargetDataset string
	SubjectID     string
	Freq          string
	BarTime       time.Time
	FactorIDs     []string
}

// Debouncer merges Storage row-change events into per-symbol task requests.
type Debouncer struct {
	mu       sync.Mutex
	window   time.Duration
	bindings []domain.FactorBinding
	buckets  map[bucketKey]*bucket
}

type bucketKey struct {
	spaceID       string
	sourceDataset string
	targetDataset string
	subjectID     string
	freq          string
}

type bucket struct {
	task     Task
	deadline time.Time
	factors  map[string]struct{}
}

// NewDebouncer creates a debouncer with an initial binding snapshot.
func NewDebouncer(window time.Duration, bindings []domain.FactorBinding) *Debouncer {
	return &Debouncer{window: window, bindings: append([]domain.FactorBinding(nil), bindings...), buckets: map[bucketKey]*bucket{}}
}

// SetBindings replaces the enabled binding snapshot.
func (d *Debouncer) SetBindings(bindings []domain.FactorBinding) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.bindings = append([]domain.FactorBinding(nil), bindings...)
}

// Ingest adds one Storage rows_changed event into debounce buckets.
func (d *Debouncer) Ingest(event *storagepb.TimeSeriesRowsChangedEvent, now time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, key := range event.GetKeys() {
		dataTime, err := time.Parse(time.RFC3339Nano, key.GetDataTime())
		if err != nil {
			continue
		}
		matches := d.matchBindings(key)
		if len(matches) == 0 {
			continue
		}
		for _, binding := range matches {
			targetDataset := binding.TargetDataset
			if targetDataset == "" {
				targetDataset = registry.ResultDataset(key.GetDatasetId())
			}
			bkey := bucketKey{
				spaceID:       key.GetSpaceId(),
				sourceDataset: key.GetDatasetId(),
				targetDataset: targetDataset,
				subjectID:     key.GetSubjectId(),
				freq:          key.GetFreq(),
			}
			b := d.buckets[bkey]
			if b == nil {
				b = &bucket{
					task: Task{
						SpaceID:       key.GetSpaceId(),
						SourceDataset: key.GetDatasetId(),
						TargetDataset: targetDataset,
						SubjectID:     key.GetSubjectId(),
						Freq:          key.GetFreq(),
						BarTime:       dataTime.UTC(),
					},
					deadline: now.Add(d.window),
					factors:  map[string]struct{}{},
				}
				d.buckets[bkey] = b
			}
			if dataTime.After(b.task.BarTime) {
				b.task.BarTime = dataTime.UTC()
			}
			b.factors[binding.FactorID] = struct{}{}
		}
	}
}

// Flush returns all buckets whose debounce deadline has passed.
func (d *Debouncer) Flush(now time.Time) []Task {
	d.mu.Lock()
	defer d.mu.Unlock()
	keys := make([]bucketKey, 0, len(d.buckets))
	for key, b := range d.buckets {
		if !now.Before(b.deadline) {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		return strings.Join([]string{a.spaceID, a.sourceDataset, a.targetDataset, a.subjectID, a.freq}, "\x00") <
			strings.Join([]string{b.spaceID, b.sourceDataset, b.targetDataset, b.subjectID, b.freq}, "\x00")
	})
	tasks := make([]Task, 0, len(keys))
	for _, key := range keys {
		b := d.buckets[key]
		delete(d.buckets, key)
		b.task.FactorIDs = orderedFactorIDs(b.factors, d.bindings)
		tasks = append(tasks, b.task)
	}
	return tasks
}

func (d *Debouncer) matchBindings(key *storagepb.TimeSeriesKey) []domain.FactorBinding {
	out := []domain.FactorBinding{}
	for _, binding := range d.bindings {
		if binding.Status != domain.BindingStatusEnabled {
			continue
		}
		if binding.SpaceID != key.GetSpaceId() || binding.SourceDataset != key.GetDatasetId() || binding.Freq != key.GetFreq() {
			continue
		}
		if !subjectAllowed(binding, key.GetSubjectId()) {
			continue
		}
		out = append(out, binding)
	}
	return out
}

func subjectAllowed(binding domain.FactorBinding, subjectID string) bool {
	if binding.SubjectMode == "" || binding.SubjectMode == domain.SubjectModeAll {
		return true
	}
	if binding.SubjectMode != domain.SubjectModeInclude {
		return false
	}
	var subjects []string
	if err := json.Unmarshal([]byte(binding.SubjectsJSON), &subjects); err != nil {
		return false
	}
	for _, subject := range subjects {
		if subject == subjectID {
			return true
		}
	}
	return false
}

func orderedFactorIDs(set map[string]struct{}, bindings []domain.FactorBinding) []string {
	out := []string{}
	seen := map[string]struct{}{}
	for _, binding := range bindings {
		if _, ok := set[binding.FactorID]; !ok {
			continue
		}
		if _, ok := seen[binding.FactorID]; ok {
			continue
		}
		out = append(out, binding.FactorID)
		seen[binding.FactorID] = struct{}{}
	}
	return out
}
