package rpc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/modules/factor/internal/registry"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/mooyang-code/moox/modules/factor/internal/taskrunner"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
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
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
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

func TestUpdateFactorRejectsEveryDefinitionChangeWhileEnabled(t *testing.T) {
	for name, mutate := range map[string]func(*factorpb.FactorDef){
		"source":   func(factor *factorpb.FactorDef) { factor.SourceCode += "\n# changed" },
		"inputs":   func(factor *factorpb.FactorDef) { factor.InputColumns = []string{"open"} },
		"params":   func(factor *factorpb.FactorDef) { factor.ParamsJson = `{"window":10}` },
		"lookback": func(factor *factorpb.FactorDef) { factor.LookbackPeriods++ },
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			db := openRPCTestDB(t)
			seedRPCFactorDefinition(t, db, "factor")
			svc := NewWithRuntime(db, nil, WithFactorsDir(t.TempDir()))
			candidate := genericFactorPB("factor", "TestFactor", []string{"value"})
			candidate.Status = domain.FactorStatusEnabled
			candidate.LookbackPeriods = 2
			candidate.SourceCode = "x"
			mutate(candidate)

			rsp, err := svc.UpdateFactor(ctx, &factorpb.UpdateFactorReq{FactorId: "factor", Factor: candidate})
			require.NoError(t, err)
			require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
			require.Contains(t, rsp.GetRetInfo().GetMsg(), "disable")
		})
	}
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

func TestUpdateDisabledFactorDoesNotSyncRuntimeMetadata(t *testing.T) {
	db := openRPCTestDB(t)
	seedRPCFactorDefinitionWithStatus(t, db, "factor", domain.FactorStatusDisabled)
	seedRPCDisabledBinding(t, db, "factor", "space")
	seedRPCDisabledBindingWithScope(t, db, "factor", "space", "second-binding", "source-2")
	metadata := newRecordingFactorMetadataClient("space", "factor")
	svc := NewWithRuntime(db, nil,
		WithFactorsDir(t.TempDir()),
		WithMetadataSync(registry.NewMetadataSync(metadata, nil)),
	)
	updated := genericFactorPB("factor", "TestFactor", []string{"value"})
	updated.ParamsJson = `{"window":10}`
	updated.Status = domain.FactorStatusDisabled
	updated.LookbackPeriods = 2

	rsp, err := svc.UpdateFactor(context.Background(), &factorpb.UpdateFactorReq{
		FactorId: "factor", Factor: updated,
	})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Empty(t, metadata.updatedFactors)
	require.Zero(t, metadata.targetCalls)
}

func TestUpsertDisabledBindingDoesNotAccessStorageMetadata(t *testing.T) {
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
	require.Empty(t, metadata.updatedFactors)
	require.Zero(t, metadata.targetCalls)
	require.Zero(t, metadata.listViewsCalls)
}

func TestUpsertPendingBindingPersistsWithoutActiveSourceIndex(t *testing.T) {
	db := openRPCTestDB(t)
	seedRPCFactorDefinitionWithStatus(t, db, "factor", domain.FactorStatusEnabled)
	metadata := newRecordingFactorMetadataClient("space", "factor")
	svc := NewWithRuntime(db, nil,
		WithFactorsDir(t.TempDir()),
		WithMetadataSync(registry.NewMetadataSync(metadata, nil)),
	)
	binding := testBindingPB(domain.SubjectModeAll, `[]`)
	binding.Status = domain.BindingStatusPendingView

	rsp, err := svc.UpsertBinding(context.Background(), &factorpb.UpsertBindingReq{Binding: binding})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, domain.BindingStatusPendingView, rsp.GetBinding().GetStatus())
	rows, total, listErr := db.Bindings().List(context.Background(), store.BindingFilter{})
	require.NoError(t, listErr)
	require.EqualValues(t, 1, total)
	require.Equal(t, domain.BindingStatusPendingView, rows[0].Status)
}

func TestUpsertBindingDisablesLocallyWithoutMetadataAccess(t *testing.T) {
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
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
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

func TestUpsertEnabledBindingValidatesContractBeforePersisting(t *testing.T) {
	ctx := context.Background()
	db := openRPCTestDB(t)
	seedRPCFactorDefinitionWithStatus(t, db, "factor", domain.FactorStatusDisabled)
	metadata := newRecordingFactorMetadataClient("space", "factor")
	svc := NewWithRuntime(db, nil,
		WithFactorsDir(t.TempDir()),
		WithMetadataSync(registry.NewMetadataSync(metadata, nil)),
	)
	binding := testBindingPB(domain.SubjectModeAll, `[]`)
	binding.Status = domain.BindingStatusEnabled

	rsp, err := svc.UpsertBinding(ctx, &factorpb.UpsertBindingReq{Binding: binding})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
	require.Equal(t, 1, metadata.listViewsCalls)
	rows, total, listErr := db.Bindings().List(ctx, store.BindingFilter{})
	require.NoError(t, listErr)
	require.EqualValues(t, 1, total)
	require.Equal(t, domain.BindingStatusEnabled, rows[0].Status)
}

func TestUpsertBindingRejectsCandidateSetSourceTargetConflictForEnabledFactor(t *testing.T) {
	ctx := context.Background()
	db := openRPCTestDB(t)
	seedRPCFactorDefinitionWithStatus(t, db, "factor", domain.FactorStatusEnabled)
	require.NoError(t, db.Bindings().Upsert(ctx, domain.FactorBinding{
		BindingID: "a-to-b", FactorID: "factor", SpaceID: "space",
		SourceDataset: "a", TargetDataset: "b", Freq: "1m",
		SubjectMode: domain.SubjectModeAll, SubjectsJSON: domain.DefaultSubjectsJSON,
		Status: domain.BindingStatusEnabled,
	}))
	metadata := newRecordingFactorMetadataClient("space", "factor")
	svc := NewWithRuntime(
		db,
		nil,
		WithFactorsDir(t.TempDir()),
		WithMetadataSync(registry.NewMetadataSync(metadata, nil)),
	)

	rsp, err := svc.UpsertBinding(ctx, &factorpb.UpsertBindingReq{Binding: &factorpb.FactorBinding{
		BindingId: "c-to-a", FactorId: "factor", SpaceId: "space",
		SourceDataset: "c", TargetDataset: "a", Freq: "1m",
		SubjectMode: domain.SubjectModeAll, Status: domain.BindingStatusEnabled,
	}})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
	require.Contains(t, rsp.GetRetInfo().GetMsg(), "also targeted")
	rows, total, listErr := db.Bindings().List(ctx, store.BindingFilter{})
	require.NoError(t, listErr)
	require.EqualValues(t, 1, total)
	require.Equal(t, "a-to-b", rows[0].BindingID)
}

func TestUpsertBindingReturnsExistingBindingLookupError(t *testing.T) {
	db := openRPCTestDB(t)
	require.NoError(t, db.Close())
	svc := NewWithRuntime(db, nil, WithFactorsDir(t.TempDir()))

	rsp, err := svc.UpsertBinding(context.Background(), &factorpb.UpsertBindingReq{
		Binding: testBindingPB(domain.SubjectModeAll, domain.DefaultSubjectsJSON),
	})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INNER_ERR, rsp.GetRetInfo().GetCode())
	require.Contains(t, rsp.GetRetInfo().GetMsg(), `find existing binding "bind"`)
}

func TestUpsertEnabledBindingRejectsFactorResultSourceWithoutPersisting(t *testing.T) {
	ctx := context.Background()
	db := openRPCTestDB(t)
	seedRPCFactorDefinitionWithStatus(t, db, "factor", domain.FactorStatusDisabled)
	metadata := newRecordingFactorMetadataClient("space", "factor")
	metadata.sourceRole = "factor_result"
	svc := NewWithRuntime(db, nil,
		WithFactorsDir(t.TempDir()),
		WithMetadataSync(registry.NewMetadataSync(metadata, nil)),
	)
	binding := testBindingPB(domain.SubjectModeAll, `[]`)
	binding.Status = domain.BindingStatusEnabled

	rsp, err := svc.UpsertBinding(ctx, &factorpb.UpsertBindingReq{Binding: binding})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
	require.Contains(t, rsp.GetRetInfo().GetMsg(), "factor_result")
	_, total, listErr := db.Bindings().List(ctx, store.BindingFilter{})
	require.NoError(t, listErr)
	require.Zero(t, total)
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

func TestSetFactorStatusEnabledRevalidatesExistingEnabledFactor(t *testing.T) {
	ctx := context.Background()
	db := openRPCTestDB(t)
	seedRPCFactorAndBinding(t, db, domain.FactorStatusEnabled)
	metadata := newRecordingFactorMetadataClient("crypto", "bias")
	metadata.secondaryOnlyView = true
	svc := NewWithRuntime(
		db,
		nil,
		WithFactorsDir(t.TempDir()),
		WithMetadataSync(registry.NewMetadataSync(metadata, nil)),
	)

	rsp, err := svc.SetFactorStatus(ctx, &factorpb.SetFactorStatusReq{
		FactorId: "bias", Status: domain.FactorStatusEnabled,
	})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
	require.Contains(t, rsp.GetRetInfo().GetMsg(), "active primary view")
}

func TestSetFactorStatusDisableWaitsForRunningTask(t *testing.T) {
	ctx := context.Background()
	db := openRPCTestDB(t)
	seedRPCFactorAndBinding(t, db, domain.FactorStatusEnabled)
	metadata := newRecordingFactorMetadataClient("crypto", "bias")
	gate := taskrunner.NewFactorGate()
	runner := &fakeRPCTaskRunner{}
	svc := NewWithRuntime(
		db,
		runner,
		WithFactorsDir(t.TempDir()),
		WithMetadataSync(registry.NewMetadataSync(metadata, nil)),
		WithFactorGate(gate),
	)
	release := gate.AcquireRun("bias")
	done := make(chan *factorpb.SetFactorStatusRsp, 1)
	go func() {
		rsp, _ := svc.SetFactorStatus(ctx, &factorpb.SetFactorStatusReq{
			FactorId: "bias", Status: domain.FactorStatusDisabled,
		})
		done <- rsp
	}()

	select {
	case <-done:
		t.Fatal("disable returned while factor task still held the run gate")
	case <-time.After(20 * time.Millisecond):
	}
	release()
	select {
	case rsp := <-done:
		require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	case <-time.After(time.Second):
		t.Fatal("disable did not complete after the running task released the gate")
	}
}

func TestUpdatedDisabledFactorDoesNotNeedQueuedTaskCleanup(t *testing.T) {
	db := openRPCTestDB(t)
	seedRPCFactorDefinitionWithStatus(t, db, "factor", domain.FactorStatusDisabled)
	runner := &fakeRPCTaskRunner{}
	svc := NewWithRuntime(
		db,
		runner,
		WithFactorsDir(t.TempDir()),
		WithFactorGate(taskrunner.NewFactorGate()),
	)
	updated := genericFactorPB("factor", "TestFactor", []string{"value"})
	updated.Status = domain.FactorStatusDisabled
	updated.ParamsJson = `{"window":10}`

	rsp, err := svc.UpdateFactor(context.Background(), &factorpb.UpdateFactorReq{
		FactorId: "factor", Factor: updated,
	})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
}

func TestFactorMutationLifecyclePreventsOldWriteAfterRecalc(t *testing.T) {
	ctx := context.Background()
	db := openRPCTestDB(t)
	seedRPCFactorAndBinding(t, db, domain.FactorStatusEnabled)
	gate := taskrunner.NewFactorGate()
	exec := newLifecycleExecutor()
	storage := &lifecycleStorage{}
	runner := taskrunner.NewService(1, storage, exec, taskrunner.WithFactorGate(gate))
	metadata := newRecordingFactorMetadataClient("crypto", "bias")
	metadata.existingTarget = true
	svc := NewWithRuntime(
		db,
		runner,
		WithFactorsDir(t.TempDir()),
		WithMetadataSync(registry.NewMetadataSync(metadata, nil)),
		WithFactorGate(gate),
	)
	factor, err := db.Factors().Get(ctx, "bias")
	require.NoError(t, err)
	start := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	oldTask, err := taskrunner.BuildTask(taskrunner.TaskScope{
		TaskID: "old", TriggerType: "recalc",
		SpaceID: "crypto", SourceDataset: "bars", TargetDataset: "bars_factor",
		SubjectID: "BTC", Freq: "1m", StartTime: start, EndTime: start.Add(time.Nanosecond),
	}, *factor, t.TempDir())
	require.NoError(t, err)
	oldDone := make(chan error, 1)
	go func() {
		oldDone <- runner.Run(ctx, oldTask)
	}()
	select {
	case <-exec.firstStarted:
	case <-time.After(time.Second):
		t.Fatal("old executor did not start")
	}

	disableDone := make(chan *factorpb.SetFactorStatusRsp, 1)
	disableStarted := make(chan struct{})
	go func() {
		close(disableStarted)
		rsp, _ := svc.SetFactorStatus(ctx, &factorpb.SetFactorStatusReq{
			FactorId: "bias", Status: domain.FactorStatusDisabled,
		})
		disableDone <- rsp
	}()
	<-disableStarted
	select {
	case <-disableDone:
		t.Fatal("disable returned before old executor completed")
	case <-time.After(20 * time.Millisecond):
	}

	close(exec.releaseFirst)
	require.NoError(t, <-oldDone)
	disableRsp := <-disableDone
	require.Equal(t, commonpb.ErrorCode_SUCCESS, disableRsp.GetRetInfo().GetCode())

	updated := genericFactorPB("bias", "Bias", []string{"bias"})
	updated.Status = domain.FactorStatusDisabled
	updateRsp, err := svc.UpdateFactor(ctx, &factorpb.UpdateFactorReq{
		FactorId: "bias", Factor: updated,
	})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, updateRsp.GetRetInfo().GetCode())
	enableRsp, err := svc.SetFactorStatus(ctx, &factorpb.SetFactorStatusReq{
		FactorId: "bias", Status: domain.FactorStatusEnabled,
	})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, enableRsp.GetRetInfo().GetCode(), enableRsp.GetRetInfo().GetMsg())
	recalcRsp, err := svc.RecalcFactor(ctx, &factorpb.RecalcFactorReq{
		FactorId: "bias", SpaceId: "crypto", SourceDataset: "bars",
		SubjectId: "BTC", Freq: "1m",
		StartTime: start.Format(time.RFC3339Nano),
		EndTime:   start.Add(time.Minute).Format(time.RFC3339Nano),
	})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, recalcRsp.GetRetInfo().GetCode())
	require.Equal(t, []float64{1, 2}, storage.values())
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
	runner := &fakeRPCTaskRunner{}
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
	svc := NewWithRuntime(openRPCTestDB(t), &fakeRPCTaskRunner{}, WithFactorsDir(t.TempDir()))
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

func TestRecalcFactorReportsStaleTaskAsConflict(t *testing.T) {
	db := openRPCTestDB(t)
	seedRPCFactorAndBinding(t, db, domain.FactorStatusEnabled)
	svc := NewWithRuntime(
		db,
		&fakeRPCTaskRunner{err: errors.Join(taskrunner.ErrStaleTask, gorm.ErrRecordNotFound)},
		WithFactorsDir(t.TempDir()),
	)
	rsp, err := svc.RecalcFactor(context.Background(), &factorpb.RecalcFactorReq{
		FactorId: "bias", SpaceId: "crypto", SourceDataset: "bars",
		SubjectId: "BTC", Freq: "1m",
		StartTime: "2026-07-26T00:00:00Z", EndTime: "2026-07-26T01:00:00Z",
	})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_CONFLICT, rsp.GetRetInfo().GetCode())
	require.Contains(t, rsp.GetRetInfo().GetMsg(), "stale")
}

func TestRecalcHonorsBindingSubjectScope(t *testing.T) {
	db := openRPCTestDB(t)
	seedRPCFactorAndBinding(t, db, domain.FactorStatusEnabled)
	require.NoError(t, db.Bindings().Upsert(context.Background(), domain.FactorBinding{
		BindingID: "bind", FactorID: "bias", SpaceID: "crypto", SourceDataset: "bars",
		Freq: "1m", SubjectMode: domain.SubjectModeInclude, SubjectsJSON: `["ETH"]`,
		TargetDataset: "bars_factor", Status: domain.BindingStatusEnabled,
	}))
	runner := &fakeRPCTaskRunner{}
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

func TestGetEngineStatusReportsWorkerAndTaskCounts(t *testing.T) {
	runner := &fakeRPCTaskRunner{status: taskrunner.Status{Workers: 100, ActiveTasks: 7, PendingTasks: 93}}
	svc := NewWithRuntime(openRPCTestDB(t), runner)
	rsp, err := svc.GetEngineStatus(context.Background(), &factorpb.GetEngineStatusReq{})
	require.NoError(t, err)
	require.EqualValues(t, 100, rsp.GetPythonWorkers())
	require.EqualValues(t, 7, rsp.GetActiveTasks())
	require.EqualValues(t, 93, rsp.GetPendingTasks())
}

type fakeRPCTaskRunner struct {
	mu     sync.Mutex
	status taskrunner.Status
	tasks  []taskrunner.Task
	err    error
}

func (f *fakeRPCTaskRunner) Status() taskrunner.Status { return f.status }
func (f *fakeRPCTaskRunner) Run(_ context.Context, task taskrunner.Task) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tasks = append(f.tasks, task)
	return f.err
}

type lifecycleExecutor struct {
	mu           sync.Mutex
	calls        int
	firstStarted chan struct{}
	releaseFirst chan struct{}
}

func newLifecycleExecutor() *lifecycleExecutor {
	return &lifecycleExecutor{
		firstStarted: make(chan struct{}),
		releaseFirst: make(chan struct{}),
	}
}

func (e *lifecycleExecutor) Execute(
	ctx context.Context,
	task *engine.FactorTask,
	frame *engine.DataFrame,
) (*engine.FactorResult, error) {
	e.mu.Lock()
	e.calls++
	call := e.calls
	e.mu.Unlock()
	if call == 1 {
		close(e.firstStarted)
		select {
		case <-e.releaseFirst:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	rows := make([]engine.FactorResultRow, 0, len(frame.DataTimes))
	for _, dataTime := range frame.DataTimes {
		rows = append(rows, engine.FactorResultRow{
			DataTime: dataTime,
			Values: map[string]any{
				task.Factor.Outputs[0]: float64(call),
			},
		})
	}
	return &engine.FactorResult{Rows: rows}, nil
}

func (*lifecycleExecutor) Close() error { return nil }

type lifecycleStorage struct {
	mu     sync.Mutex
	writes []float64
}

func (s *lifecycleStorage) ReadRangeChunk(
	_ context.Context,
	_ storageio.WindowKey,
	start time.Time,
	_ time.Time,
	_ int,
	_ int,
	_ []string,
) (*storageio.RangeChunk, error) {
	return &storageio.RangeChunk{
		Frame: &engine.DataFrame{
			DataTimes:  []time.Time{start},
			SeriesTags: []string{""},
		},
		TargetPeriods: []time.Time{start},
		Complete:      true,
	}, nil
}

func (*lifecycleStorage) ExpandEndByPeriods(
	_ context.Context,
	_ storageio.WindowKey,
	end time.Time,
	_ int,
) (*storageio.EndExpansion, error) {
	return &storageio.EndExpansion{EndTime: end, Complete: true}, nil
}

func (s *lifecycleStorage) WriteFactorPatch(
	_ context.Context,
	task *engine.FactorTask,
	result *engine.FactorResult,
) (uint64, error) {
	value := result.Rows[0].Values[task.Factor.Outputs[0]].(float64)
	s.mu.Lock()
	s.writes = append(s.writes, value)
	s.mu.Unlock()
	return uint64(len(result.Rows)), nil
}

func (s *lifecycleStorage) values() []float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]float64(nil), s.writes...)
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
	seedRPCFactorDefinitionWithStatus(t, db, factorID, domain.FactorStatusEnabled)
}

func seedRPCFactorDefinitionWithStatus(t *testing.T, db *store.Store, factorID, status string) {
	t.Helper()
	require.NoError(t, db.Factors().Create(context.Background(), domain.FactorDef{
		FactorID: factorID, Name: "TestFactor", SourceCode: "x", SourceHash: "hash",
		InputColumns: []string{"close"}, Outputs: []string{"value"}, ParamsJSON: `{}`,
		LookbackPeriods: 2, Status: status,
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
	factors           map[string]*storagepb.Factor
	updatedFactors    []*storagepb.Factor
	targetCalls       int
	getFactorRet      *commonpb.RetInfo
	createDatasetRet  *commonpb.RetInfo
	listViewsCalls    int
	sourceRole        string
	existingTarget    bool
	secondaryOnlyView bool
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

func (f *recordingFactorMetadataClient) GetDataset(_ context.Context, req *storagepb.GetDatasetReq) (*storagepb.GetDatasetRsp, error) {
	f.targetCalls++
	if req.GetDatasetId() == "target" || strings.HasSuffix(req.GetDatasetId(), "_factor") {
		if f.existingTarget {
			return &storagepb.GetDatasetRsp{RetInfo: success(), Dataset: &storagepb.Dataset{
				SpaceId: req.GetSpaceId(), DatasetId: req.GetDatasetId(), Status: "active",
				DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES,
			}}, nil
		}
		return &storagepb.GetDatasetRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_DATASET_NOT_FOUND}}, nil
	}
	return &storagepb.GetDatasetRsp{RetInfo: success(), Dataset: &storagepb.Dataset{
		SpaceId: req.GetSpaceId(), DatasetId: req.GetDatasetId(), Status: "active",
		DataKind:     storagepb.DataKind_DATA_KIND_TIME_SERIES,
		DataSourceId: "source-a", DataNodeId: "data-node", KeepDuration: "30d",
		Attributes: map[string]string{
			"dataset_role":   f.sourceRole,
			"data_source_id": "source-a",
		},
	}}, nil
}

func (f *recordingFactorMetadataClient) ListViews(_ context.Context, req *storagepb.ListViewsReq) (*storagepb.ListViewsRsp, error) {
	f.listViewsCalls++
	primaryDatasetID := req.GetDatasetId()
	if f.secondaryOnlyView {
		primaryDatasetID = "other"
	}
	return &storagepb.ListViewsRsp{RetInfo: success(), Views: []*storagepb.View{{
		SpaceId: req.GetSpaceId(), ViewId: "source_view", Status: "active",
		PrimaryDatasetId: primaryDatasetID,
		ActiveIndexId:    "index-a", ActiveColumns: []*storagepb.ViewColumn{
			{ColumnName: req.GetDatasetId() + ".close", OriginId: req.GetDatasetId() + ".close"},
		},
	}}}, nil
}

func (f *recordingFactorMetadataClient) CheckDatasetActivation(context.Context, *storagepb.CheckDatasetActivationReq) (*storagepb.CheckDatasetActivationRsp, error) {
	f.targetCalls++
	return &storagepb.CheckDatasetActivationRsp{RetInfo: success(), Ready: true}, nil
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
