package catalog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCreateSetCreatesAndActivatesResultDataset(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := NewService(db, meta, WithFactorsDir(t.TempDir()))

	set, err := svc.CreateSet(context.Background(), newSet())
	require.NoError(t, err)
	require.Equal(t, domain.SetID("dataset_prices", "1m"), set.SetID)
	require.Equal(t, domain.ResultDatasetID("dataset_prices", "1m"), set.ResultDatasetID)
	require.Equal(t, domain.SetStatusEnabled, set.Status)
	require.Equal(t, "storage-node-0", meta.createdSpec.DataNodeID)
	require.Equal(t, "720h", meta.createdSpec.KeepDuration)
	require.Equal(t, storageio.DatasetRoleFactorResult, meta.createdSpec.Attributes["dataset_role"])
	require.Equal(t, []storageio.ColumnInfo{sourceColumn("close")}, meta.createdSpec.Columns)
	require.Equal(t, "因子结果", meta.createdSpec.Name)
	require.LessOrEqual(t, len([]rune(meta.createdSpec.Name)), 10)
	require.Equal(t, storageio.DatasetStatusActive, meta.datasets[resultKey("crypto", set.ResultDatasetID)].Status)
	require.Equal(t, []string{"create", "activate"}, meta.writeOps)
}

func TestCreateSetCanonicalizesUppercaseFrequency(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	source := meta.datasets[resultKey("crypto", "dataset_prices")]
	source.Freqs = []string{"1H"}
	meta.datasets[resultKey("crypto", "dataset_prices")] = source
	set := newSet()
	set.Freq = "1H"

	created, err := NewService(db, meta, WithFactorsDir(t.TempDir())).CreateSet(context.Background(), set)
	require.NoError(t, err)
	require.Equal(t, "1h", created.Freq)
	require.Equal(t, "fset_prices_1h", created.SetID)
	require.Equal(t, "dataset_factor_prices_1h", created.ResultDatasetID)
}

func TestCreateSetRetryResumesFromPending(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	meta.activateErrs = 1
	svc := NewService(db, meta, WithFactorsDir(t.TempDir()))

	_, err := svc.CreateSet(context.Background(), newSet())
	require.ErrorContains(t, err, "activate")
	set, err := db.GetSet(context.Background(), domain.SetID("dataset_prices", "1m"))
	require.NoError(t, err)
	require.Equal(t, domain.SetStatusPending, set.Status)

	set, err = svc.CreateSet(context.Background(), newSet())
	require.NoError(t, err)
	require.Equal(t, domain.SetStatusEnabled, set.Status)
	require.Equal(t, 1, meta.createdDatasets)
	require.Equal(t, 2, meta.createCalls)
	require.Equal(t, storageio.DatasetStatusActive, meta.datasets[resultKey("crypto", set.ResultDatasetID)].Status)
}

func TestCreateSetRejectsFactorResultSource(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	source := meta.datasets[resultKey("crypto", "dataset_prices")]
	source.Attributes = map[string]string{"dataset_role": storageio.DatasetRoleFactorResult}
	meta.datasets[resultKey("crypto", "dataset_prices")] = source
	svc := NewService(db, meta)

	_, err := svc.CreateSet(context.Background(), newSet())
	require.ErrorContains(t, err, "factor_result")
	_, err = db.GetSet(context.Background(), domain.SetID("dataset_prices", "1m"))
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestCreateSetRejectsMissingFreq(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := NewService(db, meta)
	set := newSet()
	set.Freq = ""

	_, err := svc.CreateSet(context.Background(), set)
	require.ErrorContains(t, err, "freq")
	require.Empty(t, meta.writeOps)
}

func TestDeleteSetKeepsResultDatasetUnlessPurging(t *testing.T) {
	for _, purge := range []bool{false, true} {
		t.Run(fmt.Sprintf("purge_%t", purge), func(t *testing.T) {
			db := openCatalogStore(t)
			meta := newMetadataFake()
			svc := NewService(db, meta, WithFactorsDir(t.TempDir()))
			set, err := svc.CreateSet(context.Background(), newSet())
			require.NoError(t, err)
			require.NoError(t, db.SetSetStatus(context.Background(), set.SetID, domain.SetStatusEnabled, domain.SetStatusDisabled))

			require.NoError(t, svc.DeleteSet(context.Background(), set.SetID, purge))
			_, err = db.GetSet(context.Background(), set.SetID)
			require.ErrorIs(t, err, gorm.ErrRecordNotFound)
			dataset, datasetErr := meta.GetDataset(context.Background(), "crypto", set.ResultDatasetID)
			if purge {
				require.Error(t, datasetErr)
			} else {
				require.NoError(t, datasetErr)
				require.Equal(t, storageio.DatasetStatusActive, dataset.Status)
			}
		})
	}
}

func TestReconcileResumesDurableResultDatasetPurge(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := NewService(db, meta, WithFactorsDir(t.TempDir()))
	set, err := svc.CreateSet(context.Background(), newSet())
	require.NoError(t, err)
	require.NoError(t, db.SetSetStatus(context.Background(), set.SetID, domain.SetStatusEnabled, domain.SetStatusDisabled))
	meta.deleteErrs = 1

	err = svc.DeleteSet(context.Background(), set.SetID, true)
	require.ErrorContains(t, err, "temporary metadata failure")
	pending, err := db.GetSet(context.Background(), set.SetID)
	require.NoError(t, err)
	require.Equal(t, domain.SetStatusDeleting, pending.Status)
	_, err = svc.SetSetStatus(context.Background(), set.SetID, domain.SetStatusEnabled)
	require.ErrorContains(t, err, "purge is pending")
	_, err = svc.CreateFactor(context.Background(), testFactor())
	require.ErrorContains(t, err, "purge is pending")

	// Reconcile runs at startup and periodically. It resumes after failures at
	// any point after the durable intent, including a crash after physical purge.
	require.NoError(t, svc.Reconcile(context.Background()))
	_, err = db.GetSet(context.Background(), set.SetID)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	_, err = meta.GetDataset(context.Background(), set.SpaceID, set.ResultDatasetID)
	require.Error(t, err)
}

func TestDeletingSetRejectsLifecycleMutation(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := NewService(db, meta, WithFactorsDir(t.TempDir()))
	set, err := svc.CreateSet(context.Background(), newSet())
	require.NoError(t, err)
	require.NoError(t, db.SetSetStatus(context.Background(), set.SetID, domain.SetStatusEnabled, domain.SetStatusDisabled))
	require.NoError(t, db.SetSetStatus(context.Background(), set.SetID, domain.SetStatusDisabled, domain.SetStatusDeleting))

	retry := set
	retry.Status = domain.SetStatusPending
	_, err = svc.CreateSet(context.Background(), retry)
	require.ErrorContains(t, err, "purge is pending")
	_, err = svc.UpdateSetSubjects(context.Background(), set.SetID, domain.SubjectModeInclude, []string{"BTC"})
	require.ErrorContains(t, err, "purge is pending")
	_, err = svc.SetSetStatus(context.Background(), set.SetID, domain.SetStatusDisabled)
	require.ErrorContains(t, err, "purge is pending")
	_, err = svc.CreateFactor(context.Background(), testFactor())
	require.ErrorContains(t, err, "purge is pending")

	// Even if an invariant-breaking writer inserted a member, factor mutations
	// still cannot alter the pending purge into an enabled or stuck state.
	factor := testFactor()
	factor.Status = domain.FactorStatusDisabled
	require.NoError(t, db.CreateFactor(context.Background(), factor))
	_, err = svc.UpdateFactor(context.Background(), factor)
	require.ErrorContains(t, err, "purge is pending")
	_, _, err = svc.SetFactorStatus(context.Background(), factor.FactorID, domain.FactorStatusDisabled)
	require.ErrorContains(t, err, "purge is pending")
	err = svc.DeleteFactor(context.Background(), factor.FactorID)
	require.ErrorContains(t, err, "purge is pending")
	stored, err := db.GetSet(context.Background(), set.SetID)
	require.NoError(t, err)
	require.Equal(t, domain.SetStatusDeleting, stored.Status)
}

func TestReconcileFinishesPurgeAfterStorageMetadataWasAlreadyDeleted(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := NewService(db, meta, WithFactorsDir(t.TempDir()))
	set, err := svc.CreateSet(context.Background(), newSet())
	require.NoError(t, err)
	require.NoError(t, db.SetSetStatus(context.Background(), set.SetID, domain.SetStatusEnabled, domain.SetStatusDisabled))
	require.NoError(t, db.SetSetStatus(context.Background(), set.SetID, domain.SetStatusDisabled, domain.SetStatusDeleting))
	// Models process death after Storage's metadata commit and before the local
	// Factor set row is removed. DeleteDataset is idempotent for the retry.
	require.NoError(t, meta.DeleteDataset(context.Background(), set.SpaceID, set.ResultDatasetID))
	require.NoError(t, svc.Reconcile(context.Background()))
	_, err = db.GetSet(context.Background(), set.SetID)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestCreateFactorValidatesAgainstSourceColumnsAndLoadsSource(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)
	checker := &sourceCheckerFake{err: errors.New("syntax error")}
	svc.sourceChecker = checker

	_, err := svc.CreateFactor(context.Background(), testFactor())
	require.ErrorContains(t, err, "syntax error")
	require.Equal(t, 1, checker.calls)
	factorPath := filepath.Join(svc.artifacts.FactorsDir, testFactor().Name, domain.SourceHash(testFactor().SourceCode)+".py")
	_, statErr := os.Stat(factorPath)
	require.ErrorIs(t, statErr, os.ErrNotExist)
	_, err = db.GetFactor(context.Background(), "momentum")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)

	svc.sourceChecker = &sourceCheckerFake{}
	unknown := testFactor()
	unknown.InputColumns = []string{"missing"}
	_, err = svc.CreateFactor(context.Background(), unknown)
	require.ErrorContains(t, err, "unknown input column")
	_, err = svc.CreateFactor(context.Background(), testFactor())
	require.NoError(t, err)
	resultColumns, err := meta.ListColumns(context.Background(), "crypto", domain.ResultDatasetID("dataset_prices", "1m"))
	require.NoError(t, err)
	require.NotContains(t, metadataColumnNames(resultColumns), "momentum")
}

func TestEnableFactorAddsColumnsThenSubmitsRecalc(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)
	now := time.Date(2026, 10, 4, 10, 3, 37, 0, time.UTC)
	svc.now = func() time.Time { return now }
	recalc := &recalcFake{db: db}
	svc.recalc = recalc
	_, err := svc.CreateFactor(context.Background(), testFactor())
	require.NoError(t, err)
	meta.writeOps = nil

	factor, jobID, err := svc.SetFactorStatus(context.Background(), "momentum", domain.FactorStatusEnabled)
	require.NoError(t, err)
	require.Equal(t, domain.FactorStatusEnabled, factor.Status)
	require.NotEmpty(t, jobID)
	require.Equal(t, "enable-momentum", jobID)
	require.Equal(t, []string{"upsert_columns"}, meta.writeOps)
	require.Equal(t, []string{"disabled"}, recalc.order)
	require.Equal(t, domain.SetID("dataset_prices", "1m"), recalc.set.SetID)
	require.Equal(t, []string{"momentum"}, factorIDs(recalc.factors))
	require.Equal(t, time.Date(2026, 9, 4, 10, 3, 0, 0, time.UTC), recalc.start)
	require.Equal(t, time.Date(2026, 10, 4, 10, 3, 0, 0, time.UTC), recalc.end)
	var output storageio.ColumnInfo
	for _, col := range meta.columns[resultKey("crypto", domain.ResultDatasetID("dataset_prices", "1m"))] {
		if col.ColumnName == "momentum" {
			output = col
		}
	}
	require.Equal(t, storageio.ColumnOriginFactor, output.OriginType)
	require.Equal(t, map[string]string{
		"display_name": "momentum", "factor_output": "momentum", "origin_factor_id": "momentum",
	}, output.Attributes)
}

func TestUnlimitedRetentionBackfillStartsAtEarliestSourcePeriod(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)
	now := time.Date(2026, 10, 4, 10, 3, 37, 0, time.UTC)
	svc.now = func() time.Time { return now }
	set, err := db.GetSet(context.Background(), domain.SetID("dataset_prices", "1m"))
	require.NoError(t, err)
	result := meta.datasets[resultKey(set.SpaceID, set.ResultDatasetID)]
	result.KeepDuration = "0"
	meta.datasets[resultKey(set.SpaceID, set.ResultDatasetID)] = result
	earliest := time.Date(2025, 1, 2, 3, 4, 25, 0, time.UTC)
	provider := &earliestPeriodFake{period: earliest, found: true}
	svc.earliestPeriod = provider

	start, end, err := svc.backfillWindow(context.Background(), set)
	require.NoError(t, err)
	require.Equal(t, time.Date(2025, 1, 2, 3, 4, 0, 0, time.UTC), start)
	require.Equal(t, time.Date(2026, 10, 4, 10, 3, 0, 0, time.UTC), end)
	require.Equal(t, set.SpaceID, provider.spaceID)
	require.Equal(t, set.SourceDatasetID, provider.datasetID)
	require.Equal(t, set.Freq, provider.freq)
}

func TestUnlimitedRetentionWithoutSourceRowsUsesOneCompletedPeriod(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)
	set, err := db.GetSet(context.Background(), domain.SetID("dataset_prices", "1m"))
	require.NoError(t, err)
	result := meta.datasets[resultKey(set.SpaceID, set.ResultDatasetID)]
	result.KeepDuration = "0"
	meta.datasets[resultKey(set.SpaceID, set.ResultDatasetID)] = result
	svc.earliestPeriod = &earliestPeriodFake{}

	start, end, err := svc.backfillWindow(context.Background(), set)
	require.NoError(t, err)
	require.Equal(t, end.Add(-time.Minute), start)
}

func TestEnableFactorSubmitFailureLeavesFactorDisabled(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)
	recalc := &recalcFake{db: db, err: errors.New("durable job store unavailable")}
	svc.recalc = recalc
	_, err := svc.CreateFactor(context.Background(), testFactor())
	require.NoError(t, err)

	_, _, err = svc.SetFactorStatus(context.Background(), "momentum", domain.FactorStatusEnabled)
	require.ErrorContains(t, err, "durable job store unavailable")
	stored, err := db.GetFactor(context.Background(), "momentum")
	require.NoError(t, err)
	require.Equal(t, domain.FactorStatusDisabled, stored.Status)
	require.Equal(t, []string{"disabled"}, recalc.order)
}

func TestEnableFactorIsIdempotentAfterBackfillIntentIsAccepted(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)
	recalc := &recalcFake{db: db}
	svc.recalc = recalc
	_, err := svc.CreateFactor(context.Background(), testFactor())
	require.NoError(t, err)

	_, _, err = svc.SetFactorStatus(context.Background(), "momentum", domain.FactorStatusEnabled)
	require.NoError(t, err)
	_, _, err = svc.SetFactorStatus(context.Background(), "momentum", domain.FactorStatusEnabled)
	require.NoError(t, err)
	require.Len(t, recalc.order, 1)
}

func TestDisableFactorKeepsColumns(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)
	_, err := svc.CreateFactor(context.Background(), testFactor())
	require.NoError(t, err)
	_, _, err = svc.SetFactorStatus(context.Background(), "momentum", domain.FactorStatusEnabled)
	require.NoError(t, err)
	before := append([]storageio.ColumnInfo(nil), meta.columns[resultKey("crypto", domain.ResultDatasetID("dataset_prices", "1m"))]...)
	meta.writeOps = nil

	factor, jobID, err := svc.SetFactorStatus(context.Background(), "momentum", domain.FactorStatusDisabled)
	require.NoError(t, err)
	require.Equal(t, domain.FactorStatusDisabled, factor.Status)
	require.Empty(t, jobID)
	require.Empty(t, meta.writeOps)
	require.Equal(t, before, meta.columns[resultKey("crypto", domain.ResultDatasetID("dataset_prices", "1m"))])
}

func TestDeleteFactorRequiresDisabled(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)
	_, err := svc.CreateFactor(context.Background(), testFactor())
	require.NoError(t, err)
	_, _, err = svc.SetFactorStatus(context.Background(), "momentum", domain.FactorStatusEnabled)
	require.NoError(t, err)

	err = svc.DeleteFactor(context.Background(), "momentum")
	require.ErrorContains(t, err, "disabled")
	_, err = db.GetFactor(context.Background(), "momentum")
	require.NoError(t, err)
}

func TestUpdateFactorRequiresDisabled(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)
	factor, err := svc.CreateFactor(context.Background(), testFactor())
	require.NoError(t, err)
	factor.Status = domain.FactorStatusEnabled
	_, _, err = svc.SetFactorStatus(context.Background(), "momentum", domain.FactorStatusEnabled)
	require.NoError(t, err)
	factor.SourceCode = "def compute(frame, context): return frame['close'] + 1"
	factor.SourceHash = ""

	_, err = svc.UpdateFactor(context.Background(), factor)
	require.ErrorContains(t, err, "disabled")
	got, err := db.GetFactor(context.Background(), "momentum")
	require.NoError(t, err)
	require.NotEqual(t, factor.SourceCode, got.SourceCode)
}

func TestReconcileSetAddsNewSourceColumns(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)
	meta.sourceColumns = append(meta.sourceColumns, sourceColumn("volume"))
	meta.writeOps = nil

	require.NoError(t, svc.ReconcileSet(context.Background(), domain.SetID("dataset_prices", "1m")))
	require.Equal(t, []string{"upsert_columns"}, meta.writeOps)
	require.ElementsMatch(t, []storageio.ColumnInfo{sourceColumn("close"), sourceColumn("volume")}, meta.columns[resultKey("crypto", domain.ResultDatasetID("dataset_prices", "1m"))])
}

func TestReconcileRestoresMissingFactorArtifactsFromSQLite(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)
	factor, err := svc.CreateFactor(context.Background(), testFactor())
	require.NoError(t, err)
	path := filepath.Join(svc.artifacts.FactorsDir, factor.Name, factor.SourceHash+".py")
	require.NoError(t, os.Remove(path))

	require.NoError(t, svc.Reconcile(context.Background()))
	restored, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, factor.SourceCode, string(restored))
}

func TestReconcileRejectsSourceColumnCollisionWithEnabledFactor(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)
	recalc := &recalcFake{db: db}
	svc.recalc = recalc
	_, err := svc.CreateFactor(context.Background(), testFactor())
	require.NoError(t, err)
	_, _, err = svc.SetFactorStatus(context.Background(), "momentum", domain.FactorStatusEnabled)
	require.NoError(t, err)
	meta.sourceColumns = append(meta.sourceColumns, sourceColumn("momentum"))
	meta.writeOps = nil

	err = svc.ReconcileSet(context.Background(), domain.SetID("dataset_prices", "1m"))
	require.ErrorContains(t, err, `source column "momentum" collides with enabled factor output`)
	require.Empty(t, meta.writeOps)
}

func TestReconcileContinuesAfterOneSetFailure(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	secondSource := meta.datasets[resultKey("crypto", "dataset_zzz")]
	secondSource = storageio.DatasetInfo{
		SpaceID: "crypto", DatasetID: "dataset_zzz", DataSourceID: "other", DataNodeID: "storage-node-0",
		Name: "Other", DataKind: storageio.DataKindTimeSeries, Freqs: []string{"1m"}, KeepDuration: "720h",
		Status: storageio.DatasetStatusActive, Attributes: map[string]string{},
	}
	meta.datasets[resultKey("crypto", "dataset_zzz")] = secondSource
	meta.columns[resultKey("crypto", "dataset_zzz")] = []storageio.ColumnInfo{sourceColumn("close")}
	meta.sourceColumnsByDataset = map[string][]storageio.ColumnInfo{"dataset_zzz": {sourceColumn("close")}}
	svc := NewService(db, meta, WithFactorsDir(t.TempDir()))
	first, err := svc.CreateSet(context.Background(), newSet())
	require.NoError(t, err)
	secondInput := newSet()
	secondInput.SourceDatasetID = "dataset_zzz"
	secondInput.SetID = ""
	secondInput.ResultDatasetID = ""
	second, err := svc.CreateSet(context.Background(), secondInput)
	require.NoError(t, err)
	firstSet, err := db.GetSet(context.Background(), first.SetID)
	require.NoError(t, err)
	firstSet.Status = domain.SetStatusEnabled
	secondSet, err := db.GetSet(context.Background(), second.SetID)
	require.NoError(t, err)
	secondSet.Status = domain.SetStatusEnabled
	require.NoError(t, db.UpdateSet(context.Background(), firstSet))
	require.NoError(t, db.UpdateSet(context.Background(), secondSet))
	meta.datasets[resultKey("crypto", first.SourceDatasetID)] = storageio.DatasetInfo{}
	meta.sourceColumnsByDataset[second.SourceDatasetID] = []storageio.ColumnInfo{sourceColumn("close"), sourceColumn("volume")}
	meta.writeOps = nil

	err = svc.Reconcile(context.Background())
	require.ErrorContains(t, err, "reconcile factor set")
	columns, err := meta.ListColumns(context.Background(), "crypto", second.ResultDatasetID)
	require.NoError(t, err)
	require.Contains(t, metadataColumnNames(columns), "volume")
}

func TestFactorStatusChangesNotifyTrigger(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)
	notifier := &notifierFake{}
	svc.notifier = notifier
	_, err := svc.CreateFactor(context.Background(), testFactor())
	require.NoError(t, err)
	_, _, err = svc.SetFactorStatus(context.Background(), "momentum", domain.FactorStatusEnabled)
	require.NoError(t, err)
	_, _, err = svc.SetFactorStatus(context.Background(), "momentum", domain.FactorStatusDisabled)
	require.NoError(t, err)
	require.Equal(t, 2, notifier.calls)
}

func TestLifecycleOpsSerializeWithPeriodLock(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)
	unlock := svc.Locks().Lock(domain.SetID("dataset_prices", "1m"))
	done := make(chan struct{})
	started := make(chan struct{})
	go func() {
		close(started)
		defer close(done)
		_, _ = svc.UpdateSetSubjects(context.Background(), domain.SetID("dataset_prices", "1m"), domain.SubjectModeInclude, []string{"BTCUSDT"})
	}()
	<-started
	select {
	case <-done:
		t.Fatal("lifecycle operation did not wait for the shared set lock")
	case <-time.After(30 * time.Millisecond):
	}
	unlock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("lifecycle operation remained blocked after releasing the set lock")
	}
}

func TestArtifactsMaterializeImmutableSource(t *testing.T) {
	root := t.TempDir()
	artifacts := Artifacts{FactorsDir: root}
	factor := testFactor()
	factor.SourceHash = domain.SourceHash(factor.SourceCode)
	path, err := artifacts.Materialize(factor)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(root, factor.Name, factor.SourceHash+".py"), path)
	factor.SourceCode = "different source"
	_, err = artifacts.Materialize(factor)
	require.ErrorContains(t, err, "source hash")
}

func openCatalogStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "factor.db")})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db
}

func newReadyService(t *testing.T, db *store.Store, meta *metadataFake) *Service {
	t.Helper()
	svc := NewService(db, meta, WithFactorsDir(t.TempDir()))
	_, err := svc.CreateSet(context.Background(), newSet())
	require.NoError(t, err)
	svc.sourceChecker = &sourceCheckerFake{}
	svc.recalc = &recalcFake{db: db}
	return svc
}

func newSet() domain.FactorSet {
	return domain.FactorSet{
		SpaceID: "crypto", SourceDatasetID: "dataset_prices", Freq: "1m",
		SubjectMode: domain.SubjectModeAll, Subjects: []string{},
	}
}

func testFactor() domain.FactorDef {
	return domain.FactorDef{
		FactorID: "momentum", SetID: domain.SetID("dataset_prices", "1m"), Name: "momentum",
		FactorType: domain.FactorTypeTimeSeries, SourceCode: "def compute(frame, context):\n    return frame['close']",
		InputColumns: []string{"close"}, Outputs: []string{"momentum"}, ParamsJSON: "{}",
		LookbackPeriods: 3, Status: domain.FactorStatusDisabled,
	}
}

func factorIDs(factors []domain.FactorDef) []string {
	ids := make([]string, len(factors))
	for i, factor := range factors {
		ids[i] = factor.FactorID
	}
	return ids
}

func metadataColumnNames(columns []storageio.ColumnInfo) []string {
	names := make([]string, len(columns))
	for i, col := range columns {
		names[i] = col.ColumnName
	}
	return names
}

func resultKey(spaceID, datasetID string) string { return spaceID + "/" + datasetID }

func sourceColumn(name string) storageio.ColumnInfo {
	return storageio.ColumnInfo{ColumnName: name, OriginType: storageio.ColumnOriginField, OriginID: name, ValueType: storageio.ColumnTypeDouble, Status: storageio.ColumnStatusActive, Attributes: map[string]string{"display_name": "收盘价"}}
}

type metadataFake struct {
	mu                     sync.Mutex
	datasets               map[string]storageio.DatasetInfo
	columns                map[string][]storageio.ColumnInfo
	sourceColumns          []storageio.ColumnInfo
	sourceColumnsByDataset map[string][]storageio.ColumnInfo
	createdSpec            storageio.ResultDatasetSpec
	createCalls            int
	createdDatasets        int
	activateErrs           int
	deleteErrs             int
	writeOps               []string
}

func newMetadataFake() *metadataFake {
	source := storageio.DatasetInfo{
		SpaceID: "crypto", DatasetID: "dataset_prices", DataSourceID: "binance", DataNodeID: "storage-node-0",
		Name: "Prices", Description: "Price bars", DataKind: storageio.DataKindTimeSeries, Freqs: []string{"1m"},
		KeepDuration: "720h", Status: storageio.DatasetStatusActive, Attributes: map[string]string{},
	}
	cols := []storageio.ColumnInfo{sourceColumn("close"), {
		ColumnName: "subject_id", OriginType: storageio.ColumnOriginSystem, ValueType: storageio.ColumnTypeString, Status: storageio.ColumnStatusActive,
	}}
	return &metadataFake{
		datasets: map[string]storageio.DatasetInfo{resultKey("crypto", "dataset_prices"): source},
		columns:  map[string][]storageio.ColumnInfo{}, sourceColumns: cols,
		sourceColumnsByDataset: map[string][]storageio.ColumnInfo{"dataset_prices": cols},
	}
}

func (f *metadataFake) GetDataset(_ context.Context, spaceID, datasetID string) (storageio.DatasetInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	dataset, ok := f.datasets[resultKey(spaceID, datasetID)]
	if !ok {
		return storageio.DatasetInfo{}, errors.New("dataset not found")
	}
	return dataset, nil
}

func (f *metadataFake) ListColumns(_ context.Context, spaceID, datasetID string) ([]storageio.ColumnInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if datasetID == "dataset_prices" {
		return append([]storageio.ColumnInfo(nil), f.sourceColumns...), nil
	}
	if columns, ok := f.sourceColumnsByDataset[datasetID]; ok {
		return append([]storageio.ColumnInfo(nil), columns...), nil
	}
	return append([]storageio.ColumnInfo(nil), f.columns[resultKey(spaceID, datasetID)]...), nil
}

func (f *metadataFake) CreateResultDataset(_ context.Context, spec storageio.ResultDatasetSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createCalls++
	f.writeOps = append(f.writeOps, "create")
	f.createdSpec = spec
	key := resultKey(spec.SpaceID, spec.DatasetID)
	if _, exists := f.datasets[key]; !exists {
		f.createdDatasets++
		attrs := map[string]string{"dataset_role": storageio.DatasetRoleFactorResult, "source_dataset_id": spec.SourceDatasetID}
		for name, value := range spec.Attributes {
			attrs[name] = value
		}
		f.datasets[key] = storageio.DatasetInfo{
			SpaceID: spec.SpaceID, DatasetID: spec.DatasetID, DataSourceID: spec.DataSourceID, DataNodeID: spec.DataNodeID,
			Name: spec.Name, Description: spec.Description, DataKind: spec.DataKind, Freqs: []string{spec.Frequency},
			KeepDuration: spec.KeepDuration, Status: storageio.DatasetStatusDisabled, Attributes: attrs,
		}
		f.columns[key] = append([]storageio.ColumnInfo(nil), spec.Columns...)
	}
	return nil
}

func (f *metadataFake) UpsertColumns(_ context.Context, spaceID, datasetID string, cols []storageio.ColumnInfo) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writeOps = append(f.writeOps, "upsert_columns")
	key := resultKey(spaceID, datasetID)
	current := make(map[string]storageio.ColumnInfo, len(f.columns[key]))
	for _, col := range f.columns[key] {
		current[col.ColumnName] = col
	}
	for _, col := range cols {
		current[col.ColumnName] = col
	}
	merged := make([]storageio.ColumnInfo, 0, len(current))
	for _, col := range current {
		merged = append(merged, col)
	}
	f.columns[key] = merged
	return nil
}

func (f *metadataFake) ActivateDataset(_ context.Context, spaceID, datasetID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writeOps = append(f.writeOps, "activate")
	if f.activateErrs > 0 {
		f.activateErrs--
		return errors.New("activate failed")
	}
	dataset := f.datasets[resultKey(spaceID, datasetID)]
	dataset.Status = storageio.DatasetStatusActive
	f.datasets[resultKey(spaceID, datasetID)] = dataset
	return nil
}

func (f *metadataFake) DeleteDataset(_ context.Context, spaceID, datasetID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writeOps = append(f.writeOps, "delete")
	if f.deleteErrs > 0 {
		f.deleteErrs--
		return errors.New("temporary metadata failure")
	}
	delete(f.datasets, resultKey(spaceID, datasetID))
	delete(f.columns, resultKey(spaceID, datasetID))
	return nil
}

type sourceCheckerFake struct {
	calls int
	err   error
}

type notifierFake struct{ calls int }

func (f *notifierFake) SetsChanged() { f.calls++ }

func (f *sourceCheckerFake) CheckSource(context.Context, domain.FactorDef, string) error {
	f.calls++
	return f.err
}

type recalcFake struct {
	db      *store.Store
	set     domain.FactorSet
	factors []domain.FactorDef
	start   time.Time
	end     time.Time
	order   []string
	err     error
}

type earliestPeriodFake struct {
	period                   time.Time
	found                    bool
	err                      error
	spaceID, datasetID, freq string
}

func (f *earliestPeriodFake) EarliestDatasetPeriod(_ context.Context, spaceID, datasetID, freq string) (time.Time, bool, error) {
	f.spaceID, f.datasetID, f.freq = spaceID, datasetID, freq
	return f.period, f.found, f.err
}

func (f *recalcFake) PrepareEnableBackfill(ctx context.Context, set domain.FactorSet, factor domain.FactorDef, start, end time.Time) (store.RecalcJob, error) {
	stored, err := f.db.GetFactor(ctx, factor.FactorID)
	if err != nil {
		return store.RecalcJob{}, err
	}
	f.order = append(f.order, stored.Status)
	f.set, f.factors, f.start, f.end = set, []domain.FactorDef{factor}, start, end
	if f.err != nil {
		return store.RecalcJob{}, f.err
	}
	return store.RecalcJob{
		JobID: "enable-" + factor.FactorID, RequestID: "enable-" + factor.FactorID,
		SetID: set.SetID, FactorIDs: []string{factor.FactorID}, Subjects: []string{"BTC-USDT"},
		StartTime: start.Unix(), EndTime: end.Unix(), Status: store.RecalcStatusAccepted,
	}, nil
}
