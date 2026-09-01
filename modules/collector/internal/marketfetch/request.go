package marketfetch

import (
	"context"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

const (
	// MaxRealtimeItems is the maximum number of subjects assigned to one SCF
	// invocation by the scheduler.
	MaxRealtimeItems   = 30
	DefaultConcurrency = 5
)

// Request is the durable scheduler batch shape. The market_data SCF has its
// own public request contract; this type remains only for scheduler state,
// retry reconstruction, and completion bookkeeping.
type Request struct {
	BatchID string `json:"batch_id"`
	// SourceEventID identifies the payload for Storage idempotency. Retries use
	// the original retry key while BatchID remains the new completion correlation.
	SourceEventID  string                           `json:"source_event_id,omitempty"`
	SyncPointID    string                           `json:"sync_point_id,omitempty"`
	ScheduleID     string                           `json:"schedule_id,omitempty"`
	BatchKind      domain.BatchKind                 `json:"batch_kind"`
	SpaceID        string                           `json:"space_id"`
	DatasetID      string                           `json:"dataset_id,omitempty"`
	Frequency      string                           `json:"frequency,omitempty"`
	Provider       string                           `json:"provider"`
	MarketType     string                           `json:"market_type"`
	MarketID       string                           `json:"market_id,omitempty"`
	InstrumentType string                           `json:"instrument_type,omitempty"`
	SourceID       string                           `json:"source_id,omitempty"`
	SeriesTag      string                           `json:"series_tag,omitempty"`
	Region         string                           `json:"region"`
	NodeID         string                           `json:"node_id"`
	FunctionName   string                           `json:"function_name,omitempty"`
	RequestID      string                           `json:"request_id,omitempty"`
	ShardIndex     int                              `json:"shard_index,omitempty"`
	DNSRoutes      map[string]sources.DNSResolution `json:"dns_routes,omitempty"`
	Items          []domain.CollectionItem          `json:"items"`
}

// Storage is the narrow metadata and row-write boundary shared by K-line and
// instrument pipelines.
type Storage interface {
	UpsertFields(context.Context, []*storagepb.RowFieldUpsert) error
	RegisterDataSubject(context.Context, *storagepb.RegisterDataSubjectReq) error
}

type sourceStorage interface {
	UpsertFieldsWithSource(context.Context, []*storagepb.RowFieldUpsert, string) error
}
