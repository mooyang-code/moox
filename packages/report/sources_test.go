package report

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/packages/metricspb"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// 上报带上就绪判断、模块级生命周期与代报的外部程序；就绪时不带原因。
func TestHandleCarriesReadinessLifecycleAndDelegates(t *testing.T) {
	publisher := &fakePublisher{}
	h, err := NewHandlerWithPublisher(validConfig("strategy"), publisher, prometheus.NewRegistry())
	require.NoError(t, err)
	ready, reason := false, "数据库不可用"
	h.SetReadiness(func(context.Context) (bool, string) { return ready, reason })
	h.SetLifecycle(func() *metricspb.ModuleLifecycle {
		return &metricspb.ModuleLifecycle{Stage: "paused", PauseKind: "system", PausedSince: "2026-11-15T01:00:00Z"}
	})
	h.SetDelegates(func(context.Context) []*metricspb.DelegatedReport {
		return []*metricspb.DelegatedReport{{ServiceName: "factor-engine", InstanceId: "engine@mac", LastHeartbeatUnix: 1}}
	})

	require.NoError(t, h.Handle(t.Context()))
	report := publisher.payloads[0].(*metricspb.MetricReport)
	require.NotNil(t, report.Ready)
	require.False(t, report.GetReady())
	require.Equal(t, "数据库不可用", report.GetNotReadyReason())
	require.Equal(t, "system", report.GetLifecycle().GetPauseKind())
	require.Equal(t, "factor-engine", report.GetDelegated()[0].GetServiceName())

	ready = true
	require.NoError(t, h.Handle(t.Context()))
	report = publisher.payloads[1].(*metricspb.MetricReport)
	require.True(t, report.GetReady())
	require.Empty(t, report.GetNotReadyReason(), "就绪时不带原因")
}

// 没有登记就绪判断的上报方（云函数）不带就绪字段：Monitor 不能把它当成未就绪。
func TestHandleWithoutReadinessOmitsReadyField(t *testing.T) {
	publisher := &fakePublisher{}
	h, err := NewHandlerWithPublisher(validConfig("moox-collector-scf"), publisher, prometheus.NewRegistry())
	require.NoError(t, err)
	require.NoError(t, h.Handle(t.Context()))
	report := publisher.payloads[0].(*metricspb.MetricReport)
	require.Nil(t, report.Ready)
	require.Nil(t, report.GetLifecycle())
}

// 部署没注入 MOOX_BOOT_ID 时生成随机启动标识，同一进程里只生成一次。
func TestProcessBootIDIsGeneratedOncePerProcess(t *testing.T) {
	first := DefaultConfig("monitor", "monitor").BootID
	require.NotEmpty(t, first)
	require.Equal(t, first, DefaultConfig("strategy", "strategy").BootID)
}
