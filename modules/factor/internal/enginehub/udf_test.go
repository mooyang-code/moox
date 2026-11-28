package enginehub

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/mooyang-code/moox/packages/factorudf"
	"github.com/stretchr/testify/require"
)

// seedUdf 保存示例模块的一个版本；hashOverride 非空时改写哈希（同一份元数据的“另一个版本”）。
func seedUdf(t *testing.T, db *store.Store, hashOverride string) store.UdfVersion {
	t.Helper()
	wasm, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "packages", "factorudf", "testdata", "ts_mean.wasm"))
	require.NoError(t, err)
	info, err := factorudf.Inspect(wasm, factorudf.Limits{})
	require.NoError(t, err)
	manifest, err := json.Marshal(factorudf.PackageManifest{Manifest: *info.Manifest})
	require.NoError(t, err)
	v := store.UdfVersion{Hash: info.Hash, Name: info.Manifest.Name, Manifest: string(manifest), SourceRef: "r@c", SDKVersion: "0.1.0", Toolchain: "t", Report: "{}", Wasm: wasm}
	if hashOverride != "" {
		v.Hash = hashOverride
	}
	_, err = db.PutUdfVersion(context.Background(), v)
	require.NoError(t, err)
	return v
}

func seedUdfTask(t *testing.T, db *store.Store) {
	t.Helper()
	ctx := context.Background()
	spec := "output: udf.ts_mean_udf(close, 3)\n"
	require.NoError(t, db.CreateFactor(ctx, domain.FactorDef{FactorID: "fu", Name: "fu", Spec: spec, SpecHash: domain.SpecHash(spec)}))
	set := domain.FactorTask{TaskID: "task_u", SpaceID: "crypto", Name: "任务u", SourceDatasetID: "dataset_u", Freq: "1m", ResultDatasetID: "dataset_factor_u", Status: domain.TaskStatusEnabled}
	require.NoError(t, db.CreateTask(ctx, set))
	_, err := db.AddMember(ctx, "task_u", "fu", "fu", domain.MemberKindFixed, nil, domain.MemberStatusEnabled)
	require.NoError(t, err)
}

// 目录快照携带定义引用的版本；切换版本（spec 不变）时目录哈希改变，引擎据此重新同步。
func TestSnapshotCarriesUdfBindingsAndTheirSwitchChangesTheCatalogHash(t *testing.T) {
	hub, db, _ := newTestHub(t)
	ctx := context.Background()
	v1 := seedUdf(t, db, "")
	v2 := seedUdf(t, db, "00"+v1.Hash[2:])
	require.NoError(t, db.SetActiveUdf(ctx, v1.Name, v1.Hash))
	seedUdfTask(t, db)

	hash1, _, sets, _, err := hub.Snapshot(ctx, "")
	require.NoError(t, err)
	require.Len(t, sets, 1)
	def := sets[0].Members[0].Factor
	require.Equal(t, v1.Hash, def.UDFs["ts_mean_udf"].Hash)
	require.NotNil(t, def.Compiled())

	require.NoError(t, db.SetActiveUdf(ctx, v2.Name, v2.Hash))
	hash2, notModified, sets, _, err := hub.Snapshot(ctx, hash1)
	require.NoError(t, err)
	require.False(t, notModified)
	require.NotEqual(t, hash1, hash2, "切换版本后目录指纹变化")
	require.Equal(t, v2.Hash, sets[0].Members[0].Factor.UDFs["ts_mean_udf"].Hash)
}

// 补算作业领取时按当前启用版本下发定义（切换版本要求没有已经开始的作业，所以不需要按作业固化绑定）。
func TestRecalcJobUsesActiveUdfVersionAtPull(t *testing.T) {
	hub, db, _ := newTestHub(t)
	ctx := context.Background()
	v1 := seedUdf(t, db, "")
	v2 := seedUdf(t, db, "00"+v1.Hash[2:])
	require.NoError(t, db.SetActiveUdf(ctx, v1.Name, v1.Hash))
	seedUdfTask(t, db)
	hub.SetResultReady("task_u", true)
	seedJob(t, db, "job-1", "task_u")
	leaseEngineA(t, hub)

	job, set, found, err := hub.Pull(ctx, engineA)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, v1.Hash, set.Members[0].Factor.UDFs["ts_mean_udf"].Hash)
	_, err = db.UpdateRecalcJob(ctx, job.JobID, func(j *store.RecalcJob) error { j.Status = store.RecalcStatusSucceeded; return nil })
	require.NoError(t, err)

	require.NoError(t, db.SetActiveUdf(ctx, v2.Name, v2.Hash))
	seedJob(t, db, "job-2", "task_u")
	_, set, found, err = hub.Pull(ctx, engineA)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, v2.Hash, set.Members[0].Factor.UDFs["ts_mean_udf"].Hash)
}
