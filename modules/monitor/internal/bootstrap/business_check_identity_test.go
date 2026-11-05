package bootstrap

import (
	"testing"

	monitorobservability "github.com/mooyang-code/moox/modules/monitor/internal/observability"
	"github.com/stretchr/testify/require"
)

// 各模块的关注项变成的告警检查：检查 ID 带种类前缀，名称用大白话；Trade 的原有检查不变。
func TestBusinessCheckIdentityForAttentionChecks(t *testing.T) {
	for _, test := range []struct {
		kind, module, wantID, wantName string
	}{
		{"trade_attention", "target_rejected", "trade_attention:target_rejected", "交易目标被拒绝"},
		{"event_center_attention", "execution_failed", "event_center_attention:execution_failed", "事件通知失败"},
		{"event_center_attention", "delist_detected", "event_center_attention:delist_detected", "发现标的即将下架"},
		{"event_center_attention", "action_failed", "event_center_attention:action_failed", "系统动作处理失败"},
		{"collector_attention", "delist_source_failed", "collector_attention:delist_source_failed", "下架检测来源连续失败"},
		{"collector_attention", "delist_readonly_key_missing", "collector_attention:delist_readonly_key_missing", "下架检测缺少币安只读 Key"},
		{"worker_presence", "factor-engine", "worker_presence:factor-engine", monitorobservability.WorkerPresenceName("factor-engine")},
	} {
		id, name := businessCheckIdentity(monitorobservability.BusinessStatus{Kind: test.kind, Module: test.module})
		require.Equal(t, test.wantID, id, test.kind)
		require.Equal(t, test.wantName, name, test.kind)
	}
}
