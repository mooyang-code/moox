package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/stretchr/testify/require"
)

func TestComponentStatusSinceSurvivesRestartAndChangesOnlyOnTransition(t *testing.T) {
	path := filepath.Join(t.TempDir(), "monitor.db")
	db, err := Open(path)
	require.NoError(t, err)
	require.NoError(t, db.ApplySchema(schema.SQL()))
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	rows := []domain.ComponentHealthState{{HostID: "control", ComponentID: "web-host", Status: "down"}}
	require.NoError(t, db.Repositories().ComponentHealth.Reconcile(t.Context(), rows, now))
	require.NoError(t, db.Close())
	db, err = Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	repo := db.Repositories().ComponentHealth
	require.NoError(t, repo.Reconcile(t.Context(), rows, now.Add(time.Minute)))
	got, err := repo.List(t.Context())
	require.NoError(t, err)
	require.Equal(t, now, got[0].SinceAt)
	rows[0].Status = "healthy"
	require.NoError(t, repo.Reconcile(t.Context(), rows, now.Add(2*time.Minute)))
	got, err = repo.List(t.Context())
	require.NoError(t, err)
	require.Equal(t, now.Add(2*time.Minute), got[0].SinceAt)
	rows[0].Status = "down"
	require.NoError(t, repo.Reconcile(t.Context(), rows, now))
	got, err = repo.List(t.Context())
	require.NoError(t, err)
	require.Equal(t, "healthy", got[0].Status, "late observation cannot rewind status")
	require.NoError(t, repo.Reconcile(t.Context(), nil, now.Add(3*time.Minute)))
	got, err = repo.List(t.Context())
	require.NoError(t, err)
	require.Empty(t, got)
}
