package metrics

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/log"
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
	Frequency             string
	MarketID              string
	CalendarID            string
	Timezone              string
	Sessions              []string
	LatestCompletedPeriod time.Time
	// AllowedLatenessSeconds 来自 Collector 清单；0 表示没有提供。
	AllowedLatenessSeconds int
	LatestCompletedStatus  string
	ViewLastDataTime       time.Time
	ObservedAt             time.Time
	Enabled                bool
	OwnershipVerified      bool
	ResultStatus           string
	// HistorySeeding 表示结果数据集在首次接入的历史补齐中；HistorySeedFinishedAt 是补齐结束的时刻，没有补齐记录或还在补齐时为零。
	HistorySeeding        bool
	HistorySeedFinishedAt time.Time
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

// CollectorTaskResultInventorySource 按配置的空间逐个拉取一次完整任务结果清单。
type CollectorTaskResultInventorySource struct {
	client   collectorTaskResultInventoryClient
	spaceIDs []string
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
	return &CollectorTaskResultInventorySource{client: client, spaceIDs: normalizedSpaces, maxItems: maxItems}, nil
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
	req := &collectorpb.GetTaskResultInventoryReq{SpaceId: spaceID}
	rpcCtx := contextWithInventorySpaceID(ctx, spaceID)
	rsp, err := s.client.GetTaskResultInventory(rpcCtx, req)
	if err != nil {
		return TaskResultInventorySnapshot{}, fmt.Errorf("fetch collector task-result inventory: %w", err)
	}
	if rsp == nil || rsp.GetRetInfo() == nil || rsp.GetRetInfo().GetCode() != collectorpb.ErrorCode_SUCCESS {
		message := "empty response"
		if rsp != nil && rsp.GetRetInfo() != nil {
			message = fmt.Sprintf("code=%s msg=%s", rsp.GetRetInfo().GetCode().String(), rsp.GetRetInfo().GetMsg())
		}
		return TaskResultInventorySnapshot{}, fmt.Errorf("collector inventory failed: %s", message)
	}
	observedAt := rsp.GetObservedAt()
	parsed, err := inventoryInstant(observedAt)
	if err != nil {
		return TaskResultInventorySnapshot{}, fmt.Errorf("collector inventory observed_at: %w", err)
	}
	if len(rsp.GetEntries()) > maxItems {
		return TaskResultInventorySnapshot{}, fmt.Errorf("collector inventory exceeds limit %d", maxItems)
	}
	snapshot.ObservedAt = parsed
	snapshot.ID = fmt.Sprintf("%s:%d:%d", spaceID, observedAt, len(rsp.GetEntries()))
	for _, raw := range rsp.GetEntries() {
		entry, err := taskResultInventoryEntryFromProto(raw)
		if err != nil {
			return TaskResultInventorySnapshot{}, fmt.Errorf("collector inventory: %w", err)
		}
		if entry.SpaceID != spaceID {
			return TaskResultInventorySnapshot{}, fmt.Errorf("collector inventory returned task from Space %q for requested Space %q", entry.SpaceID, spaceID)
		}
		snapshot.Entries = append(snapshot.Entries, entry)
	}
	return snapshot, nil
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
		DatasetID: strings.TrimSpace(raw.GetDatasetId()),
		Frequency: strings.TrimSpace(raw.GetFrequency()), MarketID: strings.TrimSpace(raw.GetMarketId()),
		CalendarID: strings.TrimSpace(raw.GetCalendarId()), Timezone: strings.TrimSpace(raw.GetTimezone()),
		Sessions: append([]string(nil), raw.GetSessions()...), LatestCompletedStatus: strings.TrimSpace(raw.GetLatestCompletedStatus()),
		Enabled: raw.GetEnabled(), OwnershipVerified: raw.GetOwnershipVerified(), ResultStatus: strings.TrimSpace(raw.GetResultStatus()),
		AllowedLatenessSeconds: int(raw.GetAllowedLatenessSeconds()),
		HistorySeeding:         raw.GetHistorySeeding(),
	}
	if finished := raw.GetHistorySeedFinishedAt(); finished > 0 {
		entry.HistorySeedFinishedAt = time.UnixMilli(finished).UTC()
	}
	var err error
	if entry.ObservedAt, err = inventoryInstant(raw.GetObservedAt()); err != nil {
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

// inventoryInstant 把清单里的观察时刻（Unix 毫秒）换成时间；0 或负数报错。
func inventoryInstant(ms int64) (time.Time, error) {
	if ms <= 0 {
		return time.Time{}, errors.New("timestamp is zero")
	}
	return time.UnixMilli(ms).UTC(), nil
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
	store       InventoryStore
}

// InventoryStore 持久化最后一份有效的任务结果清单：Collector 不可用时仍可继续检查，重启后也可用。
type InventoryStore interface {
	SaveInventory(ctx context.Context, snapshot []byte, fetchedAt time.Time) error
	LoadInventory(ctx context.Context) ([]byte, time.Time, error)
}

// SetStore 设置持久化存储。
func (c *TaskResultInventoryCache) SetStore(store InventoryStore) {
	c.mu.Lock()
	c.store = store
	c.mu.Unlock()
}

// GetAllowStale 返回最近一份有效的清单：新鲜时直接返回；刷新失败时返回内存或持久化的旧清单并标记 stale；
// 只有从未取到过清单时才返回错误。
func (c *TaskResultInventoryCache) GetAllowStale(ctx context.Context, now time.Time) (TaskResultInventorySnapshot, bool, error) {
	snapshot, err := c.Get(ctx, now)
	if err == nil {
		return snapshot, false, nil
	}
	if stale, ok := c.lastKnown(ctx); ok {
		return stale, true, nil
	}
	return TaskResultInventorySnapshot{}, false, err
}

// lastKnown 返回内存里的旧清单；内存没有时从持久化存储恢复一次。
func (c *TaskResultInventoryCache) lastKnown(ctx context.Context) (TaskResultInventorySnapshot, bool) {
	c.mu.RLock()
	snapshot, store := c.snapshot, c.store
	c.mu.RUnlock()
	if snapshot.ID != "" {
		return cloneTaskResultInventorySnapshot(snapshot), true
	}
	if store == nil {
		return TaskResultInventorySnapshot{}, false
	}
	raw, fetchedAt, err := store.LoadInventory(ctx)
	if err != nil || len(raw) == 0 {
		return TaskResultInventorySnapshot{}, false
	}
	var restored TaskResultInventorySnapshot
	if err := json.Unmarshal(raw, &restored); err != nil || validateTaskResultInventorySnapshot(restored, c.maxItems) != nil {
		return TaskResultInventorySnapshot{}, false
	}
	c.mu.Lock()
	if c.snapshot.ID == "" {
		c.snapshot = cloneTaskResultInventorySnapshot(restored)
		if c.fetched.IsZero() {
			c.fetched = fetchedAt
		}
	}
	c.mu.Unlock()
	return restored, true
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
	c.persist(ctx, snapshot, now)
	return cloneTaskResultInventorySnapshot(snapshot), nil
}

// persist 保存最后一份有效清单；失败只记录日志，不影响检查。
func (c *TaskResultInventoryCache) persist(ctx context.Context, snapshot TaskResultInventorySnapshot, now time.Time) {
	c.mu.RLock()
	store := c.store
	c.mu.RUnlock()
	if store == nil {
		return
	}
	raw, err := json.Marshal(snapshot)
	if err == nil {
		err = store.SaveInventory(ctx, raw, now)
	}
	if err != nil {
		log.WarnContextf(ctx, "monitor task-result inventory persistence failed: %v", err)
	}
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

func validateTaskResultInventorySnapshot(snapshot TaskResultInventorySnapshot, maxItems int) error {
	if strings.TrimSpace(snapshot.ID) == "" || snapshot.ObservedAt.IsZero() {
		return errors.New("task-result inventory snapshot identity and observation time are required")
	}
	if len(snapshot.Entries) > maxItems {
		return fmt.Errorf("task-result inventory has %d entries, exceeds limit %d", len(snapshot.Entries), maxItems)
	}
	seenTasks := make(map[string]struct{}, len(snapshot.Entries))
	seenResults := make(map[string]struct{}, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		if strings.TrimSpace(entry.SpaceID) == "" || strings.TrimSpace(entry.TaskID) == "" ||
			strings.TrimSpace(entry.DatasetID) == "" ||
			strings.TrimSpace(entry.Frequency) == "" || entry.ObservedAt.IsZero() {
			return errors.New("task-result inventory entry is missing a required identity or observation time")
		}
		taskKey := entry.SpaceID + "\x00" + entry.TaskID
		if _, exists := seenTasks[taskKey]; exists {
			return fmt.Errorf("duplicate task-result inventory task %s/%s", entry.SpaceID, entry.TaskID)
		}
		seenTasks[taskKey] = struct{}{}
		if entry.Enabled && entry.OwnershipVerified {
			resultKey := strings.Join([]string{entry.SpaceID, entry.DatasetID, entry.Frequency}, "\x00")
			if _, exists := seenResults[resultKey]; exists {
				return fmt.Errorf("duplicate task-result inventory result %s/%s", entry.SpaceID, entry.DatasetID)
			}
			seenResults[resultKey] = struct{}{}
		}
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
