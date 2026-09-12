package bootstrap

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	"github.com/stretchr/testify/require"
)

type cleanupRecorder struct{ tasks []engine.FactorTask }

func (r *cleanupRecorder) ClearFactorOutputs(_ context.Context, task *engine.FactorTask) error {
	r.tasks = append(r.tasks, *task)
	return nil
}

func TestBindingCleanerUsesPersistedOldGeneration(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "factor.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(factorschema.AllSQL()))
	old := engine.FactorTask{BindingID: "b", BindingGeneration: "old", SpaceID: "old-space", ResultDatasetID: "old-result", SubjectID: "BTC", Freq: "1m", PeriodTime: 60, Factor: engine.FactorSpec{FactorID: "old-factor", Outputs: []string{"old-output"}}}
	raw, err := json.Marshal(old)
	require.NoError(t, err)
	key := store.OutputManifestKey{BindingID: "b", BindingGeneration: "old", CleanupTaskJSON: string(raw), SubjectID: "BTC", Frequency: "1m", PeriodTime: time.Unix(60, 0).UTC()}
	require.NoError(t, db.OutputManifests().Replace(ctx, key, []string{"row"}))
	recorder := &cleanupRecorder{}
	cleaner := bindingOutputCleaner{storage: recorder, manifests: db.OutputManifests()}
	require.NoError(t, cleaner.ClearBindingOutputs(ctx, domain.FactorBinding{BindingID: "b", SpaceID: "new-space", ResultDatasetID: "new-result"}, domain.FactorDef{FactorID: "new-factor", Outputs: []string{"new-output"}}))
	require.Len(t, recorder.tasks, 1)
	actual := recorder.tasks[0]
	require.Equal(t, old.BindingGeneration, actual.BindingGeneration)
	require.Equal(t, old.ResultDatasetID, actual.ResultDatasetID)
	require.Equal(t, old.SpaceID, actual.SpaceID)
	require.Equal(t, old.Factor, actual.Factor)
}
