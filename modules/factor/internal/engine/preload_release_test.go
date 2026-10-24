package engine

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
)

// mutableSets 是可以随时增删集合的目录。
type mutableSets struct{ sets map[string]domain.EngineSet }

func (m *mutableSets) SetIDs() []string {
	ids := make([]string, 0, len(m.sets))
	for id := range m.sets {
		ids = append(ids, id)
	}
	return ids
}

func (m *mutableSets) SetByID(id string) (domain.EngineSet, SetLookup) {
	set, ok := m.sets[id]
	if !ok {
		return domain.EngineSet{}, SetGone
	}
	return set, SetFound
}

func includeSet(id, dataset string) domain.EngineSet {
	return domain.EngineSet{Set: domain.FactorSet{SetID: id, SpaceID: "crypto", SourceDatasetID: dataset, Freq: "1m",
		SubjectMode: domain.SubjectModeInclude, Subjects: []string{"A"}}}
}

// 集合离开目录（删除、停用）：预载循环取消它在数据源上的登记，数据源没有别的集合用时窗口与同步随之释放。
func TestPreloadReleasesSetsThatLeftTheCatalog(t *testing.T) {
	ctx := context.Background()
	catalog := &mutableSets{sets: map[string]domain.EngineSet{"s1": includeSet("s1", "dataset_a")}}
	data := new(preloadDataFake)
	p := newPreloader(catalog, includeSubjects{}, data, nil, nil)
	p.once(ctx)
	require.True(t, p.Ready("s1"))
	require.Empty(t, data.released)

	delete(catalog.sets, "s1")
	p.once(ctx)

	require.False(t, p.Ready("s1"))
	require.Len(t, data.released, 1)
	require.Equal(t, "s1", data.released[0].SetID)
	require.Equal(t, "dataset_a", data.released[0].SourceDatasetID)
	p.once(ctx)
	require.Len(t, data.released, 1, "已释放的集合不会重复释放")
}

// 私有因子集随实例改绑 View：集合 ID 不变、数据源变了，先取消旧数据源上的登记，再按新数据源预载。
func TestPreloadReleasesOldSourceWhenSetIsRebound(t *testing.T) {
	ctx := context.Background()
	catalog := &mutableSets{sets: map[string]domain.EngineSet{"s1": includeSet("s1", "dataset_a")}}
	data := new(preloadDataFake)
	p := newPreloader(catalog, includeSubjects{}, data, nil, nil)
	p.once(ctx)
	require.Equal(t, []string{"s1"}, data.prepared)

	catalog.sets["s1"] = includeSet("s1", "dataset_b")
	p.once(ctx)

	require.Len(t, data.released, 1)
	require.Equal(t, "dataset_a", data.released[0].SourceDatasetID, "释放的是旧数据源上的登记")
	require.Equal(t, []string{"s1", "s1"}, data.prepared, "新数据源重新预载")
	require.True(t, p.Ready("s1"))
}
