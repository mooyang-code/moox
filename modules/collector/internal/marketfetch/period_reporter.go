package marketfetch

import (
	"context"
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
	ReportDatasetPeriodCollected(context.Context, string, *storageeventpb.DatasetPeriodCollected) error
}

type PeriodReporter struct {
	periods         *store.PeriodReadinessRepository
	storage         DatasetPeriodReporter
	spaceID         string
	batchSize       int
	parentRetention time.Duration
	itemRetention   int
	now             func() time.Time
	metrics         *Metrics
}

// SetItemRetention trims the per-period subject snapshot after a report has
// been durably published. Parent rows remain available for the longer
// reporting retention window; only their detailed task rows are pruned.
func (r *PeriodReporter) SetItemRetention(periods int) {
	if r != nil {
		r.itemRetention = periods
	}
}

// SetMetrics attaches the process-wide low-cardinality period metrics sink.
// Keeping this setter optional preserves the small reporter test seam.
func (r *PeriodReporter) SetMetrics(metrics *Metrics) {
	if r != nil {
		r.metrics = metrics
	}
}

func NewPeriodReporter(periods *store.PeriodReadinessRepository, storage DatasetPeriodReporter, spaceID string, parentRetention time.Duration) *PeriodReporter {
	if parentRetention <= 0 {
		parentRetention = 7 * 24 * time.Hour
	}
	return &PeriodReporter{periods: periods, storage: storage, spaceID: spaceID, batchSize: 100, parentRetention: parentRetention, now: time.Now}
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
	if _, err := r.periods.FinalizeDue(ctx, now().UTC(), r.batchSize); err != nil {
		return fmt.Errorf("finalize period readiness: %w", err)
	}
	reports, err := r.periods.ListPendingReports(ctx, r.batchSize)
	if err != nil {
		return fmt.Errorf("list pending period reports: %w", err)
	}
	pending := make(map[string]int)
	for _, report := range reports {
		key := report.Readiness.DatasetID + "\x00" + report.Readiness.Frequency
		pending[key]++
	}
	var firstReportErr error
	for _, report := range reports {
		payload, err := r.payload(ctx, report)
		if err != nil {
			r.metrics.ObservePeriodReportRetry(report.Readiness.DatasetID, report.Readiness.Frequency)
			if firstReportErr == nil {
				firstReportErr = err
			}
			continue
		}
		if err := r.storage.ReportDatasetPeriodCollected(ctx, r.spaceID, payload); err != nil {
			r.metrics.ObservePeriodReportRetry(report.Readiness.DatasetID, report.Readiness.Frequency)
			if firstReportErr == nil {
				firstReportErr = fmt.Errorf("report dataset period collected dataset=%s period=%s: %w", report.Readiness.DatasetID, report.Readiness.PeriodTime.Format(time.RFC3339), err)
			}
			continue
		}
		if err := r.periods.MarkReported(ctx, report.Readiness.ID); err != nil {
			r.metrics.ObservePeriodReportRetry(report.Readiness.DatasetID, report.Readiness.Frequency)
			if firstReportErr == nil {
				firstReportErr = fmt.Errorf("mark period report reported id=%d: %w", report.Readiness.ID, err)
			}
			continue
		}
	}
	if r.parentRetention > 0 {
		if _, err := r.periods.DeleteBefore(ctx, now().UTC().Add(-r.parentRetention)); err != nil {
			if firstReportErr == nil {
				firstReportErr = fmt.Errorf("cleanup period readiness: %w", err)
			}
		}
	}
	if r.itemRetention > 0 {
		if _, err := r.periods.DeleteReportedItemsOutsideWindow(ctx, r.itemRetention); err != nil {
			if firstReportErr == nil {
				firstReportErr = fmt.Errorf("cleanup period readiness items: %w", err)
			}
		}
	}
	counts, err := r.periods.CountPendingReports(ctx)
	if err != nil {
		if firstReportErr == nil {
			firstReportErr = fmt.Errorf("count pending period reports: %w", err)
		}
	} else {
		for key := range pending {
			parts := strings.SplitN(key, "\x00", 2)
			count := counts[key]
			r.metrics.ObservePeriodPending(parts[0], parts[1], count)
		}
		for key, count := range counts {
			if _, seen := pending[key]; seen {
				continue
			}
			parts := strings.SplitN(key, "\x00", 2)
			r.metrics.ObservePeriodPending(parts[0], parts[1], count)
		}
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

func (r *PeriodReporter) payload(ctx context.Context, report domain.PeriodReport) (*storageeventpb.DatasetPeriodCollected, error) {
	if report.Readiness.PayloadJSON != "" && report.Readiness.PayloadJSON != "{}" {
		payload := &storageeventpb.DatasetPeriodCollected{}
		if err := protojson.Unmarshal([]byte(report.Readiness.PayloadJSON), payload); err != nil {
			return nil, fmt.Errorf("decode fixed period payload id=%d: %w", report.Readiness.ID, err)
		}
		return payload, nil
	}
	subjects := make([]string, 0, len(report.Items))
	failed := make([]string, 0)
	for _, item := range report.Items {
		subjects = append(subjects, item.SubjectID)
		if item.State != domain.PeriodItemSuccess {
			failed = append(failed, item.SubjectID)
		}
	}
	sort.Strings(subjects)
	sort.Strings(failed)
	status := report.Readiness.Status
	if status == "" {
		status = domain.PeriodStatusDegraded
	}
	payload := &storageeventpb.DatasetPeriodCollected{
		DatasetId:      report.Readiness.DatasetID,
		Frequency:      report.Readiness.Frequency,
		PeriodTime:     report.Readiness.PeriodTime.Unix(),
		Status:         status,
		SubjectIds:     subjects,
		FailedSubjects: failed,
		CollectedAt:    timestamppb.New(report.Readiness.CollectedAt.UTC()),
	}
	raw, err := protojson.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode fixed period payload id=%d: %w", report.Readiness.ID, err)
	}
	if err := r.periods.PersistPayload(ctx, report.Readiness.ID, string(raw)); err != nil {
		return nil, fmt.Errorf("persist fixed period payload id=%d: %w", report.Readiness.ID, err)
	}
	return payload, nil
}
