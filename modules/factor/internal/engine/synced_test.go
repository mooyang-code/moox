package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
	"github.com/stretchr/testify/require"
)

type flakyListing struct {
	subjects []string
	err      error
}

func (f *flakyListing) ListDatasetSubjects(context.Context, string, string) ([]string, error) {
	return f.subjects, f.err
}

// 查询失败时沿用上一次的清单，从没成功过才报错；没有同源启用的任务时为空；预载的清单直接更新缓存。
func TestSyncedScopeFallsBackToLastList(t *testing.T) {
	obs, _ := sharedTasks()
	clock := rt0
	listing := &flakyListing{err: errors.New("元数据暂时不可用")}
	scope := &syncedScope{catalog: func() []domain.EngineTask { return []domain.EngineTask{obs} }, source: listing,
		now: func() time.Time { return clock }}
	src := seriesstore.Source{SpaceID: "crypto", DatasetID: "dataset_a", Freq: "1m"}
	ctx := context.Background()
	_, err := scope.Subjects(ctx, src)
	require.Error(t, err, "从没成功过")

	listing.subjects, listing.err = []string{"A", "B"}, nil
	got, err := scope.Subjects(ctx, src)
	require.NoError(t, err)
	require.Equal(t, []string{"A", "B"}, got)

	clock = clock.Add(2 * syncedScopeTTL)
	listing.err = errors.New("元数据暂时不可用")
	got, err = scope.Subjects(ctx, src)
	require.NoError(t, err)
	require.Equal(t, []string{"A", "B"}, got, "沿用上一次的清单")

	scope.Update(obs.Set, []string{"A"})
	got, err = scope.Subjects(ctx, src)
	require.NoError(t, err)
	require.Equal(t, []string{"A"}, got, "预载的清单直接生效")

	got, err = scope.Subjects(ctx, seriesstore.Source{SpaceID: "crypto", DatasetID: "other", Freq: "1m"})
	require.NoError(t, err)
	require.Empty(t, got, "没有同源启用的任务")
}
