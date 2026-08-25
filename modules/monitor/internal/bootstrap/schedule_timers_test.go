package bootstrap

import (
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
		monitorDataCleanupTimerService: {11500, "0 0 */6 * * *?startAtOnce=1", 120000},
		monitorCheckTimerService:       {11501, "*/30 * * * * *?startAtOnce=1", 30000},
		monitorHostSilenceTimerService: {11503, "*/30 * * * * *?startAtOnce=1", 30000},
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
