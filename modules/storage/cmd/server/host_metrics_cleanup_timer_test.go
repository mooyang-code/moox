//go:build legacy_storage

package main

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"

	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/config"
	storagesvc "github.com/mooyang-code/moox/modules/storage/internal/service/primarystore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v2"
	trpc "trpc.group/trpc-go/trpc-go"
)

func TestHostMetricsCleanupTimerConfigs(t *testing.T) {
	type serviceConfig struct {
		Name     string `yaml:"name"`
		Port     int    `yaml:"port"`
		Network  string `yaml:"network"`
		Protocol string `yaml:"protocol"`
		Timeout  int    `yaml:"timeout"`
	}
	type trpcConfig struct {
		Server struct {
			Service []serviceConfig `yaml:"service"`
		} `yaml:"server"`
	}
	for _, test := range []struct {
		path string
		want bool
	}{
		{path: "../../config/trpc_go.yaml", want: true},
		{path: "../../config/trpc_go.primary.yaml", want: true},
		{path: "../../config/storage_view/trpc_go.yaml"},
	} {
		t.Run(test.path, func(t *testing.T) {
			raw, err := os.ReadFile(test.path)
			require.NoError(t, err)
			var cfg trpcConfig
			require.NoError(t, yaml.Unmarshal(raw, &cfg))
			var found *serviceConfig
			for i := range cfg.Server.Service {
				if cfg.Server.Service[i].Name == hostMetricsCleanupTimerService {
					found = &cfg.Server.Service[i]
				}
			}
			if !test.want {
				assert.Nil(t, found)
				return
			}
			require.NotNil(t, found)
			assert.Equal(t, 20308, found.Port)
			assert.Equal(t, "0 0 * * * *?startAtOnce=1", found.Network)
			assert.Equal(t, "timer", found.Protocol)
			assert.Equal(t, 60000, found.Timeout)
		})
	}
}

func TestNewHostMetricsCleanupJobSkipsOverlap(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	access := &fakeHostMetricsCleanupAccess{run: func(context.Context, storagesvc.HostMetricsCleanupOptions) (storagesvc.HostMetricsCleanupResult, error) {
		close(started)
		<-release
		return storagesvc.HostMetricsCleanupResult{Deleted: 3, Batches: 1}, nil
	}}
	job, err := newHostMetricsCleanupJob(access, enabledHostMetricsCleanupConfig(), time.Second, func() time.Time { return time.Now().UTC() })
	require.NoError(t, err)
	firstDone := make(chan error, 1)
	go func() { firstDone <- job.Handle(context.Background()) }()
	<-started
	require.NoError(t, job.Handle(context.Background()))
	close(release)
	require.NoError(t, <-firstDone)
	assert.Equal(t, int32(1), access.calls.Load())
}

func TestNewHostMetricsCleanupJobAppliesTimeout(t *testing.T) {
	access := &fakeHostMetricsCleanupAccess{run: func(ctx context.Context, _ storagesvc.HostMetricsCleanupOptions) (storagesvc.HostMetricsCleanupResult, error) {
		<-ctx.Done()
		return storagesvc.HostMetricsCleanupResult{}, ctx.Err()
	}}
	job, err := newHostMetricsCleanupJob(access, enabledHostMetricsCleanupConfig(), 10*time.Millisecond, time.Now)
	require.NoError(t, err)
	assert.ErrorIs(t, job.Handle(context.Background()), context.DeadlineExceeded)
}

func TestRegisterHostMetricsCleanupTimerRoleAndDependencyRules(t *testing.T) {
	viewCfg := storageconfig.StorageConfig{Roles: []string{"view"}}
	viewCfg.ApplyDefaults()
	require.NoError(t, registerHostMetricsCleanupTimer(nil, nil, viewCfg))

	disabled := false
	accessCfg := storageconfig.StorageConfig{Roles: []string{"primary"}, Maintenance: storageconfig.StorageMaintenance{HostMetricsCleanup: enabledHostMetricsCleanupConfig()}}
	accessCfg.Maintenance.HostMetricsCleanup.Enabled = &disabled
	cfg, err := trpc.LoadConfig("../../config/trpc_go.primary.yaml")
	require.NoError(t, err)
	server := trpc.NewServerWithConfig(cfg)
	require.NoError(t, registerHostMetricsCleanupTimer(server, nil, accessCfg))

	accessCfg.Maintenance.HostMetricsCleanup.Enabled = boolPointer(true)
	require.Error(t, registerHostMetricsCleanupTimer(server, nil, accessCfg))
}

func TestNewHostMetricsCleanupJobRejectsInvalidConfig(t *testing.T) {
	_, err := newHostMetricsCleanupJob(&fakeHostMetricsCleanupAccess{}, storageconfig.HostMetricsCleanupConfig{MaxAge: "bad"}, time.Minute, time.Now)
	require.Error(t, err)
}

type fakeHostMetricsCleanupAccess struct {
	calls atomic.Int32
	run   func(context.Context, storagesvc.HostMetricsCleanupOptions) (storagesvc.HostMetricsCleanupResult, error)
}

func (f *fakeHostMetricsCleanupAccess) CleanupExpiredHostMetrics(ctx context.Context, opts storagesvc.HostMetricsCleanupOptions) (storagesvc.HostMetricsCleanupResult, error) {
	f.calls.Add(1)
	if f.run == nil {
		return storagesvc.HostMetricsCleanupResult{}, nil
	}
	return f.run(ctx, opts)
}

func enabledHostMetricsCleanupConfig() storageconfig.HostMetricsCleanupConfig {
	return storageconfig.HostMetricsCleanupConfig{
		Enabled: boolPointer(true), DatasetIDs: []string{"host_resource_v1"}, MaxAge: "48h", BatchSize: 1000, MaxBatchesPerRun: 10,
	}
}

func boolPointer(value bool) *bool { return &value }
