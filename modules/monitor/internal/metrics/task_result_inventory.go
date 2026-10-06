package metrics

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/codec"
)

const (
	maxCollectorTaskResultInventoryPage   = 100
	maxCollectorTaskResultInventory       = 1000
	maxCollectorTaskResultInventorySpaces = 16
)

// TaskResultInventoryEntry is a Collector-owned result identity, validated
// against Storage metadata before it is admitted to Monitor's active snapshot.
type TaskResultInventoryEntry struct {
	SpaceID               string
	TaskID                string
	DatasetID             string
	ViewID                string
	Frequency             string
	MarketID              string
	CalendarID            string
	Timezone              string
	Sessions              []string
	LatestCompletedPeriod time.Time
	LatestCompletedStatus string
	ViewLastDataTime      time.Time
	ObservedAt            time.Time
	Enabled               bool
	OwnershipVerified     bool
	ResultStatus          string
}

type TaskResultInventorySnapshot struct {
	ID         string
	ObservedAt time.Time
	Entries    []TaskResultInventoryEntry
}

type TaskResultInventoryProvider interface {
	FetchTaskResultInventory(context.Context) (TaskResultInventorySnapshot, error)
}

type collectorTaskResultInventoryClient interface {
	GetTaskResultInventory(context.Context, *collectorpb.GetTaskResultInventoryReq, ...client.Option) (*collectorpb.GetTaskResultInventoryRsp, error)
}

// CollectorTaskResultInventorySource pages one stable snapshot per configured
// Space and rejects partial or mixed-generation inventories.
type CollectorTaskResultInventorySource struct {
	client   collectorTaskResultInventoryClient
	spaceIDs []string
	pageSize int
	maxItems int
}

func NewCollectorTaskResultInventorySource(client collectorTaskResultInventoryClient, spaceIDs []string, pageSize, maxItems int) (*CollectorTaskResultInventorySource, error) {
	if client == nil {
		return nil, errors.New("collector task-result inventory client is required")
	}
	if len(spaceIDs) == 0 || len(spaceIDs) > maxCollectorTaskResultInventorySpaces {
		return nil, fmt.Errorf("collector inventory spaces must contain between 1 and %d Space IDs", maxCollectorTaskResultInventorySpaces)
	}
	normalizedSpaces := make([]string, 0, len(spaceIDs))
	seenSpaces := make(map[string]struct{}, len(spaceIDs))
	for _, spaceID := range spaceIDs {
		spaceID = strings.TrimSpace(spaceID)
		if spaceID == "" {
			return nil, errors.New("collector inventory Space IDs must not be empty")
		}
		if _, exists := seenSpaces[spaceID]; exists {
			return nil, fmt.Errorf("collector inventory Space %q is duplicated", spaceID)
		}
		seenSpaces[spaceID] = struct{}{}
		normalizedSpaces = append(normalizedSpaces, spaceID)
	}
	if pageSize <= 0 || pageSize > maxCollectorTaskResultInventoryPage {
		return nil, fmt.Errorf("collector inventory page size must be between 1 and %d", maxCollectorTaskResultInventoryPage)
	}
	if maxItems <= 0 || maxItems > maxCollectorTaskResultInventory {
		return nil, fmt.Errorf("collector inventory max entries must be between 1 and %d", maxCollectorTaskResultInventory)
	}
	return &CollectorTaskResultInventorySource{client: client, spaceIDs: normalizedSpaces, pageSize: pageSize, maxItems: maxItems}, nil
}

func (s *CollectorTaskResultInventorySource) FetchTaskResultInventory(ctx context.Context) (TaskResultInventorySnapshot, error) {
	if s == nil || s.client == nil || len(s.spaceIDs) == 0 {
		return TaskResultInventorySnapshot{}, errors.New("collector task-result inventory client is unavailable")
	}
	snapshot := TaskResultInventorySnapshot{Entries: make([]TaskResultInventoryEntry, 0)}
	snapshotParts := make([]string, 0, len(s.spaceIDs))
	for _, spaceID := range s.spaceIDs {
		if err := ctx.Err(); err != nil {
			return TaskResultInventorySnapshot{}, err
		}
		spaceSnapshot, err := s.fetchSpace(ctx, spaceID, s.maxItems-len(snapshot.Entries))
		if err != nil {
			return TaskResultInventorySnapshot{}, fmt.Errorf("Space %s: %w", spaceID, err)
		}
		snapshot.Entries = append(snapshot.Entries, spaceSnapshot.Entries...)
		snapshotParts = append(snapshotParts, spaceID+":"+spaceSnapshot.ID)
		if snapshot.ObservedAt.IsZero() || spaceSnapshot.ObservedAt.Before(snapshot.ObservedAt) {
			snapshot.ObservedAt = spaceSnapshot.ObservedAt
		}
	}
	checksum := sha256.Sum256([]byte(strings.Join(snapshotParts, "\n")))
	snapshot.ID = hex.EncodeToString(checksum[:])
	return snapshot, nil
}

func (s *CollectorTaskResultInventorySource) fetchSpace(ctx context.Context, spaceID string, maxItems int) (TaskResultInventorySnapshot, error) {
	var snapshot TaskResultInventorySnapshot
	if maxItems < 0 {
		return snapshot, fmt.Errorf("combined collector inventory exceeds limit %d", s.maxItems)
	}
	var expectedTotal int
	var snapshotID string
	var observedAt string
	for page := 1; ; page++ {
		if page > maxItems/s.pageSize+2 {
			return TaskResultInventorySnapshot{}, fmt.Errorf("collector inventory exceeded page bound %d", maxItems)
		}
		req := &collectorpb.GetTaskResultInventoryReq{
			SpaceId: spaceID, Page: &commonpb.Page{Page: uint32(page), Size: uint32(s.pageSize)}, SnapshotId: snapshotID,
		}
		rpcCtx := contextWithInventorySpaceID(ctx, spaceID)
		rsp, err := s.client.GetTaskResultInventory(rpcCtx, req)
		if err != nil {
			return TaskResultInventorySnapshot{}, fmt.Errorf("fetch collector task-result inventory page %d: %w", page, err)
		}
		if rsp == nil || rsp.GetRetInfo() == nil || rsp.GetRetInfo().GetCode() != collectorpb.ErrorCode_SUCCESS {
			message := "empty response"
			if rsp != nil && rsp.GetRetInfo() != nil {
				message = fmt.Sprintf("code=%s msg=%s", rsp.GetRetInfo().GetCode().String(), rsp.GetRetInfo().GetMsg())
			}
			return TaskResultInventorySnapshot{}, fmt.Errorf("collector inventory page %d failed: %s", page, message)
		}
		pageInfo := rsp.GetPage()
		if pageInfo == nil {
			return TaskResultInventorySnapshot{}, fmt.Errorf("collector inventory page %d omitted pagination metadata", page)
		}
		if pageInfo.GetPage() != uint32(page) || pageInfo.GetSize() != uint32(s.pageSize) {
			return TaskResultInventorySnapshot{}, fmt.Errorf("collector inventory page metadata mismatch on page %d", page)
		}
		if page == 1 {
			if rsp.GetSnapshotId() == "" {
				return TaskResultInventorySnapshot{}, errors.New("collector inventory omitted snapshot_id")
			}
			if pageInfo.GetTotal() > uint32(maxItems) {
				return TaskResultInventorySnapshot{}, fmt.Errorf("collector inventory total %d exceeds limit %d", pageInfo.GetTotal(), maxItems)
			}
			snapshotID, expectedTotal, observedAt = rsp.GetSnapshotId(), int(pageInfo.GetTotal()), rsp.GetObservedAt()
			snapshot.ID = snapshotID
			parsed, err := parseInventoryTimestamp(observedAt)
			if err != nil {
				return TaskResultInventorySnapshot{}, fmt.Errorf("collector inventory observed_at: %w", err)
			}
			snapshot.ObservedAt = parsed
		} else if rsp.GetSnapshotId() != snapshotID || int(pageInfo.GetTotal()) != expectedTotal || rsp.GetObservedAt() != observedAt {
			return TaskResultInventorySnapshot{}, fmt.Errorf("collector inventory snapshot changed while paging at page %d", page)
		}
		for _, raw := range rsp.GetEntries() {
			entry, err := taskResultInventoryEntryFromProto(raw)
			if err != nil {
				return TaskResultInventorySnapshot{}, fmt.Errorf("collector inventory page %d: %w", page, err)
			}
			if entry.SpaceID != spaceID {
				return TaskResultInventorySnapshot{}, fmt.Errorf("collector inventory returned task from Space %q for requested Space %q", entry.SpaceID, spaceID)
			}
			snapshot.Entries = append(snapshot.Entries, entry)
		}
		if len(snapshot.Entries) > maxItems {
			return TaskResultInventorySnapshot{}, fmt.Errorf("collector inventory exceeds limit %d", maxItems)
		}
		if pageInfo.GetHasMore() {
			if len(rsp.GetEntries()) == 0 || len(snapshot.Entries) >= expectedTotal {
				return TaskResultInventorySnapshot{}, fmt.Errorf("collector inventory page %d has invalid has_more state", page)
			}
			continue
		}
		if len(snapshot.Entries) != expectedTotal {
			return TaskResultInventorySnapshot{}, fmt.Errorf("collector inventory is incomplete: got %d of %d entries", len(snapshot.Entries), expectedTotal)
		}
		return snapshot, nil
	}
}

func contextWithInventorySpaceID(ctx context.Context, spaceID string) context.Context {
	ctx, message := codec.WithCloneMessage(ctx)
	metadata := message.ClientMetaData().Clone()
	if metadata == nil {
		metadata = codec.MetaData{}
	}
	metadata["space_id"] = []byte(spaceID)
	message.WithClientMetaData(metadata)
	return ctx
}

func taskResultInventoryEntryFromProto(raw *collectorpb.TaskResultInventoryEntry) (TaskResultInventoryEntry, error) {
	if raw == nil {
		return TaskResultInventoryEntry{}, errors.New("entry is nil")
	}
	entry := TaskResultInventoryEntry{
		SpaceID: strings.TrimSpace(raw.GetSpaceId()), TaskID: strings.TrimSpace(raw.GetTaskId()),
		DatasetID: strings.TrimSpace(raw.GetDatasetId()), ViewID: strings.TrimSpace(raw.GetViewId()),
		Frequency: strings.TrimSpace(raw.GetFrequency()), MarketID: strings.TrimSpace(raw.GetMarketId()),
		CalendarID: strings.TrimSpace(raw.GetCalendarId()), Timezone: strings.TrimSpace(raw.GetTimezone()),
		Sessions: append([]string(nil), raw.GetSessions()...), LatestCompletedStatus: strings.TrimSpace(raw.GetLatestCompletedStatus()),
		Enabled: raw.GetEnabled(), OwnershipVerified: raw.GetOwnershipVerified(), ResultStatus: strings.TrimSpace(raw.GetResultStatus()),
	}
	var err error
	if entry.ObservedAt, err = parseInventoryTimestamp(raw.GetObservedAt()); err != nil {
		return TaskResultInventoryEntry{}, fmt.Errorf("invalid entry observed_at: %w", err)
	}
	if raw.GetLatestCompletedPeriod() != "" {
		if entry.LatestCompletedPeriod, err = parseInventoryTimestamp(raw.GetLatestCompletedPeriod()); err != nil {
			return TaskResultInventoryEntry{}, fmt.Errorf("invalid latest_completed_period: %w", err)
		}
	}
	if raw.GetViewLastDataTime() != "" {
		if entry.ViewLastDataTime, err = parseInventoryTimestamp(raw.GetViewLastDataTime()); err != nil {
			return TaskResultInventoryEntry{}, fmt.Errorf("invalid view_last_data_time: %w", err)
		}
	}
	return entry, nil
}

func parseInventoryTimestamp(raw string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(raw))
	if err != nil || parsed.IsZero() {
		if err == nil {
			err = errors.New("timestamp is zero")
		}
		return time.Time{}, err
	}
	return parsed.UTC(), nil
}

type TaskResultInventoryCache struct {
	provider    TaskResultInventoryProvider
	ttl         time.Duration
	maxItems    int
	now         func() time.Time
	refresh     sync.Mutex
	mu          sync.RWMutex
	snapshot    TaskResultInventorySnapshot
	fetched     time.Time
	lastAttempt time.Time
	lastError   string
}

type TaskResultInventoryCacheState struct {
	LastAttempt time.Time
	LastSuccess time.Time
	Age         time.Duration
	Available   bool
	LastError   string
}

type TaskResultInventoryRefreshError struct {
	Cause error
	State TaskResultInventoryCacheState
}

func (e *TaskResultInventoryRefreshError) Error() string {
	if e == nil || e.Cause == nil {
		return "task-result inventory refresh failed"
	}
	return "task-result inventory refresh failed: " + e.Cause.Error()
}

func (e *TaskResultInventoryRefreshError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func NewTaskResultInventoryCache(provider TaskResultInventoryProvider, ttl time.Duration, maxItems int) (*TaskResultInventoryCache, error) {
	if provider == nil {
		return nil, errors.New("task-result inventory provider is required")
	}
	if ttl <= 0 || ttl > 10*time.Minute {
		return nil, errors.New("task-result inventory refresh TTL must be between 0 and 10m")
	}
	if maxItems <= 0 || maxItems > maxCollectorTaskResultInventory {
		return nil, fmt.Errorf("task-result inventory max entries must be between 1 and %d", maxCollectorTaskResultInventory)
	}
	return &TaskResultInventoryCache{provider: provider, ttl: ttl, maxItems: maxItems, now: time.Now}, nil
}

func (c *TaskResultInventoryCache) Get(ctx context.Context, nowValues ...time.Time) (TaskResultInventorySnapshot, error) {
	if c == nil {
		return TaskResultInventorySnapshot{}, errors.New("task-result inventory cache is unavailable")
	}
	now := time.Time{}
	if len(nowValues) > 0 {
		now = nowValues[0]
	}
	if now.IsZero() {
		now = c.now()
	}
	now = now.UTC()
	if snapshot, ok := c.freshSnapshot(now); ok {
		observeTaskResultInventoryCache(c.State(now))
		return snapshot, nil
	}
	c.refresh.Lock()
	defer c.refresh.Unlock()
	if snapshot, ok := c.freshSnapshot(now); ok {
		observeTaskResultInventoryCache(c.State(now))
		return snapshot, nil
	}
	snapshot, err := c.provider.FetchTaskResultInventory(ctx)
	if err != nil {
		c.recordRefresh(now, TaskResultInventorySnapshot{}, err)
		return TaskResultInventorySnapshot{}, err
	}
	if err := validateTaskResultInventorySnapshot(snapshot, c.maxItems); err != nil {
		c.recordRefresh(now, TaskResultInventorySnapshot{}, err)
		return TaskResultInventorySnapshot{}, err
	}
	c.recordRefresh(now, snapshot, nil)
	return cloneTaskResultInventorySnapshot(snapshot), nil
}

func (c *TaskResultInventoryCache) recordRefresh(now time.Time, snapshot TaskResultInventorySnapshot, refreshErr error) {
	c.mu.Lock()
	c.lastAttempt = now
	if refreshErr == nil {
		c.snapshot = cloneTaskResultInventorySnapshot(snapshot)
		c.fetched = now
		c.lastError = ""
	} else {
		c.lastError = refreshErr.Error()
	}
	state := c.stateLocked(now)
	c.mu.Unlock()
	result := "success"
	if refreshErr != nil {
		result = "error"
	}
	recordTaskResultInventoryRefresh(result, state)
}

func (c *TaskResultInventoryCache) State(now time.Time) TaskResultInventoryCacheState {
	if c == nil {
		return TaskResultInventoryCacheState{LastError: "task-result inventory cache is unavailable"}
	}
	if now.IsZero() {
		now = c.now()
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.stateLocked(now.UTC())
}

func (c *TaskResultInventoryCache) stateLocked(now time.Time) TaskResultInventoryCacheState {
	state := TaskResultInventoryCacheState{LastAttempt: c.lastAttempt, LastSuccess: c.fetched, LastError: c.lastError}
	if c.fetched.IsZero() {
		return state
	}
	state.Age = now.UTC().Sub(c.fetched)
	state.Available = state.Age >= 0 && state.Age < c.ttl
	return state
}

func (c *TaskResultInventoryCache) freshSnapshot(now time.Time) (TaskResultInventorySnapshot, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	age := now.UTC().Sub(c.fetched)
	if c.snapshot.ID == "" || age < 0 || age >= c.ttl {
		return TaskResultInventorySnapshot{}, false
	}
	return cloneTaskResultInventorySnapshot(c.snapshot), true
}

// Current returns the latest complete cache snapshot only while it remains
// within its configured TTL. Ingestion uses this non-blocking view to avoid
// storing per-subject metrics before a Collector inventory has been verified.
func (c *TaskResultInventoryCache) Current(now time.Time) (TaskResultInventorySnapshot, bool) {
	if c == nil {
		return TaskResultInventorySnapshot{}, false
	}
	if now.IsZero() {
		now = c.now()
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	age := now.UTC().Sub(c.fetched)
	if c.snapshot.ID == "" || age < 0 || age >= c.ttl {
		return TaskResultInventorySnapshot{}, false
	}
	return cloneTaskResultInventorySnapshot(c.snapshot), true
}

func validateTaskResultInventorySnapshot(snapshot TaskResultInventorySnapshot, maxItems int) error {
	if strings.TrimSpace(snapshot.ID) == "" || snapshot.ObservedAt.IsZero() {
		return errors.New("task-result inventory snapshot identity and observation time are required")
	}
	if len(snapshot.Entries) > maxItems {
		return fmt.Errorf("task-result inventory has %d entries, exceeds limit %d", len(snapshot.Entries), maxItems)
	}
	seenTasks := make(map[string]struct{}, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		if strings.TrimSpace(entry.SpaceID) == "" || strings.TrimSpace(entry.TaskID) == "" ||
			strings.TrimSpace(entry.DatasetID) == "" || strings.TrimSpace(entry.ViewID) == "" ||
			strings.TrimSpace(entry.Frequency) == "" || entry.ObservedAt.IsZero() {
			return errors.New("task-result inventory entry is missing a required identity or observation time")
		}
		taskKey := entry.SpaceID + "\x00" + entry.TaskID
		if _, exists := seenTasks[taskKey]; exists {
			return fmt.Errorf("duplicate task-result inventory task %s/%s", entry.SpaceID, entry.TaskID)
		}
		seenTasks[taskKey] = struct{}{}
	}
	return nil
}

func cloneTaskResultInventorySnapshot(snapshot TaskResultInventorySnapshot) TaskResultInventorySnapshot {
	clone := snapshot
	clone.Entries = make([]TaskResultInventoryEntry, len(snapshot.Entries))
	for index, entry := range snapshot.Entries {
		clone.Entries[index] = entry
		clone.Entries[index].Sessions = append([]string(nil), entry.Sessions...)
	}
	return clone
}
