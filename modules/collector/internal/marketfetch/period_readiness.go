package marketfetch

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/mooyang-code/moox/packages/storagepb"
)

// PeriodReadinessService owns the small control-plane projection that turns
// successful Storage row events into one completion decision per period.
type PeriodReadinessService struct {
	instances *store.TaskInstanceRepository
	periods   *store.PeriodReadinessRepository
	grace     time.Duration
}

func NewPeriodReadinessService(instances *store.TaskInstanceRepository, periods *store.PeriodReadinessRepository, grace time.Duration) *PeriodReadinessService {
	if grace <= 0 {
		grace = 2 * time.Minute
	}
	return &PeriodReadinessService{instances: instances, periods: periods, grace: grace}
}

// EnsureCurrentAndNext prebuilds both closed-period candidates. The second
// candidate is what lets the deadline loop report a completely missed timer.
func (s *PeriodReadinessService) EnsureCurrentAndNext(ctx context.Context, spaceID string, now time.Time) error {
	if s == nil || s.instances == nil || s.periods == nil {
		return fmt.Errorf("period readiness service is not initialized")
	}
	tasks, err := listAllTaskInstances(ctx, s.instances, strings.TrimSpace(spaceID))
	if err != nil {
		return err
	}
	type groupKey struct{ dataset, frequency string }
	groups := make(map[groupKey][]domain.TaskInstance)
	for _, task := range tasks {
		if task.IsDeleted || strings.TrimSpace(task.DatasetID) == "" || strings.TrimSpace(task.Frequency) == "" {
			continue
		}
		frequency, normalizeErr := report.NormalizeDatasetFrequency(task.Frequency)
		if normalizeErr != nil {
			return fmt.Errorf("normalize task frequency %q: %w", task.Frequency, normalizeErr)
		}
		task.Frequency = frequency
		groups[groupKey{dataset: task.DatasetID, frequency: frequency}] = append(groups[groupKey{dataset: task.DatasetID, frequency: frequency}], task)
	}
	keys := make([]groupKey, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].dataset != keys[j].dataset {
			return keys[i].dataset < keys[j].dataset
		}
		return keys[i].frequency < keys[j].frequency
	})
	for _, key := range keys {
		windows, windowErr := periodWindows(now, key.frequency)
		if windowErr != nil {
			return windowErr
		}
		seedTasks := make([]domain.PeriodTaskSeed, 0, len(groups[key]))
		seedBySubject := make(map[string]int, len(groups[key]))
		for _, task := range groups[key] {
			seed := domain.PeriodTaskSeed{
				TaskID: task.TaskID, SubjectID: task.SubjectID,
				FunctionName:   task.FunctionName,
				WriteSource:    writeSourceForFunctionName(task.FunctionName),
				RequiredFields: requiredFieldsJSON(task),
			}
			if existing, ok := seedBySubject[task.SubjectID]; ok {
				// Two enabled rules may intentionally cover the same subject. A
				// readiness item is subject-level, so accept either writer instead
				// of rejecting the whole dataset group or waiting for one chosen rule.
				seedTasks[existing].FunctionName = ""
				seedTasks[existing].WriteSource = ""
				continue
			}
			seedBySubject[task.SubjectID] = len(seedTasks)
			seedTasks = append(seedTasks, seed)
		}
		sort.Slice(seedTasks, func(i, j int) bool { return seedTasks[i].SubjectID < seedTasks[j].SubjectID })
		for _, window := range windows {
			grace := readinessGrace(key.frequency, s.grace)
			if _, ensureErr := s.periods.EnsurePeriod(ctx, domain.PeriodSeed{
				PeriodKey:  domain.PeriodKey{SpaceID: spaceID, DatasetID: key.dataset, Frequency: key.frequency, PeriodTime: window.PeriodTime},
				DeadlineAt: window.CloseAt.Add(grace), Tasks: seedTasks,
			}); ensureErr != nil {
				return ensureErr
			}
		}
	}
	return nil
}

// readinessGrace keeps the default personal deployment forgiving for slower
// frequencies without making test/injected short grace periods surprising.
// The production default is 2m; for it, use min(2*frequency, 10m).
func readinessGrace(frequency string, configured time.Duration) time.Duration {
	if configured <= 0 {
		configured = 2 * time.Minute
	}
	if configured < 2*time.Minute {
		return configured
	}
	canonical, err := report.NormalizeDatasetFrequency(frequency)
	if err != nil {
		return configured
	}
	count, err := strconv.Atoi(canonical[:len(canonical)-1])
	if err != nil || count <= 0 {
		return configured
	}
	var unit time.Duration
	switch canonical[len(canonical)-1] {
	case 'm':
		unit = time.Minute
	case 'H':
		unit = time.Hour
	case 'D':
		unit = 24 * time.Hour
	case 'W':
		unit = 7 * 24 * time.Hour
	case 'M':
		unit = 30 * 24 * time.Hour
	case 'Y':
		unit = 365 * 24 * time.Hour
	default:
		return configured
	}
	interval := time.Duration(count) * unit
	grace := 2 * interval
	if grace > 10*time.Minute {
		grace = 10 * time.Minute
	}
	if grace > configured {
		return grace
	}
	return configured
}

// ApplyRows updates only the period represented by each row's data_time. The
// envelope publication time is intentionally ignored.
func (s *PeriodReadinessService) ApplyRows(ctx context.Context, payload *storagepb.DatasetRowsUpserted) error {
	if s == nil || s.periods == nil || payload == nil {
		return fmt.Errorf("period readiness payload/service is nil")
	}
	functionName := functionNameFromWriteSource(payload.GetWriteSource())
	if functionName == "" {
		return nil
	}
	writeSource := payload.GetWriteSource()
	for _, row := range payload.GetRows() {
		if row == nil || row.GetKey() == nil || row.GetKey().GetTimeSeries() == nil {
			continue
		}
		key := row.GetKey().GetTimeSeries()
		frequency, err := report.NormalizeDatasetFrequency(key.GetFreq())
		if err != nil {
			return fmt.Errorf("normalize storage frequency %q: %w", key.GetFreq(), err)
		}
		dataTime, err := time.Parse(time.RFC3339Nano, key.GetDataTime())
		if err != nil {
			return fmt.Errorf("parse row data_time %q: %w", key.GetDataTime(), err)
		}
		fieldIDs := make([]string, 0, len(row.GetFields()))
		for _, field := range row.GetFields() {
			if field != nil {
				fieldIDs = append(fieldIDs, field.GetFieldId())
			}
		}
		if err := s.periods.MarkSubjectSuccessWithFields(ctx, domain.PeriodKey{
			SpaceID: payload.GetSpaceId(), DatasetID: payload.GetDatasetId(), Frequency: frequency, PeriodTime: dataTime.UTC(),
		}, key.GetSubjectId(), functionName, writeSource, fieldIDs, dataTime.UTC()); err != nil {
			return err
		}
	}
	return nil
}

func requiredFieldsJSON(task domain.TaskInstance) string {
	if strings.EqualFold(strings.TrimSpace(task.DataType), "kline") {
		return `["open","high","low","close","volume","quote_volume","trade_num"]`
	}
	// Other providers may define their own field contract later. Keep the
	// immutable snapshot explicit rather than guessing from opaque parameters.
	return "[]"
}

func listAllTaskInstances(ctx context.Context, repo *store.TaskInstanceRepository, spaceID string) ([]domain.TaskInstance, error) {
	const pageSize = 1000
	var result []domain.TaskInstance
	var afterID int
	for {
		rows, err := repo.ListAfterID(ctx, spaceID, afterID, pageSize)
		if err != nil {
			return nil, err
		}
		result = append(result, rows...)
		if len(rows) < pageSize {
			return result, nil
		}
		afterID = rows[len(rows)-1].ID
	}
}
