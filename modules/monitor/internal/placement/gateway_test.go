package placement

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/stretchr/testify/require"
)

type sampledSource struct {
	*memorySource
	statuses map[string]*adminpb.HostGatewayRuntimeStatus
	failures map[string]error
	calls    []string
}

func (s *sampledSource) GatewayStatus(_ context.Context, hostID string) (*adminpb.HostGatewayRuntimeStatus, error) {
	s.calls = append(s.calls, hostID)
	return s.statuses[hostID], s.failures[hostID]
}

func TestGatewaySamplingKeepsOtherHostsOnReadFailureAndSkipsDisabledHosts(t *testing.T) {
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, manager.Close()) })
	require.NoError(t, manager.ApplySchema(schema.SQL()))
	repos := manager.Repositories()
	now := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	source := &sampledSource{memorySource: &memorySource{snapshot: testSnapshot(t)}, statuses: map[string]*adminpb.HostGatewayRuntimeStatus{}, failures: map[string]error{}}
	for _, hostID := range []string{"control", "storage"} {
		source.statuses[hostID] = &adminpb.HostGatewayRuntimeStatus{InstanceId: hostID + "-first", ExpectedHash: "desired", AppliedHash: "old", LastSeenAt: now.Format(time.RFC3339Nano)}
	}
	syncer := NewSyncer(repos.Checks, source, testHTTPS(), repos.Gateways)
	syncer.now = func() time.Time { return now }
	_, err = syncer.Sync(t.Context())
	require.NoError(t, err)
	first := now
	now = now.Add(3 * time.Minute)
	source.failures["storage"] = errors.New("GetHostRoutes temporarily unavailable")
	_, err = syncer.Sync(t.Context())
	require.ErrorContains(t, err, "storage")
	rows, err := repos.Gateways.List(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, "control", rows[0].HostID)
	require.Empty(t, rows[0].ReadError)
	require.WithinDuration(t, now, *rows[0].ObservedAt, time.Millisecond)
	require.WithinDuration(t, first, *rows[1].ObservedAt, time.Millisecond)
	require.WithinDuration(t, first, *rows[1].HashMismatchSince, time.Millisecond)
	require.Contains(t, rows[1].ReadError, "temporarily unavailable")
	source.snapshot.Hosts[1].Status = "disabled"
	source.calls = nil
	_, err = syncer.Sync(t.Context())
	require.NoError(t, err)
	require.Equal(t, []string{"control"}, source.calls)
	rows, err = repos.Gateways.List(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 1)
}
