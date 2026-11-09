package engine

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
	"github.com/stretchr/testify/require"
)

// “正在同步”只来自预载最近一次取到的清单：没取到过为空，更新立即生效，返回副本，按数据源区分。
func TestSyncedScopeHoldsLatestPreloadList(t *testing.T) {
	obs, _ := sharedTasks()
	scope := &syncedScope{}
	src := seriesstore.Source{SpaceID: "crypto", DatasetID: "dataset_a", Freq: "1m"}
	ctx := context.Background()
	got, err := scope.Subjects(ctx, src)
	require.NoError(t, err)
	require.Empty(t, got)

	scope.Update(obs.Set, []string{"A", "B"})
	got, _ = scope.Subjects(ctx, src)
	require.Equal(t, []string{"A", "B"}, got)
	got[0] = "Z"
	again, _ := scope.Subjects(ctx, src)
	require.Equal(t, []string{"A", "B"}, again, "返回副本")

	scope.Update(obs.Set, []string{"A"})
	got, _ = scope.Subjects(ctx, src)
	require.Equal(t, []string{"A"}, got, "更新立即生效")

	got, _ = scope.Subjects(ctx, seriesstore.Source{SpaceID: "crypto", DatasetID: "other", Freq: "1m"})
	require.Empty(t, got)
}
