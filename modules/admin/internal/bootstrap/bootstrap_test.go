package bootstrap

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v2"
)

func TestRegisterMetricsReporter_NilServer_ShouldNoPanic(t *testing.T) {
	require.NotPanics(t, func() {
		registerMetricsReporter(nil)
	})
}

func TestAdminCertificateWatchTimerConfig(t *testing.T) {
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
		if service.Name == certificateWatchTimerService {
			assert.Equal(t, 11307, service.Port)
			assert.Equal(t, "0 */10 * * * *", service.Network)
			assert.Equal(t, "timer", service.Protocol)
			assert.Equal(t, int(certificateWatchTimeout/time.Millisecond), service.Timeout)
			return
		}
	}
	t.Fatalf("timer service %q not found", certificateWatchTimerService)
}
