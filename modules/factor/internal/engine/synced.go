package engine

import (
	"context"
	"sync"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
)

// syncedScope 是数据源上“正在同步”的标的：只有一个来源，就是预载最近一次从 Storage 取到的标的清单
// （preloader.onSubjects，任务的标的就是数据集在册的标的）。实时、补算、回放都读这同一份清单，清单变了立即生效；
// 预载查询失败时不更新，沿用上一次的清单。预载还没取到过清单的数据源为空。
type syncedScope struct {
	mu    sync.Mutex
	lists map[string][]string
}

func syncedKey(spaceID, datasetID string) string { return spaceID + "/" + datasetID }

// Subjects 实现 localdata.Config.Synced 与 ReplayDeps.Synced。
func (s *syncedScope) Subjects(_ context.Context, src seriesstore.Source) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.lists[syncedKey(src.SpaceID, src.DatasetID)]...), nil
}

// Update 记下预载刚取到的标的清单。
func (s *syncedScope) Update(set domain.FactorTask, subjects []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lists == nil {
		s.lists = map[string][]string{}
	}
	s.lists[syncedKey(set.SpaceID, set.SourceDatasetID)] = append([]string(nil), subjects...)
}
