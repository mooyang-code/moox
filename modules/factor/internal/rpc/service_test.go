package rpc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/registry"
	"github.com/mooyang-code/moox/modules/factor/internal/scheduler"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
)

type inventoryStub struct {
	dirty     int
	refreshes int
	err       error
}

func (s *inventoryStub) MarkDirty() {
	s.dirty++
}

func (s *inventoryStub) Refresh(context.Context) error {
	s.refreshes++
	return s.err
}

func TestCreateFactorUsesGenericContractAndComputedSourceHash(t *testing.T) {
	svc := NewWithRuntime(openRPCTestDB(t), nil, WithFactorsDir(t.TempDir()))
	factor := genericFactorPB("bias", "Bias", []string{"bias"})
	factor.SourceHash = "untrusted"
	rsp, err := svc.CreateFactor(context.Background(), &factorpb.CreateFactorReq{Factor: factor})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, []string{"close"}, rsp.GetFactor().GetInputColumns())
	require.Equal(t, []string{"bias"}, rsp.GetFactor().GetOutputs())
	require.NotEqual(t, "untrusted", rsp.GetFactor().GetSourceHash())
	require.Equal(t, domain.FactorStatusDisabled, rsp.GetFactor().GetStatus())
}

func TestFactorMutationRefreshFailureDoesNotRollback(t *testing.T) {
	ctx := context.Background()
	db := openRPCTestDB(t)
	inventory := &inventoryStub{err: errors.New("inventory unavailable")}
	svc := NewWithRuntime(
		db,
		nil,
		WithFactorsDir(t.TempDir()),
		WithRealtimeInventory(inventory),
	)

	rsp, err := svc.CreateFactor(ctx, &factorpb.CreateFactorReq{
		Factor: genericFactorPB("bias", "Bias", []string{"bias"}),
	})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	_, err = db.Factors().Get(ctx, "bias")
	require.NoError(t, err)
	require.Equal(t, 1, inventory.dirty)
	require.Equal(t, 1, inventory.refreshes)
}

func TestCreateFactorRejectsDuplicateIDOrNameWithoutOverwritingOutputs(t *testing.T) {
	db := openRPCTestDB(t)
	svc := NewWithRuntime(db, nil, WithFactorsDir(t.TempDir()))
	first := genericFactorPB("factor-1", "First", []string{"original"})
	rsp, err := svc.CreateFactor(context.Background(), &factorpb.CreateFactorReq{Factor: first})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())

	duplicateID := genericFactorPB("factor-1", "First", []string{"changed"})
	rsp, err = svc.CreateFactor(context.Background(), &factorpb.CreateFactorReq{Factor: duplicateID})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
	stored, err := db.Factors().Get(context.Background(), "factor-1")
	require.NoError(t, err)
	require.Equal(t, []string{"original"}, stored.Outputs)

	duplicateName := genericFactorPB("factor-2", "First", []string{"other"})
	rsp, err = svc.CreateFactor(context.Background(), &factorpb.CreateFactorReq{Factor: duplicateName})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestDeleteFactorRemovesDefinitionAndArtifacts(t *testing.T) {
	ctx := context.Background()
	db := openRPCTestDB(t)
	factorsDir := t.TempDir()
	svc := NewWithRuntime(db, nil, WithFactorsDir(factorsDir))
	factor := genericFactorPB("factor-1", "First", []string{"value"})
	createRsp, err := svc.CreateFactor(ctx, &factorpb.CreateFactorReq{Factor: factor})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, createRsp.GetRetInfo().GetCode())
	require.FileExists(t, filepath.Join(factorsDir, "First.py"))
	require.DirExists(t, filepath.Join(factorsDir, ".versions", "factor", "First"))
	cacheDir := filepath.Join(factorsDir, "__pycache__")
	require.NoError(t, os.Mkdir(cacheDir, 0o755))
	for _, name := range []string{"First.cpython-311.pyc", "First.cpython-314.pyc", "Other.cpython-314.pyc"} {
		require.NoError(t, os.WriteFile(filepath.Join(cacheDir, name), []byte("bytecode"), 0o644))
	}

	deleteRsp, err := svc.DeleteFactor(ctx, &factorpb.DeleteFactorReq{FactorId: "factor-1"})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, deleteRsp.GetRetInfo().GetCode())
	_, err = db.Factors().Get(ctx, "factor-1")
	require.Error(t, err)
	require.NoFileExists(t, filepath.Join(factorsDir, "First.py"))
	require.NoDirExists(t, filepath.Join(factorsDir, ".versions", "factor", "First"))
	require.NoFileExists(t, filepath.Join(cacheDir, "First.cpython-311.pyc"))
	require.NoFileExists(t, filepath.Join(cacheDir, "First.cpython-314.pyc"))
	require.FileExists(t, filepath.Join(cacheDir, "Other.cpython-314.pyc"))
}

func TestDeleteFactorReportsStagedArtifactRemovalFailure(t *testing.T) {
	ctx := context.Background()
	db := openRPCTestDB(t)
	factorsDir := t.TempDir()
	svc := NewWithRuntime(db, nil, WithFactorsDir(factorsDir))
	factor := genericFactorPB("factor-1", "First", []string{"value"})
	createRsp, err := svc.CreateFactor(ctx, &factorpb.CreateFactorReq{Factor: factor})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, createRsp.GetRetInfo().GetCode())
	svc.removeStage = func(*factorArtifactStage) error {
		return errors.New("injected remove failure")
	}

	deleteRsp, err := svc.DeleteFactor(ctx, &factorpb.DeleteFactorReq{FactorId: "factor-1"})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INNER_ERR, deleteRsp.GetRetInfo().GetCode())
	require.Contains(t, deleteRsp.GetRetInfo().GetMsg(), "injected remove failure")
	_, err = db.Factors().Get(ctx, "factor-1")
	require.Error(t, err)
	matches, globErr := filepath.Glob(filepath.Join(factorsDir, ".delete-First-*"))
	require.NoError(t, globErr)
	require.NotEmpty(t, matches)
}

func TestConcurrentDeleteFactorLeavesNoArtifacts(t *testing.T) {
	ctx := context.Background()
	db := openRPCTestDB(t)
	factorsDir := t.TempDir()
	svc := NewWithRuntime(db, nil, WithFactorsDir(factorsDir))
	factor := genericFactorPB("factor-1", "First", []string{"value"})
	createRsp, err := svc.CreateFactor(ctx, &factorpb.CreateFactorReq{Factor: factor})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, createRsp.GetRetInfo().GetCode())

	start := make(chan struct{})
	type deleteResult struct {
		code commonpb.ErrorCode
		err  error
	}
	results := make(chan deleteResult, 2)
	for range 2 {
		go func() {
			<-start
			rsp, callErr := svc.DeleteFactor(ctx, &factorpb.DeleteFactorReq{FactorId: "factor-1"})
			results <- deleteResult{code: rsp.GetRetInfo().GetCode(), err: callErr}
		}()
	}
	close(start)
	first, second := <-results, <-results
	require.NoError(t, first.err)
	require.NoError(t, second.err)
	codes := []commonpb.ErrorCode{first.code, second.code}
	require.Equal(t, 1, countErrorCode(codes, commonpb.ErrorCode_SUCCESS))
	_, err = db.Factors().Get(ctx, "factor-1")
	require.Error(t, err)
	require.NoFileExists(t, filepath.Join(factorsDir, "First.py"))
	require.NoDirExists(t, filepath.Join(factorsDir, ".versions", "factor", "First"))
}

func TestDeleteFactorWithBindingLeavesDefinitionAndArtifacts(t *testing.T) {
	ctx := context.Background()
	db := openRPCTestDB(t)
	factorsDir := t.TempDir()
	svc := NewWithRuntime(db, nil, WithFactorsDir(factorsDir))
	factor := genericFactorPB("factor-1", "First", []string{"value"})
	createRsp, err := svc.CreateFactor(ctx, &factorpb.CreateFactorReq{Factor: factor})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, createRsp.GetRetInfo().GetCode())
	require.NoError(t, db.Bindings().Upsert(ctx, domain.FactorBinding{
		BindingID: "binding-1", FactorID: "factor-1", SpaceID: "space",
		SourceDataset: "source", Freq: "1m", SubjectMode: domain.SubjectModeAll,
		SubjectsJSON: "[]", TargetDataset: "target", Status: domain.BindingStatusDisabled,
	}))

	deleteRsp, err := svc.DeleteFactor(ctx, &factorpb.DeleteFactorReq{FactorId: "factor-1"})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, deleteRsp.GetRetInfo().GetCode())
	_, err = db.Factors().Get(ctx, "factor-1")
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(factorsDir, "First.py"))
	require.DirExists(t, filepath.Join(factorsDir, ".versions", "factor", "First"))
}

func TestStageFactorArtifactsRestoresSourceWhenVersionStagingFails(t *testing.T) {
	factorsDir := t.TempDir()
	sourcePath := filepath.Join(factorsDir, "First.py")
	require.NoError(t, os.WriteFile(sourcePath, []byte("source"), 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(factorsDir, ".versions"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(factorsDir, ".versions", "factor"), []byte("not a directory"), 0o644))

	_, err := stageFactorArtifacts(factorsDir, "First")
	require.Error(t, err)
	require.FileExists(t, sourcePath)
	raw, readErr := os.ReadFile(sourcePath)
	require.NoError(t, readErr)
	require.Equal(t, "source", string(raw))
}

func countErrorCode(codes []commonpb.ErrorCode, want commonpb.ErrorCode) int {
	count := 0
	for _, code := range codes {
		if code == want {
			count++
		}
	}
	return count
}

func TestUpdateFactorRejectsOutputChangesButUpdatesMutableFields(t *testing.T) {
	db := openRPCTestDB(t)
	svc := NewWithRuntime(db, nil, WithFactorsDir(t.TempDir()))
	created := genericFactorPB("factor-1", "First", []string{"value"})
	rsp, err := svc.CreateFactor(context.Background(), &factorpb.CreateFactorReq{Factor: created})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())

	changed := genericFactorPB("factor-1", "First", []string{"changed"})
	changed.Status = domain.FactorStatusDisabled
	updateRsp, err := svc.UpdateFactor(context.Background(), &factorpb.UpdateFactorReq{FactorId: "factor-1", Factor: changed})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, updateRsp.GetRetInfo().GetCode())

	renamed := genericFactorPB("factor-1", "Renamed", []string{"value"})
	renamed.Status = domain.FactorStatusDisabled
	updateRsp, err = svc.UpdateFactor(context.Background(), &factorpb.UpdateFactorReq{FactorId: "factor-1", Factor: renamed})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, updateRsp.GetRetInfo().GetCode())

	mutable := genericFactorPB("factor-1", "First", []string{"value"})
	mutable.Status = domain.FactorStatusDisabled
	mutable.ParamsJson = `{"window":10}`
	updateRsp, err = svc.UpdateFactor(context.Background(), &factorpb.UpdateFactorReq{FactorId: "factor-1", Factor: mutable})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, updateRsp.GetRetInfo().GetCode())
	require.Equal(t, "First", updateRsp.GetFactor().GetName())
	require.Equal(t, `{"window":10}`, updateRsp.GetFactor().GetParamsJson())
}

func TestUpdateFactorCannotEnableDisabledDefinition(t *testing.T) {
	ctx := context.Background()
	db := openRPCTestDB(t)
	seedRPCFactorAndBinding(t, db, domain.FactorStatusDisabled)
	svc := NewWithRuntime(db, nil, WithFactorsDir(t.TempDir()))
	updated := genericFactorPB("bias", "Bias", []string{"bias"})
	updated.Status = domain.FactorStatusEnabled

	rsp, err := svc.UpdateFactor(ctx, &factorpb.UpdateFactorReq{
		FactorId: "bias",
		Factor:   updated,
	})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
	require.Contains(t, rsp.GetRetInfo().GetMsg(), "SetFactorStatus")

	stored, err := db.Factors().Get(ctx, "bias")
	require.NoError(t, err)
	require.Equal(t, domain.FactorStatusDisabled, stored.Status)
	executable, err := db.Bindings().ListExecutable(ctx)
	require.NoError(t, err)
	require.Empty(t, executable)
}

func TestUpdateFactorReconcilesStorageMetadataWithOnlyDisabledBinding(t *testing.T) {
	db := openRPCTestDB(t)
	seedRPCFactorDefinition(t, db, "factor")
	seedRPCDisabledBinding(t, db, "factor", "space")
	seedRPCDisabledBindingWithScope(t, db, "factor", "space", "second-binding", "source-2")
	metadata := newRecordingFactorMetadataClient("space", "factor")
	svc := NewWithRuntime(db, nil,
		WithFactorsDir(t.TempDir()),
		WithMetadataSync(registry.NewMetadataSync(metadata, nil)),
	)
	updated := genericFactorPB("factor", "TestFactor", []string{"value"})
	updated.ParamsJson = `{"window":10}`

	rsp, err := svc.UpdateFactor(context.Background(), &factorpb.UpdateFactorReq{
		FactorId: "factor", Factor: updated,
	})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Len(t, metadata.updatedFactors, 1)
	require.Equal(t, `{"window":10}`, metadata.updatedFactors[0].GetParamsJson())
	require.Zero(t, metadata.targetCalls)
}

func TestUpsertDisabledBindingStillReconcilesFactorMetadata(t *testing.T) {
	db := openRPCTestDB(t)
	seedRPCFactorDefinition(t, db, "factor")
	metadata := newRecordingFactorMetadataClient("space", "factor")
	svc := NewWithRuntime(db, nil,
		WithFactorsDir(t.TempDir()),
		WithMetadataSync(registry.NewMetadataSync(metadata, nil)),
	)
	binding := testBindingPB(domain.SubjectModeAll, `[]`)
	binding.Status = domain.BindingStatusDisabled

	rsp, err := svc.UpsertBinding(context.Background(), &factorpb.UpsertBindingReq{Binding: binding})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Len(t, metadata.updatedFactors, 1)
	require.Zero(t, metadata.targetCalls)
}

func TestUpsertBindingDisablesLocallyBeforeMetadataFailure(t *testing.T) {
	db := openRPCTestDB(t)
	seedRPCFactorDefinition(t, db, "factor")
	require.NoError(t, db.Bindings().Upsert(context.Background(), domain.FactorBinding{
		BindingID: "bind", FactorID: "factor", SpaceID: "space",
		SourceDataset: "source", Freq: "1m", SubjectMode: domain.SubjectModeAll,
		SubjectsJSON: "[]", TargetDataset: "target", Status: domain.BindingStatusEnabled,
	}))
	metadata := newRecordingFactorMetadataClient("space", "factor")
	metadata.getFactorRet = &commonpb.RetInfo{Code: commonpb.ErrorCode_INNER_ERR, Msg: "metadata unavailable"}
	svc := NewWithRuntime(db, nil,
		WithFactorsDir(t.TempDir()),
		WithMetadataSync(registry.NewMetadataSync(metadata, nil)),
	)
	binding := testBindingPB(domain.SubjectModeAll, `[]`)
	binding.Status = domain.BindingStatusDisabled

	rsp, err := svc.UpsertBinding(context.Background(), &factorpb.UpsertBindingReq{Binding: binding})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INNER_ERR, rsp.GetRetInfo().GetCode())
	rows, total, listErr := db.Bindings().List(context.Background(), store.BindingFilter{})
	require.NoError(t, listErr)
	require.EqualValues(t, 1, total)
	require.Equal(t, domain.BindingStatusDisabled, rows[0].Status)
	executable, listErr := db.Bindings().ListExecutable(context.Background())
	require.NoError(t, listErr)
	require.Empty(t, executable)
}

func TestUpsertEnabledBindingMetadataFailureDoesNotPersist(t *testing.T) {
	db := openRPCTestDB(t)
	seedRPCFactorDefinition(t, db, "factor")
	metadata := newRecordingFactorMetadataClient("space", "factor")
	metadata.getFactorRet = &commonpb.RetInfo{Code: commonpb.ErrorCode_INNER_ERR, Msg: "metadata unavailable"}
	svc := NewWithRuntime(db, nil,
		WithFactorsDir(t.TempDir()),
		WithMetadataSync(registry.NewMetadataSync(metadata, nil)),
	)
	binding := testBindingPB(domain.SubjectModeAll, `[]`)
	binding.Status = domain.BindingStatusEnabled

	rsp, err := svc.UpsertBinding(context.Background(), &factorpb.UpsertBindingReq{Binding: binding})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INNER_ERR, rsp.GetRetInfo().GetCode())
	rows, total, listErr := db.Bindings().List(context.Background(), store.BindingFilter{})
	require.NoError(t, listErr)
	require.Zero(t, total)
	require.Empty(t, rows)
}

func TestSetFactorStatusDisabledReconcilesStorageMetadata(t *testing.T) {
	db := openRPCTestDB(t)
	seedRPCFactorDefinition(t, db, "factor")
	seedRPCDisabledBinding(t, db, "factor", "space")
	metadata := newRecordingFactorMetadataClient("space", "factor")
	svc := NewWithRuntime(db, nil,
		WithFactorsDir(t.TempDir()),
		WithMetadataSync(registry.NewMetadataSync(metadata, nil)),
	)

	rsp, err := svc.SetFactorStatus(context.Background(), &factorpb.SetFactorStatusReq{
		FactorId: "factor", Status: domain.FactorStatusDisabled,
	})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Len(t, metadata.updatedFactors, 1)
	require.Equal(t, "disabled", metadata.updatedFactors[0].GetStatus())
	require.Zero(t, metadata.targetCalls)
}

func TestSetFactorStatusEnableReconciliationFailureRemainsNonExecutable(t *testing.T) {
	ctx := context.Background()
	db := openRPCTestDB(t)
	seedRPCFactorAndBinding(t, db, domain.FactorStatusDisabled)
	metadata := newRecordingFactorMetadataClient("crypto", "bias")
	metadata.createDatasetRet = &commonpb.RetInfo{Code: commonpb.ErrorCode_INNER_ERR, Msg: "target unavailable"}
	svc := NewWithRuntime(db, nil,
		WithFactorsDir(t.TempDir()),
		WithMetadataSync(registry.NewMetadataSync(metadata, nil)),
	)

	rsp, err := svc.SetFactorStatus(ctx, &factorpb.SetFactorStatusReq{
		FactorId: "bias", Status: " " + domain.FactorStatusEnabled + " ",
	})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INNER_ERR, rsp.GetRetInfo().GetCode())
	factor, getErr := db.Factors().Get(ctx, "bias")
	require.NoError(t, getErr)
	require.Equal(t, domain.FactorStatusDisabled, factor.Status)
	executable, listErr := db.Bindings().ListExecutable(ctx)
	require.NoError(t, listErr)
	require.Empty(t, executable)
	require.NotZero(t, metadata.targetCalls)
}

func TestSetFactorStatusRejectsInvalidStatusWithoutMutationOrSync(t *testing.T) {
	ctx := context.Background()
	db := openRPCTestDB(t)
	seedRPCFactorAndBinding(t, db, domain.FactorStatusDisabled)
	metadata := newRecordingFactorMetadataClient("crypto", "bias")
	svc := NewWithRuntime(db, nil,
		WithFactorsDir(t.TempDir()),
		WithMetadataSync(registry.NewMetadataSync(metadata, nil)),
	)

	rsp, err := svc.SetFactorStatus(ctx, &factorpb.SetFactorStatusReq{
		FactorId: "bias", Status: "paused",
	})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
	factor, getErr := db.Factors().Get(ctx, "bias")
	require.NoError(t, getErr)
	require.Equal(t, domain.FactorStatusDisabled, factor.Status)
	require.Empty(t, metadata.updatedFactors)
	require.Zero(t, metadata.targetCalls)
}

func TestRecalcFactorRunsSynchronousRange(t *testing.T) {
	db := openRPCTestDB(t)
	seedRPCFactorAndBinding(t, db, domain.FactorStatusEnabled)
	runner := &fakeRPCScheduler{}
	svc := NewWithRuntime(db, runner, WithFactorsDir(t.TempDir()))
	rsp, err := svc.RecalcFactor(context.Background(), &factorpb.RecalcFactorReq{
		FactorId: "bias", SpaceId: "crypto", SourceDataset: "bars",
		SubjectId: "BTC", Freq: "1m",
		StartTime: "2026-07-26T00:00:00Z", EndTime: "2026-07-26T01:00:00Z",
	})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Len(t, runner.tasks, 1)
	require.Equal(t, time.Hour, runner.tasks[0].EndTime.Sub(runner.tasks[0].StartTime))
}

func TestRecalcRejectsMissingOrInvalidRange(t *testing.T) {
	svc := NewWithRuntime(openRPCTestDB(t), &fakeRPCScheduler{}, WithFactorsDir(t.TempDir()))
	for _, req := range []*factorpb.RecalcFactorReq{
		{SpaceId: "crypto", SourceDataset: "bars", SubjectId: "BTC", Freq: "1m"},
		{SpaceId: "crypto", SourceDataset: "bars", SubjectId: "BTC", Freq: "1m", StartTime: "bad", EndTime: "2026-07-26T01:00:00Z"},
		{SpaceId: "crypto", SourceDataset: "bars", SubjectId: "BTC", Freq: "1m", StartTime: "2026-07-26T01:00:00Z", EndTime: "2026-07-26T00:00:00Z"},
	} {
		rsp, err := svc.RecalcFactor(context.Background(), req)
		require.NoError(t, err)
		require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
	}
}

func TestRecalcHonorsBindingSubjectScope(t *testing.T) {
	db := openRPCTestDB(t)
	seedRPCFactorAndBinding(t, db, domain.FactorStatusEnabled)
	require.NoError(t, db.Bindings().Upsert(context.Background(), domain.FactorBinding{
		BindingID: "bind", FactorID: "bias", SpaceID: "crypto", SourceDataset: "bars",
		Freq: "1m", SubjectMode: domain.SubjectModeInclude, SubjectsJSON: `["ETH"]`,
		TargetDataset: "bars_factor", Status: domain.BindingStatusEnabled,
	}))
	runner := &fakeRPCScheduler{}
	svc := NewWithRuntime(db, runner, WithFactorsDir(t.TempDir()))
	rsp, err := svc.RecalcFactor(context.Background(), &factorpb.RecalcFactorReq{
		FactorId: "bias", SpaceId: "crypto", SourceDataset: "bars",
		SubjectId: "BTC", Freq: "1m",
		StartTime: "2026-07-26T00:00:00Z", EndTime: "2026-07-26T01:00:00Z",
	})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
	require.Empty(t, runner.tasks)
}

func TestUpsertBindingRejectsInvalidIncludeSubjectsWithoutPersisting(t *testing.T) {
	for _, raw := range []string{"", "null", `{}`, `[""]`, `["BTC"," "]`, `[1]`, `[]`} {
		t.Run(raw, func(t *testing.T) {
			db := openRPCTestDB(t)
			seedRPCFactorDefinition(t, db, "factor")
			svc := NewWithRuntime(db, nil, WithFactorsDir(t.TempDir()))
			rsp, err := svc.UpsertBinding(context.Background(), &factorpb.UpsertBindingReq{
				Binding: testBindingPB(domain.SubjectModeInclude, raw),
			})
			require.NoError(t, err)
			require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
			rows, total, listErr := db.Bindings().List(context.Background(), store.BindingFilter{})
			require.NoError(t, listErr)
			require.Zero(t, total)
			require.Empty(t, rows)
		})
	}
}

func TestUpsertBindingPersistsCanonicalSubjects(t *testing.T) {
	db := openRPCTestDB(t)
	seedRPCFactorDefinition(t, db, "factor")
	svc := NewWithRuntime(db, nil, WithFactorsDir(t.TempDir()))
	binding := testBindingPB(domain.SubjectModeInclude, `[" ETH ","BTC","ETH"]`)
	binding.Status = domain.BindingStatusDisabled
	rsp, err := svc.UpsertBinding(context.Background(), &factorpb.UpsertBindingReq{Binding: binding})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, `["BTC","ETH"]`, rsp.GetBinding().GetSubjectsJson())
	rows, total, err := db.Bindings().List(context.Background(), store.BindingFilter{})
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Equal(t, `["BTC","ETH"]`, rows[0].SubjectsJSON)
}

func TestUpsertBindingAllModeDiscardsSubjects(t *testing.T) {
	db := openRPCTestDB(t)
	seedRPCFactorDefinition(t, db, "factor")
	svc := NewWithRuntime(db, nil, WithFactorsDir(t.TempDir()))
	binding := testBindingPB(domain.SubjectModeAll, `not-json`)
	binding.Status = domain.BindingStatusDisabled
	rsp, err := svc.UpsertBinding(context.Background(), &factorpb.UpsertBindingReq{Binding: binding})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, domain.DefaultSubjectsJSON, rsp.GetBinding().GetSubjectsJson())
}

func testBindingPB(mode, subjectsJSON string) *factorpb.FactorBinding {
	return &factorpb.FactorBinding{
		BindingId: "bind", FactorId: "factor", SpaceId: "space",
		SourceDataset: "source", Freq: "1m", SubjectMode: mode,
		SubjectsJson: subjectsJSON, TargetDataset: "target",
	}
}

func TestGetEngineStatusIsMinimal(t *testing.T) {
	runner := &fakeRPCScheduler{status: scheduler.Status{QueueDepth: 3, QueueOverflowCount: 4}}
	svc := NewWithRuntime(openRPCTestDB(t), runner)
	rsp, err := svc.GetEngineStatus(context.Background(), &factorpb.GetEngineStatusReq{})
	require.NoError(t, err)
	require.EqualValues(t, 3, rsp.GetQueueDepth())
	require.EqualValues(t, 4, rsp.GetQueueOverflowCount())
}

type fakeRPCScheduler struct {
	status scheduler.Status
	tasks  []scheduler.Task
	err    error
}

func (f *fakeRPCScheduler) Status() scheduler.Status { return f.status }
func (f *fakeRPCScheduler) Run(_ context.Context, task scheduler.Task) error {
	f.tasks = append(f.tasks, task)
	return f.err
}

func openRPCTestDB(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "factor.db")})
	require.NoError(t, err)
	require.NoError(t, db.ApplySchema(factorschema.AllSQL()))
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func seedRPCFactorAndBinding(t *testing.T, db *store.Store, status string) {
	t.Helper()
	require.NoError(t, db.Factors().Create(context.Background(), domain.FactorDef{
		FactorID: "bias", Name: "Bias", SourceCode: "x", SourceHash: "hash",
		InputColumns: []string{"close"}, Outputs: []string{"bias"}, ParamsJSON: `{}`,
		LookbackPeriods: 20, Status: status,
	}))
	require.NoError(t, db.Bindings().Upsert(context.Background(), domain.FactorBinding{
		BindingID: "bind", FactorID: "bias", SpaceID: "crypto", SourceDataset: "bars",
		Freq: "1m", SubjectMode: domain.SubjectModeAll, SubjectsJSON: "[]",
		TargetDataset: "bars_factor", Status: domain.BindingStatusEnabled,
	}))
}

func seedRPCFactorDefinition(t *testing.T, db *store.Store, factorID string) {
	t.Helper()
	require.NoError(t, db.Factors().Create(context.Background(), domain.FactorDef{
		FactorID: factorID, Name: "TestFactor", SourceCode: "x", SourceHash: "hash",
		InputColumns: []string{"close"}, Outputs: []string{"value"}, ParamsJSON: `{}`,
		LookbackPeriods: 2, Status: domain.FactorStatusEnabled,
	}))
}

func seedRPCDisabledBinding(t *testing.T, db *store.Store, factorID, spaceID string) {
	t.Helper()
	seedRPCDisabledBindingWithScope(t, db, factorID, spaceID, "disabled-binding", "source")
}

func seedRPCDisabledBindingWithScope(t *testing.T, db *store.Store, factorID, spaceID, bindingID, sourceDataset string) {
	t.Helper()
	require.NoError(t, db.Bindings().Upsert(context.Background(), domain.FactorBinding{
		BindingID: bindingID, FactorID: factorID, SpaceID: spaceID,
		SourceDataset: sourceDataset, Freq: "1m", SubjectMode: domain.SubjectModeAll,
		SubjectsJSON: "[]", TargetDataset: "target", Status: domain.BindingStatusDisabled,
	}))
}

type recordingFactorMetadataClient struct {
	factors          map[string]*storagepb.Factor
	updatedFactors   []*storagepb.Factor
	targetCalls      int
	getFactorRet     *commonpb.RetInfo
	createDatasetRet *commonpb.RetInfo
}

func newRecordingFactorMetadataClient(spaceID, factorID string) *recordingFactorMetadataClient {
	return &recordingFactorMetadataClient{factors: map[string]*storagepb.Factor{
		spaceID + "/" + factorID: {SpaceId: spaceID, FactorId: factorID},
	}}
}

func (f *recordingFactorMetadataClient) CreateFactor(_ context.Context, req *storagepb.CreateFactorReq) (*storagepb.CreateFactorRsp, error) {
	f.factors[req.GetFactor().GetSpaceId()+"/"+req.GetFactor().GetFactorId()] = req.GetFactor()
	return &storagepb.CreateFactorRsp{RetInfo: success()}, nil
}

func (f *recordingFactorMetadataClient) UpdateFactor(_ context.Context, req *storagepb.UpdateFactorReq) (*storagepb.UpdateFactorRsp, error) {
	f.updatedFactors = append(f.updatedFactors, req.GetFactor())
	f.factors[req.GetFactor().GetSpaceId()+"/"+req.GetFactor().GetFactorId()] = req.GetFactor()
	return &storagepb.UpdateFactorRsp{RetInfo: success()}, nil
}

func (f *recordingFactorMetadataClient) GetFactor(_ context.Context, req *storagepb.GetFactorReq) (*storagepb.GetFactorRsp, error) {
	if f.getFactorRet != nil {
		return &storagepb.GetFactorRsp{RetInfo: f.getFactorRet}, nil
	}
	factor := f.factors[req.GetSpaceId()+"/"+req.GetFactorId()]
	if factor == nil {
		return &storagepb.GetFactorRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_FACTOR_NOT_FOUND}}, nil
	}
	return &storagepb.GetFactorRsp{RetInfo: success(), Factor: factor}, nil
}

func (f *recordingFactorMetadataClient) CreateDataset(context.Context, *storagepb.CreateDatasetReq) (*storagepb.CreateDatasetRsp, error) {
	f.targetCalls++
	if f.createDatasetRet != nil {
		return &storagepb.CreateDatasetRsp{RetInfo: f.createDatasetRet}, nil
	}
	return &storagepb.CreateDatasetRsp{RetInfo: success()}, nil
}

func (f *recordingFactorMetadataClient) UpdateDataset(context.Context, *storagepb.UpdateDatasetReq) (*storagepb.UpdateDatasetRsp, error) {
	f.targetCalls++
	return &storagepb.UpdateDatasetRsp{RetInfo: success()}, nil
}

func (f *recordingFactorMetadataClient) GetDataset(context.Context, *storagepb.GetDatasetReq) (*storagepb.GetDatasetRsp, error) {
	f.targetCalls++
	return &storagepb.GetDatasetRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_DATASET_NOT_FOUND}}, nil
}

func (f *recordingFactorMetadataClient) CheckDatasetActivation(context.Context, *storagepb.CheckDatasetActivationReq) (*storagepb.CheckDatasetActivationRsp, error) {
	f.targetCalls++
	return &storagepb.CheckDatasetActivationRsp{RetInfo: success()}, nil
}

func (f *recordingFactorMetadataClient) ActivateDataset(context.Context, *storagepb.ActivateDatasetReq) (*storagepb.ActivateDatasetRsp, error) {
	f.targetCalls++
	return &storagepb.ActivateDatasetRsp{RetInfo: success()}, nil
}

func (f *recordingFactorMetadataClient) UpsertDatasetColumn(context.Context, *storagepb.UpsertDatasetColumnReq) (*storagepb.UpsertDatasetColumnRsp, error) {
	f.targetCalls++
	return &storagepb.UpsertDatasetColumnRsp{RetInfo: success()}, nil
}

func genericFactorPB(id, name string, outputs []string) *factorpb.FactorDef {
	return &factorpb.FactorDef{
		FactorId: id, Name: name, SourceCode: "def compute(df, params): return {}",
		InputColumns: []string{"close"}, Outputs: outputs, ParamsJson: `{}`,
		LookbackPeriods: 20, Status: domain.FactorStatusEnabled,
	}
}
