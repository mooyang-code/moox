package storageio

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	"github.com/stretchr/testify/require"
)

func TestSourceSeriesManifestsPreserveOtherPartitionOutputs(t *testing.T) {
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "factor.db")})
	require.NoError(t, err)
	defer db.Close()
	require.NoError(t, db.ApplySchema(factorschema.AllSQL()))
	access := &fakeAccessClient{}
	client := (&Client{access: access}).WithOutputManifests(db.OutputManifests())
	at := time.Unix(60, 0)
	a := engine.FactorTask{TaskID: "A", BindingID: "binding", BindingGeneration: "g", SpaceID: "space", ResultDatasetID: "result", SubjectID: "BTC", Freq: "1m", PeriodTime: 60, SourceSeriesTag: "source-a", FilterSourceSeriesTag: true, Factor: engine.FactorSpec{FactorID: "factor", SourceHash: "hash", Outputs: []string{"value"}}}
	b := a
	b.TaskID, b.SourceSeriesTag = "B", "source-b"
	result := func(tag string) *engine.FactorResult {
		return &engine.FactorResult{Rows: []engine.FactorResultRow{{DataTime: at, SeriesTag: tag, Values: map[string]any{"value": 1.0}}}}
	}
	ctx := context.Background()
	_, err = client.WriteFactorPatch(ctx, &a, result("output-a"))
	require.NoError(t, err)
	_, err = client.WriteFactorPatch(ctx, &b, result("output-b"))
	require.NoError(t, err)
	require.Len(t, access.writeReqs, 2, "B must not clear A's output")
	_, err = client.WriteFactorPatch(ctx, &b, result("output-a"))
	require.ErrorContains(t, err, "another source series")
	require.Len(t, access.writeReqs, 2, "conflicts must fail before remote writes")
	_, err = client.WriteFactorPatch(ctx, &b, &engine.FactorResult{})
	require.NoError(t, err)
	require.Len(t, access.writeReqs, 3)
	require.Equal(t, "output-b", access.writeReqs[2].GetRows()[0].GetKey().GetTimeSeries().GetSeriesTag())
	owned, err := db.OutputManifests().Get(ctx, outputManifestKey(&a))
	require.NoError(t, err)
	require.Len(t, owned, 1)
}
