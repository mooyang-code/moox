package domain

import (
	"time"
)

type BatchKind string

const (
	BatchKindRealtime  BatchKind = "realtime"
	BatchKindCatchup   BatchKind = "catchup"
	BatchKindBackfill  BatchKind = "backfill"
	BatchKindGapRepair BatchKind = "gap_repair"
)

type BatchStatus string

const (
	BatchStatusPlanned       BatchStatus = "planned"
	BatchStatusDispatched    BatchStatus = "dispatched"
	BatchStatusSucceeded     BatchStatus = "succeeded"
	BatchStatusPartialFailed BatchStatus = "partial_failed"
	BatchStatusFailed        BatchStatus = "failed"
	BatchStatusTimedOut      BatchStatus = "timed_out"
)

type ItemOutcome string

const (
	ItemOutcomeSuccess      ItemOutcome = "success"
	ItemOutcomeHTTP429      ItemOutcome = "http_429"
	ItemOutcomeHTTP5xx      ItemOutcome = "http_5xx"
	ItemOutcomeNetworkError ItemOutcome = "network_error"
	ItemOutcomeStorageError ItemOutcome = "storage_error"
	// ItemOutcomeProviderError means the selected upstream returned a malformed,
	// incomplete, or otherwise unusable response. It is retryable so a later
	// retry window can advance to the next configured provider candidate.
	ItemOutcomeProviderError ItemOutcome = "provider_error"
	ItemOutcomeInvalid       ItemOutcome = "invalid_request"
)

type CollectionItem struct {
	// InstanceID is the stable TaskInstance identity.
	InstanceID     string `json:"instance_id,omitempty"`
	SubjectID      string `json:"subject_id"`
	Symbol         string `json:"symbol"`
	TargetDataTime string `json:"target_data_time,omitempty"`
	StartTime      string `json:"start_time,omitempty"`
	EndTime        string `json:"end_time,omitempty"`
	SnapshotAt     string `json:"snapshot_at,omitempty"`
	BarLimit       int    `json:"bar_limit,omitempty"`
	// Canary marks an operator/deployment probe. Stock canaries use the
	// bundled trading calendar to resolve the latest closed session rather
	// than treating a weekend or holiday as a missing-data failure.
	Canary bool `json:"canary,omitempty"`
	// RequirePeriodCommit is carried into durable retry records so retries keep
	// the same Storage commit contract as their source batch.
	RequirePeriodCommit bool `json:"require_period_commit,omitempty"`
	// CandidateIndex is advanced when a retry has already exhausted the
	// current provider window. Keeping it on the item makes retries resume at
	// the next candidate instead of repeatedly starting at the same bad feed.
	CandidateIndex  int     `json:"candidate_index,omitempty"`
	RateBudgetRatio float64 `json:"rate_budget_ratio,omitempty"`
	SourceEventID   string  `json:"source_event_id,omitempty"`
	Provider        string  `json:"provider"`
	SourceID        string  `json:"source_id,omitempty"`
	MarketID        string  `json:"market_id,omitempty"`
	InstrumentType  string  `json:"instrument_type,omitempty"`
	MarketType      string  `json:"market_type"`
	DataType        string  `json:"data_type"`
	DatasetID       string  `json:"dataset_id"`
	Frequency       string  `json:"frequency,omitempty"`
	// OutputFields limits the fields persisted for each fetched row. An empty
	// list writes every normalized field.
	OutputFields       []string `json:"output_fields,omitempty"`
	SnapshotShardIndex int      `json:"snapshot_shard_index,omitempty"`
	SnapshotShardCount int      `json:"snapshot_shard_count,omitempty"`
	// Series identity belongs to the Dataset/WriteTarget side. These fields are
	// populated while materializing one task's Tag union and are deliberately
	// excluded from Provider request_key calculation.
	SeriesIndex   uint32 `json:"series_index,omitempty"`
	SeriesHash    string `json:"series_hash,omitempty"`
	ExpectedCount uint32 `json:"expected_count,omitempty"`
	// PeriodReservationID fences deployment canary writes from other publishers
	// while the target Storage period is waiting.
	PeriodReservationID string `json:"period_reservation_id,omitempty"`
}

type ItemResult struct {
	CollectionItem
	Outcome       ItemOutcome    `json:"outcome"`
	ErrorType     string         `json:"error_type,omitempty"`
	ErrorSummary  string         `json:"error_summary,omitempty"`
	TargetResults []TargetResult `json:"target_results,omitempty"`
}

type TargetResult struct{ WriteTargetID, DatasetID, Status, ErrorSummary string }

type PeriodFailureReportState string

const (
	PeriodFailureReportPending        PeriodFailureReportState = "pending"
	PeriodFailureReportAcknowledged   PeriodFailureReportState = "acknowledged"
	PeriodFailureReportMissedDeadline PeriodFailureReportState = "missed_deadline"
)

type PeriodFailureTargetResult struct {
	WriteTargetID string    `json:"write_target_id"`
	SpaceID       string    `json:"space_id"`
	DatasetID     string    `json:"dataset_id"`
	Frequency     string    `json:"frequency"`
	PeriodTime    time.Time `json:"period_time"`
	SeriesHash    string    `json:"series_hash"`
	ExpectedCount uint32    `json:"expected_count"`
	SeriesIndex   uint32    `json:"series_index"`
	Disposition   string    `json:"disposition"`
	ObservedAt    time.Time `json:"observed_at"`
}
