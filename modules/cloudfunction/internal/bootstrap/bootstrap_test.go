package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/cloudfunction/internal/config"
	"github.com/mooyang-code/moox/modules/cloudfunction/internal/health"
	cloudfunctionrpc "github.com/mooyang-code/moox/modules/cloudfunction/internal/rpc"
	"github.com/mooyang-code/moox/modules/cloudfunction/internal/store"
	"github.com/mooyang-code/moox/modules/cloudfunction/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestCloudFunctionManagerListenerIsNativeLoopbackWithLongEnvelope(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "config", "trpc_go.yaml"))
	require.NoError(t, err)
	var config struct {
		Server struct {
			Service []struct {
				Name     string `yaml:"name"`
				IP       string `yaml:"ip"`
				Port     int    `yaml:"port"`
				Protocol string `yaml:"protocol"`
				Timeout  int    `yaml:"timeout"`
			} `yaml:"service"`
		} `yaml:"server"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &config))
	count := 0
	for _, service := range config.Server.Service {
		if service.Name != "trpc.moox.cloudfunction.CloudFunctionMgr" {
			continue
		}
		count++
		require.Equal(t, "127.0.0.1", service.IP)
		require.Equal(t, 11401, service.Port)
		require.Equal(t, "trpc", service.Protocol)
		require.Equal(t, 960000, service.Timeout)
	}
	require.Equal(t, 1, count)
}

func TestStartFunctionJobRunnerUsesRuntimeContextAndRecoversInterruptedItems(t *testing.T) {
	dbm, err := store.Open(&config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "cloudfunction.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = dbm.Close() })
	require.NoError(t, dbm.ApplySchema(schema.AllSQL()))
	catalog := dbm.Catalog()
	require.NoError(t, catalog.CreateFunctionJob(context.Background(), store.FunctionJobCreate{
		SpaceID: "crypto", JobID: "bootstrap-recovery", Operation: "create_instances",
		Items: []store.FunctionJobItemCreate{{ItemID: "item-0", InstanceID: "instance-0", RequestJSON: `{}`}},
	}))
	taken, err := catalog.TakePendingFunctionJobItems(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, taken, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	require.NoError(t, startFunctionJobRunner(ctx, cloudfunctionrpc.New(dbm), config.Default()))

	aggregate, err := catalog.GetFunctionJob(context.Background(), "crypto", "bootstrap-recovery")
	require.NoError(t, err)
	assert.Equal(t, 1, aggregate.PendingCount)
	assert.Equal(t, store.FunctionJobPending, aggregate.Job.Status)
}

func TestCloudFunctionHealthSnapshot(t *testing.T) {
	cfg := config.Default()
	dbm, err := store.Open(&config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "cloudfunction.db")})
	if err != nil {
		t.Fatalf("initialize database: %v", err)
	}
	t.Cleanup(func() { _ = dbm.Close() })
	state := health.New("cloudfunction", "cloudfunction", "", "")
	rsp := cloudfunctionHealthSnapshot(cfg, dbm, state)(context.Background())

	if rsp.Module != "cloudfunction" || !rsp.Ready || rsp.Status != "ok" {
		t.Fatalf("health response = %+v", rsp)
	}
}
