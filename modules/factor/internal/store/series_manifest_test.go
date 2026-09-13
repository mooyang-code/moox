package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	"github.com/stretchr/testify/require"
)

func TestManifestPartitionsSourceSeriesAndRejectsSharedOutput(t *testing.T) {
	db, err := Open(&Options{Path: filepath.Join(t.TempDir(), "factor.db")})
	require.NoError(t, err)
	defer db.Close()
	require.NoError(t, db.ApplySchema(factorschema.AllSQL()))
	repo := db.OutputManifests()
	ctx := context.Background()
	a := OutputManifestKey{BindingID: "b", BindingGeneration: "g", SubjectID: "BTC", Frequency: "1m", PeriodTime: time.Unix(60, 0), SourceSeriesTag: "a", FilterSourceSeriesTag: true}
	b := a
	b.SourceSeriesTag = ""
	unfiltered := b
	unfiltered.FilterSourceSeriesTag = false
	require.NoError(t, repo.Replace(ctx, a, []string{"row-a"}))
	require.NoError(t, repo.Replace(ctx, b, []string{"row-b"}))
	require.NoError(t, repo.Replace(ctx, unfiltered, []string{"row-unfiltered"}))
	got, err := repo.Get(ctx, a)
	require.NoError(t, err)
	require.Equal(t, []string{"row-a"}, got)
	require.ErrorContains(t, repo.Replace(ctx, b, []string{"row-a"}), "another source series")
	require.NoError(t, repo.Replace(ctx, b, nil))
	got, err = repo.Get(ctx, a)
	require.NoError(t, err)
	require.Equal(t, []string{"row-a"}, got)
	owned, err := repo.ListOwned(ctx)
	require.NoError(t, err)
	require.Len(t, owned, 2)
}
