package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/stretchr/testify/require"
)

// 集合的标的列表变化后，预载要重新做（窗口释放被移出的标的）；只是顺序不同不算变化。
func TestPreloaderRepreparesWhenSubjectListChanges(t *testing.T) {
	p := newPreloader(nil, nil, nil, nil, nil)
	set := domain.EngineSet{Set: domain.FactorSet{SetID: "s1", SubjectMode: domain.SubjectModeInclude, Subjects: []string{"A", "B"}}}
	require.False(t, p.upToDate(set))
	p.markPrepared(set, set.Set.Subjects)
	require.True(t, p.upToDate(set))

	reordered := set
	reordered.Set.Subjects = []string{"B", "A"}
	require.True(t, p.upToDate(reordered))

	removed := set
	removed.Set.Subjects = []string{"A"}
	require.False(t, p.upToDate(removed), "标的被移出后要重新预载")

	all := domain.EngineSet{Set: domain.FactorSet{SetID: "s2", SubjectMode: domain.SubjectModeAll}}
	p.markPrepared(all, nil)
	require.True(t, p.upToDate(all))
}

type flakyPrepare struct{ fail bool }

func (f *flakyPrepare) Prepare(context.Context, domain.EngineSet, []string) error {
	if f.fail {
		return errors.New("预载失败")
	}
	return nil
}

type oneSet struct{ set domain.EngineSet }

func (o oneSet) SetIDs() []string { return []string{o.set.Set.SetID} }
func (o oneSet) SetByID(string) (domain.EngineSet, SetLookup) {
	return o.set, SetFound
}

// 重新预载失败后，集合不能还当作已预载：后面按提示改回原来的标的列表时才会重新预载（审查意见 4）。
func TestFailedRepreparationClearsPreparedState(t *testing.T) {
	set := domain.EngineSet{Set: domain.FactorSet{SetID: "s1", SubjectMode: domain.SubjectModeInclude, Subjects: []string{"A"}}}
	data := &flakyPrepare{}
	p := newPreloader(oneSet{set}, includeSubjects{}, data, nil, nil)
	require.NoError(t, p.PrepareSet(context.Background(), "s1"))
	require.True(t, p.Ready("s1"))

	set.Set.Subjects = []string{"A", "G"}
	p.sets = oneSet{set}
	data.fail = true
	require.Error(t, p.PrepareSet(context.Background(), "s1"))
	require.False(t, p.Ready("s1"), "预载失败，集合不再就绪")

	set.Set.Subjects = []string{"A"} // 按提示移除零数据标的，回到原来的列表
	p.sets = oneSet{set}
	data.fail = false
	require.NoError(t, p.PrepareSet(context.Background(), "s1"))
	require.True(t, p.Ready("s1"), "回到原列表也要重新预载成功")
}

type includeSubjects struct{}

func (includeSubjects) DatasetColumns(context.Context, string, string) ([]string, error) {
	return nil, nil
}
func (includeSubjects) ListDatasetSubjects(context.Context, string, string) ([]string, error) {
	return nil, nil
}

type recordingPrepare struct{ calls [][]string }

func (r *recordingPrepare) Prepare(_ context.Context, _ domain.EngineSet, subjects []string) error {
	r.calls = append(r.calls, append([]string(nil), subjects...))
	return nil
}

type mutableSubjects struct{ list []string }

func (m *mutableSubjects) DatasetColumns(context.Context, string, string) ([]string, error) {
	return nil, nil
}
func (m *mutableSubjects) ListDatasetSubjects(context.Context, string, string) ([]string, error) {
	return append([]string(nil), m.list...), nil
}

// 「全部标的」的集合：用户在数据集的标签里删除标的后，清单变了，下一轮预载要重新准备，窗口才会释放它（设计 6.2）。
func TestPreloaderRepreparesWhenDatasetSubjectsChange(t *testing.T) {
	set := domain.EngineSet{Set: domain.FactorSet{SetID: "s1", SubjectMode: domain.SubjectModeAll}}
	source := &mutableSubjects{list: []string{"A", "B", "C"}}
	data := &recordingPrepare{}
	p := newPreloader(oneSet{set}, source, data, nil, nil)
	ctx := context.Background()

	p.once(ctx)
	require.Equal(t, [][]string{{"A", "B", "C"}}, data.calls)
	p.once(ctx)
	require.Len(t, data.calls, 1, "清单没变，不重新预载")

	source.list = []string{"A", "C"} // 用户把 B 在标签里设为删除状态
	p.once(ctx)
	require.Equal(t, [][]string{{"A", "B", "C"}, {"A", "C"}}, data.calls, "清单少了 B，重新预载，传入的是新清单")
	p.once(ctx)
	require.Len(t, data.calls, 2)

	source.list = []string{"A", "C", "D"} // 新增标的同样会触发
	p.once(ctx)
	require.Len(t, data.calls, 3)
}
