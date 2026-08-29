package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

const (
	ResampleAlignmentEpochUTC  = "epoch_utc"
	ResampleTaskSchemaVersion  = 1
	MaxResampleBackfillBuckets = 10080
)

// SettleDelay returns the rule-specific delay after a target bucket closes.
func (p *CollectParams) SettleDelay() time.Duration {
	return p.SettleDelayOr(0)
}

// SettleDelayOr applies the process-wide default when a rule leaves the
// optional delay unset. A caller may supply its configured default explicitly.
func (p *CollectParams) SettleDelayOr(defaultDelay time.Duration) time.Duration {
	if defaultDelay < 0 {
		defaultDelay = 0
	}
	if p == nil || p.SettleDelayMS <= 0 {
		return defaultDelay
	}
	return time.Duration(p.SettleDelayMS) * time.Millisecond
}

// CanonicalJSON persists the normalized public collect-params contract.
func (p *CollectParams) CanonicalJSON() (string, error) {
	if p == nil {
		return "", fmt.Errorf("collect params are required")
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("marshal collect params: %w", err)
	}
	return string(raw), nil
}

// ValidateSameResampleIdentity allows operational changes such as settle delay
// while locking every field that determines the source or target row identity.
func ValidateSameResampleIdentity(existing, desired *CollectParams) error {
	if existing == nil || desired == nil {
		return fmt.Errorf("resample collect params are required")
	}
	fields := []struct {
		name      string
		existing  string
		desired   string
		equalFold bool
	}{
		{name: "provider", existing: existing.Provider, desired: desired.Provider, equalFold: true},
		{name: "market_type", existing: existing.MarketType, desired: desired.MarketType, equalFold: true},
		{name: "source_dataset_id", existing: existing.SourceDatasetID, desired: desired.SourceDatasetID},
		{name: "source_frequency", existing: existing.SourceFrequency, desired: desired.SourceFrequency},
		{name: "source_series_tag", existing: existing.SourceSeriesTag, desired: desired.SourceSeriesTag},
		{name: "target_dataset_id", existing: existing.TargetDatasetID, desired: desired.TargetDatasetID},
		{name: "target_frequency", existing: existing.TargetFrequency, desired: desired.TargetFrequency},
		{name: "alignment", existing: existing.Alignment, desired: desired.Alignment},
	}
	for _, field := range fields {
		equal := field.existing == field.desired
		if field.equalFold {
			equal = strings.EqualFold(field.existing, field.desired)
		}
		if !equal {
			return fmt.Errorf("immutable resample field %s cannot change", field.name)
		}
	}
	return nil
}

// ValidateKlineResample validates the immutable resample rule contract.
func (p *CollectParams) ValidateKlineResample() error {
	if p.SourceDatasetID == "" {
		return fmt.Errorf("source_dataset_id is required")
	}
	if p.TargetDatasetID == "" {
		return fmt.Errorf("target_dataset_id is required")
	}
	if p.SourceDatasetID == p.TargetDatasetID {
		return fmt.Errorf("source and target Dataset must differ")
	}
	if p.SourceSeriesTag == "" {
		return fmt.Errorf("source_series_tag is required")
	}
	if p.Alignment != ResampleAlignmentEpochUTC {
		return fmt.Errorf("alignment must be epoch_utc")
	}
	if p.SettleDelayMS < 0 {
		return fmt.Errorf("settle_delay_ms must be non-negative")
	}
	source, err := parseFixedFrequencyDuration(p.SourceFrequency)
	if err != nil {
		return fmt.Errorf("source_frequency: %w", err)
	}
	target, err := parseFixedFrequencyDuration(p.TargetFrequency)
	if err != nil {
		return fmt.Errorf("target_frequency: %w", err)
	}
	if target <= source {
		return fmt.Errorf("target_frequency must be greater than source_frequency")
	}
	if target%source != 0 {
		return fmt.Errorf("target_frequency must be a multiple of source_frequency")
	}
	if target > 30*24*time.Hour {
		return fmt.Errorf("target_frequency must not exceed 30 days")
	}
	if target/source > 10080 {
		return fmt.Errorf("target bucket must not contain more than 10080 source bars")
	}
	if err := validateResampleTargetDatasetID(p.TargetDatasetID, normalizeFixedFrequency(p.TargetFrequency)); err != nil {
		return err
	}
	return nil
}

func validateResampleTargetDatasetID(datasetID, canonicalFrequency string) error {
	if len(datasetID) == 0 || len(datasetID) > 25 || !isLowerSnakeID(datasetID) {
		return fmt.Errorf("target_dataset_id must be lower snake case and no more than 25 characters")
	}
	suffix := "_" + strings.ToLower(canonicalFrequency)
	if !strings.HasSuffix(datasetID, suffix) {
		return fmt.Errorf("target_dataset_id must end with %s", suffix)
	}
	return nil
}

func isLowerSnakeID(value string) bool {
	if value == "" || value[0] < 'a' || value[0] > 'z' || value[len(value)-1] == '_' {
		return false
	}
	underscore := false
	for _, char := range value {
		if char == '_' {
			if underscore {
				return false
			}
			underscore = true
			continue
		}
		underscore = false
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') {
			return false
		}
	}
	return true
}

func normalizeFixedFrequency(raw string) string {
	duration, err := parseFixedFrequencyDuration(raw)
	if err != nil {
		return strings.TrimSpace(raw)
	}
	minutes := int64(duration / time.Minute)
	if minutes%(24*60) == 0 {
		return strconv.FormatInt(minutes/(24*60), 10) + "D"
	}
	if minutes%60 == 0 {
		return strconv.FormatInt(minutes/60, 10) + "H"
	}
	return strconv.FormatInt(minutes, 10) + "m"
}

func parseFixedFrequencyDuration(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) < 2 {
		return 0, fmt.Errorf("frequency must be a positive integer followed by m, h, or d")
	}
	unit := raw[len(raw)-1]
	if unit == 'M' || unit == 'W' || unit == 'Y' || unit == 'w' || unit == 'y' {
		return 0, fmt.Errorf("calendar frequency is not supported")
	}
	count, err := strconv.ParseInt(raw[:len(raw)-1], 10, 64)
	if err != nil || count <= 0 {
		return 0, fmt.Errorf("frequency must be a positive integer followed by m, h, or d")
	}
	var multiplier time.Duration
	switch unit {
	case 'm':
		multiplier = time.Minute
	case 'h', 'H':
		multiplier = time.Hour
	case 'd', 'D':
		multiplier = 24 * time.Hour
	default:
		return 0, fmt.Errorf("frequency must use m, h, or d")
	}
	if count > int64((30*24*time.Hour)/multiplier) {
		return time.Duration(count) * multiplier, nil
	}
	return time.Duration(count) * multiplier, nil
}

type ResampleTaskState string

const (
	ResampleTaskStateIdle          ResampleTaskState = "idle"
	ResampleTaskStateRunning       ResampleTaskState = "running"
	ResampleTaskStateWaitingSource ResampleTaskState = "waiting_source"
	ResampleTaskStateFailed        ResampleTaskState = "failed"
	ResampleTaskStateDisabled      ResampleTaskState = "disabled"
)

func (s ResampleTaskState) Valid() bool {
	switch s {
	case ResampleTaskStateIdle, ResampleTaskStateRunning, ResampleTaskStateWaitingSource, ResampleTaskStateFailed, ResampleTaskStateDisabled:
		return true
	default:
		return false
	}
}

type ResampleTaskOrigin string

const (
	ResampleOriginRealtime ResampleTaskOrigin = "realtime"
	ResampleOriginRepair   ResampleTaskOrigin = "repair"
	ResampleOriginBackfill ResampleTaskOrigin = "backfill"
)

func (o ResampleTaskOrigin) Valid() bool {
	return o == ResampleOriginRealtime || o == ResampleOriginRepair || o == ResampleOriginBackfill
}

type ResampleBackfillState string

const (
	ResampleBackfillRunning       ResampleBackfillState = "running"
	ResampleBackfillWaitingSource ResampleBackfillState = "waiting_source"
	ResampleBackfillSyncing       ResampleBackfillState = "syncing"
	ResampleBackfillComplete      ResampleBackfillState = "complete"
	ResampleBackfillCanceled      ResampleBackfillState = "canceled"
	ResampleBackfillFailed        ResampleBackfillState = "failed"
)

func (s ResampleBackfillState) Valid() bool {
	switch s {
	case ResampleBackfillRunning, ResampleBackfillWaitingSource, ResampleBackfillSyncing, ResampleBackfillComplete, ResampleBackfillCanceled, ResampleBackfillFailed:
		return true
	default:
		return false
	}
}

type ResampleBackfill struct {
	RequestID   string                `json:"request_id"`
	Start       time.Time             `json:"start"`
	End         time.Time             `json:"end"`
	NextBucket  time.Time             `json:"next_bucket"`
	State       ResampleBackfillState `json:"state"`
	NextRetryAt *time.Time            `json:"next_retry_at,omitempty"`
}

type ResampleBackfillRequest struct {
	RequestID string
	Start     time.Time
	End       time.Time
}

func (r ResampleBackfillRequest) Validate() error {
	if strings.TrimSpace(r.RequestID) == "" {
		return fmt.Errorf("backfill request_id is required")
	}
	if r.Start.IsZero() || r.End.IsZero() || !r.Start.Before(r.End) {
		return fmt.Errorf("backfill start must be before end")
	}
	return nil
}

// ValidateForFrequency additionally enforces the target bucket grid and a
// bounded historical request before it reaches the durable worker queue.
func (r ResampleBackfillRequest) ValidateForFrequency(target time.Duration) error {
	if err := r.Validate(); err != nil {
		return err
	}
	if target <= 0 || target%time.Minute != 0 {
		return fmt.Errorf("backfill target frequency must be a positive whole-minute duration")
	}
	start, end := r.Start.UTC(), r.End.UTC()
	if !IsEpochTimeAligned(start, target) || !IsEpochTimeAligned(end, target) {
		return fmt.Errorf("backfill start and end must align to target frequency")
	}
	if end.Sub(start)%target != 0 {
		return fmt.Errorf("backfill window must contain whole target periods")
	}
	if end.Sub(start)/target > MaxResampleBackfillBuckets {
		return fmt.Errorf("backfill window must not exceed %d target buckets", MaxResampleBackfillBuckets)
	}
	return nil
}

// IsEpochTimeAligned is the shared epoch-UTC grid check used by both domain
// validation and the resample runtime. Periods are whole minutes, so an exact
// duration remainder is sufficient and works consistently before/after epoch.
func IsEpochTimeAligned(at time.Time, period time.Duration) bool {
	if at.IsZero() || period <= 0 || period%time.Minute != 0 || at.Nanosecond() != 0 || at.Second() != 0 {
		return false
	}
	elapsed := at.UTC().Sub(time.Unix(0, 0).UTC())
	return elapsed%period == 0
}

type ResampleTaskResult struct {
	SchemaVersion      int                `json:"schema_version"`
	State              ResampleTaskState  `json:"state"`
	StateVersion       int64              `json:"state_version"`
	ActiveOrigin       ResampleTaskOrigin `json:"active_origin,omitempty"`
	ActiveBucket       *time.Time         `json:"active_bucket,omitempty"`
	LeaseUntil         *time.Time         `json:"lease_until,omitempty"`
	Attempt            int                `json:"attempt,omitempty"`
	NextRetryAt        *time.Time         `json:"next_retry_at,omitempty"`
	RealtimeNextBucket *time.Time         `json:"realtime_next_bucket,omitempty"`
	RepairNextBucket   *time.Time         `json:"repair_next_bucket,omitempty"`
	LastSuccessBucket  *time.Time         `json:"last_success_bucket,omitempty"`
	LastInputHash      string             `json:"last_input_hash,omitempty"`
	LastError          string             `json:"last_error,omitempty"`
	Backfill           *ResampleBackfill  `json:"backfill,omitempty"`
}

func NewResampleTaskResult(nextBucket time.Time) ResampleTaskResult {
	result := ResampleTaskResult{SchemaVersion: ResampleTaskSchemaVersion, State: ResampleTaskStateIdle}
	if !nextBucket.IsZero() {
		next := nextBucket.UTC()
		result.RealtimeNextBucket = &next
	}
	return result
}

func ParseResampleTaskResult(raw string) (ResampleTaskResult, error) {
	var result ResampleTaskResult
	decoder := json.NewDecoder(bytes.NewBufferString(strings.TrimSpace(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return result, fmt.Errorf("parse resample task result: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return result, fmt.Errorf("parse resample task result: trailing JSON value")
	}
	result.normalizeTimes()
	if err := result.Validate(); err != nil {
		return result, err
	}
	return result, nil
}

func (r *ResampleTaskResult) Validate() error {
	if r.SchemaVersion != ResampleTaskSchemaVersion {
		return fmt.Errorf("unsupported resample task schema_version: %d", r.SchemaVersion)
	}
	if !r.State.Valid() {
		return fmt.Errorf("invalid resample task state: %s", r.State)
	}
	if r.StateVersion < 0 {
		return fmt.Errorf("state_version must be non-negative")
	}
	if r.Attempt < 0 {
		return fmt.Errorf("attempt must be non-negative")
	}
	if r.ActiveOrigin != "" && !r.ActiveOrigin.Valid() {
		return fmt.Errorf("invalid active_origin: %s", r.ActiveOrigin)
	}
	if r.Backfill != nil {
		if strings.TrimSpace(r.Backfill.RequestID) == "" {
			return fmt.Errorf("backfill.request_id is required")
		}
		if r.Backfill.Start.IsZero() || r.Backfill.End.IsZero() || r.Backfill.NextBucket.IsZero() {
			return fmt.Errorf("backfill start, end and next_bucket are required")
		}
		if !r.Backfill.Start.Before(r.Backfill.End) {
			return fmt.Errorf("backfill start must be before end")
		}
		if !r.Backfill.State.Valid() {
			return fmt.Errorf("invalid backfill state: %s", r.Backfill.State)
		}
	}
	return nil
}

func (r *ResampleTaskResult) Marshal() (string, error) {
	if r == nil {
		return "", fmt.Errorf("resample task result is required")
	}
	r.normalizeTimes()
	if err := r.Validate(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return "", fmt.Errorf("marshal resample task result: %w", err)
	}
	return string(raw), nil
}

func (r *ResampleTaskResult) normalizeTimes() {
	normalizePtr := func(value **time.Time) {
		if *value != nil {
			utc := (*value).UTC()
			*value = &utc
		}
	}
	normalizePtr(&r.ActiveBucket)
	normalizePtr(&r.LeaseUntil)
	normalizePtr(&r.NextRetryAt)
	normalizePtr(&r.RealtimeNextBucket)
	normalizePtr(&r.RepairNextBucket)
	normalizePtr(&r.LastSuccessBucket)
	if r.Backfill != nil {
		r.Backfill.Start = r.Backfill.Start.UTC()
		r.Backfill.End = r.Backfill.End.UTC()
		r.Backfill.NextBucket = r.Backfill.NextBucket.UTC()
		normalizePtr(&r.Backfill.NextRetryAt)
	}
}
