package fetchplan

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/cloud"
	"github.com/stretchr/testify/require"
)

// noCloud 模拟没有云账号：所有云函数的读写都失败。纯推送路径不能依赖它们。
type noCloud struct{ *testCloud }

var errNoCloudAccount = errors.New("no cloud account")

func (noCloud) LatestPackage(context.Context, string, string) (*cloud.Package, error) {
	return nil, errNoCloudAccount
}

func (noCloud) Describe(context.Context, cloud.FunctionRef) (cloud.FunctionState, error) {
	return cloud.FunctionState{}, errNoCloudAccount
}

func (noCloud) DescribeCode(context.Context, cloud.FunctionRef) (cloud.FunctionState, error) {
	return cloud.FunctionState{}, errNoCloudAccount
}

func (noCloud) List(context.Context, string, string, string) ([]cloud.FunctionRef, error) {
	return nil, errNoCloudAccount
}

func (noCloud) Ensure(context.Context, cloud.FunctionSpec) error { return errNoCloudAccount }

func (noCloud) SetTimer(context.Context, cloud.FunctionRef, cloud.Timer, string) error {
	return errNoCloudAccount
}

func TestStreamOnlyPlanAppliesWithoutCloudFunctions(t *testing.T) {
	h := newIngestHarness(t, ingestStream, "1m")
	h.cloud.setPackages()
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
	require.Empty(t, h.cloudTimers(), "云上没有函数")
	require.Empty(t, h.cloud.operations(), "没有任何云端操作")
}

func TestFirstStreamPlanWaitsForCollectorStream(t *testing.T) {
	h := newIngestHarness(t, ingestStream, "1m")
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
	h := newIngestHarness(t, ingestStream, "1m")
	h.settle(true)
	h.r.StreamOnline = func(context.Context) (bool, error) { return false, nil }
	h.setSubjects(70)
	before := h.lastRecord().ID
	require.NoError(t, h.reconcile(h.r))
	require.NotEqual(t, before, h.lastRecord().ID, "已有计划的标的变更不等 collector-stream")
}

func TestPullDatasetWithoutSCFRegionReportsTheReason(t *testing.T) {
	h := newIngestHarness(t, ingestSCF, "1m")
	h.r.Options.Markets = nil
	require.Error(t, h.reconcile(h.r))
	require.Contains(t, h.r.Issue(), "no SCF region")
}
