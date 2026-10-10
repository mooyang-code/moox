package marketfetch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	periodFailureReportInterval   = time.Second
	periodFailureReportBudget     = 5 * time.Second
	periodFailureReportRPCTimeout = time.Second
	periodFailureMetricsTimeout   = 500 * time.Millisecond
	periodFailureReportPageSize   = 100
	periodFailureReportMaxRows    = 1000
)

type periodFailureReportStore interface {
	ListPendingPeriodFailuresAfter(context.Context, string, string, int) ([]domain.RetryItem, error)
	ApplyPeriodFailureReportResults(context.Context, string, string, []domain.PeriodFailureTargetResult, string) error
	CountPendingPeriodFailuresByFrequency(context.Context, string) (map[string]int64, error)
}

type periodFailureReceiptStorage interface {
	RecordDatasetPeriodFailures(context.Context, *storagepb.DatasetPeriodExpectation, []uint32) ([]*storagepb.DatasetPeriodFailureResult, error)
}

// PeriodFailureReporter drains the durable permanent-failure retry outbox
// independently of scheduling and CloudNode availability.
type PeriodFailureReporter struct {
	retries         periodFailureReportStore
	storage         func(string, string) (Storage, error)
	spaceID         string
	metrics         *Metrics
	pageSize        int
	maxRows         int
	budget          time.Duration
	rpcTimeout      time.Duration
	now             func() time.Time
	cursor          string
	runMu           sync.Mutex
	seenFrequencies map[string]struct{}
	wake            chan struct{}
}

func NewPeriodFailureReporter(retries *store.FetchRetryRepository, storage func(string, string) (Storage, error), spaceID string) *PeriodFailureReporter {
	return &PeriodFailureReporter{
		retries: retries, storage: storage, spaceID: strings.TrimSpace(spaceID),
		pageSize: periodFailureReportPageSize, maxRows: periodFailureReportMaxRows,
		budget: periodFailureReportBudget, rpcTimeout: periodFailureReportRPCTimeout, now: time.Now,
		seenFrequencies: make(map[string]struct{}), wake: make(chan struct{}, 1),
	}
}

func (r *PeriodFailureReporter) SetMetrics(metrics *Metrics) {
	if r != nil {
		r.metrics = metrics
	}
}

// Wake requests an immediate outbox scan without blocking completion handling.
func (r *PeriodFailureReporter) Wake() {
	if r == nil || r.wake == nil {
		return
	}
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// RunOnce attempts a bounded lexicographic page of durable failures. The
// cursor advances after every attempted row, including failures, so an early
// unreachable Storage row cannot starve later retry keys.
func (r *PeriodFailureReporter) RunOnce(ctx context.Context, spaceID string) error {
	if r == nil || r.retries == nil || r.storage == nil {
		return fmt.Errorf("period failure reporter is not initialized")
	}
	if !r.runMu.TryLock() {
		return nil
	}
	defer r.runMu.Unlock()
	spaceID = strings.TrimSpace(spaceID)
	if spaceID == "" {
		spaceID = r.spaceID
	}
	if spaceID == "" {
		return fmt.Errorf("period failure reporter space_id is required")
	}
	budget := r.budget
	if budget <= 0 {
		budget = periodFailureReportBudget
	}
	roundCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	roundDeadline, hasRoundDeadline := roundCtx.Deadline()
	reserve := periodFailureMetricsTimeout
	if hasRoundDeadline {
		remaining := time.Until(roundDeadline)
		if remaining <= 0 {
			reserve = 0
		} else if reserve > remaining/5 {
			reserve = remaining / 5
		}
	}
	workCtx := roundCtx
	workCancel := func() {}
	if hasRoundDeadline {
		workCtx, workCancel = context.WithDeadline(roundCtx, roundDeadline.Add(-reserve))
	}
	defer workCancel()
	pageSize := r.pageSize
	if pageSize <= 0 {
		pageSize = periodFailureReportPageSize
	}
	maxRows := r.maxRows
	if maxRows <= 0 {
		maxRows = periodFailureReportMaxRows
	}
	cursor := r.cursor
	var firstErr error
	attempted := 0
	for attempted < maxRows && workCtx.Err() == nil {
		limit := min(pageSize, maxRows-attempted)
		page, err := r.retries.ListPendingPeriodFailuresAfter(workCtx, spaceID, cursor, limit)
		if err != nil {
			logPeriodFailureReportScanError(roundCtx, spaceID, err)
			if firstErr == nil {
				firstErr = fmt.Errorf("list pending period failure receipts: %w", err)
			}
			break
		}
		if len(page) == 0 {
			cursor = ""
			break
		}
		pageComplete := true
		for _, retry := range page {
			if workCtx.Err() != nil || attempted >= maxRows {
				pageComplete = false
				break
			}
			cursor = retry.RetryKey
			attempted++
			err := r.reportRetry(workCtx, spaceID, retry)
			if err != nil {
				logPeriodFailureReportError(roundCtx, spaceID, retry, err)
				if firstErr == nil {
					firstErr = err
				}
			}
		}
		if len(page) < limit {
			if pageComplete {
				cursor = ""
			}
			break
		}
		if !pageComplete {
			break
		}
	}
	r.cursor = cursor
	if workCtx.Err() != nil && firstErr == nil {
		firstErr = workCtx.Err()
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := r.refreshPendingMetrics(roundCtx, spaceID); err != nil {
		logPeriodFailureReportScanError(roundCtx, spaceID, err)
		if firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (r *PeriodFailureReporter) reportRetry(ctx context.Context, spaceID string, retry domain.RetryItem) error {
	var item domain.CollectionItem
	if err := json.Unmarshal([]byte(retry.TaskJSON), &item); err != nil {
		return r.persistReportError(ctx, spaceID, retry, nil, fmt.Errorf("decode permanent retry task: %w", err))
	}
	var targets []domain.WriteTarget
	if err := json.Unmarshal([]byte(retry.FailureTargetsJSON), &targets); err != nil || len(targets) == 0 {
		if err == nil {
			err = errors.New("failure targets are empty")
		}
		return r.persistReportError(ctx, spaceID, retry, nil, fmt.Errorf("decode permanent retry targets: %w", err))
	}
	periodTime := retry.TargetDataTime.UTC()
	if periodTime.IsZero() {
		if parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(item.TargetDataTime)); err == nil {
			periodTime = parsed.UTC()
		}
	}
	frequency, err := marketdata.ParseFrequency(firstNonEmpty(retry.Frequency, item.Frequency))
	if err != nil || periodTime.IsZero() {
		if err == nil {
			err = errors.New("target period is missing")
		}
		return r.persistReportError(ctx, spaceID, retry, nil, fmt.Errorf("invalid permanent retry period identity: %w", err))
	}
	r.seenFrequencies[string(frequency)] = struct{}{}
	storageFrequency, err := normalizeStorageFrequency(string(frequency))
	if err != nil {
		return r.persistReportError(ctx, spaceID, retry, nil, fmt.Errorf("invalid Storage period frequency: %w", err))
	}
	previousResults := make(map[string]string)
	var persisted []domain.PeriodFailureTargetResult
	if json.Unmarshal([]byte(retry.PeriodFailureResultsJSON), &persisted) == nil {
		for _, result := range persisted {
			previousResults[result.WriteTargetID] = result.Disposition
		}
	}
	type targetEntry struct {
		target domain.WriteTarget
	}
	type reportGroup struct {
		expectation *storagepb.DatasetPeriodExpectation
		marketType  string
		targets     map[uint32][]targetEntry
	}
	groups := make(map[string]*reportGroup)
	for _, target := range targets {
		if target.ID == "" || target.DatasetID == "" || target.SeriesHash == "" || target.ExpectedCount == 0 || target.SeriesIndex >= target.ExpectedCount || (target.SpaceID != "" && target.SpaceID != spaceID) {
			return r.persistReportError(ctx, spaceID, retry, nil, fmt.Errorf("invalid durable failure target %q", target.ID))
		}
		key := strings.Join([]string{target.DatasetID, storageFrequency, periodTime.Format(time.RFC3339Nano), target.SeriesHash, fmt.Sprint(target.ExpectedCount), item.MarketType, item.PeriodReservationID}, "\x00")
		group := groups[key]
		if group == nil {
			group = &reportGroup{
				expectation: &storagepb.DatasetPeriodExpectation{SpaceId: spaceID, DatasetId: target.DatasetID, Frequency: storageFrequency, PeriodTime: periodTime.Unix(), SeriesHash: target.SeriesHash, ExpectedCount: target.ExpectedCount, ReservationId: item.PeriodReservationID},
				marketType:  item.MarketType, targets: make(map[uint32][]targetEntry),
			}
			groups[key] = group
		}
		group.targets[target.SeriesIndex] = append(group.targets[target.SeriesIndex], targetEntry{target: target})
	}
	groupKeys := make([]string, 0, len(groups))
	for key := range groups {
		groupKeys = append(groupKeys, key)
	}
	sort.Strings(groupKeys)
	results := make([]domain.PeriodFailureTargetResult, 0, len(targets))
	var firstErr error
	for _, key := range groupKeys {
		if ctx.Err() != nil {
			firstErr = ctx.Err()
			break
		}
		group := groups[key]
		storage, err := r.storage(group.marketType, "collector")
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("create Storage client for period failure dataset=%s period=%s: %w", group.expectation.GetDatasetId(), periodTime.Format(time.RFC3339), err)
			}
			r.observeReportRetry(spaceID, string(frequency), "storage_client_error")
			continue
		}
		periodClient, ok := storage.(periodFailureReceiptStorage)
		if !ok {
			if firstErr == nil {
				firstErr = fmt.Errorf("Storage client for period failure dataset=%s does not support period failures", group.expectation.GetDatasetId())
			}
			r.observeReportRetry(spaceID, string(frequency), "storage_client_error")
			continue
		}
		indexes := make([]uint32, 0, len(group.targets))
		for index := range group.targets {
			indexes = append(indexes, index)
		}
		sort.Slice(indexes, func(i, j int) bool { return indexes[i] < indexes[j] })
		rpcCtx, cancel, budgetErr := r.rpcContext(ctx)
		if budgetErr != nil {
			firstErr = budgetErr
			break
		}
		response, callErr := periodClient.RecordDatasetPeriodFailures(rpcCtx, group.expectation, indexes)
		cancel()
		if callErr == nil {
			callErr = validatePeriodFailureReceiptIndexes(indexes, response)
		}
		if callErr != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("record period failure dataset=%s period=%s: %w", group.expectation.GetDatasetId(), periodTime.Format(time.RFC3339), callErr)
			}
			r.observeReportRetry(spaceID, string(frequency), failureReportOutcome(callErr))
			continue
		}
		observedAt := r.currentTime().UTC()
		byIndex := make(map[uint32]string, len(response))
		for _, result := range response {
			byIndex[result.GetSeriesIndex()] = periodFailureDisposition(result.GetDisposition())
		}
		for _, index := range indexes {
			for _, entry := range group.targets[index] {
				target := entry.target
				results = append(results, domain.PeriodFailureTargetResult{
					WriteTargetID: target.ID, SpaceID: spaceID, DatasetID: target.DatasetID, Frequency: string(frequency), PeriodTime: periodTime,
					SeriesHash: target.SeriesHash, ExpectedCount: target.ExpectedCount, SeriesIndex: target.SeriesIndex,
					Disposition: byIndex[index], ObservedAt: observedAt,
				})
			}
		}
	}
	if err := r.retries.ApplyPeriodFailureReportResults(ctx, spaceID, retry.RetryKey, results, errorString(firstErr)); err != nil {
		if firstErr != nil {
			return fmt.Errorf("persist period failure receipt retry_key=%s after %v: %w", retry.RetryKey, firstErr, err)
		}
		return fmt.Errorf("persist period failure receipt retry_key=%s: %w", retry.RetryKey, err)
	}
	for _, result := range results {
		if result.Disposition == "missed_deadline" && previousResults[result.WriteTargetID] != "missed_deadline" {
			r.observeMissedDeadline(spaceID, result.Frequency)
		}
	}
	if firstErr != nil {
		return fmt.Errorf("period failure report retry_key=%s frequency=%s outcome=%s: %w", retry.RetryKey, frequency, failureReportOutcome(firstErr), firstErr)
	}
	return nil
}

func (r *PeriodFailureReporter) rpcContext(ctx context.Context) (context.Context, context.CancelFunc, error) {
	timeout := r.rpcTimeout
	if timeout <= 0 {
		timeout = periodFailureReportRPCTimeout
	}
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline) - 100*time.Millisecond
		if remaining <= 0 {
			return nil, nil, context.DeadlineExceeded
		}
		if timeout > remaining {
			timeout = remaining
		}
	}
	rpcCtx, cancel := context.WithTimeout(ctx, timeout)
	return rpcCtx, cancel, nil
}

func (r *PeriodFailureReporter) persistReportError(ctx context.Context, spaceID string, retry domain.RetryItem, results []domain.PeriodFailureTargetResult, reportErr error) error {
	if err := r.retries.ApplyPeriodFailureReportResults(ctx, spaceID, retry.RetryKey, results, reportErr.Error()); err != nil {
		return fmt.Errorf("persist period failure report error retry_key=%s: %w", retry.RetryKey, err)
	}
	return reportErr
}

func (r *PeriodFailureReporter) refreshPendingMetrics(ctx context.Context, spaceID string) error {
	if r.metrics == nil {
		return nil
	}
	counts, err := r.retries.CountPendingPeriodFailuresByFrequency(ctx, spaceID)
	if err != nil {
		return fmt.Errorf("count pending period failures: %w", err)
	}
	validCounts := make(map[string]int64, len(counts))
	for raw, count := range counts {
		frequency, parseErr := marketdata.ParseFrequency(raw)
		if parseErr != nil {
			continue
		}
		key := string(frequency)
		validCounts[key] += count
		r.seenFrequencies[key] = struct{}{}
	}
	for frequency := range r.seenFrequencies {
		r.metrics.ObservePeriodFailurePending(spaceID, frequency, int(validCounts[frequency]))
	}
	return nil
}

func (r *PeriodFailureReporter) observeReportRetry(spaceID, frequency, outcome string) {
	if r.metrics != nil {
		r.metrics.ObservePeriodFailureReportRetry(spaceID, frequency, outcome)
	}
}

func (r *PeriodFailureReporter) observeMissedDeadline(spaceID, frequency string) {
	if r.metrics != nil {
		r.metrics.ObservePeriodFailureMissedDeadline(spaceID, frequency)
	}
}

func (r *PeriodFailureReporter) currentTime() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

func validatePeriodFailureReceiptIndexes(indexes []uint32, results []*storagepb.DatasetPeriodFailureResult) error {
	expected := make(map[uint32]struct{}, len(indexes))
	for _, index := range indexes {
		expected[index] = struct{}{}
	}
	seen := make(map[uint32]struct{}, len(results))
	for _, result := range results {
		if result == nil {
			return errors.New("Storage returned a nil failure receipt")
		}
		index := result.GetSeriesIndex()
		if _, duplicate := seen[index]; duplicate {
			return fmt.Errorf("Storage returned duplicate failure receipt index %d", index)
		}
		if _, exists := expected[index]; !exists {
			return fmt.Errorf("Storage returned unexpected failure receipt index %d", index)
		}
		seen[index] = struct{}{}
		switch result.GetDisposition() {
		case storagepb.PeriodFailureDisposition_PERIOD_FAILURE_DISPOSITION_RECORDED,
			storagepb.PeriodFailureDisposition_PERIOD_FAILURE_DISPOSITION_ALREADY_SUCCEEDED,
			storagepb.PeriodFailureDisposition_PERIOD_FAILURE_DISPOSITION_MISSED_DEADLINE:
		default:
			return fmt.Errorf("Storage returned unknown period failure disposition %d", result.GetDisposition())
		}
	}
	if len(seen) != len(expected) {
		return fmt.Errorf("Storage returned %d failure receipts, want %d", len(seen), len(expected))
	}
	return nil
}

func periodFailureDisposition(disposition storagepb.PeriodFailureDisposition) string {
	switch disposition {
	case storagepb.PeriodFailureDisposition_PERIOD_FAILURE_DISPOSITION_RECORDED:
		return "recorded"
	case storagepb.PeriodFailureDisposition_PERIOD_FAILURE_DISPOSITION_ALREADY_SUCCEEDED:
		return "already_succeeded"
	case storagepb.PeriodFailureDisposition_PERIOD_FAILURE_DISPOSITION_MISSED_DEADLINE:
		return "missed_deadline"
	default:
		return "unknown"
	}
}

func failureReportOutcome(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	return "storage_error"
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func logPeriodFailureReportError(ctx context.Context, spaceID string, retry domain.RetryItem, err error) {
	period := retry.TargetDataTime.UTC()
	frequency := retry.Frequency
	var item domain.CollectionItem
	if json.Unmarshal([]byte(retry.TaskJSON), &item) == nil {
		if period.IsZero() {
			if parsed, parseErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(item.TargetDataTime)); parseErr == nil {
				period = parsed.UTC()
			}
		}
		frequency = firstNonEmpty(frequency, item.Frequency)
	}
	periodText := "unknown"
	if !period.IsZero() {
		periodText = period.Format(time.RFC3339Nano)
	}
	frequencyText := "unknown"
	if parsed, parseErr := marketdata.ParseFrequency(frequency); parseErr == nil {
		frequencyText = string(parsed)
	}
	log.WarnContextf(ctx, "collector period failure report failed space=%s retry_key=%s period=%s frequency=%s outcome=%s", strings.TrimSpace(spaceID), strings.TrimSpace(retry.RetryKey), periodText, frequencyText, failureReportOutcome(err))
}

func logPeriodFailureReportScanError(ctx context.Context, spaceID string, err error) {
	log.WarnContextf(ctx, "collector period failure report scan failed space=%s retry_key=unknown period=unknown frequency=unknown outcome=%s", strings.TrimSpace(spaceID), failureReportOutcome(err))
}

func StartPeriodFailureReporter(ctx context.Context, reporter *PeriodFailureReporter, spaceID string, interval time.Duration) error {
	_, err := StartPeriodFailureReporterWithDone(ctx, reporter, spaceID, interval)
	return err
}

func StartPeriodFailureReporterWithDone(ctx context.Context, reporter *PeriodFailureReporter, spaceID string, interval time.Duration) (<-chan struct{}, error) {
	if reporter == nil {
		return nil, fmt.Errorf("period failure reporter is required")
	}
	if interval <= 0 {
		interval = periodFailureReportInterval
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			_ = reporter.RunOnce(ctx, spaceID)
			select {
			case <-ctx.Done():
				return
			case <-reporter.wake:
			case <-ticker.C:
			}
		}
	}()
	return done, nil
}
