package merge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type PeriodKey struct {
	DatasetID  string
	SnapshotID string
	Frequency  string
	PeriodTime time.Time
}

type WriteReceipt struct {
	CommitID string
	NodeID   string
	StoreID  string
	Sequence uint64
}

type PeriodMarker struct {
	DatasetID          string
	Frequency          string
	SnapshotID         string
	BatchID            string
	ScopeRef           string
	Status             string
	PeriodTime         time.Time
	ExpectedSubjectIDs []string
	FailedSubjects     []string
	Positions          []WriteReceipt
}

type PeriodReporter interface {
	Report(context.Context, PeriodMarker) error
}

type PeriodLedger struct {
	store    *Ledger
	reporter PeriodReporter
}

type periodRow struct {
	ID           int64     `gorm:"column:c_id;primaryKey"`
	DatasetID    string    `gorm:"column:c_dataset_id"`
	SnapshotID   string    `gorm:"column:c_snapshot_id"`
	Frequency    string    `gorm:"column:c_frequency"`
	PeriodTime   time.Time `gorm:"column:c_period_time"`
	BatchID      string    `gorm:"column:c_batch_id"`
	ExpectedJSON string    `gorm:"column:c_expected_json"`
	Deadline     time.Time `gorm:"column:c_deadline"`
	Status       string    `gorm:"column:c_status"`
	ReportState  string    `gorm:"column:c_report_state"`
	ModifiedAt   time.Time `gorm:"column:c_mtime"`
}

func (periodRow) TableName() string { return "t_merge_periods" }

type periodSubjectRow struct {
	DatasetID  string    `gorm:"column:c_dataset_id;primaryKey"`
	SnapshotID string    `gorm:"column:c_snapshot_id;primaryKey"`
	Frequency  string    `gorm:"column:c_frequency;primaryKey"`
	PeriodTime time.Time `gorm:"column:c_period_time;primaryKey"`
	SubjectID  string    `gorm:"column:c_subject_id;primaryKey"`
	State      string    `gorm:"column:c_state"`
	CommitID   string    `gorm:"column:c_commit_id"`
	NodeID     string    `gorm:"column:c_node_id"`
	StoreID    string    `gorm:"column:c_store_id"`
	Sequence   uint64    `gorm:"column:c_sequence"`
	ModifiedAt time.Time `gorm:"column:c_mtime"`
}

func (periodSubjectRow) TableName() string { return "t_merge_period_subjects" }

type sourceCompletionRow struct {
	DatasetID       string    `gorm:"column:c_dataset_id;primaryKey"`
	SourceDatasetID string    `gorm:"column:c_source_dataset_id;primaryKey"`
	PeriodTime      time.Time `gorm:"column:c_period_time;primaryKey"`
	ExpectedJSON    string    `gorm:"column:c_expected_json"`
	ModifiedAt      time.Time `gorm:"column:c_mtime"`
}

func (sourceCompletionRow) TableName() string { return "t_merge_source_completions" }

func NewPeriodLedger(store *Ledger, reporter PeriodReporter) (*PeriodLedger, error) {
	if store == nil {
		return nil, fmt.Errorf("merge ledger is required")
	}
	if reporter == nil {
		return nil, fmt.Errorf("merge period reporter is required")
	}
	return &PeriodLedger{store: store, reporter: reporter}, nil
}

func (l *PeriodLedger) Freeze(ctx context.Context, key PeriodKey, expected []string, deadline time.Time) error {
	if l == nil {
		return fmt.Errorf("merge period ledger is not initialized")
	}
	if strings.TrimSpace(key.DatasetID) == "" || strings.TrimSpace(key.SnapshotID) == "" || strings.TrimSpace(key.Frequency) == "" || key.PeriodTime.IsZero() || deadline.IsZero() {
		return fmt.Errorf("period identity and deadline are required")
	}
	universe := uniquePreserve(expected)
	raw, err := json.Marshal(universe)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	period := periodRow{
		DatasetID: key.DatasetID, SnapshotID: key.SnapshotID, Frequency: key.Frequency, PeriodTime: key.PeriodTime.UTC(),
		BatchID: stablePeriodBatchID(key), ExpectedJSON: string(raw), Deadline: deadline.UTC(),
		Status: "waiting", ReportState: "waiting", ModifiedAt: now,
	}
	err = l.store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&period)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		for _, subject := range universe {
			item := periodSubjectRow{
				DatasetID: key.DatasetID, SnapshotID: key.SnapshotID, Frequency: key.Frequency, PeriodTime: key.PeriodTime.UTC(),
				SubjectID: subject, State: "pending", ModifiedAt: now,
			}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&item).Error; err != nil {
				return err
			}
		}
		return nil
	})
	return err
}

func (l *PeriodLedger) NoteCommit(ctx context.Context, key PeriodKey, subjectID string, receipt WriteReceipt) error {
	if l == nil {
		return fmt.Errorf("merge period ledger is not initialized")
	}
	subjectID = strings.TrimSpace(subjectID)
	if subjectID == "" {
		return fmt.Errorf("subject_id is required")
	}
	period, err := l.loadPeriod(ctx, key)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil
		}
		return err
	}
	if period.Status != "waiting" {
		return nil
	}
	now := time.Now().UTC()
	return l.store.db.WithContext(ctx).Model(&periodSubjectRow{}).Where(
		"c_dataset_id = ? AND c_snapshot_id = ? AND c_frequency = ? AND c_period_time = ? AND c_subject_id = ? AND c_state = ?",
		key.DatasetID, key.SnapshotID, key.Frequency, key.PeriodTime.UTC(), subjectID, "pending",
	).Updates(map[string]any{
		"c_state": "success", "c_commit_id": receipt.CommitID, "c_node_id": receipt.NodeID,
		"c_store_id": receipt.StoreID, "c_sequence": receipt.Sequence, "c_mtime": now,
	}).Error
}

func (l *PeriodLedger) NoteCollectorCompleted(ctx context.Context, datasetID, sourceDatasetID string, periodTime time.Time, expected []string) error {
	if l == nil {
		return fmt.Errorf("merge period ledger is not initialized")
	}
	raw, err := json.Marshal(uniquePreserve(expected))
	if err != nil {
		return err
	}
	row := sourceCompletionRow{
		DatasetID: strings.TrimSpace(datasetID), SourceDatasetID: strings.TrimSpace(sourceDatasetID),
		PeriodTime: periodTime.UTC(), ExpectedJSON: string(raw), ModifiedAt: time.Now().UTC(),
	}
	if row.DatasetID == "" || row.SourceDatasetID == "" {
		return fmt.Errorf("dataset_id and source dataset_id are required")
	}
	return l.store.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "c_dataset_id"}, {Name: "c_source_dataset_id"}, {Name: "c_period_time"},
		},
		DoUpdates: clause.AssignmentColumns([]string{"c_expected_json", "c_mtime"}),
	}).Create(&row).Error
}

func (l *PeriodLedger) CollectorUniverse(ctx context.Context, datasetID string, sourceIDs []string, periodTime time.Time) ([]string, bool, error) {
	if l == nil {
		return nil, false, fmt.Errorf("merge period ledger is not initialized")
	}
	if len(sourceIDs) == 0 {
		return nil, false, nil
	}
	var rows []sourceCompletionRow
	if err := l.store.db.WithContext(ctx).Where(
		"c_dataset_id = ? AND c_period_time = ?", strings.TrimSpace(datasetID), periodTime.UTC(),
	).Find(&rows).Error; err != nil {
		return nil, false, err
	}
	bySource := make(map[string][]string, len(rows))
	for _, row := range rows {
		var expected []string
		if strings.TrimSpace(row.ExpectedJSON) != "" {
			if err := json.Unmarshal([]byte(row.ExpectedJSON), &expected); err != nil {
				return nil, false, err
			}
		}
		bySource[row.SourceDatasetID] = expected
	}
	for _, sourceID := range sourceIDs {
		if _, ok := bySource[sourceID]; !ok {
			return nil, false, nil
		}
	}
	var universe []string
	for _, sourceID := range sourceIDs {
		universe = append(universe, bySource[sourceID]...)
	}
	return uniquePreserve(universe), true, nil
}

func (l *PeriodLedger) Accepts(ctx context.Context, key PeriodKey, subjectID string) (bool, error) {
	period, err := l.loadPeriod(ctx, key)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return true, nil
		}
		return false, err
	}
	if period.Status != "waiting" {
		return false, nil
	}
	expected := decodeExpected(period.ExpectedJSON)
	if len(expected) == 0 {
		return false, nil
	}
	subjectID = strings.TrimSpace(subjectID)
	for _, id := range expected {
		if id == subjectID {
			return true, nil
		}
	}
	return false, nil
}

func (l *PeriodLedger) Finalize(ctx context.Context, key PeriodKey, now time.Time) error {
	if l == nil {
		return fmt.Errorf("merge period ledger is not initialized")
	}
	period, err := l.loadPeriod(ctx, key)
	if err != nil {
		return err
	}
	if period.ReportState == "reported" {
		return nil
	}
	subjects, err := l.loadSubjects(ctx, key)
	if err != nil {
		return err
	}
	expected := decodeExpected(period.ExpectedJSON)
	if period.Status == "waiting" {
		pendingRemain := 0
		for _, subject := range subjects {
			if subject.State == "pending" {
				pendingRemain++
			}
		}
		if pendingRemain > 0 && now.Before(period.Deadline) {
			return nil
		}
		if err := l.closePending(ctx, key, now); err != nil {
			return err
		}
		subjects, err = l.loadSubjects(ctx, key)
		if err != nil {
			return err
		}
		status := "complete"
		for _, subject := range subjects {
			if subject.State != "success" {
				status = "degraded"
				break
			}
		}
		if err := l.store.db.WithContext(ctx).Model(&periodRow{}).Where(
			"c_dataset_id = ? AND c_snapshot_id = ? AND c_frequency = ? AND c_period_time = ?",
			key.DatasetID, key.SnapshotID, key.Frequency, key.PeriodTime.UTC(),
		).Updates(map[string]any{"c_status": status, "c_mtime": now.UTC()}).Error; err != nil {
			return err
		}
		period.Status = status
	}
	failed := make([]string, 0)
	for _, subjectID := range expected {
		state := "missing"
		for _, subject := range subjects {
			if subject.SubjectID == subjectID {
				state = subject.State
				break
			}
		}
		if state != "success" {
			failed = append(failed, subjectID)
		}
	}
	marker := PeriodMarker{
		DatasetID: period.DatasetID, Frequency: period.Frequency, SnapshotID: period.SnapshotID,
		BatchID: period.BatchID, ScopeRef: "universe:" + period.DatasetID + ":" + period.Frequency,
		Status: period.Status, PeriodTime: period.PeriodTime, ExpectedSubjectIDs: expected, FailedSubjects: failed,
		Positions: collapseReceipts(subjects),
	}
	if err := l.reporter.Report(ctx, marker); err != nil {
		return err
	}
	return l.store.db.WithContext(ctx).Model(&periodRow{}).Where(
		"c_dataset_id = ? AND c_snapshot_id = ? AND c_frequency = ? AND c_period_time = ?",
		key.DatasetID, key.SnapshotID, key.Frequency, key.PeriodTime.UTC(),
	).Updates(map[string]any{"c_report_state": "reported", "c_mtime": time.Now().UTC()}).Error
}

func (l *PeriodLedger) FinalizeDue(ctx context.Context, now time.Time) error {
	if l == nil {
		return fmt.Errorf("merge period ledger is not initialized")
	}
	var rows []periodRow
	if err := l.store.db.WithContext(ctx).Where("c_report_state = ?", "waiting").Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		if err := l.Finalize(ctx, PeriodKey{
			DatasetID: row.DatasetID, SnapshotID: row.SnapshotID, Frequency: row.Frequency, PeriodTime: row.PeriodTime,
		}, now); err != nil {
			return err
		}
	}
	return nil
}

func (l *PeriodLedger) loadPeriod(ctx context.Context, key PeriodKey) (periodRow, error) {
	var row periodRow
	err := l.store.db.WithContext(ctx).Where(
		"c_dataset_id = ? AND c_snapshot_id = ? AND c_frequency = ? AND c_period_time = ?",
		key.DatasetID, key.SnapshotID, key.Frequency, key.PeriodTime.UTC(),
	).First(&row).Error
	return row, err
}

func (l *PeriodLedger) loadSubjects(ctx context.Context, key PeriodKey) ([]periodSubjectRow, error) {
	var rows []periodSubjectRow
	err := l.store.db.WithContext(ctx).Where(
		"c_dataset_id = ? AND c_snapshot_id = ? AND c_frequency = ? AND c_period_time = ?",
		key.DatasetID, key.SnapshotID, key.Frequency, key.PeriodTime.UTC(),
	).Find(&rows).Error
	return rows, err
}

func (l *PeriodLedger) closePending(ctx context.Context, key PeriodKey, now time.Time) error {
	return l.store.db.WithContext(ctx).Model(&periodSubjectRow{}).Where(
		"c_dataset_id = ? AND c_snapshot_id = ? AND c_frequency = ? AND c_period_time = ? AND c_state = ?",
		key.DatasetID, key.SnapshotID, key.Frequency, key.PeriodTime.UTC(), "pending",
	).Updates(map[string]any{"c_state": "missing", "c_mtime": now.UTC()}).Error
}

func collapseReceipts(subjects []periodSubjectRow) []WriteReceipt {
	type posKey struct{ node, store string }
	best := map[posKey]WriteReceipt{}
	for _, subject := range subjects {
		if subject.State != "success" || subject.Sequence == 0 || subject.NodeID == "" || subject.StoreID == "" {
			continue
		}
		key := posKey{node: subject.NodeID, store: subject.StoreID}
		if current, ok := best[key]; !ok || subject.Sequence > current.Sequence {
			best[key] = WriteReceipt{CommitID: subject.CommitID, NodeID: subject.NodeID, StoreID: subject.StoreID, Sequence: subject.Sequence}
		}
	}
	if len(best) == 0 {
		return nil
	}
	out := make([]WriteReceipt, 0, len(best))
	for _, receipt := range best {
		out = append(out, receipt)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].NodeID != out[j].NodeID {
			return out[i].NodeID < out[j].NodeID
		}
		return out[i].StoreID < out[j].StoreID
	})
	return out
}

func uniquePreserve(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func decodeExpected(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil
	}
	return uniquePreserve(values)
}

func stablePeriodBatchID(key PeriodKey) string {
	payload := strings.Join([]string{
		key.DatasetID, key.SnapshotID, key.Frequency, strconv.FormatInt(key.PeriodTime.UTC().UnixNano(), 10),
	}, "\x00")
	sum := sha256.Sum256([]byte(payload))
	return "merge-" + hex.EncodeToString(sum[:16])
}
