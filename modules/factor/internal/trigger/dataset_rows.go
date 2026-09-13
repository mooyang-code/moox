package trigger

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/mooyang-code/moox/modules/factor/internal/taskrunner"
	"github.com/mooyang-code/moox/packages/events"
	publicstoragepb "github.com/mooyang-code/moox/packages/storagepb"
	"github.com/nats-io/nats.go"
)

const (
	writeKindInputCommit    = "input_commit"
	writeKindFactorPatch    = "factor_patch"
	attrInputReady          = "moox.input_ready"
	attrBindingVersion      = "moox.binding_version"
	datasetRowsTriggerType  = "dataset_rows"
	DatasetRowsConsumerName = "factor_dataset_rows_v1"
)

// DatasetRowsRunner turns complete mdataset input commits into timeseries tasks.
type DatasetRowsRunner struct {
	bindings   PeriodBindingSource
	factors    PeriodFactorSource
	taskRunner CombinationTaskRunner
	db         *store.Store
	factorsDir string
	barrier    *PeriodBarrier
}

func NewDatasetRowsRunner(bindings PeriodBindingSource, factors PeriodFactorSource, tasks CombinationTaskRunner, db *store.Store, factorsDir string) *DatasetRowsRunner {
	return &DatasetRowsRunner{bindings: bindings, factors: factors, taskRunner: tasks, db: db, factorsDir: factorsDir}
}

func (r *DatasetRowsRunner) WithPeriodBarrier(barrier *PeriodBarrier) *DatasetRowsRunner {
	if r != nil {
		r.barrier = barrier
	}
	return r
}

func DatasetRowsDeliverPolicy(existing *nats.ConsumerInfo) nats.DeliverPolicy {
	if existing != nil {
		return existing.Config.DeliverPolicy
	}
	return nats.DeliverNewPolicy
}

func DatasetRowsConsumerConfig(existing *nats.ConsumerInfo, fetchMaxWait time.Duration, filters []string) events.ConsumerConfig {
	if fetchMaxWait <= 0 {
		fetchMaxWait = time.Second
	}
	cfg := events.ConsumerConfig{
		Name: DatasetRowsConsumerName, AckWait: time.Minute, MaxDeliver: -1, MaxAckPending: 16,
		FetchMaxWait: fetchMaxWait, DeliverPolicy: DatasetRowsDeliverPolicy(existing),
		DeliverDecodeErrors: true,
	}
	if len(filters) > 0 {
		cfg.Stream = events.DatasetRowsUpserted.Stream()
		cfg.FilterSubjects = append([]string(nil), filters...)
		return cfg
	}
	cfg.Event = events.DatasetRowsUpserted
	return cfg
}

func (r *DatasetRowsRunner) HandleDatasetRows(ctx context.Context, eventID string, payload *publicstoragepb.DatasetRowsUpserted) error {
	if r == nil || r.bindings == nil || r.factors == nil || r.taskRunner == nil {
		return fmt.Errorf("dataset rows runner dependencies are required")
	}
	if payload == nil || strings.TrimSpace(payload.GetWriteKind()) != writeKindInputCommit {
		return nil
	}
	datasetID := strings.TrimSpace(payload.GetDatasetId())
	spaceID := strings.TrimSpace(payload.GetSpaceId())
	if datasetID == "" || spaceID == "" {
		return nil
	}
	bindings, err := r.bindings.ListExecutable(ctx)
	if err != nil {
		return fmt.Errorf("list executable factor bindings: %w", err)
	}
	selected := selectDatasetBindings(bindings, spaceID, datasetID)
	if len(selected) == 0 {
		return nil
	}
	catalogRevision := r.catalogRevision(ctx)
	configSnapshotID := r.configSnapshotID(ctx, datasetID)
	tasks := make([]taskrunner.Task, 0, len(payload.GetRows())*len(selected))
	for _, row := range payload.GetRows() {
		if !rowInputReady(row) {
			continue
		}
		ts := row.GetKey().GetTimeSeries()
		if ts == nil {
			continue
		}
		period, err := parseDatasetRowTime(ts.GetDataTime())
		if err != nil {
			return err
		}
		periodEnd, err := domain.NextPeriod(period, ts.GetFreq())
		if err != nil {
			return err
		}
		rowBindingVersion := rowAttributeString(row, attrBindingVersion)
		for _, binding := range selected {
			if binding.Freq != ts.GetFreq() {
				continue
			}
			if !domain.BindingAllowsSubject(binding, ts.GetSubjectId()) {
				continue
			}
			if rowBindingVersion != "" && rowBindingVersion != binding.BindingGeneration {
				continue
			}
			factor, loadErr := r.factors.Get(ctx, binding.FactorID)
			if loadErr != nil {
				return fmt.Errorf("load factor %s: %w", binding.FactorID, loadErr)
			}
			if factor == nil || factor.FactorType != domain.FactorTypeTimeSeries {
				continue
			}
			task, buildErr := taskrunner.BuildTask(taskrunner.TaskScope{
				CatalogRevision: catalogRevision,
				SourceNodeID:    payload.GetSourceNodeId(), SourceStoreID: payload.GetSourceStoreId(),
				SourceSequence: payload.GetSourceSequence(), SourceEventID: eventID,
				SourceSeriesTag: ts.GetSeriesTag(), FilterSourceSeriesTag: strings.TrimSpace(ts.GetSeriesTag()) != "",
				BindingID: binding.BindingID, BindingGeneration: binding.BindingGeneration,
				TriggerType: datasetRowsTriggerType, SpaceID: spaceID,
				SourceDataset: datasetID, TargetDataset: datasetID, ResultDatasetID: firstNonEmpty(binding.ResultDatasetID, datasetID),
				SubjectID: ts.GetSubjectId(), Freq: ts.GetFreq(), PeriodTime: period.Unix(),
				TriggerEventID: eventID, TriggeredAt: time.Now().UTC(), StartTime: period, EndTime: periodEnd,
				ConfigSnapshotID: configSnapshotID,
				StorageSchemaID:  r.storageSchemaID(ctx, datasetID),
			}, *factor, r.factorsDir)
			if buildErr != nil {
				return buildErr
			}
			task.TaskID = taskrunner.DeterministicTaskID(task)
			if task.CatalogRevision <= 0 {
				task.CatalogRevision = 1
			}
			admitted, admitErr := r.admit(ctx, task)
			if admitErr != nil {
				return admitErr
			}
			if !admitted {
				continue
			}
			tasks = append(tasks, task)
		}
	}
	if len(tasks) == 0 {
		return nil
	}
	results := r.taskRunner.RunAll(ctx, tasks)
	for _, result := range results {
		failure := ""
		if result.Err != nil {
			failure = result.Err.Error()
		}
		if r.db != nil {
			if completeErr := r.db.CompleteSubjectTask(ctx, result.Task.FactorTask, failure); completeErr != nil {
				return completeErr
			}
		}
		if err := r.recordPeriodOutcome(ctx, result); err != nil {
			return err
		}
	}
	return nil
}

func (r *DatasetRowsRunner) admit(ctx context.Context, task taskrunner.Task) (bool, error) {
	if r.db == nil {
		return true, nil
	}
	return r.db.AdmitSubjectTask(ctx, task.FactorTask)
}

func (r *DatasetRowsRunner) catalogRevision(ctx context.Context) int64 {
	if r.db == nil {
		return 0
	}
	snapshot, err := r.db.CatalogSnapshot(ctx)
	if err != nil || snapshot == nil {
		return 1
	}
	if snapshot.Revision <= 0 {
		return 1
	}
	return snapshot.Revision
}

func (r *DatasetRowsRunner) configSnapshotID(ctx context.Context, datasetID string) string {
	if r.db == nil || r.db.MergedDatasets() == nil {
		return ""
	}
	def, err := r.db.MergedDatasets().Get(ctx, datasetID)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(def.ConfigSnapshotID)
}

func (r *DatasetRowsRunner) storageSchemaID(ctx context.Context, datasetID string) string {
	if r.db == nil || r.db.MergedDatasets() == nil {
		return ""
	}
	def, err := r.db.MergedDatasets().Get(ctx, datasetID)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(def.StorageSchemaID)
}

func (r *DatasetRowsRunner) recordPeriodOutcome(ctx context.Context, result taskrunner.Result) error {
	if r == nil || r.barrier == nil {
		return nil
	}
	task := result.Task.FactorTask
	datasetID := firstNonEmpty(task.ResultDatasetID, task.SourceDataset, task.SourceViewID)
	snapshotID := firstNonEmpty(task.ConfigSnapshotID, r.configSnapshotID(ctx, datasetID), "-")
	status := PairComplete
	if result.Err != nil {
		status = PairFailed
	}
	outcome := PairOutcome{BindingID: task.BindingID, SubjectID: task.SubjectID, Status: status}
	if status == PairComplete {
		outcome.Receipt = WriteReceipt{
			CommitID: firstNonEmpty(task.TaskID, task.SourceEventID),
			NodeID:   firstNonEmpty(task.SourceNodeID, "factor-engine"),
			StoreID:  firstNonEmpty(task.SourceStoreID, "patch"),
			Sequence: task.SourceSequence,
		}
		if outcome.Receipt.Sequence == 0 {
			outcome.Receipt.Sequence = 1
		}
	}
	return r.barrier.Record(ctx, PeriodKey{
		SpaceID: task.SpaceID, DatasetID: datasetID, SnapshotID: snapshotID,
		Frequency: task.Freq, PeriodTime: task.PeriodTime,
	}, outcome)
}

func selectDatasetBindings(bindings []domain.FactorBinding, spaceID, datasetID string) []domain.FactorBinding {
	selected := make([]domain.FactorBinding, 0, len(bindings))
	for _, binding := range bindings {
		if binding.SpaceID != spaceID || binding.Freq == "" {
			continue
		}
		if bindingDatasetID(binding) != datasetID {
			continue
		}
		selected = append(selected, binding)
	}
	return selected
}

func bindingDatasetID(binding domain.FactorBinding) string {
	if id := strings.TrimSpace(binding.ResultDatasetID); id != "" {
		return id
	}
	if id := strings.TrimSpace(binding.SourceDataset); id != "" {
		return id
	}
	return strings.TrimSpace(binding.SourceViewID)
}

func rowInputReady(row *publicstoragepb.RowUpsert) bool {
	if row == nil || row.GetKey() == nil {
		return false
	}
	attr, ok := row.GetAttributes()[attrInputReady]
	return ok && attr != nil && attr.GetBoolValue()
}

func rowAttributeString(row *publicstoragepb.RowUpsert, key string) string {
	if row == nil {
		return ""
	}
	attr, ok := row.GetAttributes()[key]
	if !ok || attr == nil {
		return ""
	}
	return strings.TrimSpace(attr.GetStringValue())
}

func parseDatasetRowTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, fmt.Errorf("row data_time is required")
	}
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed.UTC(), nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("row data_time %q is invalid", value)
	}
	return parsed.UTC(), nil
}
