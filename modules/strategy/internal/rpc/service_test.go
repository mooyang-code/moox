package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/strategy/internal/compiler"
	"github.com/mooyang-code/moox/modules/strategy/internal/config"
	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
	"github.com/mooyang-code/moox/modules/strategy/schema"
	"gorm.io/gorm"
	trpc "trpc.group/trpc-go/trpc-go"
)

type legacyOwnerStub struct {
	mu              sync.Mutex
	released        []legacyRelease
	claimed         []legacyRelease
	rebound         []legacyRelease
	called          chan struct{}
	releaseErr      error
	claimGeneration int64
}

type legacyRelease struct {
	spaceID, logicalAccountID, runnerID string
}

func (s *legacyOwnerStub) Validate(context.Context, string, string) error { return nil }
func (s *legacyOwnerStub) Claim(_ context.Context, spaceID, logicalAccountID, runnerID string) error {
	s.mu.Lock()
	s.claimed = append(s.claimed, legacyRelease{spaceID: spaceID, logicalAccountID: logicalAccountID, runnerID: runnerID})
	s.mu.Unlock()
	return nil
}
func (s *legacyOwnerStub) ClaimWithGeneration(ctx context.Context, spaceID, logicalAccountID, runnerID string) (int64, error) {
	if err := s.Claim(ctx, spaceID, logicalAccountID, runnerID); err != nil {
		return 0, err
	}
	s.mu.Lock()
	generation := s.claimGeneration
	s.mu.Unlock()
	if generation <= 0 {
		generation = 1
	}
	return generation, nil
}
func (s *legacyOwnerStub) Release(_ context.Context, spaceID, logicalAccountID, runnerID string) error {
	s.mu.Lock()
	s.released = append(s.released, legacyRelease{spaceID: spaceID, logicalAccountID: logicalAccountID, runnerID: runnerID})
	called := s.called
	s.mu.Unlock()
	if called != nil {
		select {
		case called <- struct{}{}:
		default:
		}
	}
	return s.releaseErr
}
func (s *legacyOwnerStub) Rebind(_ context.Context, spaceID, logicalAccountID, runnerID, _ string) (int64, error) {
	s.mu.Lock()
	s.rebound = append(s.rebound, legacyRelease{spaceID: spaceID, logicalAccountID: logicalAccountID, runnerID: runnerID})
	called := s.called
	s.mu.Unlock()
	if called != nil {
		select {
		case called <- struct{}{}:
		default:
		}
	}
	return 1, nil
}

func TestReconcileLegacyOwnersReleasesDifferentAccount(t *testing.T) {
	repo := openLegacyOwnerStore(t)
	account := "logical-b"
	if err := repo.CreateRunner(context.Background(), domain.StrategyRunner{
		ID: "legacy-runner", StrategyID: "strategy", SpaceID: "space", SourceViewID: "view", Frequency: "1m",
		LogicalAccountID: &account, Status: domain.RunnerStatusEnabled, CreatedAt: time.UnixMilli(1), UpdatedAt: time.UnixMilli(1),
	}); err != nil {
		t.Fatal(err)
	}
	owner := &legacyOwnerStub{}
	service := &Service{Repo: repo, LogicalAccounts: owner}
	if err := service.ReconcileLegacyOwners(context.Background()); err != nil {
		t.Fatalf("ReconcileLegacyOwners() error = %v", err)
	}
	if err := service.ReconcileLegacyOwners(context.Background()); err != nil {
		t.Fatalf("second ReconcileLegacyOwners() error = %v", err)
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if len(owner.released) != 1 || owner.released[0] != (legacyRelease{spaceID: "space", logicalAccountID: "logical-a", runnerID: "legacy-runner"}) {
		t.Fatalf("released owners = %+v", owner.released)
	}
}

func TestReconcileDisabledOwnersTreatsOwnerConflictAsAlreadyReleased(t *testing.T) {
	repo := openLegacyOwnerStore(t)
	account := "logical-a"
	if err := repo.CreateRunner(context.Background(), domain.StrategyRunner{
		ID: "disabled-runner", StrategyID: "strategy", SpaceID: "space", SourceViewID: "view", Frequency: "1m",
		LogicalAccountID: &account, Status: domain.RunnerStatusDisabled, CreatedAt: time.UnixMilli(1), UpdatedAt: time.UnixMilli(1),
	}); err != nil {
		t.Fatal(err)
	}
	owner := &legacyOwnerStub{releaseErr: errors.New("Trade LogicalAccount release failed (code=14): owner conflict")}
	service := &Service{Repo: repo, LogicalAccounts: owner}
	if err := service.ReconcileDisabledOwners(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileLegacyOwnersSkipsLiveTakeover(t *testing.T) {
	repo := openLegacyOwnerStore(t)
	account := "logical-a"
	if err := repo.CreateRunner(context.Background(), domain.StrategyRunner{
		ID: "legacy-runner", StrategyID: "strategy", SpaceID: "space", SourceViewID: "view", Frequency: "1m",
		LogicalAccountID: &account, Status: domain.RunnerStatusEnabled, CreatedAt: time.UnixMilli(1), UpdatedAt: time.UnixMilli(1),
	}); err != nil {
		t.Fatal(err)
	}
	owner := &legacyOwnerStub{}
	service := &Service{Repo: repo, LogicalAccounts: owner}
	if err := service.ReconcileLegacyOwners(context.Background()); err != nil {
		t.Fatalf("ReconcileLegacyOwners() error = %v", err)
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if len(owner.released) != 0 || len(owner.rebound) != 1 || owner.rebound[0] != (legacyRelease{spaceID: "space", logicalAccountID: "logical-a", runnerID: "legacy-runner"}) {
		t.Fatalf("owner lifecycle calls = rebound=%+v released=%+v", owner.rebound, owner.released)
	}
}

func TestReconcileLegacyOwnersWaitsForRunnerLock(t *testing.T) {
	repo := openLegacyOwnerStore(t)
	owner := &legacyOwnerStub{called: make(chan struct{}, 1)}
	service := &Service{Repo: repo, LogicalAccounts: owner}
	unlock := service.lockRunner("legacy-runner")
	done := make(chan error, 1)
	go func() { done <- service.ReconcileLegacyOwners(context.Background()) }()
	select {
	case <-owner.called:
		t.Fatal("ReconcileLegacyOwners touched owner while runner lock was held")
	case <-time.After(50 * time.Millisecond):
	}
	unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ReconcileLegacyOwners() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ReconcileLegacyOwners did not finish after runner unlock")
	}
	select {
	case <-owner.called:
	case <-time.After(time.Second):
		t.Fatal("legacy owner was not reconciled")
	}
}

func TestSetRunnerStatusReclaimsOwnerWhenAlreadyEnabled(t *testing.T) {
	repo := openLegacyOwnerStore(t)
	compiled, err := json.Marshal(compiler.CompiledStrategy{
		APIVersion: config.APIVersion,
		Kind:       config.Kind,
		SpaceID:    "space",
		SourceView: compiler.CompiledView{ID: "source", Status: "active", Frequency: "1m"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveStrategy(context.Background(), domain.Strategy{
		ID: "strategy", Name: "strategy", Kind: config.Kind, CompiledJSON: compiled,
		CreatedAt: time.UnixMilli(1),
	}); err != nil {
		t.Fatal(err)
	}
	account := "logical-a"
	if err := repo.CreateRunner(context.Background(), domain.StrategyRunner{
		ID: "runner-enabled", StrategyID: "strategy", SpaceID: "space", SourceViewID: "source", Frequency: "1m",
		LogicalAccountID: &account, Status: domain.RunnerStatusEnabled, CreatedAt: time.UnixMilli(1), UpdatedAt: time.UnixMilli(1),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CommitEvaluation(context.Background(), store.CommitEvaluationRequest{
		Result:          domain.StrategyResult{ID: "old-result", RunnerID: "runner-enabled", StrategyID: "strategy", PeriodTime: time.UnixMilli(2000), InputHash: "hash", Action: domain.ActionRebalance, CreatedAt: time.UnixMilli(3000)},
		OwnerGeneration: 1,
		Evaluation:      domain.Evaluation{Action: domain.ActionRebalance, DebugInfo: map[string]any{"owner_generation": int64(1)}, Targets: []domain.InstrumentTarget{{InstrumentID: "BTC", TargetWeight: "1"}}},
	}); err != nil {
		t.Fatal(err)
	}
	owner := &legacyOwnerStub{claimGeneration: 2}
	catalog := &runnerVerifyCatalog{}
	service := &Service{Repo: repo, LogicalAccounts: owner, Compiler: &compiler.Compiler{Factors: catalog, Storage: catalog}}
	ctx := trpc.BackgroundContext()
	trpc.SetMetaData(ctx, "X-Space-Id", []byte("space"))
	response, err := service.SetRunnerStatus(ctx, &strategypb.SetRunnerStatusReq{RunnerId: "runner-enabled", Status: string(domain.RunnerStatusEnabled)})
	if err != nil || response.GetRetInfo().GetCode() != 0 {
		t.Fatalf("SetRunnerStatus() = %+v, %v", response, err)
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if len(owner.claimed) != 1 || owner.claimed[0] != (legacyRelease{spaceID: "space", logicalAccountID: "logical-a", runnerID: "runner-enabled"}) {
		t.Fatalf("claimed owners = %+v", owner.claimed)
	}
	runner, err := repo.GetRunner(context.Background(), "runner-enabled")
	if err != nil {
		t.Fatal(err)
	}
	if string(runner.CurrentTargetsJSON) != `[]` || runner.LastResultID != nil {
		t.Fatalf("same-status claim did not reset stale lifecycle: %+v", runner)
	}
}

type runnerVerifyCatalog struct{}

func (runnerVerifyCatalog) GetFactor(context.Context, string) (compiler.FactorDescriptor, error) {
	return compiler.FactorDescriptor{}, nil
}
func (runnerVerifyCatalog) ListBindings(context.Context, string) ([]compiler.BindingDescriptor, error) {
	return nil, nil
}
func (runnerVerifyCatalog) GetView(_ context.Context, id string) (compiler.ViewDescriptor, error) {
	return compiler.ViewDescriptor{ID: id, Status: "active", Frequency: "1m"}, nil
}
func (runnerVerifyCatalog) ListViewColumns(context.Context, string) ([]compiler.ViewColumn, error) {
	return nil, nil
}

func openLegacyOwnerStore(t *testing.T) *store.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "strategy.db")
	repo, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`
CREATE TABLE legacy_strategy_v1_strategy_runners_001 (
    runner_id TEXT PRIMARY KEY,
    space_id TEXT NOT NULL,
    logical_account_id TEXT,
    owner_reconciled INTEGER NOT NULL DEFAULT 0
);
INSERT INTO legacy_strategy_v1_strategy_runners_001(runner_id, space_id, logical_account_id)
VALUES ('legacy-runner', 'space', 'logical-a');
`).Error; err != nil {
		t.Fatal(err)
	}
	legacyDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := legacyDB.Close(); err != nil {
		t.Fatal(err)
	}
	return repo
}
