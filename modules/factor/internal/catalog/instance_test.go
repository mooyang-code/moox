package catalog

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/stretchr/testify/require"
)

const paramSpec = "params:\n  window: {type: int, min: 2, max: 30, default: 5}\noutput: ts_mean(close, window)\n"

func instanceRequest(members ...InstanceMember) InstanceTaskRequest {
	return InstanceTaskRequest{InstanceID: "inst-1", SpaceID: "crypto", SourceDatasetID: "dataset_prices", Freq: "1m",
		Members: members}
}

func resultColumns(meta *metadataFake, taskID string) map[string]map[string]string {
	out := map[string]map[string]string{}
	for _, column := range meta.columns[resultKey("crypto", domain.ResultDatasetID(taskID))] {
		out[column.ColumnName] = column.Attributes
	}
	return out
}

// 实例的私有因子任务：与共享任务同源也能建（同一源数据集可以有多个任务），实例是参数写入方；同一定义用两个别名加入，
// 各有自己的结果列与参数列；重复调用是幂等的；不再声明的成员停用并移出，已写入的列保留。
func TestEnsureInstanceTaskCreatesPrivateTaskWithAliasedMembers(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)
	ctx := context.Background()
	_, err := svc.CreateFactor(ctx, factorWithSpec("pmean", paramSpec))
	require.NoError(t, err)

	request := instanceRequest(InstanceMember{Alias: "fast", FactorID: "pmean"}, InstanceMember{Alias: "slow", FactorID: "pmean"})
	result, err := svc.EnsureInstanceTask(ctx, request)
	require.NoError(t, err)
	taskID := domain.InstanceTaskID("inst-1")
	require.Equal(t, taskID, result.Set.TaskID)
	require.Equal(t, "inst-1", result.Set.OwnerInstanceID)
	require.Equal(t, domain.TaskStatusEnabled, result.Set.Status)
	require.Len(t, result.Members, 2)
	for _, member := range result.Members {
		require.Equal(t, domain.MemberStatusEnabled, member.Status)
	}

	columns := resultColumns(meta, taskID)
	require.Equal(t, ColumnKindOutput, columns["fast_pmean"]["column_kind"])
	require.Equal(t, "fast", columns["fast_pmean"]["member_alias"])
	require.Equal(t, ColumnKindParam, columns["slow__window"]["column_kind"])
	require.Equal(t, "window", columns["slow__window"]["param_name"])

	again, err := svc.EnsureInstanceTask(ctx, request)
	require.NoError(t, err)
	require.Equal(t, taskID, again.Set.TaskID)
	require.Len(t, again.Members, 2)

	shrunk, err := svc.EnsureInstanceTask(ctx, instanceRequest(InstanceMember{Alias: "fast", FactorID: "pmean"}))
	require.NoError(t, err)
	require.Len(t, shrunk.Members, 1)
	require.Equal(t, "fast", shrunk.Members[0].MemberAlias)
	require.Contains(t, resultColumns(meta, taskID), "slow_pmean", "移出的成员已写入的列保留")

	// 换源数据集要先删除私有任务。
	moved := instanceRequest(InstanceMember{Alias: "fast", FactorID: "pmean"})
	moved.Freq = "5m"
	_, err = svc.EnsureInstanceTask(ctx, moved)
	require.ErrorContains(t, err, "不能改成")

	require.NoError(t, svc.ReleaseInstanceTask(ctx, "inst-1"))
	_, err = db.GetTaskByOwner(ctx, "inst-1")
	require.Error(t, err)
	require.NoError(t, svc.ReleaseInstanceTask(ctx, "inst-1"), "已经释放的再释放直接返回")
}

// 参数提交逐成员判定：只有拥有者能提交；范围外、不在网格上的被拒绝；冻结之后同一期 too_late。
// 冻结选这一期最新的已接受请求，下一期没有请求时沿用。
func TestSubmitParamsValidatesAndFreezeApplies(t *testing.T) {
	db := openCatalogStore(t)
	svc := newReadyService(t, db, newMetadataFake())
	ctx := context.Background()
	_, err := svc.CreateFactor(ctx, factorWithSpec("pmean", paramSpec))
	require.NoError(t, err)
	instance, err := svc.EnsureInstanceTask(ctx, instanceRequest(InstanceMember{Alias: "fast", FactorID: "pmean"}))
	require.NoError(t, err)
	taskID := instance.Set.TaskID
	period := time.Date(2026, 10, 23, 1, 0, 0, 0, time.UTC)
	submit := func(writer, requestID string, at time.Time, window float64) ParamSubmitResult {
		t.Helper()
		results, err := svc.SubmitParams(ctx, writer, []ParamSubmission{{RequestID: requestID, TaskID: taskID, MemberAlias: "fast",
			PeriodTime: at, Snapshot: domain.ParamSnapshot{"window": {Default: window}}}})
		require.NoError(t, err)
		require.Len(t, results, 1)
		return results[0]
	}

	require.Equal(t, SubmitRejected, submit("someone-else", "r0", period, 10).Result)
	require.Equal(t, SubmitRejected, submit("inst-1", "r1", period, 99).Result, "超出声明的范围")
	require.Equal(t, SubmitRejected, submit("inst-1", "r2", period.Add(30*time.Second), 10).Result, "不在周期网格上")
	require.Equal(t, SubmitAccepted, submit("inst-1", "r3", period, 10).Result)
	require.Equal(t, SubmitAccepted, submit("inst-1", "r4", period, 12).Result)

	frozen, err := svc.FreezeParams(ctx, []string{taskID}, period)
	require.NoError(t, err)
	require.Len(t, frozen[taskID], 1)
	require.Equal(t, domain.ParamsRequested, frozen[taskID][0].Source)
	require.Equal(t, 12.0, frozen[taskID][0].Snapshot.Value("window", "BTC"), "同一期取最新的已接受请求")
	require.Equal(t, SubmitTooLate, submit("inst-1", "r5", period, 14).Result)

	next, err := svc.FreezeParams(ctx, []string{taskID}, period.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, domain.ParamsCarried, next[taskID][0].Source)
	require.Equal(t, 12.0, next[taskID][0].Snapshot.Value("window", "BTC"))
	through, ok, err := svc.FrozenThrough(ctx, taskID)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, period.Add(time.Minute), through)
}
