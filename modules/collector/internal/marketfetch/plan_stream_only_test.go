package marketfetch

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/scfinvoker"
	"github.com/stretchr/testify/require"
)

// noCloud 模拟没有云账号：所有云函数查询都失败。纯推送路径不能依赖它们。
type noCloud struct{ *fakePlanCloud }

var errNoCloudAccount = errors.New("no cloud account")

func (noCloud) ListTimerMarketFetchers(context.Context, string) ([]scfinvoker.FunctionInstance, error) {
	return nil, errNoCloudAccount
}

func (noCloud) ListTimerMarketFetchersFresh(context.Context, string) ([]scfinvoker.FunctionInstance, error) {
	return nil, errNoCloudAccount
}

func (noCloud) ListUnreadyMarketFetchers(context.Context, string, string) ([]scfinvoker.FunctionInstance, error) {
	return nil, errNoCloudAccount
}

func (noCloud) LatestPackage(context.Context, string, string) (string, error) {
	return "", errNoCloudAccount
}

func TestStreamOnlyPlanAppliesWithoutCloudFunctions(t *testing.T) {
	h := newIngestHarness(t, DatasetMode{Mode: IngestStreaming, Executor: PlanExecutorLocal}, "1m")
	h.r.Cloud = noCloud{h.cloud}
	h.r.Deploy = PlanDeploy{}
	h.r.Options.Markets = nil
	h.settle(true)
	content := h.applied()
	require.NotEmpty(t, content.Functions)
	for _, function := range content.Functions {
		require.False(t, function.IsSCF(), "不配云函数时计划里只有推送和本地执行项")
	}
	require.Empty(t, h.r.Issue())
}

func TestFirstStreamPlanWaitsForCollectorStream(t *testing.T) {
	h := newIngestHarness(t, DatasetMode{Mode: IngestStreaming, Executor: PlanExecutorLocal}, "1m")
	online := false
	h.r.StreamOnline = func(context.Context) (bool, error) { return online, nil }
	require.NoError(t, h.reconcile(h.r))
	require.Nil(t, h.open(), "collector-stream 不在线时不建计划，否则生效周期早于它上线")
	require.True(t, strings.Contains(h.r.Issue(), "collector-stream"))

	online = true
	require.NoError(t, h.reconcile(h.r))
	require.NotNil(t, h.open())
	require.Empty(t, h.r.Issue())
}

func TestAppliedStreamPlanKeepsChangingWhileWorkerIsOffline(t *testing.T) {
	h := newIngestHarness(t, DatasetMode{Mode: IngestStreaming, Executor: PlanExecutorLocal}, "1m")
	h.settle(true)
	h.r.StreamOnline = func(context.Context) (bool, error) { return false, nil }
	h.setSubjects(70)
	require.NoError(t, h.reconcile(h.r))
	require.NotNil(t, h.open(), "已有计划的标的变更不等 collector-stream")
}

func TestPullDatasetWithoutSCFRegionReportsTheReason(t *testing.T) {
	h := newIngestHarness(t, DatasetMode{Mode: IngestPull, Executor: PlanExecutorSCF}, "1m")
	h.r.Options.Markets = nil
	require.Error(t, h.reconcile(h.r))
	require.Contains(t, h.r.Issue(), "no SCF region")
}
