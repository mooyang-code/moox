package marketfetch

import (
	"context"
	"encoding/json"
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
	groups := make(map[groupKey][]domain.PeriodTaskSeed)
	for _, task := range tasks {
		if task.IsDeleted || task.LastExecStatus == domain.InstanceStatusFailed || strings.TrimSpace(task.Frequency) == "" {
			continue
		}
		frequency, normalizeErr := report.NormalizeDatasetFrequency(task.Frequency)
		if normalizeErr != nil {
			return fmt.Errorf("normalize task frequency %q: %w", task.Frequency, normalizeErr)
		}
		targets, targetErr := s.instances.ListWriteTargets(ctx, strings.TrimSpace(spaceID), task.InstanceID)
		if targetErr != nil {
			return fmt.Errorf("list write targets for readiness instance=%s: %w", task.InstanceID, targetErr)
		}
		for _, target := range targets {
			datasetID := strings.TrimSpace(target.DatasetID)
			if datasetID == "" {
				continue
			}
			groups[groupKey{dataset: datasetID, frequency: frequency}] = append(groups[groupKey{dataset: datasetID, frequency: frequency}], domain.PeriodTaskSeed{
				InstanceID:     task.InstanceID,
				WriteTargetID:  target.ID,
				SubjectID:      canonicalPeriodSubjectID(spaceID, task.SubjectID),
				SeriesTag:      readinessSeriesTag(task),
				FunctionName:   task.FunctionName,
				WriteSource:    writeSourceForFunctionName(task.FunctionName),
				RequiredFields: requiredFieldsForWriteTarget(task, target),
			})
		}
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
		seedTasks := append([]domain.PeriodTaskSeed(nil), groups[key]...)
		sort.Slice(seedTasks, func(i, j int) bool {
			if seedTasks[i].SubjectID != seedTasks[j].SubjectID {
				return seedTasks[i].SubjectID < seedTasks[j].SubjectID
			}
			if seedTasks[i].SeriesTag != seedTasks[j].SeriesTag {
				return seedTasks[i].SeriesTag < seedTasks[j].SeriesTag
			}
			return seedTasks[i].WriteTargetID < seedTasks[j].WriteTargetID
		})
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

// realtimeMinuteGrace is how long a 1m period waits after close for Storage
// echo. Timer collectors do not publish completion, so the old 2m floor left
// View period-ready two extra minutes behind Primary even when most rows were
// already committed.
const realtimeMinuteGrace = 20 * time.Second

// readinessGrace keeps the default personal deployment forgiving for slower
// frequencies without making test/injected short grace periods surprising.
// The production default is 2m; for it, use min(2*frequency, 10m), except 1m
// which stays tight so View can advance shortly after the bar closes.
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
	if interval == time.Minute {
		return realtimeMinuteGrace
	}
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
	if functionNameFromWriteSource(payload.GetWriteSource()) == "" {
		return nil
	}
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
		periodKey := domain.PeriodKey{
			SpaceID: payload.GetSpaceId(), DatasetID: payload.GetDatasetId(), Frequency: frequency, PeriodTime: dataTime.UTC(), SeriesTag: strings.TrimSpace(key.GetSeriesTag()),
		}
		subjectID := canonicalPeriodSubjectID(payload.GetSpaceId(), key.GetSubjectId())
		// Period snapshots freeze the assigned Timer. A later reassignment can
		// make the Storage write arrive from a different SCF; the row itself is
		// still the evidence that this subject is ready.
		if err := s.periods.MarkSubjectSuccessWithFields(ctx, periodKey, subjectID, "", "", fieldIDs, dataTime.UTC()); err != nil {
			return err
		}
		if err := s.periods.NoteWritePosition(ctx, periodKey, payload.GetSourceNodeId(), payload.GetSourceStoreId(), payload.GetSourceSequence()); err != nil {
			return err
		}
	}
	return nil
}

func readinessSeriesTag(task domain.TaskInstance) string {
	if tag := strings.TrimSpace(task.SeriesTag); tag != "" {
		return tag
	}
	dataType := strings.ToLower(strings.TrimSpace(task.DataType))
	if dataType == "kline_resample" {
		if params, err := domain.ParseCollectParams(task.TaskParams, "", "", task.DataType); err == nil {
			return strings.TrimSpace(params.SourceSeriesTag)
		}
		return ""
	}
	if dataType != "kline" {
		return ""
	}
	if strings.EqualFold(strings.TrimSpace(task.SpaceID), StockCNSpaceID) {
		return "default"
	}
	if strings.TrimSpace(task.Provider) == "" && strings.TrimSpace(task.SourceID) == "" {
		return ""
	}
	return defaultMarketSeriesTag(task.Provider, task.SourceID, task.MarketType)
}

func canonicalPeriodSubjectID(spaceID, subjectID string) string {
	if strings.EqualFold(strings.TrimSpace(spaceID), "crypto") {
		return strings.ToUpper(strings.TrimSpace(subjectID))
	}
	return strings.TrimSpace(subjectID)
}

func requiredFieldsForWriteTarget(task domain.TaskInstance, target domain.WriteTarget) string {
	if strings.EqualFold(strings.TrimSpace(task.DataType), "kline") {
		var fields []string
		if raw := strings.TrimSpace(target.OutputFields); raw != "" && raw != "[]" && json.Unmarshal([]byte(raw), &fields) == nil && len(fields) > 0 {
			encoded, err := json.Marshal(fields)
			if err == nil {
				return string(encoded)
			}
		}
	}
	return requiredFieldsJSON(task)
}

func requiredFieldsJSON(task domain.TaskInstance) string {
	if strings.EqualFold(strings.TrimSpace(task.DataType), "kline") {
		params, err := domain.ParseCollectParams(task.TaskParams, "", "", task.DataType)
		if err == nil && len(params.OutputFields) > 0 {
			encoded, marshalErr := json.Marshal(params.OutputFields)
			if marshalErr == nil {
				return string(encoded)
			}
		}
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
		rows, err := repo.ListReadinessInstances(ctx, spaceID, afterID, pageSize)
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
