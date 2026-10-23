package catalog

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCreateSetCreatesAndActivatesResultDataset(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := NewService(db, meta)

	set, err := svc.CreateSet(context.Background(), newSet())
	require.NoError(t, err)
	require.Equal(t, domain.SetID("dataset_prices", "1m"), set.SetID)
	require.Equal(t, domain.ResultDatasetID(domain.SetID("dataset_prices", "1m")), set.ResultDatasetID)
	require.Equal(t, domain.SetStatusEnabled, set.Status)
	require.Equal(t, "storage-node-0", meta.createdSpec.DataNodeID)
	require.Equal(t, storageio.DatasetRoleFactorResult, meta.createdSpec.Attributes["dataset_role"])
	require.Equal(t, "factor", meta.createdSpec.Attributes["owner_module"])
	require.Equal(t, "factor", meta.createdSpec.Attributes["write_owner"])
	require.Equal(t, []storageio.ColumnInfo{sourceColumn("close")}, meta.createdSpec.Columns)
	require.Equal(t, "Prices因子", meta.createdSpec.Name)
	require.LessOrEqual(t, len([]rune(meta.createdSpec.Name)), 10)
	require.Equal(t, storageio.DatasetStatusActive, meta.datasets[resultKey("crypto", set.ResultDatasetID)].Status)
	require.Equal(t, []string{"create", "activate"}, meta.writeOps)
}

func TestCreateSetCanonicalizesUppercaseFrequency(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	source := meta.datasets[resultKey("crypto", "dataset_prices")]
	source.Freq = "1h"
	meta.datasets[resultKey("crypto", "dataset_prices")] = source
	set := newSet()
	set.Freq = "1H"

	created, err := NewService(db, meta).CreateSet(context.Background(), set)
	require.NoError(t, err)
	require.Equal(t, "1h", created.Freq)
	require.Equal(t, "fset_prices_1h", created.SetID)
	require.Equal(t, "dataset_factor_prices_1h", created.ResultDatasetID)
}

func TestCreateSetRejectsSpaceWithoutCalendar(t *testing.T) {
	svc := NewService(openCatalogStore(t), newMetadataFake())
	set := newSet()
	set.SpaceID = "stockcn"

	_, err := svc.CreateSet(context.Background(), set)

	require.ErrorIs(t, err, periodclock.ErrUnsupportedCalendar)
}

func TestCreateSetRetryResumesFromPending(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	meta.activateErrs = 1
	svc := NewService(db, meta)

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
			svc := NewService(db, meta)
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
	svc := NewService(db, meta)
	set, err := svc.CreateSet(context.Background(), newSet())
	require.NoError(t, err)
	require.NoError(t, db.SetSetStatus(context.Background(), set.SetID, domain.SetStatusEnabled, domain.SetStatusDisabled))
	_, err = svc.CreateFactor(context.Background(), testFactor())
	require.NoError(t, err)
	meta.deleteErrs = 1

	err = svc.DeleteSet(context.Background(), set.SetID, true)
	require.ErrorContains(t, err, "temporary metadata failure")
	pending, err := db.GetSet(context.Background(), set.SetID)
	require.NoError(t, err)
	require.Equal(t, domain.SetStatusDeleting, pending.Status)
	_, err = svc.SetSetStatus(context.Background(), set.SetID, domain.SetStatusEnabled)
	require.ErrorContains(t, err, "purge is pending")
	_, err = svc.AddFactorToSet(context.Background(), set.SetID, "", "momentum")
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
	svc := NewService(db, meta)
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

	// Even if an invariant-breaking writer inserted a member, member mutations
	// still cannot alter the pending purge into an enabled or stuck state.
	factor, err := svc.CreateFactor(context.Background(), testFactor())
	require.NoError(t, err)
	_, err = svc.AddFactorToSet(context.Background(), set.SetID, "", factor.FactorID)
	require.ErrorContains(t, err, "purge is pending")
	_, err = db.AddMember(context.Background(), set.SetID, factor.FactorID, factor.FactorID)
	require.NoError(t, err)
	_, _, err = svc.SetFactorMemberStatus(context.Background(), set.SetID, factor.FactorID, domain.MemberStatusEnabled)
	require.ErrorContains(t, err, "purge is pending")
	err = svc.RemoveFactorFromSet(context.Background(), set.SetID, factor.FactorID)
	require.ErrorContains(t, err, "purge is pending")
	stored, err := db.GetSet(context.Background(), set.SetID)
	require.NoError(t, err)
	require.Equal(t, domain.SetStatusDeleting, stored.Status)
}

func TestReconcileFinishesPurgeAfterStorageMetadataWasAlreadyDeleted(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := NewService(db, meta)
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

func TestCreateFactorDoesNotNeedSet(t *testing.T) {
	db := openCatalogStore(t)
	svc := NewService(db, newMetadataFake())

	factor := factorWithSpec("momentum", "outputs:\n  momentum: any_column_name\n")
	created, err := svc.CreateFactor(context.Background(), factor)
	require.NoError(t, err)
	require.Equal(t, "momentum", created.FactorID)
	info, err := svc.GetFactor(context.Background(), "momentum")
	require.NoError(t, err)
	require.Empty(t, info.Usages)
}

func TestCreateFactorRunsStaticValidation(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)

	_, err := svc.CreateFactor(context.Background(), factorWithSpec("momentum", "outputs:\n  momentum: ts_mean(close,\n"))
	require.ErrorContains(t, err, "编译失败")
	_, err = db.GetFactor(context.Background(), "momentum")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)

	_, err = svc.CreateFactor(context.Background(), factorWithSpec("momentum", "outputs:\n  series_tag: close\n"))
	require.Error(t, err)
	_, err = svc.CreateFactor(context.Background(), testFactor())
	require.NoError(t, err)
	resultColumns, err := meta.ListColumns(context.Background(), "crypto", domain.ResultDatasetID(domain.SetID("dataset_prices", "1m")))
	require.NoError(t, err)
	require.NotContains(t, metadataColumnNames(resultColumns), "momentum")
}

func TestAddFactorToSetValidatesAgainstSourceColumns(t *testing.T) {
	db := openCatalogStore(t)
	svc := newReadyService(t, db, newMetadataFake())
	unknown := factorWithSpec("unknown_input", "outputs:\n  unknown_input: missing\n")
	_, err := svc.CreateFactor(context.Background(), unknown)
	require.NoError(t, err)
	_, err = svc.AddFactorToSet(context.Background(), testSetID(), "", "unknown_input")
	require.ErrorContains(t, err, "unknown input column")

	collide := factorWithSpec("collide", "outputs:\n  close: close * 2\n")
	_, err = svc.CreateFactor(context.Background(), collide)
	require.NoError(t, err)
	_, err = svc.AddFactorToSet(context.Background(), testSetID(), "", "collide")
	require.ErrorContains(t, err, "与源列重名")
	_, err = db.GetMember(context.Background(), testSetID(), "collide")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestAddFactorToSetRejectsPendingOrDeletingSet(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)
	_, err := svc.CreateFactor(context.Background(), testFactor())
	require.NoError(t, err)
	require.NoError(t, db.SetSetStatus(context.Background(), testSetID(), domain.SetStatusEnabled, domain.SetStatusDisabled))
	require.NoError(t, db.SetSetStatus(context.Background(), testSetID(), domain.SetStatusDisabled, domain.SetStatusDeleting))
	_, err = svc.AddFactorToSet(context.Background(), testSetID(), "", "momentum")
	require.ErrorContains(t, err, "purge is pending")

	pending := domain.FactorSet{
		SetID: "fset_pending_1m", SpaceID: "crypto", SourceDatasetID: "dataset_prices", Freq: "5m",
		SubjectMode: domain.SubjectModeAll, Subjects: []string{}, ResultDatasetID: "dataset_factor_pending", Status: domain.SetStatusPending,
	}
	require.NoError(t, db.CreateSet(context.Background(), pending))
	_, err = svc.AddFactorToSet(context.Background(), pending.SetID, "", "momentum")
	require.ErrorContains(t, err, "not ready")
}

func TestAddFactorToSetRejectsOutputOwnedByOtherFactor(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)
	other := testFactor()
	other.FactorID, other.Name = "other", "other"
	_, err := svc.CreateFactor(context.Background(), other)
	require.NoError(t, err)
	// A retained result column from a removed "other" member.
	require.NoError(t, meta.UpsertColumns(context.Background(), "crypto", domain.ResultDatasetID(domain.SetID("dataset_prices", "1m")),
		appendFactorColumns(nil, domain.MemberDef{Alias: other.FactorID, Factor: domain.MustDefinition(other.FactorID, other.Name, other.Spec)})))
	_, err = svc.CreateFactor(context.Background(), testFactor())
	require.NoError(t, err)

	_, err = svc.AddFactorToSet(context.Background(), testSetID(), "", "momentum")
	require.ErrorContains(t, err, "已属于")
}

func TestAddFactorToSetDoesNotAddColumnsOrSubmitRecalc(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)
	recalc := &recalcFake{db: db}
	svc.recalc = recalc
	_, err := svc.CreateFactor(context.Background(), testFactor())
	require.NoError(t, err)
	meta.writeOps = nil

	member, err := svc.AddFactorToSet(context.Background(), testSetID(), "", "momentum")
	require.NoError(t, err)
	require.Equal(t, domain.MemberStatusDisabled, member.Status)
	require.Equal(t, "momentum", member.Factor.FactorID)
	require.Empty(t, meta.writeOps)
	require.Empty(t, recalc.order)
}

func TestReaddAfterRemoveReusesRetainedColumns(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)
	addMember(t, svc, testSetID(), "momentum", domain.MemberStatusEnabled)
	_, _, err := svc.SetFactorMemberStatus(context.Background(), testSetID(), "momentum", domain.MemberStatusDisabled)
	require.NoError(t, err)
	require.NoError(t, svc.RemoveFactorFromSet(context.Background(), testSetID(), "momentum"))

	_, err = svc.AddFactorToSet(context.Background(), testSetID(), "", "momentum")
	require.NoError(t, err)
	_, _, err = svc.SetFactorMemberStatus(context.Background(), testSetID(), "momentum", domain.MemberStatusEnabled)
	require.NoError(t, err)
}

func TestEnableMemberRevalidatesThenAddsColumnsThenSubmitsRecalc(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)
	now := time.Date(2026, 10, 4, 10, 3, 37, 0, time.UTC)
	svc.now = func() time.Time { return now }
	svc.maxBackfillPeriods = 0 // 本用例验证窗口取结果数据集的保留期；期数上限见 TestBackfillWindowIsCappedToTheMostRecentPeriods
	recalc := &recalcFake{db: db}
	svc.recalc = recalc
	addMember(t, svc, testSetID(), "momentum", domain.MemberStatusDisabled)
	meta.writeOps = nil

	member, jobID, err := svc.SetFactorMemberStatus(context.Background(), testSetID(), "momentum", domain.MemberStatusEnabled)
	require.NoError(t, err)
	require.Equal(t, domain.MemberStatusEnabled, member.Status)
	require.NotEmpty(t, jobID)
	require.Equal(t, "enable-momentum", jobID)
	require.Equal(t, []string{"upsert_columns"}, meta.writeOps)
	require.Equal(t, []string{"disabled"}, recalc.order)
	require.Equal(t, domain.SetID("dataset_prices", "1m"), recalc.set.SetID)
	require.Equal(t, []string{"momentum"}, factorIDs(recalc.factors))
	require.Equal(t, time.Date(2026, 9, 4, 10, 3, 0, 0, time.UTC), recalc.start)
	require.Equal(t, time.Date(2026, 10, 4, 10, 3, 0, 0, time.UTC), recalc.end)
	var output storageio.ColumnInfo
	for _, col := range meta.columns[resultKey("crypto", domain.ResultDatasetID(domain.SetID("dataset_prices", "1m")))] {
		if col.ColumnName == "momentum" {
			output = col
		}
	}
	require.Equal(t, storageio.ColumnOriginFactor, output.OriginType)
	require.Equal(t, map[string]string{
		"display_name": "momentum", "factor_output": "momentum", "origin_factor_id": "momentum",
		"member_alias": "momentum", "factor_id": "momentum", "category_id": domain.DefaultCategoryID, "column_kind": ColumnKindOutput, "output_name": "momentum",
	}, output.Attributes)
}

func TestUnlimitedRetentionBackfillStartsAtEarliestSourcePeriod(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)
	now := time.Date(2026, 10, 4, 10, 3, 37, 0, time.UTC)
	svc.now = func() time.Time { return now }
	svc.maxBackfillPeriods = 0
	set, err := db.GetSet(context.Background(), domain.SetID("dataset_prices", "1m"))
	require.NoError(t, err)
	result := meta.datasets[resultKey(set.SpaceID, set.ResultDatasetID)]
	result.Retention = "forever"
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
	result.Retention = "forever"
	meta.datasets[resultKey(set.SpaceID, set.ResultDatasetID)] = result
	svc.earliestPeriod = &earliestPeriodFake{}

	start, end, err := svc.backfillWindow(context.Background(), set)
	require.NoError(t, err)
	require.Equal(t, end.Add(-time.Minute), start)
}

func TestEnableMemberSubmitFailureLeavesMemberDisabled(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)
	recalc := &recalcFake{db: db, err: errors.New("durable job store unavailable")}
	svc.recalc = recalc
	addMember(t, svc, testSetID(), "momentum", domain.MemberStatusDisabled)

	_, _, err := svc.SetFactorMemberStatus(context.Background(), testSetID(), "momentum", domain.MemberStatusEnabled)
	require.ErrorContains(t, err, "durable job store unavailable")
	stored, err := db.GetMember(context.Background(), testSetID(), "momentum")
	require.NoError(t, err)
	require.Equal(t, domain.MemberStatusDisabled, stored.Status)
	require.Equal(t, []string{"disabled"}, recalc.order)
}

func TestEnableMemberIsIdempotentAfterBackfillIntentIsAccepted(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)
	recalc := &recalcFake{db: db}
	svc.recalc = recalc
	addMember(t, svc, testSetID(), "momentum", domain.MemberStatusDisabled)

	_, _, err := svc.SetFactorMemberStatus(context.Background(), testSetID(), "momentum", domain.MemberStatusEnabled)
	require.NoError(t, err)
	_, _, err = svc.SetFactorMemberStatus(context.Background(), testSetID(), "momentum", domain.MemberStatusEnabled)
	require.NoError(t, err)
	require.Len(t, recalc.order, 1)
}

func TestEnableMemberRevalidatesAgainstCurrentSourceColumns(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)
	addMember(t, svc, testSetID(), "momentum", domain.MemberStatusDisabled)
	meta.sourceColumns = []storageio.ColumnInfo{sourceColumn("volume")}

	_, _, err := svc.SetFactorMemberStatus(context.Background(), testSetID(), "momentum", domain.MemberStatusEnabled)
	require.ErrorContains(t, err, "unknown input column")
	stored, err := db.GetMember(context.Background(), testSetID(), "momentum")
	require.NoError(t, err)
	require.Equal(t, domain.MemberStatusDisabled, stored.Status)
}

func TestDisableMemberKeepsColumns(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)
	addMember(t, svc, testSetID(), "momentum", domain.MemberStatusEnabled)
	before := append([]storageio.ColumnInfo(nil), meta.columns[resultKey("crypto", domain.ResultDatasetID(domain.SetID("dataset_prices", "1m")))]...)
	meta.writeOps = nil

	member, jobID, err := svc.SetFactorMemberStatus(context.Background(), testSetID(), "momentum", domain.MemberStatusDisabled)
	require.NoError(t, err)
	require.Equal(t, domain.MemberStatusDisabled, member.Status)
	require.Empty(t, jobID)
	require.Empty(t, meta.writeOps)
	require.Equal(t, before, meta.columns[resultKey("crypto", domain.ResultDatasetID(domain.SetID("dataset_prices", "1m")))])
}

func TestRemoveRequiresDisabledMember(t *testing.T) {
	db := openCatalogStore(t)
	svc := newReadyService(t, db, newMetadataFake())
	addMember(t, svc, testSetID(), "momentum", domain.MemberStatusEnabled)

	err := svc.RemoveFactorFromSet(context.Background(), testSetID(), "momentum")
	require.ErrorIs(t, err, store.ErrConflict)
	_, _, err = svc.SetFactorMemberStatus(context.Background(), testSetID(), "momentum", domain.MemberStatusDisabled)
	require.NoError(t, err)
	require.NoError(t, svc.RemoveFactorFromSet(context.Background(), testSetID(), "momentum"))
	_, err = svc.GetFactor(context.Background(), "momentum")
	require.NoError(t, err, "removing a member keeps the definition")
}

func TestSameDefinitionEnablesIndependentlyInTwoSets(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)
	hourlySource := meta.datasets[resultKey("crypto", "dataset_prices")]
	hourlySource.DatasetID, hourlySource.Name, hourlySource.Freq = "dataset_prices_1h", "Hourly prices", "1h"
	meta.datasets[resultKey("crypto", hourlySource.DatasetID)] = hourlySource
	meta.sourceColumnsByDataset[hourlySource.DatasetID] = meta.sourceColumnsByDataset["dataset_prices"]
	hourly := newSet()
	hourly.SourceDatasetID, hourly.Freq = hourlySource.DatasetID, "1h"
	_, err := svc.CreateSet(context.Background(), hourly)
	require.NoError(t, err)
	hourlyID := domain.SetID(hourlySource.DatasetID, "1h")
	addMember(t, svc, testSetID(), "momentum", domain.MemberStatusEnabled)
	_, err = svc.AddFactorToSet(context.Background(), hourlyID, "", "momentum")
	require.NoError(t, err)

	first, err := db.GetMember(context.Background(), testSetID(), "momentum")
	require.NoError(t, err)
	second, err := db.GetMember(context.Background(), hourlyID, "momentum")
	require.NoError(t, err)
	require.Equal(t, domain.MemberStatusEnabled, first.Status)
	require.Equal(t, domain.MemberStatusDisabled, second.Status)
	info, err := svc.GetFactor(context.Background(), "momentum")
	require.NoError(t, err)
	require.Len(t, info.Usages, 2)
}

func TestUpdateFactorRejectedWhenAnyEnabledMember(t *testing.T) {
	db := openCatalogStore(t)
	svc := newReadyService(t, db, newMetadataFake())
	addMember(t, svc, testSetID(), "momentum", domain.MemberStatusEnabled)
	factor := factorWithSpec("momentum", "outputs:\n  momentum: ts_mean(close, 3) + 1\n")

	_, err := svc.UpdateFactor(context.Background(), factor)
	require.ErrorIs(t, err, store.ErrConflict)
	require.ErrorContains(t, err, "disable")
	got, err := db.GetFactor(context.Background(), "momentum")
	require.NoError(t, err)
	require.NotContains(t, got.Spec, "+ 1")

	_, _, err = svc.SetFactorMemberStatus(context.Background(), testSetID(), "momentum", domain.MemberStatusDisabled)
	require.NoError(t, err)
	updated, err := svc.UpdateFactor(context.Background(), factor)
	require.NoError(t, err)
	require.Contains(t, updated.Spec, "+ 1")
	require.Equal(t, domain.SpecHash(updated.Spec), updated.SpecHash)
}

func TestUpdateFactorRevalidatesAllReferencingSetsAndNamesTheFailingSet(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)
	addMember(t, svc, testSetID(), "momentum", domain.MemberStatusDisabled)
	factor := factorWithSpec("momentum", "outputs:\n  momentum: missing\n")

	_, err := svc.UpdateFactor(context.Background(), factor)
	require.ErrorContains(t, err, testSetID())
	require.ErrorContains(t, err, "unknown input column")
	got, err := db.GetFactor(context.Background(), "momentum")
	require.NoError(t, err)
	require.Equal(t, []string{"close"}, got.InputColumns)
}

func TestDeleteFactorRejectedWhileReferenced(t *testing.T) {
	db := openCatalogStore(t)
	svc := newReadyService(t, db, newMetadataFake())
	addMember(t, svc, testSetID(), "momentum", domain.MemberStatusDisabled)

	err := svc.DeleteFactor(context.Background(), "momentum")
	require.ErrorIs(t, err, store.ErrConflict)
	require.NoError(t, svc.RemoveFactorFromSet(context.Background(), testSetID(), "momentum"))
	require.NoError(t, svc.DeleteFactor(context.Background(), "momentum"))
}

func TestDeleteSetRejectedWhileMembersExist(t *testing.T) {
	db := openCatalogStore(t)
	svc := newReadyService(t, db, newMetadataFake())
	addMember(t, svc, testSetID(), "momentum", domain.MemberStatusDisabled)
	require.NoError(t, db.SetSetStatus(context.Background(), testSetID(), domain.SetStatusEnabled, domain.SetStatusDisabled))

	err := svc.DeleteSet(context.Background(), testSetID(), false)
	require.ErrorContains(t, err, "members")
}

func TestListFactorsWithAndWithoutSet(t *testing.T) {
	db := openCatalogStore(t)
	svc := newReadyService(t, db, newMetadataFake())
	addMember(t, svc, testSetID(), "momentum", domain.MemberStatusEnabled)
	loose := factorWithSpec("loose", "outputs:\n  loose: close\n")
	_, err := svc.CreateFactor(context.Background(), loose)
	require.NoError(t, err)

	all, err := svc.ListFactors(context.Background(), "", "")
	require.NoError(t, err)
	require.Len(t, all, 2)
	require.Empty(t, all[0].Usages) // loose sorts first
	require.Equal(t, []domain.FactorUsage{{SetID: testSetID(), MemberAlias: "momentum", Status: domain.MemberStatusEnabled}}, all[1].Usages)

	inSet, err := svc.ListFactors(context.Background(), testSetID(), domain.MemberStatusDisabled)
	require.NoError(t, err)
	require.Empty(t, inSet)
	inSet, err = svc.ListFactors(context.Background(), testSetID(), domain.MemberStatusEnabled)
	require.NoError(t, err)
	require.Len(t, inSet, 1)
}

func TestAddAndUpdateDoNotInterleave(t *testing.T) {
	db := openCatalogStore(t)
	svc := newReadyService(t, db, newMetadataFake())
	_, err := svc.CreateFactor(context.Background(), testFactor())
	require.NoError(t, err)
	_, err = svc.AddFactorToSet(context.Background(), testSetID(), "", "momentum")
	require.NoError(t, err)
	unlock, err := svc.Locks().LockFactorContext(context.Background(), "momentum")
	require.NoError(t, err)
	done := make(chan error, 3)
	go func() {
		_, err := svc.AddFactorToSet(context.Background(), testSetID(), "slow", "momentum")
		done <- err
	}()
	go func() { _, err := svc.UpdateFactor(context.Background(), testFactor()); done <- err }()
	go func() {
		_, _, err := svc.SetFactorMemberStatus(context.Background(), testSetID(), "momentum", domain.MemberStatusDisabled)
		done <- err
	}()
	select {
	case <-done:
		t.Fatal("member or definition operation did not wait for the definition lock")
	case <-time.After(50 * time.Millisecond):
	}
	unlock()
	for i := 0; i < 3; i++ {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("operation remained blocked after releasing the definition lock")
		}
	}
}

func TestReconcileSetAddsNewSourceColumns(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)
	meta.sourceColumns = append(meta.sourceColumns, sourceColumn("volume"))
	meta.writeOps = nil

	require.NoError(t, svc.ReconcileSet(context.Background(), domain.SetID("dataset_prices", "1m")))
	require.Equal(t, []string{"upsert_columns"}, meta.writeOps)
	require.ElementsMatch(t, []storageio.ColumnInfo{sourceColumn("close"), sourceColumn("volume")}, meta.columns[resultKey("crypto", domain.ResultDatasetID(domain.SetID("dataset_prices", "1m")))])
}

func TestReconcileRejectsSourceColumnCollisionWithEnabledMember(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)
	recalc := &recalcFake{db: db}
	svc.recalc = recalc
	addMember(t, svc, testSetID(), "momentum", domain.MemberStatusEnabled)
	meta.sourceColumns = append(meta.sourceColumns, sourceColumn("momentum"))
	meta.writeOps = nil

	err := svc.ReconcileSet(context.Background(), domain.SetID("dataset_prices", "1m"))
	require.ErrorContains(t, err, `source column "momentum" collides with enabled member momentum`)
	require.Empty(t, meta.writeOps)
}

func TestReconcileContinuesAfterOneSetFailure(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	secondSource := meta.datasets[resultKey("crypto", "dataset_zzz")]
	secondSource = storageio.DatasetInfo{
		SpaceID: "crypto", DatasetID: "dataset_zzz", DataSourceID: "other", DataNodeID: "storage-node-0",
		Name: "Other", DataKind: storageio.DataKindTimeSeries, Freq: "1m", Retention: "720h",
		Status: storageio.DatasetStatusActive, Attributes: map[string]string{},
	}
	meta.datasets[resultKey("crypto", "dataset_zzz")] = secondSource
	meta.columns[resultKey("crypto", "dataset_zzz")] = []storageio.ColumnInfo{sourceColumn("close")}
	meta.sourceColumnsByDataset = map[string][]storageio.ColumnInfo{"dataset_zzz": {sourceColumn("close")}}
	svc := NewService(db, meta)
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

func openCatalogStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "factor.db")})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db
}

func newReadyService(t *testing.T, db *store.Store, meta *metadataFake) *Service {
	t.Helper()
	svc := NewService(db, meta)
	_, err := svc.CreateSet(context.Background(), newSet())
	require.NoError(t, err)
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
	return factorWithSpec("momentum", "outputs:\n  momentum: ts_mean(close, 3)\n")
}

// factorWithSpec 返回一个未规范化的请求定义（与接口收到的一样），输出列名由 spec 决定。
func factorWithSpec(factorID, spec string) domain.FactorDef {
	return domain.FactorDef{FactorID: factorID, Name: factorID, Spec: spec}
}

func testSetID() string { return domain.SetID("dataset_prices", "1m") }

// addMember registers the standard test factor (if missing) and attaches it to
// the set, enabling it when status asks for it.
func addMember(t *testing.T, svc *Service, setID, factorID, status string) {
	t.Helper()
	ctx := context.Background()
	if _, err := svc.db.GetFactor(ctx, factorID); err != nil {
		_, err = svc.CreateFactor(ctx, factorWithSpec(factorID, "outputs:\n  "+factorID+": ts_mean(close, 3)\n"))
		require.NoError(t, err)
	}
	_, err := svc.AddFactorToSet(ctx, setID, "", factorID)
	require.NoError(t, err)
	if status == domain.MemberStatusEnabled {
		_, _, err = svc.SetFactorMemberStatus(ctx, setID, factorID, domain.MemberStatusEnabled)
		require.NoError(t, err)
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
	getDatasetErr          error
	writeOps               []string
}

func newMetadataFake() *metadataFake {
	source := storageio.DatasetInfo{
		SpaceID: "crypto", DatasetID: "dataset_prices", DataSourceID: "binance", DataNodeID: "storage-node-0",
		Name: "Prices", Description: "Price bars", DataKind: storageio.DataKindTimeSeries, Freq: "1m", Status: storageio.DatasetStatusActive, Attributes: map[string]string{},
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
	if f.getDatasetErr != nil {
		return storageio.DatasetInfo{}, f.getDatasetErr
	}
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
			Name: spec.Name, Description: spec.Description, DataKind: spec.DataKind, Freq: spec.Frequency, Retention: "720h", Status: storageio.DatasetStatusDisabled, Attributes: attrs,
		}
		f.columns[key] = append([]storageio.ColumnInfo(nil), spec.Columns...)
	}
	return nil
}

func (f *metadataFake) SetDatasetSubjectTags(_ context.Context, spaceID, datasetID string, tags []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writeOps = append(f.writeOps, "subject_tags")
	key := resultKey(spaceID, datasetID)
	dataset := f.datasets[key]
	dataset.SubjectTags = append([]string(nil), tags...)
	f.datasets[key] = dataset
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

type recalcFake struct {
	// requestPrefix 是回填任务请求号的前缀，默认 "enable-"；设成 "factor-enable-" 才会与真实环境一样被并入排队中的任务。
	requestPrefix string
	db            *store.Store
	set           domain.FactorSet
	factors       []domain.FactorDef
	start         time.Time
	end           time.Time
	order         []string
	err           error
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

func (f *recalcFake) PrepareEnableBackfill(ctx context.Context, set domain.FactorSet, member domain.MemberDef, start, end time.Time) (store.RecalcJob, error) {
	factor := member.Factor
	stored, err := f.db.GetMember(ctx, set.SetID, member.Alias)
	if err != nil {
		return store.RecalcJob{}, err
	}
	f.order = append(f.order, stored.Status)
	f.set, f.factors, f.start, f.end = set, []domain.FactorDef{factor}, start, end
	if f.err != nil {
		return store.RecalcJob{}, f.err
	}
	prefix := f.requestPrefix
	if prefix == "" {
		prefix = "enable-"
	}
	return store.RecalcJob{
		JobID: prefix + member.Alias, RequestID: prefix + member.Alias,
		SetID: set.SetID, Members: []string{member.Alias}, Subjects: []string{"BTC-USDT"},
		StartTime: start.Unix(), EndTime: end.Unix(), Status: store.RecalcStatusAccepted,
	}, nil
}

func TestReconcileRecordsResultReady(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	ready := map[string]bool{}
	svc := NewService(db, meta, WithReadyRecorder(func(setID string, ok bool) { ready[setID] = ok }))
	set, err := svc.CreateSet(context.Background(), newSet())
	require.NoError(t, err)
	require.True(t, ready[set.SetID], "a freshly created set is ready")

	svc.recalc = &recalcFake{db: db}
	addMember(t, svc, set.SetID, "momentum", domain.MemberStatusEnabled)
	meta.sourceColumns = append(meta.sourceColumns, sourceColumn("momentum"))
	require.Error(t, svc.ReconcileSet(context.Background(), set.SetID))
	require.False(t, ready[set.SetID], "a failed reconciliation marks the set not ready")

	meta.sourceColumns = meta.sourceColumns[:len(meta.sourceColumns)-1]
	require.NoError(t, svc.ReconcileSet(context.Background(), set.SetID))
	require.True(t, ready[set.SetID])
}

func TestReconcileKeepsReadinessOnStorageOutage(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	ready := map[string]bool{}
	svc := NewService(db, meta, WithReadyRecorder(func(setID string, ok bool) { ready[setID] = ok }))
	set, err := svc.CreateSet(context.Background(), newSet())
	require.NoError(t, err)
	require.True(t, ready[set.SetID])

	meta.getDatasetErr = fmt.Errorf("%w: connection refused", storageio.ErrInfra)
	require.Error(t, svc.ReconcileSet(context.Background(), set.SetID))

	require.True(t, ready[set.SetID], "a transient Storage failure keeps the set ready")
}

func TestReconcileFollowsSourceSubjectTags(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	ready := map[string]bool{}
	svc := NewService(db, meta, WithReadyRecorder(func(setID string, ok bool) { ready[setID] = ok }))
	set, err := svc.CreateSet(context.Background(), newSet())
	require.NoError(t, err)
	source := meta.datasets[resultKey("crypto", "dataset_prices")]
	source.SubjectTags = []string{"binance_spot", "binance_swap"}
	meta.datasets[resultKey("crypto", "dataset_prices")] = source
	meta.writeOps = nil

	require.NoError(t, svc.ReconcileSet(context.Background(), set.SetID))

	require.True(t, ready[set.SetID], "a source subject-tag change must not halt the set")
	require.Equal(t, []string{"subject_tags"}, meta.writeOps)
	require.Equal(t, []string{"binance_spot", "binance_swap"}, meta.datasets[resultKey("crypto", set.ResultDatasetID)].SubjectTags)
}

func TestResultDatasetNameIsDerivedFromItsSource(t *testing.T) {
	require.Equal(t, "现货分钟K线因子", resultDatasetName("现货分钟K线"))
	require.NotEqual(t, resultDatasetName("现货分钟K线"), resultDatasetName("合约分钟K线"), "factor sets of different sources must not collide")
	long := resultDatasetName("一二三四五六七八九十")
	require.Equal(t, "一二三四五六七八因子", long)
	require.LessOrEqual(t, len([]rune(long)), maxResultDatasetNameRunes)
	require.Equal(t, "因子结果", resultDatasetName(" "))
}

// 回填默认只取最近 maxBackfillPeriods 期：更早的因子结果没有读取方，却要把整段源数据窗口读一遍
// （2026-10-14 两个 1h 因子集按结果数据集的保留期回填一整年，把 storage-node 打到 330% CPU）。
func TestBackfillWindowIsCappedToTheMostRecentPeriods(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)
	now := time.Date(2026, 10, 4, 10, 3, 37, 0, time.UTC)
	svc.now = func() time.Time { return now }
	set, err := db.GetSet(context.Background(), domain.SetID("dataset_prices", "1m"))
	require.NoError(t, err)
	end := time.Date(2026, 10, 4, 10, 3, 0, 0, time.UTC)

	// 默认上限：结果数据集的保留期是 30 天（43200 期），只回填最近 5000 期。
	require.Equal(t, DefaultMaxBackfillPeriods, svc.maxBackfillPeriods)
	start, gotEnd, err := svc.backfillWindow(context.Background(), set)
	require.NoError(t, err)
	require.Equal(t, end, gotEnd)
	require.Equal(t, end.Add(-5000*time.Minute), start)

	// 保留期比上限短时按保留期。
	svc.maxBackfillPeriods = 100000
	start, _, err = svc.backfillWindow(context.Background(), set)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 9, 4, 10, 3, 0, 0, time.UTC), start)

	// 保留期无限、源数据最早的一期更早：同样受上限约束；最早的一期比上限更近时取最早的一期。
	svc.maxBackfillPeriods = 5000
	result := meta.datasets[resultKey(set.SpaceID, set.ResultDatasetID)]
	result.Retention = "forever"
	meta.datasets[resultKey(set.SpaceID, set.ResultDatasetID)] = result
	svc.earliestPeriod = &earliestPeriodFake{period: time.Date(2025, 1, 2, 3, 4, 25, 0, time.UTC), found: true}
	start, _, err = svc.backfillWindow(context.Background(), set)
	require.NoError(t, err)
	require.Equal(t, end.Add(-5000*time.Minute), start)
	recent := end.Add(-90 * time.Minute)
	svc.earliestPeriod = &earliestPeriodFake{period: recent, found: true}
	start, _, err = svc.backfillWindow(context.Background(), set)
	require.NoError(t, err)
	require.Equal(t, recent, start)
}

// 连续启用同一因子集的多个成员：后面的成员并进第一个回填任务，返回的任务号必须是入库后的那个，
// 否则 RPC 层按它回读任务会得到 record not found（上线时 BiasQ 的启用因此报错，成员状态却已经改了）。
func TestEnableMemberReturnsTheStoredJobWhenMergedIntoAQueuedBackfill(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)
	svc.now = func() time.Time { return time.Date(2026, 10, 4, 10, 3, 37, 0, time.UTC) }
	svc.recalc = &recalcFake{db: db, requestPrefix: "factor-enable-"}
	addMember(t, svc, testSetID(), "momentum", domain.MemberStatusDisabled)
	addMember(t, svc, testSetID(), "bias", domain.MemberStatusDisabled)

	_, first, err := svc.SetFactorMemberStatus(context.Background(), testSetID(), "momentum", domain.MemberStatusEnabled)
	require.NoError(t, err)
	_, second, err := svc.SetFactorMemberStatus(context.Background(), testSetID(), "bias", domain.MemberStatusEnabled)
	require.NoError(t, err)
	require.Equal(t, "factor-enable-momentum", first)
	require.Equal(t, first, second, "第二个成员并入第一个任务，返回的是已入库的任务号")

	job, err := db.GetRecalcJob(context.Background(), second)
	require.NoError(t, err, "返回的任务号必须能读到")
	require.ElementsMatch(t, []string{"momentum", "bias"}, job.Members)
}
