package hostmetrics

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRegistryKeepsExplicitDeploymentHostIdentityWithoutHostnameGuessing(t *testing.T) {
	db := openRegistryTestDB(t)
	registry := NewRegistry(db)
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	_, err := registry.Observe(t.Context(), HostObservation{HostID: "control", AgentID: "PHYSICAL01", Hostname: "control-name", BootID: "boot", EventID: "first", OccurredAt: now})
	require.NoError(t, err)
	_, err = registry.Observe(t.Context(), HostObservation{AgentID: "STANDALONE", Hostname: "control", BootID: "boot", EventID: "second", OccurredAt: now})
	require.NoError(t, err)
	restarted := NewRegistry(db)
	store := NewStore(nil, nil)
	store.SetRegistry(restarted)
	rows, err := store.ListAgentsAt(t.Context(), now)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	for _, row := range rows {
		if row.AgentID == "PHYSICAL01" {
			require.Equal(t, "control", row.HostID)
		} else {
			require.Empty(t, row.HostID)
		}
	}
	_, err = restarted.Observe(t.Context(), HostObservation{HostID: "wrong", AgentID: "PHYSICAL01", Hostname: "late", BootID: "boot", EventID: "late", OccurredAt: now.Add(-time.Minute)})
	require.NoError(t, err)
	rows, err = store.ListAgentsAt(t.Context(), now)
	require.NoError(t, err)
	for _, row := range rows {
		if row.AgentID == "PHYSICAL01" {
			require.Equal(t, "control", row.HostID)
		}
	}
}
