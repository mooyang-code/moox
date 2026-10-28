package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPurgeDatasetEventsRequiresConfirmation(t *testing.T) {
	err := runPurgeDatasetEvents(context.Background(), []string{"--space", "mooxsys", "--dataset", "dataset_mooxsys_host_resource"}, &bytes.Buffer{})
	require.ErrorContains(t, err, "--yes")
}

func TestPurgeDatasetEventsDryRunPrintsExactSubjects(t *testing.T) {
	var stdout bytes.Buffer
	err := runPurgeDatasetEvents(context.Background(), []string{"--space", "mooxsys", "--dataset", "dataset_mooxsys_host_resource", "--dry-run"}, &stdout)
	require.NoError(t, err)
	require.Contains(t, stdout.String(), `"status":"dry_run"`)
	require.Contains(t, stdout.String(), "moox.event.storage.dataset.rows.upserted.v2.")
	require.Contains(t, stdout.String(), "moox.event.storage.dataset.period.completed.v1.")
	require.NotContains(t, stdout.String(), ".merge.")
	require.Contains(t, stdout.String(), "moox.event.storage.dataset.sync_point.v1.")
}

func TestDatasetEventSubjectsAreExactAndDatasetScoped(t *testing.T) {
	subjects, err := datasetEventSubjects("mooxsys", "dataset_mooxsys_host_resource")
	require.NoError(t, err)
	require.Equal(t, []string{
		"moox.event.storage.dataset.rows.upserted.v2.nvxw66dtpfzq.mrqxiyltmv2f63lpn54hg6ltl5ug643ul5zgk43povzggzi",
		"moox.event.storage.dataset.period.completed.v1.nvxw66dtpfzq.mrqxiyltmv2f63lpn54hg6ltl5ug643ul5zgk43povzggzi",
		"moox.event.storage.dataset.sync_point.v1.nvxw66dtpfzq.mrqxiyltmv2f63lpn54hg6ltl5ug643ul5zgk43povzggzi",
	}, subjects)
	other, err := datasetEventSubjects("mooxsys", "other")
	require.NoError(t, err)
	require.NotEqual(t, subjects, other)
}

func TestPurgeDatasetEventsCallsPurgeAfterConfirmation(t *testing.T) {
	original := purgeDatasetEventSubjects
	t.Cleanup(func() { purgeDatasetEventSubjects = original })
	called := false
	purgeDatasetEventSubjects = func(_ context.Context, opts purgeDatasetEventsOptions, subjects []string) error {
		called = true
		require.Equal(t, "MOOX_STORAGE", opts.stream)
		require.Len(t, subjects, 3)
		return nil
	}
	var stdout bytes.Buffer
	err := runPurgeDatasetEvents(context.Background(), []string{"--space", "mooxsys", "--dataset", "dataset_mooxsys_host_resource", "--yes"}, &stdout)
	require.NoError(t, err)
	require.True(t, called)
}
