package bootstrap

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v2"
)

func TestRegisterMetricsReporter_NilServer_ShouldNoPanic(t *testing.T) {
	require.NotPanics(t, func() {
		registerMetricsReporter(nil)
	})
}

func TestAdminAuthCacheCleanupTimerConfig(t *testing.T) {
	raw, err := os.ReadFile("../../config/trpc_go.yaml")
	require.NoError(t, err)
	var cfg struct {
		Server struct {
			Service []struct {
				Name     string `yaml:"name"`
				Port     int    `yaml:"port"`
				Network  string `yaml:"network"`
				Protocol string `yaml:"protocol"`
				Timeout  int    `yaml:"timeout"`
			} `yaml:"service"`
		} `yaml:"server"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &cfg))
	for _, service := range cfg.Server.Service {
		if service.Name == authCacheCleanupTimerService {
			assert.Equal(t, 11306, service.Port)
			assert.Equal(t, "0 */5 * * * *", service.Network)
			assert.Equal(t, "timer", service.Protocol)
			assert.Equal(t, 60000, service.Timeout)
			return
		}
	}
	t.Fatalf("timer service %q not found", authCacheCleanupTimerService)
}
