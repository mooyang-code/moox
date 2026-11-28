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
)

// BatchCompleted 是一次行情函数调用的批次结果（纯进程内结构，不再作为事件发布），
// 包含已被 Storage 确认的逐项结果。
type BatchCompleted struct {
	BatchID              string       `json:"batch_id,omitempty"`
	ScheduleID           string       `json:"schedule_id,omitempty"`
	BatchKind            string       `json:"batch_kind,omitempty"`
	DatasetID            string       `json:"dataset_id,omitempty"`
	Frequency            string       `json:"frequency,omitempty"`
	Region               string       `json:"region,omitempty"`
	NodeID               string       `json:"node_id,omitempty"`
	RequestID            string       `json:"request_id,omitempty"`
	PlannedCount         int32        `json:"planned_count,omitempty"`
	SuccessCount         int32        `json:"success_count,omitempty"`
	RetryCount           int32        `json:"retry_count,omitempty"`
	PermanentFailedCount int32        `json:"permanent_failed_count,omitempty"`
	Status               string       `json:"status,omitempty"`
	ErrorSummary         string       `json:"error_summary,omitempty"`
	DurationMS           int64        `json:"duration_ms,omitempty"`
	CompletedAt          int64        `json:"completed_at,omitempty"`
	Items                []ItemResult `json:"items,omitempty"`
}

// ItemResult 是批次内单个采集项的结果。
type ItemResult struct {
	SubjectID      string         `json:"subject_id,omitempty"`
	Symbol         string         `json:"symbol,omitempty"`
	TargetDataTime string         `json:"target_data_time,omitempty"`
	Outcome        string         `json:"outcome,omitempty"`
	ErrorType      string         `json:"error_type,omitempty"`
	ErrorSummary   string         `json:"error_summary,omitempty"`
	SourceEventID  string         `json:"source_event_id,omitempty"`
	InstanceID     string         `json:"instance_id,omitempty"`
	Targets        []TargetResult `json:"targets,omitempty"`
}

// TargetResult 是单个写入目标的落库结果。
type TargetResult struct {
	WriteTargetID string `json:"write_target_id,omitempty"`
	DatasetID     string `json:"dataset_id,omitempty"`
	Status        string `json:"status,omitempty"`
	ErrorSummary  string `json:"error_summary,omitempty"`
}

func isHistoricalBatchKind(kind domain.BatchKind) bool {
	return kind == domain.BatchKindBackfill
}

func successResult(item domain.CollectionItem) domain.ItemResult {
	return domain.ItemResult{CollectionItem: item, Outcome: domain.ItemOutcomeSuccess}
}

func failureResult(item domain.CollectionItem, outcome domain.ItemOutcome, errorType string, err error) domain.ItemResult {
	return domain.ItemResult{CollectionItem: item, Outcome: outcome, ErrorType: errorType, ErrorSummary: truncateError(err)}
}

func buildCompletion(req Request, results []domain.ItemResult, completed time.Time, duration time.Duration) *BatchCompleted {
	payload := &BatchCompleted{
		BatchID: req.BatchID, ScheduleID: req.ScheduleID, BatchKind: string(req.BatchKind),
		DatasetID: req.DatasetID, Frequency: req.Frequency, Region: req.Region, NodeID: req.NodeID,
		RequestID: req.RequestID, PlannedCount: int32(len(results)), DurationMS: duration.Milliseconds(),
		CompletedAt: completed.UTC().UnixMilli(),
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
		item := ItemResult{
			SubjectID: result.SubjectID, Symbol: result.Symbol, TargetDataTime: result.TargetDataTime,
			Outcome: string(result.Outcome), ErrorType: result.ErrorType, ErrorSummary: result.ErrorSummary,
			SourceEventID: result.SourceEventID, InstanceID: result.InstanceID,
		}
		for _, target := range result.TargetResults {
			item.Targets = append(item.Targets, TargetResult{WriteTargetID: target.WriteTargetID, DatasetID: target.DatasetID, Status: target.Status, ErrorSummary: target.ErrorSummary})
		}
		payload.Items = append(payload.Items, item)
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

// InvocationResult 是同步调用一次执行端（云函数或本地执行端）的结果。
type InvocationResult struct {
	RequestID    string
	Code         int32
	Message      string
	Result       map[string]any
	DurationMS   int64
	BillDuration int64
}
