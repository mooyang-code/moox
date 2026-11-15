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

// 检查定时器一轮里主机类检查最先执行：部署探测很慢（等宕机主机超时）或失败，都不推迟、不挡住它；各步的错误合并返回。
func TestCheckRoundRunsHostChecksBeforeProbes(t *testing.T) {
	var order []string
	probeErr, watchdogErr := errors.New("probe timeout"), errors.New("storage timeout")
	round := checkRound(
		func(context.Context) error { order = append(order, "host"); return nil },
		func(context.Context) (int, error) { order = append(order, "probes"); return 0, probeErr },
		func(context.Context) error { order = append(order, "watchdog"); return watchdogErr },
	)
	err := round(t.Context())
	assert.Equal(t, []string{"host", "probes", "watchdog"}, order)
	assert.ErrorIs(t, err, probeErr)
	assert.ErrorIs(t, err, watchdogErr)

	order = nil
	hostErr := errors.New("host checks failed")
	err = checkRound(func(context.Context) error { order = append(order, "host"); return hostErr },
		func(context.Context) (int, error) { order = append(order, "probes"); return 1, nil }, nil)(t.Context())
	assert.Equal(t, []string{"host", "probes"}, order, "主机类检查失败不挡住后面的探测")
	assert.ErrorIs(t, err, hostErr)
}
