package domain

type BatchKind string

const (
	BatchKindRealtime BatchKind = "realtime"
	BatchKindBackfill BatchKind = "backfill"
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
	TargetDataTime string `json:"target_data_time,omitempty"`
	StartTime      string `json:"start_time,omitempty"`
	EndTime        string `json:"end_time,omitempty"`
	BarLimit       int    `json:"bar_limit,omitempty"`
	// CandidateIndex is advanced when a retry has already exhausted the
	// current provider window. Keeping it on the item makes retries resume at
	// the next candidate instead of repeatedly starting at the same bad feed.
	CandidateIndex int    `json:"candidate_index,omitempty"`
	Provider       string `json:"provider"`
	SourceID       string `json:"source_id,omitempty"`
	MarketID       string `json:"market_id,omitempty"`
	InstrumentType string `json:"instrument_type,omitempty"`
	MarketType     string `json:"market_type"`
	DataType       string `json:"data_type"`
	DatasetID      string `json:"dataset_id"`
	Frequency      string `json:"frequency,omitempty"`
	// OutputFields limits the fields persisted for each fetched row. An empty
	// list writes every normalized field.
	OutputFields []string `json:"output_fields,omitempty"`
}

type ItemResult struct {
	CollectionItem
	Outcome       ItemOutcome    `json:"outcome"`
	ErrorType     string         `json:"error_type,omitempty"`
	ErrorSummary  string         `json:"error_summary,omitempty"`
	TargetResults []TargetResult `json:"target_results,omitempty"`
}

type TargetResult struct{ WriteTargetID, DatasetID, Status, ErrorSummary string }
