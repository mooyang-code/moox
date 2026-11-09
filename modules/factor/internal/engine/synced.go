package engine

import (
	"context"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
	"trpc.group/trpc-go/trpc-go/log"
)

// syncedScopeTTL 是数据源标的清单的缓存时间：observed 每期都要用到，清单变化（上市、下架）本来就要等目录与预载跟上。
const syncedScopeTTL = time.Minute

// syncedScope 给出数据源上“正在同步”的标的：目录里同一数据源上启用的任务的标的范围的并集。任务的标的就是源数据集
// 当前在册的标的（setSubjects），所以只要目录里有同源启用的任务，这个并集就是数据集的在册标的；没有时为空。
// 它只看目录，与运行时的登记、准备状态无关：重启时任务逐个就绪、某个任务准备失败，都不改变 observed 的历史截面。
type syncedScope struct {
	catalog func() []domain.EngineTask
	source  interface {
		ListDatasetSubjects(ctx context.Context, spaceID, datasetID string) ([]string, error)
	}
	now func() time.Time

	mu    sync.Mutex
	cache map[string]syncedEntry
}

type syncedEntry struct {
	subjects []string
	at       time.Time
}

// Subjects 实现 localdata.Config.Synced。
func (s *syncedScope) Subjects(ctx context.Context, src seriesstore.Source) ([]string, error) {
	enabled := false
	for _, set := range s.catalog() {
		if set.Set.Status == domain.TaskStatusEnabled && set.Set.SpaceID == src.SpaceID && set.Set.SourceDatasetID == src.DatasetID &&
			set.Set.Freq == src.Freq {
			enabled = true
			break
		}
	}
	if !enabled {
		return nil, nil
	}
	key := src.SpaceID + "/" + src.DatasetID
	s.mu.Lock()
	entry, cached := s.cache[key]
	if cached && s.now().Sub(entry.at) < syncedScopeTTL {
		s.mu.Unlock()
		return append([]string(nil), entry.subjects...), nil
	}
	s.mu.Unlock()
	subjects, err := s.source.ListDatasetSubjects(ctx, src.SpaceID, src.DatasetID)
	if err != nil {
		if cached {
			// 查询失败时沿用上一次的清单：元数据一时查不到，不能让整期计算失败；从没成功过才报错。
			log.WarnContextf(ctx, "factor_synced_scope_stale source=%s error=%v", src, err)
			return append([]string(nil), entry.subjects...), nil
		}
		return nil, err
	}
	s.store(key, subjects)
	return subjects, nil
}

// Update 用预载刚取到的标的清单更新缓存（preloader.onSubjects）：实时、补算、回放与预载用同一份清单。
func (s *syncedScope) Update(set domain.FactorTask, subjects []string) {
	s.store(set.SpaceID+"/"+set.SourceDatasetID, subjects)
}

func (s *syncedScope) store(key string, subjects []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cache == nil {
		s.cache = map[string]syncedEntry{}
	}
	s.cache[key] = syncedEntry{subjects: append([]string(nil), subjects...), at: s.now()}
}
