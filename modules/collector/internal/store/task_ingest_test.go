package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/stretchr/testify/require"
)

func openIngestStore(t *testing.T, defaults map[string]string) *Store {
	t.Helper()
	opts := &Options{Path: filepath.Join(t.TempDir(), "collector.db")}
	if defaults != nil {
		opts.DefaultIngest = func(spaceID string) string { return defaults[spaceID] }
	}
	db, err := Open(opts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(schema.AllSQL()))
	return db
}

func klineTask(spaceID, taskID, datasetID, ingest string) domain.CollectionTask {
	return domain.CollectionTask{SpaceID: spaceID, TaskID: taskID, TaskName: taskID, DataType: domain.DataTypeKline, Ingest: ingest, Enabled: true,
		TagIDs: []string{"tag"}, CollectParams: `{"frequency":"1m"}`, ResultDatasetID: datasetID, ResultViewID: "view_" + datasetID}
}

// 建任务不写采集方式时取空间的市场默认值；写了就用写的；合成任务没有采集方式。
func TestCreateFillsTheMarketDefaultIngest(t *testing.T) {
	db := openIngestStore(t, map[string]string{"crypto": domain.IngestStreaming, "stockcn": domain.IngestSCF})
	ctx := context.Background()
	require.NoError(t, db.Tasks().Create(ctx, klineTask("crypto", "a", "ds_a", "")))
	require.NoError(t, db.Tasks().Create(ctx, klineTask("stockcn", "b", "ds_b", "")))
	require.NoError(t, db.Tasks().Create(ctx, klineTask("crypto", "c", "ds_c", " Local ")))
	resample := domain.CollectionTask{SpaceID: "crypto", TaskID: "r", TaskName: "r", DataType: "kline_resample", CollectParams: "{}"}
	require.NoError(t, db.Tasks().Create(ctx, resample))
	for id, want := range map[string]string{"a": domain.IngestStreaming, "c": domain.IngestLocal, "r": ""} {
		task, err := db.Tasks().GetByTaskID(ctx, "crypto", id)
		require.NoError(t, err)
		require.Equal(t, want, task.Ingest, id)
	}
	task, err := db.Tasks().GetByTaskID(ctx, "stockcn", "b")
	require.NoError(t, err)
	require.Equal(t, domain.IngestSCF, task.Ingest)
}

// 取值只有三种：旧的 pull、“推送加云函数补采”都写不进去；空间没有默认值时必须写明；合成任务不能带采集方式。
func TestCreateRejectsInvalidIngest(t *testing.T) {
	db := openIngestStore(t, map[string]string{"crypto": domain.IngestStreaming})
	ctx := context.Background()
	require.ErrorContains(t, db.Tasks().Create(ctx, klineTask("crypto", "a", "ds_a", "pull")), "stream、scf 或 local")
	require.ErrorContains(t, db.Tasks().Create(ctx, klineTask("crypto", "a", "ds_a", "stream+scf")), "stream、scf 或 local")
	require.ErrorContains(t, db.Tasks().Create(ctx, klineTask("demo", "a", "ds_a", "")), "没有登记默认采集方式")
	resample := domain.CollectionTask{SpaceID: "crypto", TaskID: "r", TaskName: "r", DataType: "kline_resample", Ingest: domain.IngestSCF, CollectParams: "{}"}
	require.ErrorContains(t, db.Tasks().Create(ctx, resample), "只有 K 线采集任务有采集方式")

	bare := openIngestStore(t, nil)
	require.ErrorContains(t, bare.Tasks().Create(ctx, klineTask("crypto", "a", "ds_a", "")), "没有登记默认采集方式")
	require.NoError(t, bare.Tasks().Create(ctx, klineTask("crypto", "a", "ds_a", domain.IngestSCF)))
}

// 表约束：采集方式只有三个值，K 线任务必须有、其他任务必须没有。绕过仓库直接写也拦得住。
func TestTaskTableChecksIngest(t *testing.T) {
	db := openIngestStore(t, nil)
	insert := func(dataType, ingest string) error {
		return db.db.Exec(`INSERT INTO t_collector_tasks (c_space_id, c_task_id, c_task_name, c_data_type, c_ingest) VALUES ('s', ?, ?, ?, ?)`,
			dataType+ingest, dataType+ingest, dataType, ingest).Error
	}
	require.NoError(t, insert("kline", "stream"))
	require.NoError(t, insert("kline", "scf"))
	require.NoError(t, insert("kline", "local"))
	require.NoError(t, insert("kline_resample", ""))
	require.Error(t, insert("kline", ""))
	require.Error(t, insert("kline", "pull"))
	require.Error(t, insert("kline_resample", "scf"))
}

// 一个结果数据集只有一个任务在写（建任务时拒绝第二个），所以数据集的采集方式不会出现两个值；
// 按数据集切换改的就是写它的那个任务，不带数据集时改空间的全部 K 线任务，合成任务不动。
func TestSetIngestSwitchesByDatasetOrWholeSpace(t *testing.T) {
	db := openIngestStore(t, map[string]string{"crypto": domain.IngestStreaming})
	ctx := context.Background()
	require.NoError(t, db.Tasks().Create(ctx, klineTask("crypto", "a", "ds_a", "")))
	require.NoError(t, db.Tasks().Create(ctx, klineTask("crypto", "b", "ds_b", "")))
	require.ErrorContains(t, db.Tasks().Create(ctx, klineTask("crypto", "a2", "ds_a", domain.IngestSCF)), "conflicts with task")
	require.NoError(t, db.Tasks().Create(ctx, domain.CollectionTask{SpaceID: "crypto", TaskID: "r", TaskName: "r", DataType: "kline_resample", CollectParams: "{}"}))

	changed, err := db.Tasks().SetIngest(ctx, "crypto", "ds_a", domain.IngestSCF)
	require.NoError(t, err)
	require.EqualValues(t, 1, changed)
	tasks, err := db.Tasks().ListKline(ctx, "crypto", "")
	require.NoError(t, err)
	require.Len(t, tasks, 2)
	require.Equal(t, []string{domain.IngestSCF, domain.IngestStreaming}, []string{tasks[0].Ingest, tasks[1].Ingest})

	changed, err = db.Tasks().SetIngest(ctx, "crypto", "", domain.IngestLocal)
	require.NoError(t, err)
	require.EqualValues(t, 2, changed)
	changed, err = db.Tasks().SetIngest(ctx, "crypto", "ds_missing", domain.IngestSCF)
	require.NoError(t, err)
	require.Zero(t, changed)
	_, err = db.Tasks().SetIngest(ctx, "crypto", "", "pull")
	require.Error(t, err)
	resample, err := db.Tasks().GetByTaskID(ctx, "crypto", "r")
	require.NoError(t, err)
	require.Empty(t, resample.Ingest)
}
