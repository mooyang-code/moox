package marketfetch

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	storageeventpb "github.com/mooyang-code/moox/packages/storagepb"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"
	"trpc.group/trpc-go/trpc-go/log"
)

// DatasetPeriodReporter is implemented by the Collector's Storage adapter.
// Keeping it small lets the readiness state machine remain testable without a
// live tRPC server.
type DatasetPeriodReporter interface {
	ReportCollectorPeriodCompleted(context.Context, string, *storageeventpb.CollectorPeriodCompleted) error
}

type PeriodReporter struct {
	periods   *store.PeriodReadinessRepository
	storage   DatasetPeriodReporter
	spaceID   string
	batchSize int
	nodeID    string
	storeID   string
	now       func() time.Time
	metrics   *Metrics
}

// SetMetrics attaches the process-wide low-cardinality period metrics sink.
// Keeping this setter optional preserves the small reporter test seam.
func (r *PeriodReporter) SetMetrics(metrics *Metrics) {
	if r != nil {
		r.metrics = metrics
	}
}

func NewPeriodReporter(periods *store.PeriodReadinessRepository, storage DatasetPeriodReporter, spaceID string) *PeriodReporter {
	return &PeriodReporter{periods: periods, storage: storage, spaceID: spaceID, batchSize: 100, now: time.Now}
}

func StartPeriodReporter(ctx context.Context, reporter *PeriodReporter, interval time.Duration) error {
	if reporter == nil {
		return fmt.Errorf("period reporter is required")
	}
	if interval <= 0 {
		interval = 5 * time.Second
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			if err := reporter.Flush(ctx); err != nil && ctx.Err() == nil {
				log.WarnContextf(ctx, "collector period readiness report failed: %v", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return nil
}

func (r *PeriodReporter) Flush(ctx context.Context) error {
	if r == nil || r.periods == nil || r.storage == nil {
		return fmt.Errorf("period reporter is not initialized")
	}
	now := time.Now
	if r.now != nil {
		now = r.now
	}
	if _, err := r.periods.FinalizeDueInSpace(ctx, r.spaceID, now().UTC(), r.batchSize); err != nil {
		return fmt.Errorf("finalize period readiness: %w", err)
	}
	reports, err := r.periods.ListPendingReportsInSpace(ctx, r.spaceID, r.batchSize)
	if err != nil {
		return fmt.Errorf("list pending period reports: %w", err)
	}
	var firstReportErr error
	for _, report := range reports {
		payload, err := r.payload(ctx, report)
		if err != nil {
			r.metrics.ObservePeriodReportRetry(r.spaceID, report.Readiness.Frequency)
			if firstReportErr == nil {
				firstReportErr = err
			}
			continue
		}
		if err := r.storage.ReportCollectorPeriodCompleted(ctx, r.spaceID, payload); err != nil {
			r.metrics.ObservePeriodReportRetry(r.spaceID, report.Readiness.Frequency)
			if firstReportErr == nil {
				firstReportErr = fmt.Errorf("report collector period completed dataset=%s period=%s: %w", report.Readiness.DatasetID, report.Readiness.PeriodTime.Format(time.RFC3339), err)
			}
			continue
		}
		if err := r.periods.MarkReported(ctx, report.Readiness.ID); err != nil {
			r.metrics.ObservePeriodReportRetry(r.spaceID, report.Readiness.Frequency)
			if firstReportErr == nil {
				firstReportErr = fmt.Errorf("mark period report reported id=%d: %w", report.Readiness.ID, err)
			}
			continue
		}
	}
	counts, err := r.periods.CountPendingReportsInSpace(ctx, r.spaceID)
	if err != nil {
		if firstReportErr == nil {
			firstReportErr = fmt.Errorf("count pending period reports: %w", err)
		}
	} else {
		pendingByFrequency := make(map[string]int)
		for key, count := range counts {
			parts := strings.SplitN(key, "\x00", 2)
			if len(parts) == 2 {
				pendingByFrequency[parts[1]] += count
			}
		}
		r.metrics.ObservePeriodPendingSnapshot(r.spaceID, pendingByFrequency)
	}
	if firstReportErr != nil {
		// Continue attempting the remaining datasets in this flush, but keep
		// the first error visible to the watchdog/logging path for retry. The
		// current pending count is still observed above so a failed report does
		// not make the gauge look healthy.
		return firstReportErr
	}
	return nil
}

func (r *PeriodReporter) payload(ctx context.Context, report domain.PeriodReport) (*storageeventpb.CollectorPeriodCompleted, error) {
	if report.Readiness.PayloadJSON != "" && report.Readiness.PayloadJSON != "{}" {
		payload := &storageeventpb.CollectorPeriodCompleted{}
		if err := protojson.Unmarshal([]byte(report.Readiness.PayloadJSON), payload); err != nil {
			// Unreadable snapshots are rebuilt from period item rows.
		} else if len(payload.GetUniverseSubjectIds()) > 0 {
			return r.completeCollectorPayload(payload, report), nil
		}
	}
	subjectsByID := make(map[string]struct{}, len(report.Items))
	failedByID := make(map[string]struct{})
	for _, item := range report.Items {
		subjectsByID[item.SubjectID] = struct{}{}
		if item.State != domain.PeriodItemSuccess {
			failedByID[item.SubjectID] = struct{}{}
		}
	}
	subjects := make([]string, 0, len(subjectsByID))
	for subjectID := range subjectsByID {
		subjects = append(subjects, subjectID)
	}
	failed := make([]string, 0, len(failedByID))
	for subjectID := range failedByID {
		failed = append(failed, subjectID)
	}
	sort.Strings(subjects)
	sort.Strings(failed)
	status := report.Readiness.Status
	if status == "" {
		status = domain.PeriodStatusDegraded
	}
	payload := r.completeCollectorPayload(&storageeventpb.CollectorPeriodCompleted{
		DatasetId:          report.Readiness.DatasetID,
		Frequency:          report.Readiness.Frequency,
		PeriodTime:         report.Readiness.PeriodTime.Unix(),
		Status:             status,
		UniverseSubjectIds: subjects,
		FailedSubjects:     failed,
		CollectedAt:        timestamppb.New(report.Readiness.CollectedAt.UTC()),
	}, report)
	raw, err := protojson.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode fixed period payload id=%d: %w", report.Readiness.ID, err)
	}
	if err := r.periods.PersistPayload(ctx, report.Readiness.ID, string(raw)); err != nil {
		return nil, fmt.Errorf("persist fixed period payload id=%d: %w", report.Readiness.ID, err)
	}
	return payload, nil
}

func (r *PeriodReporter) completeCollectorPayload(payload *storageeventpb.CollectorPeriodCompleted, report domain.PeriodReport) *storageeventpb.CollectorPeriodCompleted {
	if payload == nil {
		payload = &storageeventpb.CollectorPeriodCompleted{}
	}
	scope := fmt.Sprintf("%s:%s:%d", report.Readiness.DatasetID, report.Readiness.Frequency, report.Readiness.PeriodTime.Unix())
	if strings.TrimSpace(payload.GetBatchId()) == "" {
		payload.BatchId = scope
	}
	if strings.TrimSpace(payload.GetConfigSnapshotId()) == "" {
		payload.ConfigSnapshotId = "collector"
	}
	if strings.TrimSpace(payload.GetExpectedScopeRef()) == "" {
		payload.ExpectedScopeRef = scope
	}
	if len(payload.GetCommittedPositions()) == 0 {
		payload.CommittedPositions = decodeCommittedPositions(report.Readiness.CommittedPositionsJSON)
	}
	return payload
}

func decodeCommittedPositions(raw string) []*storageeventpb.CommittedPosition {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" || raw == "[]" {
		return nil
	}
	var stored []struct {
		NodeID   string `json:"node_id"`
		StoreID  string `json:"store_id"`
		Sequence uint64 `json:"sequence"`
	}
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return nil
	}
	positions := make([]*storageeventpb.CommittedPosition, 0, len(stored))
	for _, item := range stored {
		if strings.TrimSpace(item.NodeID) == "" || strings.TrimSpace(item.StoreID) == "" || item.Sequence == 0 {
			continue
		}
		positions = append(positions, &storageeventpb.CommittedPosition{NodeId: item.NodeID, StoreId: item.StoreID, Sequence: item.Sequence})
	}
	return positions
}
