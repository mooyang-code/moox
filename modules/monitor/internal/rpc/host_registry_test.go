package rpc

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/stretchr/testify/require"
)

func TestListHostAgentsReadsPersistedPresenceAfterRestart(t *testing.T) {
	ctx := context.Background()
	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close() })
	require.NoError(t, mgr.ApplySchema(schema.SQL()))

	registry, err := store.WithDatabase(mgr, hostmetrics.NewRegistry)
	require.NoError(t, err)
	t0 := time.Now().UTC().Add(-2 * time.Minute).Truncate(time.Second)
	_, err = registry.Observe(ctx, hostmetrics.HostObservation{
		AgentID: "agent-a", Hostname: "host-a", BootID: "boot-a",
		OccurredAt: t0, EventID: "event-0",
	})
	require.NoError(t, err)
	scanner := hostmetrics.NewSilenceScanner(registry, 90*time.Second, nil)
	require.NoError(t, scanner.Scan(ctx, t0.Add(91*time.Second)))

	restartedRegistry, err := store.WithDatabase(mgr, hostmetrics.NewRegistry)
	require.NoError(t, err)
	restartedStore := hostmetrics.NewStore(nil, nil)
	restartedStore.SetRegistry(restartedRegistry)
	service := New(mgr.Repositories(), Options{HostStore: restartedStore})
	rsp, err := service.ListHostAgents(ctx, &monitorpb.ListHostAgentsReq{})
	require.NoError(t, err)
	require.Len(t, rsp.GetAgents(), 1)
	require.Equal(t, "agent-a", rsp.GetAgents()[0].GetAgentId())
	require.False(t, rsp.GetAgents()[0].GetReachable())
	require.GreaterOrEqual(t, rsp.GetAgents()[0].GetStaleSeconds(), int64(91))
}
