package catalogsync

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/mooyang-code/moox/modules/factor/internal/taskrunner"
	"github.com/stretchr/testify/require"
)

type ownershipRows []store.OutputManifestKey

func (rows ownershipRows) ListOwned(context.Context) ([]store.OutputManifestKey, error) {
	return rows, nil
}

type clearOutputsFunc func(context.Context, *engine.FactorTask) error

func (f clearOutputsFunc) ClearFactorOutputs(ctx context.Context, task *engine.FactorTask) error {
	return f(ctx, task)
}

func TestEngineActivationDrainsAndCleansOldOwnershipBeforeCommit(t *testing.T) {
	at := time.Now().UTC().Truncate(time.Second)
	old := engine.FactorTask{BindingID: "removed", BindingGeneration: "old-generation", SubjectID: "BTC", Freq: "1m", PeriodTime: at.Unix(), ResultDatasetID: "old-dataset", Factor: engine.FactorSpec{Outputs: []string{"old-output"}}}
	raw, err := json.Marshal(old)
	require.NoError(t, err)
	rows := ownershipRows{{BindingID: old.BindingID, BindingGeneration: old.BindingGeneration, SubjectID: old.SubjectID, Frequency: old.Freq, PeriodTime: at, CleanupTaskJSON: string(raw)}}
	gate := taskrunner.NewOperationGate()
	cleared, committed := false, false
	activate, err := EngineActivation(gate, rows, clearOutputsFunc(func(_ context.Context, task *engine.FactorTask) error {
		require.False(t, committed)
		require.Equal(t, "old-dataset", task.ResultDatasetID)
		require.Equal(t, []string{"old-output"}, task.Factor.Outputs)
		require.NotEmpty(t, task.TriggerEventID)
		cleared = true
		return nil
	}))
	require.NoError(t, err)
	commit := func() error { require.True(t, cleared); committed = true; return nil }
	release := gate.Acquire()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err = activate(ctx, domain.CatalogSnapshot{}, domain.CatalogSnapshot{}, commit)
	release()
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.False(t, cleared)
	require.False(t, committed)
	require.NoError(t, activate(context.Background(), domain.CatalogSnapshot{}, domain.CatalogSnapshot{}, commit))
	require.True(t, committed)
	committed = false
	failure := errors.New("storage unavailable")
	activate, err = EngineActivation(gate, rows, clearOutputsFunc(func(context.Context, *engine.FactorTask) error { return failure }))
	require.NoError(t, err)
	require.ErrorIs(t, activate(context.Background(), domain.CatalogSnapshot{}, domain.CatalogSnapshot{}, commit), failure)
	require.False(t, committed)
}
