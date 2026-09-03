package marketfetch

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/clsreporter"
)

const (
	DefaultConcurrency = 5
	MaxConcurrency     = 64
	// MaxRealtimeItems bounds the work accepted by one short-lived SCF. The
	// assignment planner keeps each non-stock function at or below this limit.
	MaxRealtimeItems = 30
	MaxRealtimeRows  = 3
)

// Request is the JSON payload accepted by a market_fetch SCF invocation.
// Fetching and persistence are implemented by the common market pipelines;
// this type only carries the bounded invocation contract.
type Request struct {
	BatchID string `json:"batch_id"`
	// SyncPointID is the stable logical catchup fence identity. Retry batches
	// get a new BatchID for outbox/write idempotency, but keep the same fence.
	SyncPointID    string                           `json:"sync_point_id,omitempty"`
	ScheduleID     string                           `json:"schedule_id,omitempty"`
	BatchKind      domain.BatchKind                 `json:"batch_kind"`
	SpaceID        string                           `json:"space_id"`
	MarketID       string                           `json:"market_id,omitempty"`
	InstrumentType string                           `json:"instrument_type,omitempty"`
	DatasetID      string                           `json:"dataset_id,omitempty"`
	Frequency      string                           `json:"frequency,omitempty"`
	Provider       string                           `json:"provider"`
	SourceID       string                           `json:"source_id,omitempty"`
	MarketType     string                           `json:"market_type"`
	Region         string                           `json:"region"`
	NodeID         string                           `json:"node_id"`
	FunctionName   string                           `json:"function_name,omitempty"`
	RequestID      string                           `json:"request_id,omitempty"`
	ShardIndex     int                              `json:"shard_index,omitempty"`
	GroupID        int                              `json:"group_id,omitempty"`
	GroupCount     int                              `json:"group_count,omitempty"`
	Concurrency    int                              `json:"concurrency,omitempty"`
	DNSRoutes      map[string]sources.DNSResolution `json:"dns_routes,omitempty"`
	Items          []domain.CollectionItem          `json:"items"`
}

func (r *Request) validate() error {
	if r == nil {
		return fmt.Errorf("request is required")
	}
	if strings.TrimSpace(r.BatchID) == "" || strings.TrimSpace(r.SpaceID) == "" {
		return fmt.Errorf("batch_id and space_id are required")
	}
	if len(r.Items) == 0 {
		return fmt.Errorf("items must not be empty")
	}
	r.DatasetID = strings.TrimSpace(r.DatasetID)
	if r.DatasetID == "" {
		return fmt.Errorf("dataset_id is required")
	}
	if r.BatchKind == "" {
		r.BatchKind = domain.BatchKindRealtime
	}
	maxItems := MaxRealtimeItems
	switch r.BatchKind {
	case domain.BatchKindRealtime:
	case domain.BatchKindCatchup, domain.BatchKindBackfill, domain.BatchKindGapRepair, domain.BatchKindInstrumentSnapshot:
		maxItems = 1
	default:
		return fmt.Errorf("unsupported batch_kind %q", r.BatchKind)
	}
	if len(r.Items) > maxItems && !(r.BatchKind == domain.BatchKindRealtime && strings.EqualFold(r.SpaceID, StockCNSpaceID)) {
		return fmt.Errorf("items exceed maximum batch size %d for %s", maxItems, r.BatchKind)
	}
	seenTaskIDs := make(map[string]struct{}, len(r.Items))
	for index, item := range r.Items {
		if r.BatchKind != domain.BatchKindInstrumentSnapshot && (strings.TrimSpace(item.SubjectID) == "" || strings.TrimSpace(item.Symbol) == "") {
			return fmt.Errorf("items[%d] subject_id and symbol are required", index)
		}
		if provider := strings.TrimSpace(r.Provider); provider != "" && strings.TrimSpace(item.Provider) != "" && !strings.EqualFold(provider, strings.TrimSpace(item.Provider)) {
			return fmt.Errorf("items[%d] source binding provider %q differs from batch provider %q", index, item.Provider, provider)
		}
		if sourceID := strings.TrimSpace(r.SourceID); sourceID != "" && strings.TrimSpace(item.SourceID) != "" && !strings.EqualFold(sourceID, strings.TrimSpace(item.SourceID)) {
			return fmt.Errorf("items[%d] source binding source_id %q differs from batch source_id %q", index, item.SourceID, sourceID)
		}
		if strings.TrimSpace(item.DatasetID) != r.DatasetID {
			return fmt.Errorf("items[%d] dataset_id differs from batch dataset", index)
		}
		if strings.TrimSpace(item.TaskID) != "" {
			if _, exists := seenTaskIDs[item.TaskID]; exists {
				return fmt.Errorf("items[%d] task_id %q is duplicated", index, item.TaskID)
			}
			seenTaskIDs[item.TaskID] = struct{}{}
		}
		start, startErr := parseRequestTime(item.StartTime)
		if startErr != nil {
			return fmt.Errorf("items[%d] start_time is invalid: %w", index, startErr)
		}
		end, endErr := parseRequestTime(item.EndTime)
		if endErr != nil {
			return fmt.Errorf("items[%d] end_time is invalid: %w", index, endErr)
		}
		if !start.IsZero() && !end.IsZero() && !end.After(start) {
			return fmt.Errorf("items[%d] end_time must be after start_time", index)
		}
		if r.BatchKind == domain.BatchKindInstrumentSnapshot {
			if _, snapshotErr := parseRequestTime(item.SnapshotAt); snapshotErr != nil {
				return fmt.Errorf("items[%d] snapshot_at is invalid: %w", index, snapshotErr)
			}
		} else if item.CandidateIndex < 0 {
			return fmt.Errorf("items[%d] candidate_index must not be negative", index)
		}
		if math.IsNaN(item.RateBudgetRatio) || math.IsInf(item.RateBudgetRatio, 0) || item.RateBudgetRatio < 0 || item.RateBudgetRatio > 1 {
			return fmt.Errorf("items[%d] rate_budget_ratio must be between 0 and 1", index)
		}
		if isHistoricalBatchKind(r.BatchKind) {
			if strings.TrimSpace(item.StartTime) == "" || item.BarLimit <= 0 || item.BarLimit > 1000 {
				return fmt.Errorf("historical item requires start_time and bar_limit 1..1000")
			}
		} else if r.BatchKind == domain.BatchKindRealtime && item.BarLimit > MaxRealtimeRows {
			return fmt.Errorf("realtime bar_limit must be between 1 and %d", MaxRealtimeRows)
		}
	}
	if r.Concurrency < 0 || r.Concurrency > MaxConcurrency {
		return fmt.Errorf("concurrency must be between 0 and %d", MaxConcurrency)
	}
	return nil
}

func parseRequestTime(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("must be RFC3339Nano: %w", err)
	}
	return parsed, nil
}

// Storage is the write boundary shared by the market pipelines.
type Storage interface {
	UpsertFields(context.Context, []*storagepb.RowFieldUpsert) error
	RegisterDataSubject(context.Context, *storagepb.RegisterDataSubjectReq) error
}

// StorageReader adds the bounded reads needed by scheduling and gap audit.
type StorageReader interface {
	Storage
	LatestTimeSeriesTime(context.Context, *storagepb.TimeSeriesSelector) (time.Time, bool, error)
	ReadTimeSeriesRows(context.Context, *storagepb.ReadTimeSeriesRowsReq) (*storagepb.ReadTimeSeriesRowsRsp, error)
	ListDatasetSubjects(context.Context, string, string) ([]*storagepb.DatasetSubject, error)
}

type sourceStorage interface {
	UpsertFieldsWithSource(context.Context, []*storagepb.RowFieldUpsert, string) error
}

// ItemReporter receives final per-item outcomes from the common invocation
// handler. It is deliberately small so CLS remains an optional boundary.
type ItemReporter interface{ Report(clsreporter.Entry) }
