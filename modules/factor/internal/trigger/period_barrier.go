package trigger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/report"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	PairPending      = "pending"
	PairComplete     = "complete"
	PairFailed       = "failed"
	PairSkipped      = "skipped"
	PairMissingInput = "missing_input"
)

type PeriodKey struct {
	SpaceID    string
	DatasetID  string
	SnapshotID string
	Frequency  string
	PeriodTime int64
}

type FrozenBinding struct {
	BindingID  string
	FactorID   string
	FactorType string
	SourceHash string
	Subjects   []string
}

type WriteReceipt struct {
	CommitID string
	NodeID   string
	StoreID  string
	Sequence uint64
}

type PairOutcome struct {
	BindingID string
	SubjectID string
	Status    string
	Receipt   WriteReceipt
}

type FreezeSpec struct {
	Key              PeriodKey
	ExpectedSubjects []string
	FailedSubjects   []string
	Bindings         []FrozenBinding
	BatchID          string
	ScopeRef         string
}

type FactorPeriodReporter interface {
	ReportFactorPeriodComputed(context.Context, string, *storagepb.FactorPeriodComputedMarker) error
}

type PeriodBarrier struct {
	db       *store.Store
	reporter FactorPeriodReporter
}

type periodBarrierRow struct {
	SpaceID      string `gorm:"column:c_space_id;primaryKey"`
	DatasetID    string `gorm:"column:c_dataset_id;primaryKey"`
	SnapshotID   string `gorm:"column:c_snapshot_id;primaryKey"`
	Frequency    string `gorm:"column:c_frequency;primaryKey"`
	PeriodTime   int64  `gorm:"column:c_period_time;primaryKey"`
	BatchID      string `gorm:"column:c_batch_id"`
	ScopeRef     string `gorm:"column:c_scope_ref"`
	Frozen       int    `gorm:"column:c_frozen"`
	Status       string `gorm:"column:c_status"`
	ReportState  string `gorm:"column:c_report_state"`
	BindingsJSON string `gorm:"column:c_bindings_json"`
	ExpectedJSON string `gorm:"column:c_expected_json"`
	FailedJSON   string `gorm:"column:c_failed_json"`
}

func (periodBarrierRow) TableName() string { return "t_factor_period_barriers" }

type periodPairRow struct {
	SpaceID          string `gorm:"column:c_space_id;primaryKey"`
	DatasetID        string `gorm:"column:c_dataset_id;primaryKey"`
	SnapshotID       string `gorm:"column:c_snapshot_id;primaryKey"`
	Frequency        string `gorm:"column:c_frequency;primaryKey"`
	PeriodTime       int64  `gorm:"column:c_period_time;primaryKey"`
	BindingID        string `gorm:"column:c_binding_id;primaryKey"`
	SubjectID        string `gorm:"column:c_subject_id;primaryKey"`
	State            string `gorm:"column:c_state"`
	ReceiptConfirmed int    `gorm:"column:c_receipt_confirmed"`
	CommitID         string `gorm:"column:c_commit_id"`
	NodeID           string `gorm:"column:c_node_id"`
	StoreID          string `gorm:"column:c_store_id"`
	Sequence         uint64 `gorm:"column:c_sequence"`
}

func (periodPairRow) TableName() string { return "t_factor_period_pairs" }

type periodGCRow struct {
	ID              int64 `gorm:"column:c_id;primaryKey"`
	CompletedBefore int64 `gorm:"column:c_completed_before"`
}

func (periodGCRow) TableName() string { return "t_factor_period_gc" }

func NewPeriodBarrier(db *store.Store, reporter FactorPeriodReporter) (*PeriodBarrier, error) {
	if db == nil {
		return nil, fmt.Errorf("factor period barrier store is required")
	}
	if reporter == nil {
		return nil, fmt.Errorf("factor period reporter is required")
	}
	return &PeriodBarrier{db: db, reporter: reporter}, nil
}

func (b *PeriodBarrier) Freeze(ctx context.Context, spec FreezeSpec) error {
	if b == nil {
		return fmt.Errorf("factor period barrier is not initialized")
	}
	if err := spec.Key.validate(); err != nil {
		return err
	}
	expected := uniqueSorted(spec.ExpectedSubjects)
	failed := uniqueSorted(spec.FailedSubjects)
	bindings := normalizeFrozenBindings(spec.Bindings, expected)
	expectedJSON, err := json.Marshal(expected)
	if err != nil {
		return err
	}
	failedJSON, err := json.Marshal(failed)
	if err != nil {
		return err
	}
	bindingsJSON, err := json.Marshal(bindings)
	if err != nil {
		return err
	}
	err = b.db.WithTx(ctx, func(tx *gorm.DB) error {
		cutoff, err := loadPeriodGC(tx)
		if err != nil {
			return err
		}
		if spec.Key.PeriodTime <= cutoff {
			return nil
		}
		var existing periodBarrierRow
		lookup := tx.Where(periodBarrierWhere(spec.Key)).Take(&existing).Error
		if lookup != nil && !errors.Is(lookup, gorm.ErrRecordNotFound) {
			return lookup
		}
		if lookup == nil && existing.Frozen == 1 {
			return nil
		}
		row := periodBarrierRow{
			SpaceID: spec.Key.SpaceID, DatasetID: spec.Key.DatasetID, SnapshotID: spec.Key.SnapshotID,
			Frequency: spec.Key.Frequency, PeriodTime: spec.Key.PeriodTime,
			BatchID: firstNonEmpty(spec.BatchID, existing.BatchID), ScopeRef: firstNonEmpty(spec.ScopeRef, existing.ScopeRef),
			Frozen: 1, Status: "open", ReportState: "waiting",
			BindingsJSON: string(bindingsJSON), ExpectedJSON: string(expectedJSON), FailedJSON: string(failedJSON),
		}
		if err := tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&row).Error; err != nil {
			return err
		}
		failedSet := make(map[string]struct{}, len(failed))
		for _, subject := range failed {
			failedSet[subject] = struct{}{}
		}
		var existingPairs []periodPairRow
		if err := tx.Where(periodBarrierWhere(spec.Key)).Find(&existingPairs).Error; err != nil {
			return err
		}
		keep := make(map[string]struct{}, len(existingPairs))
		for _, current := range existingPairs {
			if current.State != PairPending {
				keep[current.BindingID+"\x00"+current.SubjectID] = struct{}{}
			}
		}
		pairs := make([]periodPairRow, 0, len(bindings)*8)
		for _, binding := range bindings {
			for _, subject := range binding.Subjects {
				if _, ok := keep[binding.BindingID+"\x00"+subject]; ok {
					continue
				}
				pair := periodPairRow{
					SpaceID: spec.Key.SpaceID, DatasetID: spec.Key.DatasetID, SnapshotID: spec.Key.SnapshotID,
					Frequency: spec.Key.Frequency, PeriodTime: spec.Key.PeriodTime,
					BindingID: binding.BindingID, SubjectID: subject, State: PairPending,
				}
				if _, ok := failedSet[subject]; ok {
					pair.State = PairMissingInput
				}
				pairs = append(pairs, pair)
			}
		}
		if len(pairs) == 0 {
			return nil
		}
		return tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "c_space_id"}, {Name: "c_dataset_id"}, {Name: "c_snapshot_id"},
				{Name: "c_frequency"}, {Name: "c_period_time"}, {Name: "c_binding_id"}, {Name: "c_subject_id"},
			},
			DoUpdates: clause.AssignmentColumns([]string{"c_state"}),
		}).CreateInBatches(pairs, 200).Error
	})
	if err != nil {
		return err
	}
	return b.CloseIfReady(ctx, spec.Key)
}

func (b *PeriodBarrier) Record(ctx context.Context, key PeriodKey, outcome PairOutcome) error {
	if b == nil {
		return fmt.Errorf("factor period barrier is not initialized")
	}
	if err := key.validate(); err != nil {
		return err
	}
	outcome.BindingID = strings.TrimSpace(outcome.BindingID)
	outcome.SubjectID = strings.TrimSpace(outcome.SubjectID)
	if outcome.BindingID == "" || outcome.SubjectID == "" {
		return fmt.Errorf("period pair identity is required")
	}
	if !validPairState(outcome.Status) {
		return fmt.Errorf("period pair status %q is invalid", outcome.Status)
	}
	err := b.db.WithTx(ctx, func(tx *gorm.DB) error {
		cutoff, err := loadPeriodGC(tx)
		if err != nil {
			return err
		}
		if key.PeriodTime <= cutoff {
			return nil
		}
		allowed, frozen, err := frozenAllowsPair(tx, key, outcome.BindingID, outcome.SubjectID)
		if err != nil {
			return err
		}
		if frozen && !allowed {
			return nil
		}
		row := periodPairRow{
			SpaceID: key.SpaceID, DatasetID: key.DatasetID, SnapshotID: key.SnapshotID,
			Frequency: key.Frequency, PeriodTime: key.PeriodTime,
			BindingID: outcome.BindingID, SubjectID: outcome.SubjectID, State: outcome.Status,
		}
		applyReceipt(&row, outcome.Receipt)
		updates := map[string]any{"c_state": outcome.Status}
		if row.ReceiptConfirmed == 1 {
			updates["c_receipt_confirmed"] = 1
			updates["c_commit_id"] = row.CommitID
			updates["c_node_id"] = row.NodeID
			updates["c_store_id"] = row.StoreID
			updates["c_sequence"] = row.Sequence
		}
		return tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "c_space_id"}, {Name: "c_dataset_id"}, {Name: "c_snapshot_id"},
				{Name: "c_frequency"}, {Name: "c_period_time"}, {Name: "c_binding_id"}, {Name: "c_subject_id"},
			},
			DoUpdates: clause.Assignments(updates),
		}).Create(&row).Error
	})
	if err != nil {
		return err
	}
	return b.CloseIfReady(ctx, key)
}

func (b *PeriodBarrier) ConfirmReceipt(ctx context.Context, key PeriodKey, bindingID, subjectID string, receipt WriteReceipt) error {
	if b == nil {
		return fmt.Errorf("factor period barrier is not initialized")
	}
	if err := key.validate(); err != nil {
		return err
	}
	bindingID = strings.TrimSpace(bindingID)
	subjectID = strings.TrimSpace(subjectID)
	if bindingID == "" || subjectID == "" {
		return fmt.Errorf("period pair identity is required")
	}
	err := b.db.WithTx(ctx, func(tx *gorm.DB) error {
		cutoff, err := loadPeriodGC(tx)
		if err != nil {
			return err
		}
		if key.PeriodTime <= cutoff {
			return nil
		}
		result := tx.Model(&periodPairRow{}).Where(periodPairWhere(key, bindingID, subjectID)).Updates(map[string]any{
			"c_receipt_confirmed": 1,
			"c_commit_id":         strings.TrimSpace(receipt.CommitID),
			"c_node_id":           strings.TrimSpace(receipt.NodeID),
			"c_store_id":          strings.TrimSpace(receipt.StoreID),
			"c_sequence":          receipt.Sequence,
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("period pair receipt has no matching task")
		}
		return nil
	})
	if err != nil {
		return err
	}
	return b.CloseIfReady(ctx, key)
}

func (b *PeriodBarrier) hasUnreportedFrozen(ctx context.Context, key PeriodKey) (bool, error) {
	if b == nil {
		return false, fmt.Errorf("factor period barrier is not initialized")
	}
	if err := key.validate(); err != nil {
		return false, err
	}
	interval, err := report.ParseDatasetFrequency(key.Frequency)
	if err != nil || interval <= 0 {
		interval = time.Minute
	}
	cutoff := key.PeriodTime - int64(interval/time.Second)
	var n int64
	err = b.db.WithTx(ctx, func(tx *gorm.DB) error {
		return tx.Model(&periodBarrierRow{}).Where(
			"c_space_id = ? AND c_dataset_id = ? AND c_snapshot_id = ? AND c_frequency = ? AND c_frozen = ? AND c_report_state = ? AND c_period_time <> ? AND c_period_time >= ?",
			key.SpaceID, key.DatasetID, key.SnapshotID, key.Frequency, 1, "waiting", key.PeriodTime, cutoff,
		).Count(&n).Error
	})
	return n > 0, err
}

func (b *PeriodBarrier) hasLaterPeriod(ctx context.Context, key PeriodKey) (bool, error) {
	if b == nil {
		return false, fmt.Errorf("factor period barrier is not initialized")
	}
	if err := key.validate(); err != nil {
		return false, err
	}
	var n int64
	err := b.db.WithTx(ctx, func(tx *gorm.DB) error {
		return tx.Model(&periodBarrierRow{}).Where(
			"c_space_id = ? AND c_dataset_id = ? AND c_snapshot_id = ? AND c_frequency = ? AND c_period_time > ?",
			key.SpaceID, key.DatasetID, key.SnapshotID, key.Frequency, key.PeriodTime,
		).Count(&n).Error
	})
	return n > 0, err
}

func (b *PeriodBarrier) CloseIfReady(ctx context.Context, key PeriodKey) error {
	if b == nil {
		return fmt.Errorf("factor period barrier is not initialized")
	}
	if err := key.validate(); err != nil {
		return err
	}
	var marker *storagepb.FactorPeriodComputedMarker
	err := b.db.WithTx(ctx, func(tx *gorm.DB) error {
		cutoff, err := loadPeriodGC(tx)
		if err != nil {
			return err
		}
		if key.PeriodTime <= cutoff {
			return nil
		}
		var row periodBarrierRow
		if err := tx.Where(periodBarrierWhere(key)).Take(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if row.Frozen != 1 || row.ReportState == "reported" {
			return nil
		}
		var pairs []periodPairRow
		if err := tx.Where(periodBarrierWhere(key)).Order("c_binding_id, c_subject_id").Find(&pairs).Error; err != nil {
			return err
		}
		var bindings []FrozenBinding
		if err := json.Unmarshal([]byte(row.BindingsJSON), &bindings); err != nil {
			return err
		}
		if !periodPairsTerminal(bindings, pairs) {
			return nil
		}
		built, err := buildFactorPeriodMarker(key, row, bindings, pairs)
		if err != nil {
			return err
		}
		if err := tx.Model(&periodBarrierRow{}).Where(periodBarrierWhere(key)).Updates(map[string]any{
			"c_status": built.GetStatus(), "c_report_state": "reported",
		}).Error; err != nil {
			return err
		}
		marker = built
		return nil
	})
	if err != nil || marker == nil {
		return err
	}
	if reportErr := b.reporter.ReportFactorPeriodComputed(ctx, key.SpaceID, marker); reportErr != nil {
		_ = b.db.WithTx(ctx, func(tx *gorm.DB) error {
			return tx.Model(&periodBarrierRow{}).Where(periodBarrierWhere(key)).Update("c_report_state", "waiting").Error
		})
		return reportErr
	}
	return nil
}

func (b *PeriodBarrier) abandonEmptyBindingLedgers(ctx context.Context) error {
	return b.db.WithTx(ctx, func(tx *gorm.DB) error {
		return tx.Model(&periodBarrierRow{}).
			Where("c_frozen = ? AND c_report_state = ? AND (c_bindings_json = ? OR TRIM(c_bindings_json) = ?)", 1, "waiting", "[]", "").
			Update("c_report_state", "reported").Error
	})
}

func (b *PeriodBarrier) CloseWaiting(ctx context.Context) error {
	if b == nil {
		return fmt.Errorf("factor period barrier is not initialized")
	}
	if err := b.abandonEmptyBindingLedgers(ctx); err != nil {
		return err
	}
	if err := b.ExpirePending(ctx, time.Now().UTC()); err != nil {
		return err
	}
	var keys []PeriodKey
	err := b.db.WithTx(ctx, func(tx *gorm.DB) error {
		var rows []periodBarrierRow
		if err := tx.Where("c_frozen = ? AND c_report_state = ?", 1, "waiting").Order("c_period_time").Find(&rows).Error; err != nil {
			return err
		}
		keys = make([]PeriodKey, 0, len(rows))
		for _, row := range rows {
			if strings.TrimSpace(row.BindingsJSON) == "" || row.BindingsJSON == "[]" {
				if err := tx.Model(&periodBarrierRow{}).Where(periodBarrierWhere(PeriodKey{
					SpaceID: row.SpaceID, DatasetID: row.DatasetID, SnapshotID: row.SnapshotID,
					Frequency: row.Frequency, PeriodTime: row.PeriodTime,
				})).Update("c_report_state", "reported").Error; err != nil {
					return err
				}
				continue
			}
			keys = append(keys, PeriodKey{
				SpaceID: row.SpaceID, DatasetID: row.DatasetID, SnapshotID: row.SnapshotID,
				Frequency: row.Frequency, PeriodTime: row.PeriodTime,
			})
		}
		return nil
	})
	if err != nil {
		return err
	}
	var first error
	for _, key := range keys {
		if err := b.CloseIfReady(ctx, key); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (b *PeriodBarrier) ExpirePending(ctx context.Context, now time.Time) error {
	if b == nil {
		return fmt.Errorf("factor period barrier is not initialized")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	var keys []PeriodKey
	err := b.db.WithTx(ctx, func(tx *gorm.DB) error {
		var rows []periodBarrierRow
		if err := tx.Where("c_frozen = ? AND c_report_state = ?", 1, "waiting").Order("c_period_time").Find(&rows).Error; err != nil {
			return err
		}
		cutoff := now.Unix()
		for _, row := range rows {
			key := PeriodKey{
				SpaceID: row.SpaceID, DatasetID: row.DatasetID, SnapshotID: row.SnapshotID,
				Frequency: row.Frequency, PeriodTime: row.PeriodTime,
			}
			if cutoff <= periodPendingDeadline(key) {
				continue
			}
			if err := tx.Model(&periodPairRow{}).Where(periodBarrierWhere(key)).Where("c_state = ?", PairPending).
				Update("c_state", PairMissingInput).Error; err != nil {
				return err
			}
			keys = append(keys, key)
		}
		return nil
	})
	if err != nil {
		return err
	}
	var first error
	for _, key := range keys {
		if err := b.CloseIfReady(ctx, key); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func periodPendingDeadline(key PeriodKey) int64 {
	interval, err := report.ParseDatasetFrequency(key.Frequency)
	if err != nil || interval <= 0 {
		interval = time.Minute
	}
	// Strategy ValidUntil is two bars after bar end (period+3m on 1m). Report
	// FactorPeriodComputed at period+2m so the result can still be committed.
	return key.PeriodTime + int64((2 * interval) / time.Second)
}

func periodCatchupFreezeDeadline(key PeriodKey) int64 {
	interval, err := report.ParseDatasetFrequency(key.Frequency)
	if err != nil || interval <= 0 {
		interval = time.Minute
	}
	return key.PeriodTime + int64((interval + 14*time.Minute) / time.Second)
}

func (b *PeriodBarrier) PruneClosedBefore(ctx context.Context, cutoff int64) error {
	if b == nil {
		return fmt.Errorf("factor period barrier is not initialized")
	}
	if cutoff <= 0 {
		return fmt.Errorf("positive factor period cutoff is required")
	}
	return b.db.WithTx(ctx, func(tx *gorm.DB) error {
		var latest int64
		if err := tx.Model(&periodBarrierRow{}).Where("c_report_state = ?", "reported").Select("COALESCE(MAX(c_period_time), 0)").Scan(&latest).Error; err != nil {
			return err
		}
		if latest > 0 && cutoff >= latest {
			return fmt.Errorf("replay window still covers the latest reported factor period")
		}
		var outstanding int64
		if err := tx.Model(&periodBarrierRow{}).Where("c_period_time <= ? AND c_report_state <> ?", cutoff, "reported").Count(&outstanding).Error; err != nil {
			return err
		}
		if outstanding != 0 {
			return fmt.Errorf("factor period barrier contains unfinished tasks")
		}
		if err := tx.Where("c_period_time <= ?", cutoff).Delete(&periodPairRow{}).Error; err != nil {
			return err
		}
		if err := tx.Where("c_period_time <= ?", cutoff).Delete(&periodBarrierRow{}).Error; err != nil {
			return err
		}
		return tx.Exec("UPDATE t_factor_period_gc SET c_completed_before = MAX(c_completed_before, ?) WHERE c_id = 1", cutoff).Error
	})
}

func AcceptsStrategyViewReady(completionKind string, factorBacked bool) bool {
	if !factorBacked {
		return true
	}
	return completionKind == events.FactorPeriodComputed.Name()
}

func (k PeriodKey) validate() error {
	if strings.TrimSpace(k.SpaceID) == "" || strings.TrimSpace(k.DatasetID) == "" || strings.TrimSpace(k.SnapshotID) == "" || strings.TrimSpace(k.Frequency) == "" || k.PeriodTime <= 0 {
		return fmt.Errorf("factor period identity is incomplete")
	}
	return nil
}

func periodBarrierWhere(key PeriodKey) map[string]any {
	return map[string]any{
		"c_space_id": key.SpaceID, "c_dataset_id": key.DatasetID, "c_snapshot_id": key.SnapshotID,
		"c_frequency": key.Frequency, "c_period_time": key.PeriodTime,
	}
}

func periodPairWhere(key PeriodKey, bindingID, subjectID string) map[string]any {
	where := periodBarrierWhere(key)
	where["c_binding_id"] = bindingID
	where["c_subject_id"] = subjectID
	return where
}

func loadPeriodGC(tx *gorm.DB) (int64, error) {
	var row periodGCRow
	if err := tx.Where("c_id = 1").Take(&row).Error; err != nil {
		return 0, err
	}
	return row.CompletedBefore, nil
}

func frozenAllowsPair(tx *gorm.DB, key PeriodKey, bindingID, subjectID string) (allowed bool, frozen bool, err error) {
	var row periodBarrierRow
	lookup := tx.Where(periodBarrierWhere(key)).Take(&row).Error
	if errors.Is(lookup, gorm.ErrRecordNotFound) {
		return true, false, nil
	}
	if lookup != nil {
		return false, false, lookup
	}
	if row.Frozen != 1 {
		return true, false, nil
	}
	var bindings []FrozenBinding
	if err := json.Unmarshal([]byte(row.BindingsJSON), &bindings); err != nil {
		return false, true, err
	}
	for _, binding := range bindings {
		if binding.BindingID != bindingID {
			continue
		}
		for _, subject := range binding.Subjects {
			if subject == subjectID {
				return true, true, nil
			}
		}
	}
	return false, true, nil
}

func collapseFactorWrite(write storageio.FactorWrite) (WriteReceipt, bool) {
	var best WriteReceipt
	ok := false
	for _, receipt := range write.Receipts {
		if strings.TrimSpace(receipt.NodeID) == "" || strings.TrimSpace(receipt.StoreID) == "" || receipt.Sequence == 0 {
			continue
		}
		if !ok || receipt.Sequence > best.Sequence {
			best = WriteReceipt{CommitID: receipt.CommitID, NodeID: receipt.NodeID, StoreID: receipt.StoreID, Sequence: receipt.Sequence}
			ok = true
		}
	}
	return best, ok
}

func applyReceipt(row *periodPairRow, receipt WriteReceipt) {
	if row == nil {
		return
	}
	if strings.TrimSpace(receipt.CommitID) == "" && strings.TrimSpace(receipt.NodeID) == "" && receipt.Sequence == 0 {
		return
	}
	row.ReceiptConfirmed = 1
	row.CommitID = strings.TrimSpace(receipt.CommitID)
	row.NodeID = strings.TrimSpace(receipt.NodeID)
	row.StoreID = strings.TrimSpace(receipt.StoreID)
	row.Sequence = receipt.Sequence
}

func validPairState(state string) bool {
	switch state {
	case PairComplete, PairFailed, PairSkipped, PairMissingInput:
		return true
	default:
		return false
	}
}

func pairTerminal(row periodPairRow) bool {
	switch row.State {
	case PairFailed, PairSkipped, PairMissingInput:
		return true
	case PairComplete:
		return row.ReceiptConfirmed == 1
	default:
		return false
	}
}

func periodPairsTerminal(bindings []FrozenBinding, pairs []periodPairRow) bool {
	found := make(map[string]periodPairRow, len(pairs))
	for _, pair := range pairs {
		found[pair.BindingID+"\x00"+pair.SubjectID] = pair
	}
	if len(bindings) == 0 {
		return true
	}
	for _, binding := range bindings {
		for _, subject := range binding.Subjects {
			pair, ok := found[binding.BindingID+"\x00"+subject]
			if !ok || !pairTerminal(pair) {
				return false
			}
		}
	}
	return true
}

func buildFactorPeriodMarker(key PeriodKey, row periodBarrierRow, bindings []FrozenBinding, pairs []periodPairRow) (*storagepb.FactorPeriodComputedMarker, error) {
	var expected []string
	if err := json.Unmarshal([]byte(row.ExpectedJSON), &expected); err != nil {
		return nil, err
	}
	byBinding := make(map[string][]periodPairRow)
	bestPos := make(map[string]*storagepb.CommittedPosition)
	for _, pair := range pairs {
		byBinding[pair.BindingID] = append(byBinding[pair.BindingID], pair)
		nodeID := strings.TrimSpace(pair.NodeID)
		storeID := strings.TrimSpace(pair.StoreID)
		if pair.ReceiptConfirmed != 1 || nodeID == "" || storeID == "" || pair.Sequence == 0 {
			continue
		}
		token := nodeID + "\x00" + storeID
		if current, ok := bestPos[token]; ok && pair.Sequence <= current.GetSequence() {
			continue
		}
		bestPos[token] = &storagepb.CommittedPosition{NodeId: nodeID, StoreId: storeID, Sequence: pair.Sequence}
	}
	positions := make([]*storagepb.CommittedPosition, 0, len(bestPos))
	for _, position := range bestPos {
		positions = append(positions, position)
	}
	sort.Slice(positions, func(i, j int) bool {
		if positions[i].GetNodeId() != positions[j].GetNodeId() {
			return positions[i].GetNodeId() < positions[j].GetNodeId()
		}
		if positions[i].GetStoreId() != positions[j].GetStoreId() {
			return positions[i].GetStoreId() < positions[j].GetStoreId()
		}
		return positions[i].GetSequence() < positions[j].GetSequence()
	})
	states := make([]*storagepb.FactorBindingPeriodState, 0, len(bindings))
	status := "complete"
	for _, binding := range bindings {
		state := &storagepb.FactorBindingPeriodState{
			BindingId: binding.BindingID, FactorId: binding.FactorID, Status: "complete", SourceHash: binding.SourceHash,
		}
		for _, pair := range byBinding[binding.BindingID] {
			switch pair.State {
			case PairFailed:
				state.FailedSubjects = append(state.FailedSubjects, pair.SubjectID)
				state.Status = "degraded"
				status = "degraded"
			case PairSkipped, PairMissingInput:
				state.SkippedSubjects = append(state.SkippedSubjects, pair.SubjectID)
				state.Status = "degraded"
				status = "degraded"
			}
		}
		state.SkippedSubjects = uniqueSorted(state.SkippedSubjects)
		state.FailedSubjects = uniqueSorted(state.FailedSubjects)
		states = append(states, state)
	}
	scope := strings.TrimSpace(row.ScopeRef)
	if scope == "" {
		scope = fmt.Sprintf("%s:%s:%d", key.DatasetID, key.Frequency, key.PeriodTime)
	}
	return &storagepb.FactorPeriodComputedMarker{
		DatasetId: key.DatasetID, Frequency: key.Frequency, PeriodTime: key.PeriodTime, Status: status,
		BatchId: row.BatchID, ConfigSnapshotId: key.SnapshotID, ExpectedScopeRef: scope,
		UniverseSubjectIds: expected, Bindings: states, CommittedPositions: positions,
		ComputedAt: timestamppb.Now(), TriggerEventId: row.BatchID,
	}, nil
}

func normalizeFrozenBindings(bindings []FrozenBinding, expected []string) []FrozenBinding {
	out := make([]FrozenBinding, 0, len(bindings))
	expectedSet := make(map[string]struct{}, len(expected))
	for _, subject := range expected {
		expectedSet[subject] = struct{}{}
	}
	for _, binding := range bindings {
		binding.BindingID = strings.TrimSpace(binding.BindingID)
		binding.FactorID = strings.TrimSpace(binding.FactorID)
		if binding.BindingID == "" {
			continue
		}
		subjects := uniqueSorted(binding.Subjects)
		if len(expectedSet) > 0 {
			filtered := make([]string, 0, len(subjects))
			for _, subject := range subjects {
				if _, ok := expectedSet[subject]; ok {
					filtered = append(filtered, subject)
				}
			}
			subjects = filtered
		}
		binding.Subjects = subjects
		out = append(out, binding)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].BindingID < out[j].BindingID })
	return out
}

func uniqueSorted(values []string) []string {
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
	sort.Strings(out)
	return out
}
