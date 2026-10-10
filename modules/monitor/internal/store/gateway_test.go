package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/stretchr/testify/require"
)

func TestGatewayDeadlineSurvivesRestartReadFailuresAndAppliedHashChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "monitor.db")
	manager, err := Open(path)
	require.NoError(t, err)
	require.NoError(t, manager.ApplySchema(schema.SQL()))
	now := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	row := domain.GatewayObservation{HostID: "control", StatusJSON: `{"instance_id":"first"}`, ExpectedHash: "expected-a", AppliedHash: "old", HostEnabledAt: now}
	require.NoError(t, manager.Repositories().Gateways.Reconcile(t.Context(), []domain.GatewayObservation{row}, now))
	require.NoError(t, manager.Close())
	manager, err = Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, manager.Close()) })
	repository := manager.Repositories().Gateways
	row.AppliedHash = "another-unapplied-snapshot"
	require.NoError(t, repository.Reconcile(t.Context(), []domain.GatewayObservation{row}, now.Add(3*time.Minute)))
	rows, err := repository.List(t.Context())
	require.NoError(t, err)
	require.WithinDuration(t, now, *rows[0].HashMismatchSince, time.Millisecond)
	failed := domain.GatewayObservation{HostID: "control", ReadError: "admin unavailable", HostEnabledAt: now}
	require.NoError(t, repository.Reconcile(t.Context(), []domain.GatewayObservation{failed}, now.Add(4*time.Minute)))
	rows, err = repository.List(t.Context())
	require.NoError(t, err)
	require.Equal(t, "expected-a", rows[0].ExpectedHash)
	require.Equal(t, row.StatusJSON, rows[0].StatusJSON)
	require.WithinDuration(t, now, *rows[0].HashMismatchSince, time.Millisecond)
	require.WithinDuration(t, now.Add(3*time.Minute), *rows[0].ObservedAt, time.Millisecond)
	row.ExpectedHash = "expected-b"
	require.NoError(t, repository.Reconcile(t.Context(), []domain.GatewayObservation{row}, now.Add(5*time.Minute)))
	rows, err = repository.List(t.Context())
	require.NoError(t, err)
	require.WithinDuration(t, now.Add(5*time.Minute), *rows[0].HashMismatchSince, time.Millisecond)
	row.AppliedHash = row.ExpectedHash
	require.NoError(t, repository.Reconcile(t.Context(), []domain.GatewayObservation{row}, now.Add(6*time.Minute)))
	rows, err = repository.List(t.Context())
	require.NoError(t, err)
	require.Nil(t, rows[0].HashMismatchSince)
	require.Empty(t, rows[0].ReadError)
	require.NoError(t, repository.Reconcile(t.Context(), nil, now.Add(7*time.Minute)))
	rows, err = repository.List(t.Context())
	require.NoError(t, err)
	require.Empty(t, rows, "disabled/deleted hosts must leave active gateway monitoring")
}
