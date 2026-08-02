package bootstrap

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/config"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/health"
	cloudnoderpc "github.com/mooyang-code/moox/modules/cloudnode/internal/rpc"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/store"
	"github.com/mooyang-code/moox/modules/cloudnode/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStartNodeBatchRunnerUsesRuntimeContextAndRecoversInterruptedItems(t *testing.T) {
	dbm, err := store.Open(&config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "cloudnode.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = dbm.Close() })
	require.NoError(t, dbm.ApplySchema(schema.AllSQL()))
	catalog := dbm.Catalog()
	require.NoError(t, catalog.CreateNodeBatch(context.Background(), store.NodeBatchCreate{
		SpaceID: "crypto", JobID: "bootstrap-recovery", Operation: "create_nodes",
		Items: []store.NodeBatchItemCreate{{ItemID: "item-0", NodeID: "node-0", RequestJSON: `{}`}},
	}))
	taken, err := catalog.TakePendingNodeBatchItems(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, taken, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	require.NoError(t, startNodeBatchRunner(ctx, cloudnoderpc.New(dbm), config.Default()))

	aggregate, err := catalog.GetNodeBatch(context.Background(), "crypto", "bootstrap-recovery")
	require.NoError(t, err)
	assert.Equal(t, 1, aggregate.PendingCount)
	assert.Equal(t, store.NodeBatchPending, aggregate.Job.Status)
}

func TestCloudNodeHealthSnapshot(t *testing.T) {
	cfg := config.Default()
	dbm, err := store.Open(&config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "cloudnode.db")})
	if err != nil {
		t.Fatalf("initialize database: %v", err)
	}
	t.Cleanup(func() { _ = dbm.Close() })
	state := health.New("cloudnode", "cloudnode", "", "")
	rsp := cloudnodeHealthSnapshot(cfg, dbm, state)(context.Background())

	if rsp.Module != "cloudnode" || !rsp.Ready || rsp.Status != "ok" {
		t.Fatalf("health response = %+v", rsp)
	}
	if rsp.Details["queue_backend"] != "jetstream" {
		t.Fatalf("queue_backend = %v", rsp.Details["queue_backend"])
	}
}
