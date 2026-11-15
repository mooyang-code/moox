package bootstrap

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestMonitorScheduleTimerConfigs(t *testing.T) {
	raw, err := os.ReadFile("../../config/trpc_go.yaml")
	require.NoError(t, err)
	var cfg struct {
		Server struct {
			Services []struct {
				Name     string `yaml:"name"`
				Port     int    `yaml:"port"`
				Network  string `yaml:"network"`
				Protocol string `yaml:"protocol"`
				Timeout  int    `yaml:"timeout"`
			} `yaml:"service"`
		} `yaml:"server"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &cfg))
	want := map[string]struct {
		port    int
		network string
		timeout int
	}{
		monitorDataCleanupTimerService: {11500, "0 */5 * * * *?startAtOnce=1", 120000},
		monitorCheckTimerService:       {11501, "*/30 * * * * *?startAtOnce=1", 30000},
	}
	for _, service := range cfg.Server.Services {
		expected, ok := want[service.Name]
		if !ok {
			continue
		}
		assert.Equal(t, expected.port, service.Port)
		assert.Equal(t, expected.network, service.Network)
		assert.Equal(t, "timer", service.Protocol)
		assert.Equal(t, expected.timeout, service.Timeout)
		delete(want, service.Name)
	}
	assert.Empty(t, want)
}

// 检查定时器一轮里主机类检查最先执行，业务评估很慢或失败都不推迟、不挡住它；各步的错误合并返回。
// 主机类检查成功时记下这一轮的时间（Monitor 的就绪检查据此判断评估是否停滞），失败时不记。
func TestCheckRoundRunsHostChecksFirst(t *testing.T) {
	var order []string
	runtime := &Runtime{}
	watchdogErr := errors.New("storage timeout")
	round := checkRound(runtime,
		func(context.Context) error { order = append(order, "host"); return nil },
		func(context.Context) error { order = append(order, "watchdog"); return watchdogErr },
	)
	err := round(t.Context())
	assert.Equal(t, []string{"host", "watchdog"}, order)
	assert.ErrorIs(t, err, watchdogErr)
	assert.NotZero(t, runtime.checkRoundAt.Load())

	order = nil
	runtime.checkRoundAt.Store(0)
	hostErr := errors.New("host checks failed")
	err = checkRound(runtime, func(context.Context) error { order = append(order, "host"); return hostErr },
		func(context.Context) error { order = append(order, "watchdog"); return nil })(t.Context())
	assert.Equal(t, []string{"host", "watchdog"}, order, "主机类检查失败不挡住后面的业务评估")
	assert.ErrorIs(t, err, hostErr)
	assert.Zero(t, runtime.checkRoundAt.Load())
}
