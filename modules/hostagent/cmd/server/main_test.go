package main

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/hostagent/internal/config"
	"github.com/stretchr/testify/require"
)

// 配了主机策略缓存但打不开网关客户端（首装时本机网关还没监听）：返回错误，由 main 以非零退出、交给 systemd 拉起，
// 不能悄悄放弃同步后继续跑。没配主机策略缓存时不同步，也不报错。
func TestPolicySyncFailsWhenGatewayClientCannotOpen(t *testing.T) {
	stop, err := startPolicySync(context.Background(), &config.Config{HostPolicyPath: t.TempDir() + "/host-policy.json"})
	require.Error(t, err)
	require.Nil(t, stop)

	stop, err = startPolicySync(context.Background(), &config.Config{})
	require.NoError(t, err)
	stop()
}
