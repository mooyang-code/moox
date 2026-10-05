package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/stretchr/testify/require"
)

func engineSet(setID, datasetID string, ready bool, factorIDs ...string) domain.EngineSet {
	factors := make([]domain.FactorDef, 0, len(factorIDs))
	for _, id := range factorIDs {
		source := "def compute(frame, params, context):\n    return frame  # " + id + "\n"
		factors = append(factors, domain.FactorDef{
			FactorID: id, Name: id, FactorType: domain.FactorTypeTimeSeries, SourceCode: source,
			SourceHash: domain.SourceHash(source), InputColumns: []string{"close"}, Outputs: []string{id + "_value"},
			ParamsJSON: "{}", LookbackPeriods: 2,
		})
	}
	return domain.EngineSet{
		Set: domain.FactorSet{
			SetID: setID, SpaceID: "crypto", SourceDatasetID: datasetID, Freq: "1m",
			SubjectMode: domain.SubjectModeAll, ResultDatasetID: "result_" + datasetID, Status: domain.SetStatusEnabled,
		},
		Factors: factors, ResultReady: ready,
	}
}

func newTestCache(t *testing.T) (*CatalogCache, string, string) {
	t.Helper()
	root := t.TempDir()
	factorsDir := filepath.Join(root, "factors")
	stateFile := filepath.Join(root, "data", "catalog.json")
	cache, err := NewCatalogCache(factorsDir, stateFile)
	require.NoError(t, err)
	return cache, factorsDir, stateFile
}

func TestCatalogApplyMaterializesSourcesAndPersists(t *testing.T) {
	cache, factorsDir, stateFile := newTestCache(t)
	set := engineSet("fset_a", "dataset_a", true, "bias")

	changed, err := cache.Apply(CatalogSnapshot{Hash: "hash-1", Sets: []domain.EngineSet{set}})

	require.NoError(t, err)
	require.True(t, changed)
	source, err := os.ReadFile(filepath.Join(factorsDir, "bias", set.Factors[0].SourceHash+".py"))
	require.NoError(t, err)
	require.Equal(t, set.Factors[0].SourceCode, string(source))
	info, err := os.Stat(stateFile)
	require.NoError(t, err)
	require.False(t, info.IsDir())
	status := cache.Status()
	require.True(t, status.Loaded)
	require.Equal(t, "hash-1", status.Hash)
	require.Equal(t, 1, status.Sets)
}

func TestCatalogLoadFromStateFile(t *testing.T) {
	cache, factorsDir, stateFile := newTestCache(t)
	_, err := cache.Apply(CatalogSnapshot{Hash: "hash-1", Sets: []domain.EngineSet{engineSet("fset_a", "dataset_a", true, "bias")}})
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(factorsDir))

	restarted, err := NewCatalogCache(factorsDir, stateFile)
	require.NoError(t, err)
	loaded, err := restarted.Load()

	require.NoError(t, err)
	require.True(t, loaded)
	require.Equal(t, "hash-1", restarted.Status().Hash)
	set, factors, found, err := restarted.EnabledSetByDataset(context.Background(), "crypto", "dataset_a", "1m")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "fset_a", set.SetID)
	require.Len(t, factors, 1)
	_, err = os.Stat(filepath.Join(factorsDir, "bias"))
	require.NoError(t, err, "loading re-materializes the sources")
}

func TestCatalogLoadWithoutStateFile(t *testing.T) {
	cache, _, _ := newTestCache(t)

	loaded, err := cache.Load()

	require.NoError(t, err)
	require.False(t, loaded)
	_, _, _, err = cache.EnabledSetByDataset(context.Background(), "crypto", "dataset_a", "1m")
	require.ErrorIs(t, err, storageio.ErrInfra, "events wait until a catalog exists")
}

func TestCatalogApplyFailureKeepsPreviousSnapshot(t *testing.T) {
	cache, _, _ := newTestCache(t)
	_, err := cache.Apply(CatalogSnapshot{Hash: "hash-1", Sets: []domain.EngineSet{engineSet("fset_a", "dataset_a", true, "bias")}})
	require.NoError(t, err)
	broken := engineSet("fset_b", "dataset_b", true, "cci")
	broken.Factors[0].SourceHash = "does-not-match"

	_, err = cache.Apply(CatalogSnapshot{Hash: "hash-2", Sets: []domain.EngineSet{broken}})

	require.Error(t, err)
	require.Equal(t, "hash-1", cache.Status().Hash)
	_, _, found, err := cache.EnabledSetByDataset(context.Background(), "crypto", "dataset_a", "1m")
	require.NoError(t, err)
	require.True(t, found)
}

func TestCatalogNotModifiedKeepsSets(t *testing.T) {
	cache, _, _ := newTestCache(t)
	_, err := cache.Apply(CatalogSnapshot{Hash: "hash-1", Sets: []domain.EngineSet{engineSet("fset_a", "dataset_a", true, "bias")}})
	require.NoError(t, err)

	changed, err := cache.Apply(CatalogSnapshot{Hash: "hash-1", NotModified: true})

	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, 1, cache.Status().Sets)
}

func TestCatalogSyncFailureKeepsServing(t *testing.T) {
	cache, _, _ := newTestCache(t)
	_, err := cache.Apply(CatalogSnapshot{Hash: "hash-1", Sets: []domain.EngineSet{engineSet("fset_a", "dataset_a", true, "bias")}})
	require.NoError(t, err)

	cache.MarkSyncFailed(errors.New("manager unreachable"))

	_, _, found, err := cache.EnabledSetByDataset(context.Background(), "crypto", "dataset_a", "1m")
	require.NoError(t, err)
	require.True(t, found)
	status := cache.Status()
	require.False(t, status.SyncErrorSince.IsZero())
	require.Equal(t, "manager unreachable", status.SyncError)

	_, err = cache.Apply(CatalogSnapshot{Hash: "hash-1", NotModified: true})
	require.NoError(t, err)
	require.True(t, cache.Status().SyncErrorSince.IsZero(), "a successful sync clears the failure")
}

func TestCatalogNotReadySetReturnsInfraError(t *testing.T) {
	cache, _, _ := newTestCache(t)
	_, err := cache.Apply(CatalogSnapshot{Hash: "hash-1", Sets: []domain.EngineSet{engineSet("fset_a", "dataset_a", false, "bias")}})
	require.NoError(t, err)

	_, _, _, err = cache.EnabledSetByDataset(context.Background(), "crypto", "dataset_a", "1m")

	require.ErrorIs(t, err, storageio.ErrInfra)
}

func TestCatalogUnknownDatasetNotFound(t *testing.T) {
	cache, _, _ := newTestCache(t)
	_, err := cache.Apply(CatalogSnapshot{Hash: "hash-1", Sets: []domain.EngineSet{engineSet("fset_a", "dataset_a", true, "bias")}})
	require.NoError(t, err)

	_, _, found, err := cache.EnabledSetByDataset(context.Background(), "crypto", "dataset_other", "1m")

	require.NoError(t, err)
	require.False(t, found)
}

func TestCatalogFilterSubjects(t *testing.T) {
	cache, _, _ := newTestCache(t)
	_, err := cache.Apply(CatalogSnapshot{Hash: "hash-1", Sets: []domain.EngineSet{
		engineSet("fset_b", "dataset_b", true, "bias"), engineSet("fset_a", "dataset_a", true, "bias"),
	}})
	require.NoError(t, err)

	filters, err := cache.FilterSubjects(context.Background())

	require.NoError(t, err)
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	var want []string
	for _, dataset := range []string{"dataset_a", "dataset_b"} {
		subject, renderErr := registry.RenderSubject(events.CollectorPeriodCompleted, "crypto", dataset)
		require.NoError(t, renderErr)
		want = append(want, subject)
	}
	require.ElementsMatch(t, want, filters)
}

func TestCatalogApplyReportsSetChanges(t *testing.T) {
	cache, _, _ := newTestCache(t)
	_, err := cache.Apply(CatalogSnapshot{Hash: "hash-1", Sets: []domain.EngineSet{engineSet("fset_a", "dataset_a", true, "bias")}})
	require.NoError(t, err)

	changed, err := cache.Apply(CatalogSnapshot{Hash: "hash-2", Sets: []domain.EngineSet{engineSet("fset_a", "dataset_a", true, "bias", "cci")}})
	require.NoError(t, err)
	require.False(t, changed, "a member change keeps the same subscriptions")

	changed, err = cache.Apply(CatalogSnapshot{Hash: "hash-3", Sets: []domain.EngineSet{
		engineSet("fset_a", "dataset_a", true, "bias"), engineSet("fset_b", "dataset_b", true, "bias"),
	}})
	require.NoError(t, err)
	require.True(t, changed)
}

func TestNextSyncAtAlignsToIntervalPlusOffset(t *testing.T) {
	at := func(hms string) time.Time {
		parsed, err := time.Parse("15:04:05", hms)
		require.NoError(t, err)
		return time.Date(2026, 10, 5, parsed.Hour(), parsed.Minute(), parsed.Second(), 0, time.UTC)
	}
	require.Equal(t, at("12:00:45"), NextSyncAt(at("12:00:10"), time.Minute, 45*time.Second))
	require.Equal(t, at("12:01:45"), NextSyncAt(at("12:00:45"), time.Minute, 45*time.Second))
	require.Equal(t, at("12:01:45"), NextSyncAt(at("12:00:50"), time.Minute, 45*time.Second))
	require.Equal(t, at("12:01:00"), NextSyncAt(at("12:00:00"), time.Minute, 0))
	require.Equal(t, at("12:05:30"), NextSyncAt(at("12:01:00"), 5*time.Minute, 30*time.Second))
}

type catalogClientFake struct {
	snapshots []CatalogSnapshot
	err       error
	known     []string
}

func (f *catalogClientFake) SyncCatalog(_ context.Context, knownHash string) (CatalogSnapshot, error) {
	f.known = append(f.known, knownHash)
	if f.err != nil {
		return CatalogSnapshot{}, f.err
	}
	next := f.snapshots[0]
	f.snapshots = f.snapshots[1:]
	return next, nil
}

func TestSyncerSendsKnownHashAndNotifiesChanges(t *testing.T) {
	cache, _, _ := newTestCache(t)
	client := &catalogClientFake{snapshots: []CatalogSnapshot{
		{Hash: "hash-1", Sets: []domain.EngineSet{engineSet("fset_a", "dataset_a", true, "bias")}},
		{Hash: "hash-1", NotModified: true},
	}}
	changes := 0
	syncer := &catalogSyncer{client: client, cache: cache, onChange: func() { changes++ }}

	require.NoError(t, syncer.SyncOnce(context.Background()))
	require.NoError(t, syncer.SyncOnce(context.Background()))

	require.Equal(t, []string{"", "hash-1"}, client.known)
	require.Equal(t, 1, changes)
}

func TestSyncerFailureMarksCache(t *testing.T) {
	cache, _, _ := newTestCache(t)
	syncer := &catalogSyncer{client: &catalogClientFake{err: errors.New("dial tcp: timeout")}, cache: cache}

	require.Error(t, syncer.SyncOnce(context.Background()))

	require.Equal(t, "dial tcp: timeout", cache.Status().SyncError)
}
