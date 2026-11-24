package bootstrap

import (
	"testing"

	monitorobservability "github.com/mooyang-code/moox/modules/monitor/internal/observability"
	"github.com/stretchr/testify/require"
)

// 业务检查的检查 ID 带种类前缀，名称用大白话。关注项的检查 ID 与名称由生产者给出，不经这里。
func TestBusinessCheckIdentity(t *testing.T) {
	for _, test := range []struct {
		kind, module, wantID, wantName string
	}{
		{"balance", "trade", "balance:trade", "账户余额同步 trade"},
		{"worker_presence", "factor-engine", "worker_presence:factor-engine", monitorobservability.WorkerPresenceName("factor-engine")},
	} {
		id, name := businessCheckIdentity(monitorobservability.BusinessStatus{Kind: test.kind, Module: test.module})
		require.Equal(t, test.wantID, id, test.kind)
		require.Equal(t, test.wantName, name, test.kind)
	}
}
