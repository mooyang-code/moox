package marketfetch

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/httpclient"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/packages/marketfetchpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func isHistoricalBatchKind(kind domain.BatchKind) bool {
	switch kind {
	case domain.BatchKindCatchup, domain.BatchKindBackfill, domain.BatchKindGapRepair:
		return true
	default:
		return false
	}
}

func successResult(item domain.CollectionItem) domain.ItemResult {
	return domain.ItemResult{CollectionItem: item, Outcome: domain.ItemOutcomeSuccess}
}

func failureResult(item domain.CollectionItem, outcome domain.ItemOutcome, errorType string, err error) domain.ItemResult {
	return domain.ItemResult{CollectionItem: item, Outcome: outcome, ErrorType: errorType, ErrorSummary: truncateError(err)}
}

func buildCompletion(req Request, results []domain.ItemResult, completed time.Time, duration time.Duration) *marketfetchpb.MarketFetchBatchCompleted {
	payload := &marketfetchpb.MarketFetchBatchCompleted{
		BatchId: req.BatchID, ScheduleId: req.ScheduleID, BatchKind: string(req.BatchKind),
		DatasetId: req.DatasetID, Frequency: req.Frequency, Region: req.Region, NodeId: req.NodeID,
		RequestId: req.RequestID, PlannedCount: int32(len(results)), DurationMs: duration.Milliseconds(),
		CompletedAt: timestamppb.New(completed.UTC()),
	}
	var firstError string
	for _, result := range results {
		if result.Outcome == domain.ItemOutcomeSuccess {
			payload.SuccessCount++
		} else {
			if isRetryable(result.Outcome) {
				payload.RetryCount++
			} else {
				payload.PermanentFailedCount++
			}
			if firstError == "" {
				firstError = result.ErrorSummary
			}
		}
		payload.Items = append(payload.Items, &marketfetchpb.MarketFetchItemResult{
			SubjectId: result.SubjectID, Symbol: result.Symbol, TargetDataTime: result.TargetDataTime,
			Outcome: string(result.Outcome), ErrorType: result.ErrorType, ErrorSummary: result.ErrorSummary,
			SourceEventId: result.SourceEventID, TaskId: result.TaskID,
		})
	}
	switch {
	case payload.SuccessCount == payload.PlannedCount:
		payload.Status = "succeeded"
	case payload.SuccessCount > 0:
		payload.Status = "partial_failed"
	default:
		payload.Status = "failed"
	}
	payload.ErrorSummary = truncateString(firstError, 256)
	return payload
}

func classifyError(err error) domain.ItemOutcome {
	if err == nil {
		return domain.ItemOutcomeSuccess
	}
	if errors.Is(err, marketdata.ErrRateLimited) {
		return domain.ItemOutcomeHTTP429
	}
	if errors.Is(err, marketdata.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
		return domain.ItemOutcomeNetworkError
	}
	if errors.Is(err, marketdata.ErrProtocol) || errors.Is(err, marketdata.ErrNoClosedBar) ||
		errors.Is(err, marketdata.ErrHistoryOutOfRange) || errors.Is(err, marketdata.ErrHistoryCoverage) ||
		errors.Is(err, marketdata.ErrProviderNotFound) {
		return domain.ItemOutcomeProviderError
	}
	var statusErr *httpclient.StatusError
	if errors.As(err, &statusErr) {
		switch {
		case statusErr.StatusCode == 429:
			return domain.ItemOutcomeHTTP429
		case statusErr.StatusCode >= 500:
			return domain.ItemOutcomeHTTP5xx
		default:
			return domain.ItemOutcomeInvalid
		}
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "429") {
		return domain.ItemOutcomeHTTP429
	}
	if strings.Contains(message, "500") || strings.Contains(message, "502") || strings.Contains(message, "503") || strings.Contains(message, "504") || strings.Contains(message, "5xx") {
		return domain.ItemOutcomeHTTP5xx
	}
	if strings.Contains(message, "deadline") || strings.Contains(message, "timeout") || strings.Contains(message, "connection") {
		return domain.ItemOutcomeNetworkError
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || strings.Contains(message, "eof") {
		return domain.ItemOutcomeNetworkError
	}
	return domain.ItemOutcomeInvalid
}

func errorType(err error) string {
	if err == nil {
		return ""
	}
	return string(classifyError(err))
}

func isRetryable(outcome domain.ItemOutcome) bool {
	return outcome == domain.ItemOutcomeHTTP429 || outcome == domain.ItemOutcomeHTTP5xx || outcome == domain.ItemOutcomeNetworkError || outcome == domain.ItemOutcomeStorageError || outcome == domain.ItemOutcomeProviderError
}

func truncateError(err error) string {
	if err == nil {
		return ""
	}
	return truncateString(err.Error(), 256)
}

func truncateString(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
