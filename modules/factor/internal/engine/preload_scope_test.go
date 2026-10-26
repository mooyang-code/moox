package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/stretchr/testify/require"
)

// 任务的数据源（源数据集）变了，预载要重新做；标的清单变化由 subjectsChanged 单独检测。
func TestPreloaderRepreparesWhenSourceChanges(t *testing.T) {
	p := newPreloader(nil, nil, nil, nil, nil)
	set := domain.EngineTask{Set: domain.FactorTask{TaskID: "s1", SpaceID: "crypto", SourceDatasetID: "dataset_a", Freq: "1m"}}
	require.False(t, p.upToDate(set))
	p.markPrepared(set, nil)
	require.True(t, p.upToDate(set))

	rebound := set
	rebound.Set.SourceDatasetID = "dataset_b"
	require.False(t, p.upToDate(rebound), "改绑数据源后要重新预载")
}

// 源数据集的标的清单变了（上市、下架），subjectsChanged 报告需要重新预载；只是顺序不同不算变化。
func TestPreloaderDetectsSourceSubjectChanges(t *testing.T) {
	source := &listSubjects{subjects: []string{"A", "B"}}
	p := newPreloader(nil, source, nil, nil, nil)
	set := domain.EngineTask{Set: domain.FactorTask{TaskID: "s1", SpaceID: "crypto", SourceDatasetID: "dataset_a", Freq: "1m"}}
	p.markPrepared(set, source.subjects)
	require.False(t, p.subjectsChanged(context.Background(), set))

	source.subjects = []string{"B", "A"}
	require.False(t, p.subjectsChanged(context.Background(), set), "顺序不同不算变化")

	source.subjects = []string{"A"}
	require.True(t, p.subjectsChanged(context.Background(), set), "标的被移出后要重新预载")
}

type listSubjects struct{ subjects []string }

func (l *listSubjects) DatasetColumns(context.Context, string, string) ([]string, error) {
	return nil, nil
}
func (l *listSubjects) ListDatasetSubjects(context.Context, string, string) ([]string, error) {
	return append([]string(nil), l.subjects...), nil
}

type flakyPrepare struct{ fail bool }

func (f *flakyPrepare) Prepare(context.Context, domain.EngineTask, []string) error {
	if f.fail {
		return errors.New("预载失败")
	}
	return nil
}

func (f *flakyPrepare) ReleaseTask(domain.FactorTask) {}

type oneTask struct{ set domain.EngineTask }

func (o oneTask) TaskIDs() []string { return []string{o.set.Set.TaskID} }
func (o oneTask) SetByID(string) (domain.EngineTask, SetLookup) {
	return o.set, SetFound
}

// 重新预载失败后，任务不能还当作已预载：后面改回原来的数据源时才会重新预载（审查意见 4）。
func TestFailedRepreparationClearsPreparedState(t *testing.T) {
	set := domain.EngineTask{Set: domain.FactorTask{TaskID: "s1"}}
	data := &flakyPrepare{}
	p := newPreloader(oneTask{set}, includeSubjects{}, data, nil, nil)
	require.NoError(t, p.PrepareSet(context.Background(), "s1"))
	require.True(t, p.Ready("s1"))

	set.Set.SourceDatasetID = "dataset_b" // 改绑数据源，需要重新预载
	p.sets = oneTask{set}
	data.fail = true
	require.Error(t, p.PrepareSet(context.Background(), "s1"))
	require.False(t, p.Ready("s1"), "预载失败，任务不再就绪")

	set.Set.SourceDatasetID = "" // 回到原来的数据源
	p.sets = oneTask{set}
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

func (r *recordingPrepare) Prepare(_ context.Context, _ domain.EngineTask, subjects []string) error {
	r.calls = append(r.calls, append([]string(nil), subjects...))
	return nil
}

func (r *recordingPrepare) ReleaseTask(domain.FactorTask) {}

type mutableSubjects struct{ list []string }

func (m *mutableSubjects) DatasetColumns(context.Context, string, string) ([]string, error) {
	return nil, nil
}
func (m *mutableSubjects) ListDatasetSubjects(context.Context, string, string) ([]string, error) {
	return append([]string(nil), m.list...), nil
}

// 「全部标的」的任务：用户在数据集的标签里删除标的后，清单变了，下一轮预载要重新准备，窗口才会释放它（设计 6.2）。
func TestPreloaderRepreparesWhenDatasetSubjectsChange(t *testing.T) {
	set := domain.EngineTask{Set: domain.FactorTask{TaskID: "s1"}}
	source := &mutableSubjects{list: []string{"A", "B", "C"}}
	data := &recordingPrepare{}
	p := newPreloader(oneTask{set}, source, data, nil, nil)
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

type failingSubjects struct{}

func (failingSubjects) DatasetColumns(context.Context, string, string) ([]string, error) {
	return nil, nil
}
func (failingSubjects) ListDatasetSubjects(context.Context, string, string) ([]string, error) {
	return nil, errors.New("storage unavailable")
}

// 诊断入口直接写正式结果：显式指定的标的必须属于源数据集；没有指定标的时不查询；查询失败要报错。
func TestCheckSubjectsInDataset(t *testing.T) {
	set := domain.EngineTask{Set: domain.FactorTask{TaskID: "s1", SpaceID: "crypto", SourceDatasetID: "dataset_a", Freq: "1m"}}
	source := &listSubjects{subjects: []string{"BTC", "ETH"}}
	ctx := context.Background()

	require.NoError(t, checkSubjectsInDataset(ctx, source, set, nil), "没有指定标的：取源数据集全部，不需要核对")
	require.NoError(t, checkSubjectsInDataset(ctx, source, set, []string{"ETH", "BTC"}))
	err := checkSubjectsInDataset(ctx, source, set, []string{"BTC", "DOGE"})
	require.ErrorContains(t, err, "DOGE")
	require.ErrorContains(t, err, "dataset_a")
	require.ErrorContains(t, checkSubjectsInDataset(ctx, failingSubjects{}, set, []string{"BTC"}), "storage unavailable")
	require.NoError(t, checkSubjectsInDataset(ctx, failingSubjects{}, set, nil), "不指定标的时不会去查询")
}
