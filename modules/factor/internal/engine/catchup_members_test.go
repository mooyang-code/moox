package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/localdata"
	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/stretchr/testify/require"
)

// 自定义算子模块还没开始拉取（或正在拉取、编译）时，引用它的任务照常参与追赶：追赶报阻塞、不能 0/0 直接算追平；
// 只有拉取失败、编译失败才排除（见 TestExcludedTaskWithFailedModuleDoesNotBlockRelease）。
func TestPendingModuleKeepsTaskInCatchup(t *testing.T) {
	wasm := exampleWasm(t, "ts_mean")
	task, hash := udfTask(t, wasm)
	f := &fakeFetcher{files: map[string][]byte{hash: wasm}}
	r, _ := newLifecycleRuntime(t)
	startRunningModule(t, r)
	r.udf = newModules(t, f)
	_, err := r.catalog.Apply(CatalogSnapshot{Hash: "h-udf", Sets: []domain.EngineTask{task}})
	require.NoError(t, err)
	ctx := context.Background()
	r.refreshTaskChecks(ctx)
	checks, _ := r.checks.get("ftask_u")
	require.Equal(t, domain.TaskCheckFail, checkStatus(checks, modulesCheckName), "模块还没开始拉取，检查不通过")
	require.Len(t, r.catchupMembers(), 1, "还没开始拉取不是失败，任务照常参与追赶")

	_, err = r.life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdPause})
	require.NoError(t, err)
	_, err = r.life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdResume, ManualRelease: true})
	require.NoError(t, err)
	progress, err := r.catchupHook(ctx, time.Now())
	require.NoError(t, err)
	require.False(t, progress.CaughtUp, "参与者不是 0 个，不能立即报追平")
	require.Contains(t, progress.Message, "自定义算子")

	f.fail.Store(true)
	require.Error(t, r.udf.Ensure(ctx, hash, "ts_mean_udf"))
	require.Empty(t, r.catchupMembers(), "拉取失败要人工处理，才排除出追赶")
}

// 数据源不能计算（零数据标的、回看超过窗口、窗口内存超过预算、变更日志断档）的任务不参与追赶；
// 排除与任务检查用的是同一个判断（sourceError）：检查项给出对应的名字。
func TestBlockedSourceExcludesTaskFromCatchup(t *testing.T) {
	for _, tc := range []struct {
		name, check string
		err         error
	}{
		{"零数据标的", "本地数据", &localdata.NoDataError{Subjects: []string{"GHOST"}}},
		{"回看超过窗口", "回看数量", &localdata.LookbackExceededError{}},
		{"窗口内存超过预算", "变更同步", errors.New("窗口内存估算超过 window.max_memory")},
		{"变更日志断档", "变更同步", localdata.ErrChangeGap},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newLifecycleRuntime(t, engineTask("ftask_a", "dataset_a", true, "bias"), engineTask("ftask_b", "dataset_b", true, "bias"))
			// 断档、零数据等状态只能由真实的准备流程产生，这里换成假的本地数据判断。
			r.sources = sourcesFunc(func(src seriesstore.Source) error {
				if src.DatasetID == "dataset_b" {
					return tc.err
				}
				return nil
			})
			r.refreshTaskChecks(context.Background())
			checks, _ := r.checks.get("ftask_b")
			require.Equal(t, domain.TaskCheckFail, checkStatus(checks, tc.check))
			members := r.catchupMembers()
			require.Len(t, members, 1)
			require.Equal(t, "ftask_a", members[0].Set.TaskID)
		})
	}
}

// sourcesFunc 是测试里替代本地数据层的数据源判断。
type sourcesFunc func(seriesstore.Source) error

func (f sourcesFunc) Blocked(src seriesstore.Source) error { return f(src) }
