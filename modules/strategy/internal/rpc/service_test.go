package rpc

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	strategyaction "github.com/mooyang-code/moox/modules/strategy/internal/action"
	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/modules/strategy/internal/registry"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
	"github.com/mooyang-code/moox/modules/strategy/schema"
	trpc "trpc.group/trpc-go/trpc-go"
)

const rpcManifest = "api_version: moox.strategy/v1\nentrypoint: strategy.py:run\ninput:\n  history_bars: 1\n"

type fakeRuntime struct {
	output domain.Output
	hash   string
	err    error
	loads  int
	ready  int
	last   domain.ExecutionRequest
}

func (f *fakeRuntime) Load(context.Context, domain.Strategy) error {
	f.loads++
	return f.err
}

func (f *fakeRuntime) Run(
	_ context.Context,
	request domain.ExecutionRequest,
	_ domain.Strategy,
) (domain.Output, string, error) {
	f.last = request
	return f.output, f.hash, f.err
}

func (f *fakeRuntime) ReadyWorkers() int {
	return f.ready
}

type fakeLogicalAccountOwner struct {
	validateErr, claimErr, releaseErr error
	validated, claimed, released      []string
}

func (f *fakeLogicalAccountOwner) Validate(
	_ context.Context,
	spaceID, logicalAccountID string,
) error {
	f.validated = append(f.validated, spaceID+"/"+logicalAccountID)
	return f.validateErr
}

func (f *fakeLogicalAccountOwner) Claim(
	_ context.Context,
	spaceID, logicalAccountID, runnerID string,
) error {
	f.claimed = append(f.claimed, spaceID+"/"+logicalAccountID+"/"+runnerID)
	return f.claimErr
}

func (f *fakeLogicalAccountOwner) Release(
	_ context.Context,
	spaceID, logicalAccountID, runnerID string,
) error {
	f.released = append(f.released, spaceID+"/"+logicalAccountID+"/"+runnerID)
	return f.releaseErr
}

func TestCreateStrategyStoresImmutableArtifact(t *testing.T) {
	service, repo, runtime := newRPCServiceForTest(t)
	request := &strategypb.CreateStrategyReq{Strategy: &strategypb.Strategy{
		StrategyId: "strategy-1", Name: "trend", ManifestYaml: rpcManifest,
		SourceCode: "def run(context, data, params): return {'action':'hold','targets':[]}",
	}}
	first, err := service.CreateStrategy(context.Background(), request)
	if err != nil || first.GetRetInfo().GetCode() != 0 {
		t.Fatalf("CreateStrategy() = %+v, %v", first, err)
	}
	request.Strategy.SourceCode += "\n# changed"
	second, err := service.CreateStrategy(context.Background(), request)
	if err != nil || second.GetRetInfo().GetCode() == 0 {
		t.Fatalf("immutable replacement accepted: %+v, %v", second, err)
	}
	stored, err := repo.GetStrategy(context.Background(), "strategy-1")
	if err != nil {
		t.Fatal(err)
	}
	if stored.SourceHash != first.GetStrategy().GetSourceHash() || runtime.loads != 2 {
		t.Fatalf("stored=%+v loads=%d", stored, runtime.loads)
	}
}

func TestGetEngineStatusUsesCurrentRuntimeReadiness(t *testing.T) {
	service, _, runtime := newRPCServiceForTest(t)
	runtime.ready = 2
	response, err := service.GetEngineStatus(
		context.Background(),
		&strategypb.GetEngineStatusReq{},
	)
	if err != nil || response.GetWorkers() != 2 || response.GetReadyWorkers() != 2 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	runtime.ready = 0
	response, err = service.GetEngineStatus(
		context.Background(),
		&strategypb.GetEngineStatusReq{},
	)
	if err != nil || response.GetReadyWorkers() != 0 {
		t.Fatalf("degraded response=%+v err=%v", response, err)
	}
}

func TestCreateRunnerValidatesLogicalAccountOwnership(t *testing.T) {
	service, _, _ := newRPCServiceForTest(t)
	createRPCStrategy(t, service, "strategy-1")
	owner := &fakeLogicalAccountOwner{validateErr: errors.New("logical account missing")}
	service.LogicalAccounts = owner
	request := &strategypb.CreateRunnerReq{Runner: &strategypb.StrategyRunner{
		RunnerId: "runner-1", StrategyId: "strategy-1", SpaceId: "crypto",
		ViewId: "view-1", Frequency: "1m", ParamsJson: "{}",
		LogicalAccountId: "logical-1", Status: "ENABLED",
	}}
	rejected, err := service.CreateRunner(context.Background(), request)
	if err != nil || rejected.GetRetInfo().GetCode() == 0 {
		t.Fatalf("invalid logical account accepted: %+v, %v", rejected, err)
	}
	owner.validateErr = nil
	created, err := service.CreateRunner(context.Background(), request)
	if err != nil || created.GetRetInfo().GetCode() != 0 {
		t.Fatalf("CreateRunner() = %+v, %v", created, err)
	}
	if created.GetRunner().GetStatus() != "DISABLED" ||
		created.GetRunner().GetCommandSequence() != 0 ||
		len(created.GetRunner().GetCurrentTargets()) != 0 {
		t.Fatalf("new runner = %+v", created.GetRunner())
	}
	if len(owner.validated) != 2 {
		t.Fatalf("validation calls = %v", owner.validated)
	}
}

func TestRunOncePreviewDoesNotCreateResult(t *testing.T) {
	service, repo, runtime := newRPCServiceForTest(t)
	createRPCStrategy(t, service, "strategy-1")
	createRPCRunner(t, service, "runner-1", "strategy-1", "space-a", "")
	runtime.output = domain.Output{Action: domain.ActionRebalance, Targets: []domain.InstrumentTarget{}}
	runtime.hash = "input-hash"
	response, err := service.RunOnce(context.Background(), &strategypb.RunOnceReq{
		RunnerId: "runner-1", TriggerBarTime: "2026-07-29T10:00:00Z",
		Namespace: "preview", DataJson: `[{"time":"2026-07-29T10:00:00Z"}]`,
	})
	if err != nil || response.GetRetInfo().GetCode() != 0 || response.GetAccepted() {
		t.Fatalf("RunOnce() = %+v, %v", response, err)
	}
	if countRPCResults(t, repo) != 0 || runtime.last.RunnerID != "runner-1" {
		t.Fatalf("results=%d request=%+v", countRPCResults(t, repo), runtime.last)
	}
}

func TestRunOnceFailedAttemptReturnsNoResult(t *testing.T) {
	service, repo, runtime := newRPCServiceForTest(t)
	createRPCStrategy(t, service, "strategy-1")
	createRPCRunner(t, service, "runner-1", "strategy-1", "space-a", "")
	if err := repo.SetRunnerStatus(
		context.Background(), "runner-1", domain.RunnerStatusEnabled, time.Now(),
	); err != nil {
		t.Fatal(err)
	}
	runtime.err = errors.New("python failed")
	response, err := service.RunOnce(context.Background(), &strategypb.RunOnceReq{
		RunnerId: "runner-1", TriggerBarTime: "2026-07-29T10:00:00Z",
		DataJson: `[{"time":"2026-07-29T10:00:00Z"}]`,
	})
	if err != nil || response.GetRetInfo().GetCode() == 0 || response.GetResult() != nil {
		t.Fatalf("RunOnce() = %+v, %v", response, err)
	}
	runner, err := repo.GetRunner(context.Background(), "runner-1")
	if err != nil {
		t.Fatal(err)
	}
	if countRPCResults(t, repo) != 0 || runner.LastError == nil ||
		*runner.LastError != "python failed" {
		t.Fatalf("runner=%+v result_count=%d", runner, countRPCResults(t, repo))
	}
}

func TestRunOnceAcceptsCompleteHistoryWithoutStateOrDataRevision(t *testing.T) {
	service, repo, runtime := newRPCServiceForTest(t)
	createRPCStrategy(t, service, "strategy-1")
	createRPCRunner(t, service, "runner-1", "strategy-1", "space-a", "")
	if err := repo.SetRunnerStatus(
		context.Background(), "runner-1", domain.RunnerStatusEnabled, time.Now(),
	); err != nil {
		t.Fatal(err)
	}
	runtime.output = domain.Output{Action: domain.ActionHold}
	runtime.hash = "input-hash"
	response, err := service.RunOnce(context.Background(), &strategypb.RunOnceReq{
		RunnerId: "runner-1", TriggerBarTime: "2026-07-29T10:00:00Z",
		Namespace: "default", DataJson: `[{"time":"2026-07-29T10:00:00Z","close":"1"}]`,
	})
	if err != nil || response.GetRetInfo().GetCode() != 0 || !response.GetAccepted() ||
		response.GetResult() == nil {
		t.Fatalf("RunOnce() = %+v, %v", response, err)
	}
	if runtime.last.Params == nil || runtime.last.Data == nil ||
		countRPCResults(t, repo) != 1 {
		t.Fatalf("runtime request=%+v result_count=%d", runtime.last, countRPCResults(t, repo))
	}
}

func TestRunnerQueriesEnforceSpaceScope(t *testing.T) {
	service, _, _ := newRPCServiceForTest(t)
	createRPCStrategy(t, service, "strategy-1")
	createRPCRunner(t, service, "runner-a", "strategy-1", "space-a", "")
	createRPCRunner(t, service, "runner-b", "strategy-1", "space-b", "")
	ctx := trpc.BackgroundContext()
	trpc.SetMetaData(ctx, "X-Space-Id", []byte("space-a"))
	response, err := service.ListRunners(ctx, &strategypb.ListRunnersReq{SpaceId: "space-b"})
	if err != nil || response.GetRetInfo().GetCode() != 0 {
		t.Fatalf("ListRunners() = %+v, %v", response, err)
	}
	if len(response.GetRunners()) != 1 ||
		response.GetRunners()[0].GetRunnerId() != "runner-a" {
		t.Fatalf("scoped runners = %+v", response.GetRunners())
	}
	get, err := service.GetRunner(ctx, &strategypb.GetRunnerReq{RunnerId: "runner-b"})
	if err != nil || get.GetRetInfo().GetCode() == 0 {
		t.Fatalf("out-of-scope runner returned: %+v, %v", get, err)
	}
}

func newRPCServiceForTest(t *testing.T) (*Service, *store.Store, *fakeRuntime) {
	t.Helper()
	repo, err := store.Open(filepath.Join(t.TempDir(), "strategy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatal(err)
	}
	runtime := &fakeRuntime{}
	service := &Service{
		Repo: repo, Registry: &registry.Service{Repo: repo}, Runtime: runtime,
		Results: &strategyaction.Service{Repo: repo},
		Workers: 2,
		Now:     func() time.Time { return time.UnixMilli(20_000).UTC() },
		NewID:   func() string { return "result-generated" },
	}
	return service, repo, runtime
}

func createRPCStrategy(t *testing.T, service *Service, strategyID string) {
	t.Helper()
	response, err := service.CreateStrategy(context.Background(), &strategypb.CreateStrategyReq{
		Strategy: &strategypb.Strategy{
			StrategyId: strategyID, Name: strategyID, ManifestYaml: rpcManifest,
			SourceCode: "def run(context, data, params): return {'action':'hold','targets':[]}",
		},
	})
	if err != nil || response.GetRetInfo().GetCode() != 0 {
		t.Fatalf("CreateStrategy() = %+v, %v", response, err)
	}
}

func createRPCRunner(
	t *testing.T,
	service *Service,
	runnerID, strategyID, spaceID, logicalAccountID string,
) {
	t.Helper()
	response, err := service.CreateRunner(context.Background(), &strategypb.CreateRunnerReq{
		Runner: &strategypb.StrategyRunner{
			RunnerId: runnerID, StrategyId: strategyID, SpaceId: spaceID,
			ViewId: "view-1", Frequency: "1m", ParamsJson: "{}",
			LogicalAccountId: logicalAccountID,
		},
	})
	if err != nil || response.GetRetInfo().GetCode() != 0 {
		t.Fatalf("CreateRunner() = %+v, %v", response, err)
	}
}

func countRPCResults(t *testing.T, repo *store.Store) int64 {
	t.Helper()
	results, err := repo.ListResults(context.Background(), store.ResultFilter{})
	if err != nil {
		t.Fatal(err)
	}
	return int64(len(results))
}
