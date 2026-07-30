package bootstrap

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/modules/factor/internal/scheduler"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/mooyang-code/moox/modules/factor/internal/trigger"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type inventoryReconcilerStub struct {
	due       bool
	refreshes int
	err       error
}

func (s *inventoryReconcilerStub) Due(time.Time) bool { return s.due }
func (s *inventoryReconcilerStub) Refresh(context.Context) error {
	s.refreshes++
	return s.err
}

type metricsReporterStub struct{ calls int }

func (s *metricsReporterStub) Handle(context.Context) error {
	s.calls++
	return nil
}

type startupContractValidatorStub struct {
	err   error
	calls int
}

type factorTaskRepositoryStub struct {
	factor        *domain.FactorDef
	err           error
	useContextErr bool
}

func (s factorTaskRepositoryStub) Get(ctx context.Context, _ string) (*domain.FactorDef, error) {
	if s.useContextErr {
		return nil, ctx.Err()
	}
	return s.factor, s.err
}

type bindingTaskRepositoryStub struct {
	bindings []domain.FactorBinding
	err      error
}

func (s bindingTaskRepositoryStub) ListExecutable(context.Context) ([]domain.FactorBinding, error) {
	return s.bindings, s.err
}

func (s *startupContractValidatorStub) ValidateAllEnabledBindings(context.Context) error {
	s.calls++
	return s.err
}

func TestValidateStartupFactorContractsFailsClosed(t *testing.T) {
	validator := &startupContractValidatorStub{err: errors.New("persisted binding cycle")}
	err := validateStartupFactorContracts(context.Background(), validator)
	require.ErrorContains(t, err, "persisted binding cycle")
	require.Equal(t, 1, validator.calls)
}

func TestBuildSchedulerTaskUsesRealtimeHalfOpenRange(t *testing.T) {
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "factor.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(factorschema.AllSQL()))
	require.NoError(t, db.Factors().Create(context.Background(), domain.FactorDef{
		FactorID: "bias", Name: "Bias", SourceCode: "x", SourceHash: "hash",
		InputColumns: []string{"close"}, Outputs: []string{"bias"}, ParamsJSON: `{}`,
		LookbackPeriods: 100, Status: domain.FactorStatusEnabled,
	}))
	at := time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC)
	end := at.Add(3*time.Minute + time.Nanosecond)
	tasks, err := buildSchedulerTasks(context.Background(), db.Factors(), t.TempDir(), trigger.Task{
		SpaceID: "crypto", SourceDataset: "bars", TargetDataset: "bars_factor",
		SubjectID: "BTC", Freq: "1m", StartTime: at, EndTime: end, FactorIDs: []string{"bias"},
	})
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	task := tasks[0]
	require.Equal(t, at, task.StartTime)
	require.Equal(t, end, task.EndTime)
	require.Equal(t, scheduler.DeterministicTaskID(task), task.TaskID)
}

func TestDisabledFactorMakesRealtimeTaskNoop(t *testing.T) {
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "factor.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(factorschema.AllSQL()))
	require.NoError(t, db.Factors().Create(context.Background(), domain.FactorDef{
		FactorID: "bias", Name: "Bias", SourceCode: "x", SourceHash: "hash",
		InputColumns: []string{"close"}, Outputs: []string{"bias"}, ParamsJSON: `{}`,
		LookbackPeriods: 100, Status: domain.FactorStatusDisabled,
	}))
	tasks, err := buildSchedulerTasks(context.Background(), db.Factors(), t.TempDir(), trigger.Task{
		SpaceID: "crypto", SourceDataset: "bars", SubjectID: "BTC", Freq: "1m",
		StartTime: time.Now(), EndTime: time.Now().Add(time.Nanosecond), FactorIDs: []string{"bias"},
	})
	require.NoError(t, err)
	require.Empty(t, tasks)
}

func TestTaskValidatorRechecksDefinitionAndBindingScope(t *testing.T) {
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "factor.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(factorschema.AllSQL()))
	ctx := context.Background()
	factor := domain.FactorDef{
		FactorID: "bias", Name: "Bias", SourceCode: "x", SourceHash: "hash",
		InputColumns: []string{"close"}, Outputs: []string{"bias"}, ParamsJSON: `{}`,
		LookbackPeriods: 20, Status: domain.FactorStatusEnabled,
	}
	require.NoError(t, db.Factors().Create(ctx, factor))
	binding := domain.FactorBinding{
		BindingID: "bind", FactorID: "bias", SpaceID: "crypto",
		SourceDataset: "bars", TargetDataset: "bars_factor", Freq: "1m",
		SubjectMode: domain.SubjectModeInclude, SubjectsJSON: `["BTC"]`,
		Status: domain.BindingStatusEnabled,
	}
	require.NoError(t, db.Bindings().Upsert(ctx, binding))
	validate := newTaskValidator(db.Factors(), db.Bindings())
	task := scheduler.Task{FactorTask: engine.FactorTask{
		Factor: engine.FactorSpec{
			FactorID: "bias", SourceHash: "hash",
			InputColumns: []string{"close"}, ParamsJSON: `{}`,
		},
		SpaceID: "crypto", SourceDataset: "bars", TargetDataset: "bars_factor",
		SubjectID: "BTC", Freq: "1m", LookbackPeriods: 20,
	}}
	require.NoError(t, validate(ctx, task))

	binding.TargetDataset = domain.DefaultBindingTargetID
	require.NoError(t, db.Bindings().Upsert(ctx, binding))
	require.NoError(t, validate(ctx, task))

	require.NoError(t, db.Factors().SetStatus(ctx, factor.FactorID, domain.FactorStatusDisabled))
	require.ErrorIs(t, validate(ctx, task), scheduler.ErrStaleTask)

	factor.Status = domain.FactorStatusDisabled
	factor.ParamsJSON = `{"window":10}`
	require.NoError(t, db.Factors().Update(ctx, factor))
	require.NoError(t, db.Factors().SetStatus(ctx, factor.FactorID, domain.FactorStatusEnabled))
	require.ErrorIs(t, validate(ctx, task), scheduler.ErrStaleTask)

	task.Factor.ParamsJSON = factor.ParamsJSON
	task.SubjectID = "ETH"
	require.ErrorIs(t, validate(ctx, task), scheduler.ErrStaleTask)
}

func TestTaskValidatorPreservesRepositoryAndContextErrors(t *testing.T) {
	task := scheduler.Task{FactorTask: engine.FactorTask{
		Factor: engine.FactorSpec{
			FactorID: "bias", SourceHash: "hash",
			InputColumns: []string{"close"}, ParamsJSON: `{}`,
		},
		SpaceID: "crypto", SourceDataset: "bars", TargetDataset: "bars_factor",
		SubjectID: "BTC", Freq: "1m", LookbackPeriods: 20,
	}}
	factor := &domain.FactorDef{
		FactorID: "bias", SourceHash: "hash", InputColumns: []string{"close"},
		ParamsJSON: `{}`, LookbackPeriods: 20, Status: domain.FactorStatusEnabled,
	}
	repositoryErr := errors.New("repository unavailable")
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	tests := map[string]struct {
		ctx      context.Context
		validate scheduler.TaskValidator
		want     error
	}{
		"factor get": {
			ctx: context.Background(),
			validate: newTaskValidator(
				factorTaskRepositoryStub{err: repositoryErr},
				bindingTaskRepositoryStub{},
			),
			want: repositoryErr,
		},
		"binding list": {
			ctx: context.Background(),
			validate: newTaskValidator(
				factorTaskRepositoryStub{factor: factor},
				bindingTaskRepositoryStub{err: repositoryErr},
			),
			want: repositoryErr,
		},
		"context canceled": {
			ctx: canceledCtx,
			validate: newTaskValidator(
				factorTaskRepositoryStub{useContextErr: true},
				bindingTaskRepositoryStub{},
			),
			want: context.Canceled,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			err := tc.validate(tc.ctx, task)
			require.ErrorIs(t, err, tc.want)
			require.NotErrorIs(t, err, scheduler.ErrStaleTask)
		})
	}
}

func TestTaskValidatorTreatsDeletedFactorAsStale(t *testing.T) {
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "factor.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(factorschema.AllSQL()))
	ctx := context.Background()
	require.NoError(t, db.Factors().Create(ctx, domain.FactorDef{
		FactorID: "deleted", Name: "Deleted", SourceCode: "x", SourceHash: "hash",
		InputColumns: []string{"close"}, Outputs: []string{"value"}, ParamsJSON: `{}`,
		LookbackPeriods: 2, Status: domain.FactorStatusEnabled,
	}))
	require.NoError(t, db.Factors().Delete(ctx, "deleted"))
	validate := newTaskValidator(db.Factors(), db.Bindings())

	err = validate(ctx, scheduler.Task{FactorTask: engine.FactorTask{
		Factor: engine.FactorSpec{
			FactorID: "deleted", SourceHash: "hash",
			InputColumns: []string{"close"}, ParamsJSON: `{}`,
		},
		SpaceID: "crypto", SourceDataset: "bars", TargetDataset: "bars_factor",
		SubjectID: "BTC", Freq: "1m", LookbackPeriods: 2,
	}})
	require.ErrorIs(t, err, scheduler.ErrStaleTask)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestRealtimeConsumerReadyWhenEventBusDisabled(t *testing.T) {
	cfg := Default()
	cfg.EventBus.URLs = nil
	require.True(t, realtimeConsumerReady(cfg, nil))
}

func TestMetricsTimerRefreshFailureDoesNotBlockReporter(t *testing.T) {
	inventory := &inventoryReconcilerStub{due: true, err: errors.New("refresh failed")}
	reporter := &metricsReporterStub{}
	handler := metricsTimerHandler(inventory, reporter, time.Now)

	require.NoError(t, handler(context.Background()))
	require.Equal(t, 1, inventory.refreshes)
	require.Equal(t, 1, reporter.calls)
}

func TestFactorAuthInfoSignsPrimaryRequestFromRuntimeSecret(t *testing.T) {
	t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET", " primary-secret ")
	auth := factorAuthInfo()
	require.Equal(t, "moox-factor", auth.GetAppId())
	require.Equal(t, mooxsecurity.HMACSHA256Hex(" primary-secret ", []byte("moox-factor")), auth.GetAppKey())
}
