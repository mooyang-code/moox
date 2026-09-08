package marketfetch

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"trpc.group/trpc-go/trpc-go/log"
)

const taskInstanceFallbackTimeout = 2 * time.Second

// TaskInstanceDatasetSource preserves the last persisted task inventory as a
// planner fallback. It prevents a transient metadata outage from deleting the
// whole Timer fleet; the next successful metadata read still becomes the
// authoritative source.
type TaskInstanceDatasetSource struct {
	primary   datasetSource
	instances *store.TaskInstanceRepository
	mu        sync.RWMutex
	cache     map[string][]domain.DatasetSubject
	datasets  map[string]storagesource.DatasetInfo
}

// NewTaskInstanceDatasetSource wraps a metadata source with the Collector's
// durable task inventory fallback.
func NewTaskInstanceDatasetSource(primary datasetSource, instances *store.TaskInstanceRepository) *TaskInstanceDatasetSource {
	return &TaskInstanceDatasetSource{primary: primary, instances: instances, cache: make(map[string][]domain.DatasetSubject), datasets: make(map[string]storagesource.DatasetInfo)}
}

func (s *TaskInstanceDatasetSource) GetDataset(ctx context.Context, spaceID, datasetID string) (storagesource.DatasetInfo, error) {
	if s == nil || s.primary == nil {
		return storagesource.DatasetInfo{}, fmt.Errorf("dataset source is not initialized")
	}
	info, err := s.primary.GetDataset(ctx, spaceID, datasetID)
	if err == nil {
		s.mu.Lock()
		s.datasets[datasetCacheKey(spaceID, datasetID)] = info
		s.mu.Unlock()
		return info, nil
	}
	key := datasetCacheKey(spaceID, datasetID)
	s.mu.RLock()
	cached, ok := s.datasets[key]
	s.mu.RUnlock()
	if ok {
		log.WarnContextf(ctx, "storage dataset read failed, used last-good dataset metadata fallback space=%s dataset=%s error=%v", spaceID, datasetID, err)
		return cached, nil
	}
	fallback, fallbackErr := s.datasetFromInstances(ctx, spaceID, datasetID)
	if fallbackErr == nil {
		log.WarnContextf(ctx, "storage dataset read failed, used task-instance dataset fallback space=%s dataset=%s error=%v", spaceID, datasetID, err)
		return fallback, nil
	}
	return storagesource.DatasetInfo{}, fmt.Errorf("%w; task-instance fallback: %v", err, fallbackErr)
}

func (s *TaskInstanceDatasetSource) ListSubjects(ctx context.Context, spaceID, datasetID, dataSourceID string) ([]domain.DatasetSubject, error) {
	if s == nil || s.primary == nil {
		return nil, fmt.Errorf("dataset source is not initialized")
	}
	subjects, err := s.primary.ListSubjects(ctx, spaceID, datasetID, dataSourceID)
	if err == nil && len(subjects) > 0 {
		s.mu.Lock()
		s.cache[datasetCacheKey(spaceID, datasetID)] = cloneDatasetSubjects(subjects)
		s.mu.Unlock()
		return subjects, nil
	}
	key := datasetCacheKey(spaceID, datasetID)
	s.mu.RLock()
	cached := cloneDatasetSubjects(s.cache[key])
	s.mu.RUnlock()
	if len(cached) > 0 {
		log.WarnContextf(ctx, "storage subject read failed or empty, used last-good subject fallback space=%s dataset=%s error=%v", spaceID, datasetID, err)
		return cached, nil
	}
	fallback, fallbackErr := s.subjectsFromInstances(ctx, spaceID, datasetID)
	if fallbackErr == nil && len(fallback) > 0 {
		s.mu.Lock()
		s.cache[key] = cloneDatasetSubjects(fallback)
		s.mu.Unlock()
		log.WarnContextf(ctx, "storage subject read failed or empty, used task-instance subject fallback space=%s dataset=%s subjects=%d error=%v", spaceID, datasetID, len(fallback), err)
		return fallback, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w; task-instance fallback: %v", err, fallbackErr)
	}
	return subjects, nil
}

func (s *TaskInstanceDatasetSource) datasetFromInstances(ctx context.Context, spaceID, datasetID string) (storagesource.DatasetInfo, error) {
	instances, err := s.listMatchingInstances(ctx, spaceID, datasetID)
	if err != nil {
		return storagesource.DatasetInfo{}, err
	}
	if len(instances) == 0 {
		return storagesource.DatasetInfo{}, fmt.Errorf("no persisted kline instances for dataset %s", datasetID)
	}
	first := instances[0]
	dataSourceID := strings.TrimSpace(first.SourceID)
	if dataSourceID == "" {
		dataSourceID = strings.TrimSpace(first.Provider)
	}
	marketType := strings.TrimSpace(first.MarketType)
	return storagesource.DatasetInfo{
		DataSourceID: dataSourceID,
		DataKind:     storagepb.DataKind_DATA_KIND_TIME_SERIES,
		Status:       "active",
		Freqs:        []string{strings.TrimSpace(first.Frequency)},
		Attributes:   map[string]string{"market_type": marketType},
	}, nil
}

func (s *TaskInstanceDatasetSource) subjectsFromInstances(ctx context.Context, spaceID, datasetID string) ([]domain.DatasetSubject, error) {
	instances, err := s.listMatchingInstances(ctx, spaceID, datasetID)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]domain.DatasetSubject, len(instances))
	for _, instance := range instances {
		subjectID := strings.TrimSpace(instance.SubjectID)
		if subjectID == "" {
			continue
		}
		external := ""
		if !strings.EqualFold(strings.TrimSpace(instance.MarketType), "equity") {
			external = fallbackCryptoSymbol(subjectID, instance.MarketType)
		}
		seen[subjectID] = domain.DatasetSubject{SubjectID: subjectID, SubjectName: subjectID, ExternalSymbol: external, Status: "active"}
	}
	if len(seen) == 0 {
		return nil, fmt.Errorf("no persisted subjects for dataset %s", datasetID)
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := make([]domain.DatasetSubject, 0, len(ids))
	for _, id := range ids {
		result = append(result, seen[id])
	}
	return result, nil
}

func (s *TaskInstanceDatasetSource) listMatchingInstances(ctx context.Context, spaceID, datasetID string) ([]domain.TaskInstance, error) {
	if s.instances == nil {
		return nil, fmt.Errorf("task-instance repository is not initialized")
	}
	fallbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), taskInstanceFallbackTimeout)
	defer cancel()
	instances, err := s.instances.ListActiveKline(fallbackCtx, spaceID)
	if err != nil {
		return nil, err
	}
	matched := make([]domain.TaskInstance, 0)
	for _, instance := range instances {
		params, parseErr := domain.ParseCollectParams(instance.TaskParams, instance.Provider, instance.MarketType, instance.DataType)
		if parseErr != nil || !strings.EqualFold(strings.TrimSpace(params.Source.DatasetID), strings.TrimSpace(datasetID)) {
			continue
		}
		matched = append(matched, instance)
	}
	return matched, nil
}

func fallbackCryptoSymbol(subjectID, marketType string) string {
	parts := strings.Split(strings.ToUpper(strings.TrimSpace(subjectID)), "-")
	if len(parts) < 3 || !strings.EqualFold(parts[len(parts)-1], strings.TrimSpace(marketType)) {
		return ""
	}
	for _, part := range parts[:len(parts)-1] {
		if strings.TrimSpace(part) == "" {
			return ""
		}
	}
	return strings.Join(parts[:len(parts)-1], "")
}

func datasetCacheKey(spaceID, datasetID string) string {
	return strings.ToLower(strings.TrimSpace(spaceID)) + "\x00" + strings.TrimSpace(datasetID)
}

func cloneDatasetSubjects(items []domain.DatasetSubject) []domain.DatasetSubject {
	return append([]domain.DatasetSubject(nil), items...)
}
