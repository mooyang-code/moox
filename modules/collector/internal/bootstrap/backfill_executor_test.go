package bootstrap

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/stretchr/testify/require"
)

// 补采执行端看任务的采集方式；任务已删除、只保留了数据时按空间的默认采集方式，crypto 默认推送，补采走本地执行端。
func TestBackfillExecutorFollowsTheTaskOrTheSpaceDefault(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db"), DefaultIngest: markets.DefaultIngest})
	require.NoError(t, err)
	require.NoError(t, db.ApplySchema(schema.AllSQL()))
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.Tasks().Create(ctx, domain.CollectionTask{
		SpaceID: "crypto", TaskID: "spot-scf", TaskName: "spot-scf", DataType: domain.DataTypeKline, Ingest: domain.IngestSCF, Enabled: true,
		CollectParams: `{"target_dataset_id":"ds_scf","frequency":"1m"}`, ResultDatasetID: "ds_scf",
	}))
	executor := backfillExecutor(db.Tasks(), markets.DefaultIngest)

	for _, item := range []struct{ space, dataset, want, why string }{
		{"crypto", "ds_scf", marketfetch.PlanExecutorSCF, "任务写明 scf"},
		{"crypto", "ds_deleted", marketfetch.PlanExecutorLocal, "任务已删除：crypto 默认推送"},
		{"stockcn", "ds_deleted", marketfetch.PlanExecutorSCF, "任务已删除：A 股默认云函数"},
	} {
		got, err := executor(ctx, item.space, item.dataset)
		require.NoError(t, err)
		require.Equal(t, item.want, got, item.why)
	}
}
