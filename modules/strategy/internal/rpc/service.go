package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/compiler"
	"github.com/mooyang-code/moox/modules/strategy/internal/config"
	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/modules/strategy/internal/registry"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"gorm.io/gorm"
	trpc "trpc.group/trpc-go/trpc-go"
)

type LogicalAccountOwner interface {
	Validate(context.Context, string, string) error
	Claim(context.Context, string, string, string) error
	Release(context.Context, string, string, string) error
}

// LogicalAccountOwnerRebinder is implemented by the Trade client when a V1
// archived runner is reused by a V2 runner with the same identity. Keeping it
// separate preserves compatibility with lightweight owner stubs while making
// the production handoff explicit and atomic.
type LogicalAccountOwnerRebinder interface {
	Rebind(context.Context, string, string, string, string) (int64, error)
}

// LogicalAccountOwnerClaimer is an optional stronger claim contract. The
// returned generation lets Strategy repair its local snapshot when an
// already-enabled Runner's Trade owner was lost and Claim starts a new
// lifecycle. Lightweight test/embedded owners may continue implementing only
// LogicalAccountOwner.
type LogicalAccountOwnerClaimer interface {
	ClaimWithGeneration(context.Context, string, string, string) (int64, error)
}

type Service struct {
	Repo            *store.Store
	Registry        *registry.Service
	Compiler        *compiler.Compiler
	CompilerFactory func(string) *compiler.Compiler
	LogicalAccounts LogicalAccountOwner
	Now             func() time.Time
	runnerLocks     sync.Map
}

// ReconcileDisabledOwners repairs the cross-service half of a disable
// transition. Strategy persists DISABLED before asking Trade to release an
// owner; if the process or network fails between those steps, the next
// reconciliation pass retries the idempotent release.
func (s *Service) ReconcileDisabledOwners(ctx context.Context) error {
	if s == nil || s.Repo == nil || s.LogicalAccounts == nil {
		return nil
	}
	runners, err := s.Repo.ListRunners(ctx, store.RunnerFilter{Status: domain.RunnerStatusDisabled})
	if err != nil {
		return err
	}
	var releaseErr error
	for _, runner := range runners {
		// SetRunnerStatus acquires the same per-runner lock while it claims a
		// new owner. Re-read under that lock so a stale DISABLED list cannot
		// release an owner that EnableRunner has just claimed.
		unlock := s.lockRunner(runner.ID)
		current, getErr := s.Repo.GetRunner(ctx, runner.ID)
		if getErr != nil {
			unlock()
			releaseErr = errors.Join(releaseErr, fmt.Errorf("runner %s: reload: %w", runner.ID, getErr))
			continue
		}
		if current.Status != domain.RunnerStatusDisabled || current.LogicalAccountID == nil {
			unlock()
			continue
		}
		if err := s.releaseRunner(ctx, current); err != nil {
			// A different Runner may have claimed the account after this
			// Runner was disabled. Trade's owner-conflict response means the
			// disabled Runner is already released and is therefore reconciled.
			if isOwnerConflict(err) {
				unlock()
				continue
			}
			releaseErr = errors.Join(releaseErr, fmt.Errorf("runner %s: %w", runner.ID, err))
		}
		unlock()
	}
	return releaseErr
}

func isOwnerConflict(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "owner conflict") || strings.Contains(message, "code=14")
}

// ReconcileLegacyOwners releases Trade owners left behind when a V1 runner
// was archived during the V2 schema migration. The operation is idempotent;
// it is retried periodically until Trade confirms that the old owner is gone.
func (s *Service) ReconcileLegacyOwners(ctx context.Context) error {
	if s == nil || s.Repo == nil || s.LogicalAccounts == nil {
		return nil
	}
	owners, err := s.Repo.ListLegacyRunnerOwners(ctx)
	if err != nil {
		return err
	}
	var releaseErr error
	for _, owner := range owners {
		// SetRunnerStatus uses the same per-runner lock while claiming a new
		// owner. Re-read under that lock so an archived row cannot release a
		// live V2 owner between the read and the Trade release call.
		unlock := s.lockRunner(owner.RunnerID)
		current, getErr := s.Repo.GetRunner(ctx, owner.RunnerID)
		if getErr == nil {
			// Reusing a runner ID for the same space/account is a deliberate
			// takeover of the archived binding. Rebind must advance Trade's
			// lifecycle generation and clear the old target without dropping
			// ownership or pausing automation.
			if current.Status == domain.RunnerStatusEnabled &&
				current.SpaceID == owner.SpaceID &&
				current.LogicalAccountID != nil &&
				*current.LogicalAccountID == owner.LogicalAccountID {
				rebinder, ok := s.LogicalAccounts.(LogicalAccountOwnerRebinder)
				if !ok {
					unlock()
					releaseErr = errors.Join(releaseErr, fmt.Errorf("legacy runner %s: Trade owner rebind is unavailable", owner.RunnerID))
					continue
				}
				rebindKey := owner.TableName + "/" + owner.RunnerID
				generation, err := rebinder.Rebind(ctx, owner.SpaceID, owner.LogicalAccountID, owner.RunnerID, rebindKey)
				if err != nil {
					unlock()
					releaseErr = errors.Join(releaseErr, fmt.Errorf("legacy runner %s: rebind: %w", owner.RunnerID, err))
					continue
				}
				if err := s.Repo.ResetRunnerLifecycle(ctx, owner.RunnerID, generation, s.nowTime()); err != nil {
					unlock()
					releaseErr = errors.Join(releaseErr, fmt.Errorf("legacy runner %s: reset Strategy lifecycle: %w", owner.RunnerID, err))
					continue
				}
				markErr := s.Repo.MarkLegacyRunnerOwnerReconciled(ctx, owner)
				unlock()
				if markErr != nil {
					releaseErr = errors.Join(releaseErr, fmt.Errorf("legacy runner %s: mark takeover: %w", owner.RunnerID, markErr))
				}
				continue
			}
		} else if !errors.Is(getErr, gorm.ErrRecordNotFound) {
			unlock()
			releaseErr = errors.Join(releaseErr, fmt.Errorf("legacy runner %s: reload: %w", owner.RunnerID, getErr))
			continue
		}
		releaseErrForOwner := s.LogicalAccounts.Release(ctx, owner.SpaceID, owner.LogicalAccountID, owner.RunnerID)
		if err := releaseErrForOwner; err != nil {
			unlock()
			releaseErr = errors.Join(releaseErr, fmt.Errorf("legacy runner %s: %w", owner.RunnerID, err))
			continue
		}
		if err := s.Repo.MarkLegacyRunnerOwnerReconciled(ctx, owner); err != nil {
			unlock()
			releaseErr = errors.Join(releaseErr, fmt.Errorf("legacy runner %s: mark release: %w", owner.RunnerID, err))
			continue
		}
		unlock()
	}
	return releaseErr
}

func (s *Service) lockRunner(runnerID string) func() {
	value, _ := s.runnerLocks.LoadOrStore(runnerID, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	lock.Lock()
	return lock.Unlock
}

func (s *Service) CreateStrategy(ctx context.Context, req *strategypb.CreateStrategyReq) (*strategypb.CreateStrategyRsp, error) {
	if req == nil || req.GetStrategy() == nil || s.Registry == nil {
		return &strategypb.CreateStrategyRsp{RetInfo: invalid(errors.New("strategy registry and manifest are required"))}, nil
	}
	value := req.GetStrategy()
	spaceID, scopeErr := requireSpaceID(ctx)
	if scopeErr != nil {
		return &strategypb.CreateStrategyRsp{RetInfo: invalid(scopeErr)}, nil
	}
	selectedCompiler := s.compilerFor(spaceID)
	if selectedCompiler == nil {
		return &strategypb.CreateStrategyRsp{RetInfo: invalid(errors.New("strategy compiler is unavailable"))}, nil
	}
	strategy, err := s.Registry.PrepareCoinSelection(ctx, value.GetStrategyId(), value.GetName(), value.GetManifestYaml(), spaceID, *selectedCompiler)
	if err != nil {
		return &strategypb.CreateStrategyRsp{RetInfo: invalid(err)}, nil
	}
	if err := s.Registry.Save(ctx, strategy); err != nil {
		return &strategypb.CreateStrategyRsp{RetInfo: invalid(err)}, nil
	}
	return &strategypb.CreateStrategyRsp{RetInfo: success(), Strategy: strategyProto(strategy)}, nil
}

func (s *Service) CreateRunner(ctx context.Context, req *strategypb.CreateRunnerReq) (*strategypb.CreateRunnerRsp, error) {
	if req == nil || req.GetRunner() == nil || s.Repo == nil {
		return &strategypb.CreateRunnerRsp{RetInfo: invalid(errors.New("runner is required"))}, nil
	}
	value := req.GetRunner()
	if err := validateRunnerIdentity(value); err != nil {
		return &strategypb.CreateRunnerRsp{RetInfo: invalid(err)}, nil
	}
	scoped, scopeErr := requireSpaceID(ctx)
	if scopeErr != nil {
		return &strategypb.CreateRunnerRsp{RetInfo: invalid(scopeErr)}, nil
	}
	if scoped != value.GetSpaceId() {
		return &strategypb.CreateRunnerRsp{RetInfo: invalid(errors.New("runner is outside the current space"))}, nil
	}
	strategy, err := s.Repo.GetStrategy(ctx, value.GetStrategyId())
	if err != nil {
		return &strategypb.CreateRunnerRsp{RetInfo: invalid(fmt.Errorf("get strategy: %w", err))}, nil
	}
	compiled, err := decodeCompiled(strategy)
	if err != nil {
		return &strategypb.CreateRunnerRsp{RetInfo: invalid(err)}, nil
	}
	if compiled.SpaceID != value.GetSpaceId() {
		return &strategypb.CreateRunnerRsp{RetInfo: invalid(errors.New("runner space_id does not match compiled strategy"))}, nil
	}
	if value.GetLogicalAccountId() != "" {
		if s.LogicalAccounts == nil {
			return &strategypb.CreateRunnerRsp{RetInfo: invalid(errors.New("logical account owner client is unavailable"))}, nil
		}
		if err := s.LogicalAccounts.Validate(ctx, value.GetSpaceId(), value.GetLogicalAccountId()); err != nil {
			return &strategypb.CreateRunnerRsp{RetInfo: invalid(err)}, nil
		}
	}
	now := s.nowTime()
	runner := domain.StrategyRunner{ID: value.GetRunnerId(), StrategyID: value.GetStrategyId(), SpaceID: value.GetSpaceId(), SourceViewID: compiled.SourceView.ID, Frequency: compiled.SourceView.Frequency, LogicalAccountID: optionalString(value.GetLogicalAccountId()), Status: domain.RunnerStatusDisabled, CreatedAt: now, UpdatedAt: now}
	if err := s.Repo.CreateRunner(ctx, runner); err != nil {
		return &strategypb.CreateRunnerRsp{RetInfo: invalid(err)}, nil
	}
	created, err := s.Repo.GetRunner(ctx, runner.ID)
	if err != nil {
		return &strategypb.CreateRunnerRsp{RetInfo: invalid(err)}, nil
	}
	return &strategypb.CreateRunnerRsp{RetInfo: success(), Runner: runnerProto(created)}, nil
}

func (s *Service) GetStrategy(ctx context.Context, req *strategypb.GetStrategyReq) (*strategypb.GetStrategyRsp, error) {
	if req == nil || req.GetStrategyId() == "" || s.Repo == nil {
		return &strategypb.GetStrategyRsp{RetInfo: invalid(errors.New("strategy_id is required"))}, nil
	}
	if _, err := requireSpaceID(ctx); err != nil {
		return &strategypb.GetStrategyRsp{RetInfo: invalid(err)}, nil
	}
	value, err := s.Repo.GetStrategy(ctx, req.GetStrategyId())
	if err != nil {
		return &strategypb.GetStrategyRsp{RetInfo: invalid(err)}, nil
	}
	if err := ensureStrategyScope(ctx, value); err != nil {
		return &strategypb.GetStrategyRsp{RetInfo: invalid(err)}, nil
	}
	return &strategypb.GetStrategyRsp{RetInfo: success(), Strategy: strategyProto(value)}, nil
}

func (s *Service) ListStrategies(ctx context.Context, req *strategypb.ListStrategiesReq) (*strategypb.ListStrategiesRsp, error) {
	if s.Repo == nil {
		return &strategypb.ListStrategiesRsp{RetInfo: invalid(errors.New("strategy repository is unavailable"))}, nil
	}
	if _, err := requireSpaceID(ctx); err != nil {
		return &strategypb.ListStrategiesRsp{RetInfo: invalid(err)}, nil
	}
	values, err := s.Repo.ListStrategies(ctx)
	if err != nil {
		return &strategypb.ListStrategiesRsp{RetInfo: invalid(err)}, nil
	}
	scopedValues := values
	if requestSpaceID(ctx) != "" {
		scopedValues = make([]domain.Strategy, 0, len(values))
		for _, value := range values {
			if ensureStrategyScope(ctx, value) == nil {
				scopedValues = append(scopedValues, value)
			}
		}
	}
	page, size, start, end := pageBounds(req.GetPage(), len(scopedValues))
	items := make([]*strategypb.Strategy, 0, end-start)
	for _, value := range scopedValues[start:end] {
		items = append(items, strategyProto(value))
	}
	return &strategypb.ListStrategiesRsp{RetInfo: success(), Strategies: items, Total: int64(len(scopedValues)), Page: int32(page), PageSize: int32(size)}, nil
}

func (s *Service) GetRunner(ctx context.Context, req *strategypb.GetRunnerReq) (*strategypb.GetRunnerRsp, error) {
	if req == nil || req.GetRunnerId() == "" || s.Repo == nil {
		return &strategypb.GetRunnerRsp{RetInfo: invalid(errors.New("runner_id is required"))}, nil
	}
	if _, err := requireSpaceID(ctx); err != nil {
		return &strategypb.GetRunnerRsp{RetInfo: invalid(err)}, nil
	}
	value, err := s.Repo.GetRunner(ctx, req.GetRunnerId())
	if err != nil {
		return &strategypb.GetRunnerRsp{RetInfo: invalid(err)}, nil
	}
	if err := ensureRunnerScope(ctx, value); err != nil {
		return &strategypb.GetRunnerRsp{RetInfo: invalid(err)}, nil
	}
	return &strategypb.GetRunnerRsp{RetInfo: success(), Runner: runnerProto(value)}, nil
}

func (s *Service) ListRunners(ctx context.Context, req *strategypb.ListRunnersReq) (*strategypb.ListRunnersRsp, error) {
	if s.Repo == nil {
		return &strategypb.ListRunnersRsp{RetInfo: invalid(errors.New("strategy repository is unavailable"))}, nil
	}
	scoped, scopeErr := requireSpaceID(ctx)
	if scopeErr != nil {
		return &strategypb.ListRunnersRsp{RetInfo: invalid(scopeErr)}, nil
	}
	filter := store.RunnerFilter{}
	if req != nil {
		filter.StrategyID, filter.SpaceID, filter.Status = req.GetStrategyId(), req.GetSpaceId(), domain.RunnerStatus(req.GetStatus())
	}
	filter.SpaceID = scoped
	values, err := s.Repo.ListRunners(ctx, filter)
	if err != nil {
		return &strategypb.ListRunnersRsp{RetInfo: invalid(err)}, nil
	}
	page, size, start, end := pageBounds(req.GetPage(), len(values))
	items := make([]*strategypb.StrategyRunner, 0, end-start)
	for _, value := range values[start:end] {
		items = append(items, runnerProto(value))
	}
	return &strategypb.ListRunnersRsp{RetInfo: success(), Runners: items, Total: int64(len(values)), Page: int32(page), PageSize: int32(size)}, nil
}

func (s *Service) UpdateRunner(ctx context.Context, req *strategypb.UpdateRunnerReq) (*strategypb.UpdateRunnerRsp, error) {
	if req == nil || req.GetRunner() == nil || s.Repo == nil {
		return &strategypb.UpdateRunnerRsp{RetInfo: invalid(errors.New("runner is required"))}, nil
	}
	if _, err := requireSpaceID(ctx); err != nil {
		return &strategypb.UpdateRunnerRsp{RetInfo: invalid(err)}, nil
	}
	value := req.GetRunner()
	unlock := s.lockRunner(value.GetRunnerId())
	defer unlock()
	if err := validateRunnerIdentity(value); err != nil {
		return &strategypb.UpdateRunnerRsp{RetInfo: invalid(err)}, nil
	}
	current, err := s.Repo.GetRunner(ctx, value.GetRunnerId())
	if err != nil {
		return &strategypb.UpdateRunnerRsp{RetInfo: invalid(err)}, nil
	}
	if err := ensureRunnerScope(ctx, current); err != nil {
		return &strategypb.UpdateRunnerRsp{RetInfo: invalid(err)}, nil
	}
	if current.Status != domain.RunnerStatusDisabled {
		return &strategypb.UpdateRunnerRsp{RetInfo: invalid(store.ErrRunnerEnabled)}, nil
	}
	if value.GetSpaceId() != current.SpaceID {
		return &strategypb.UpdateRunnerRsp{RetInfo: invalid(errors.New("runner space_id is immutable"))}, nil
	}
	strategy, err := s.Repo.GetStrategy(ctx, value.GetStrategyId())
	if err != nil {
		return &strategypb.UpdateRunnerRsp{RetInfo: invalid(err)}, nil
	}
	compiled, err := decodeCompiled(strategy)
	if err != nil || compiled.SpaceID != current.SpaceID {
		if err == nil {
			err = errors.New("compiled strategy space_id does not match runner")
		}
		return &strategypb.UpdateRunnerRsp{RetInfo: invalid(err)}, nil
	}
	if value.GetLogicalAccountId() != "" {
		if s.LogicalAccounts == nil {
			return &strategypb.UpdateRunnerRsp{RetInfo: invalid(errors.New("logical account owner client is unavailable"))}, nil
		}
		if err := s.LogicalAccounts.Validate(ctx, current.SpaceID, value.GetLogicalAccountId()); err != nil {
			return &strategypb.UpdateRunnerRsp{RetInfo: invalid(err)}, nil
		}
	}
	if current.LogicalAccountID != nil && dereference(current.LogicalAccountID) != value.GetLogicalAccountId() {
		if err := s.releaseRunner(ctx, current); err != nil {
			return &strategypb.UpdateRunnerRsp{RetInfo: invalid(err)}, nil
		}
	}
	updated := domain.StrategyRunner{ID: current.ID, StrategyID: value.GetStrategyId(), SpaceID: current.SpaceID, SourceViewID: compiled.SourceView.ID, Frequency: compiled.SourceView.Frequency, LogicalAccountID: optionalString(value.GetLogicalAccountId()), UpdatedAt: s.nowTime()}
	if err := s.Repo.UpdateRunner(ctx, updated); err != nil {
		return &strategypb.UpdateRunnerRsp{RetInfo: invalid(err)}, nil
	}
	result, err := s.Repo.GetRunner(ctx, current.ID)
	if err != nil {
		return &strategypb.UpdateRunnerRsp{RetInfo: invalid(err)}, nil
	}
	return &strategypb.UpdateRunnerRsp{RetInfo: success(), Runner: runnerProto(result)}, nil
}

func (s *Service) SetRunnerStatus(ctx context.Context, req *strategypb.SetRunnerStatusReq) (*strategypb.SetRunnerStatusRsp, error) {
	if req == nil || req.GetRunnerId() == "" || s.Repo == nil {
		return &strategypb.SetRunnerStatusRsp{RetInfo: invalid(errors.New("runner_id and status are required"))}, nil
	}
	if _, err := requireSpaceID(ctx); err != nil {
		return &strategypb.SetRunnerStatusRsp{RetInfo: invalid(err)}, nil
	}
	unlock := s.lockRunner(req.GetRunnerId())
	defer unlock()
	status := domain.RunnerStatus(req.GetStatus())
	if status != domain.RunnerStatusEnabled && status != domain.RunnerStatusDisabled {
		return &strategypb.SetRunnerStatusRsp{RetInfo: invalid(errors.New("runner status must be ENABLED or DISABLED"))}, nil
	}
	runner, err := s.Repo.GetRunner(ctx, req.GetRunnerId())
	if err != nil {
		return &strategypb.SetRunnerStatusRsp{RetInfo: invalid(err)}, nil
	}
	if err := ensureRunnerScope(ctx, runner); err != nil {
		return &strategypb.SetRunnerStatusRsp{RetInfo: invalid(err)}, nil
	}
	if runner.Status == status {
		// Both same-status operations reconcile the cross-service owner. An
		// ENABLED retry repairs a missing Trade claim; a DISABLED retry repairs
		// a release that may have failed after the local status was persisted.
		if status == domain.RunnerStatusEnabled {
			if err := s.verifyRunnerDependencies(ctx, runner); err != nil {
				return &strategypb.SetRunnerStatusRsp{RetInfo: invalid(err)}, nil
			}
			generation, err := s.claimRunnerWithGeneration(ctx, runner)
			if err != nil {
				return &strategypb.SetRunnerStatusRsp{RetInfo: invalid(err)}, nil
			}
			if generation > 0 {
				if err := s.Repo.ResetRunnerLifecycle(ctx, runner.ID, generation, s.nowTime()); err != nil {
					return &strategypb.SetRunnerStatusRsp{RetInfo: invalid(err)}, nil
				}
			}
			// ResetRunnerLifecycle may clear the cached target/result after the
			// claim. Return the persisted state rather than the stale pre-claim
			// runner snapshot.
			updated, getErr := s.Repo.GetRunner(ctx, runner.ID)
			if getErr != nil {
				return &strategypb.SetRunnerStatusRsp{RetInfo: invalid(getErr)}, nil
			}
			return &strategypb.SetRunnerStatusRsp{RetInfo: success(), Runner: runnerProto(updated)}, nil
		} else if err := s.releaseRunner(ctx, runner); err != nil {
			return &strategypb.SetRunnerStatusRsp{RetInfo: invalid(err)}, nil
		}
		return &strategypb.SetRunnerStatusRsp{RetInfo: success(), Runner: runnerProto(runner)}, nil
	}
	if status == domain.RunnerStatusEnabled {
		if err := s.verifyRunnerDependencies(ctx, runner); err != nil {
			return &strategypb.SetRunnerStatusRsp{RetInfo: invalid(err)}, nil
		}
		if err := s.claimRunner(ctx, runner); err != nil {
			return &strategypb.SetRunnerStatusRsp{RetInfo: invalid(err)}, nil
		}
		if err := s.Repo.SetRunnerStatus(ctx, runner.ID, status, s.nowTime()); err != nil {
			_ = s.releaseRunner(ctx, runner)
			return &strategypb.SetRunnerStatusRsp{RetInfo: invalid(err)}, nil
		}
	} else {
		if err := s.Repo.SetRunnerStatus(ctx, runner.ID, status, s.nowTime()); err != nil {
			return &strategypb.SetRunnerStatusRsp{RetInfo: invalid(err)}, nil
		}
		if err := s.releaseRunner(ctx, runner); err != nil {
			return &strategypb.SetRunnerStatusRsp{RetInfo: invalid(err)}, nil
		}
	}
	updated, err := s.Repo.GetRunner(ctx, runner.ID)
	if err != nil {
		return &strategypb.SetRunnerStatusRsp{RetInfo: invalid(err)}, nil
	}
	return &strategypb.SetRunnerStatusRsp{RetInfo: success(), Runner: runnerProto(updated)}, nil
}

func (s *Service) ListStrategyResults(ctx context.Context, req *strategypb.ListStrategyResultsReq) (*strategypb.ListStrategyResultsRsp, error) {
	if s.Repo == nil {
		return &strategypb.ListStrategyResultsRsp{RetInfo: invalid(errors.New("strategy repository is unavailable"))}, nil
	}
	if req == nil || strings.TrimSpace(req.GetRunnerId()) == "" {
		return &strategypb.ListStrategyResultsRsp{RetInfo: invalid(errors.New("runner_id is required"))}, nil
	}
	if _, err := requireSpaceID(ctx); err != nil {
		return &strategypb.ListStrategyResultsRsp{RetInfo: invalid(err)}, nil
	}
	filter := store.ResultFilter{RunnerID: req.GetRunnerId()}
	runner, err := s.Repo.GetRunner(ctx, filter.RunnerID)
	if err != nil {
		return &strategypb.ListStrategyResultsRsp{RetInfo: invalid(err)}, nil
	}
	if err := ensureRunnerScope(ctx, runner); err != nil {
		return &strategypb.ListStrategyResultsRsp{RetInfo: invalid(err)}, nil
	}
	values, err := s.Repo.ListResults(ctx, filter)
	if err != nil {
		return &strategypb.ListStrategyResultsRsp{RetInfo: invalid(err)}, nil
	}
	page, size, start, end := pageBounds(req.GetPage(), len(values))
	items := make([]*strategypb.StrategyResult, 0, end-start)
	for _, value := range values[start:end] {
		items = append(items, resultProto(value))
	}
	return &strategypb.ListStrategyResultsRsp{RetInfo: success(), Results: items, Total: int64(len(values)), Page: int32(page), PageSize: int32(size)}, nil
}

func (s *Service) GetStrategyResult(ctx context.Context, req *strategypb.GetStrategyResultReq) (*strategypb.GetStrategyResultRsp, error) {
	if req == nil || req.GetResultId() == "" || s.Repo == nil {
		return &strategypb.GetStrategyResultRsp{RetInfo: invalid(errors.New("result_id is required"))}, nil
	}
	if _, err := requireSpaceID(ctx); err != nil {
		return &strategypb.GetStrategyResultRsp{RetInfo: invalid(err)}, nil
	}
	value, err := s.Repo.GetResult(ctx, req.GetResultId())
	if err != nil {
		return &strategypb.GetStrategyResultRsp{RetInfo: invalid(err)}, nil
	}
	runner, err := s.Repo.GetRunner(ctx, value.RunnerID)
	if err != nil {
		return &strategypb.GetStrategyResultRsp{RetInfo: invalid(err)}, nil
	}
	if err := ensureRunnerScope(ctx, runner); err != nil {
		return &strategypb.GetStrategyResultRsp{RetInfo: invalid(err)}, nil
	}
	return &strategypb.GetStrategyResultRsp{RetInfo: success(), Result: resultProto(value)}, nil
}

func (s *Service) ListStrategyTargets(ctx context.Context, req *strategypb.ListStrategyTargetsReq) (*strategypb.ListStrategyTargetsRsp, error) {
	if req == nil || req.GetRunnerId() == "" || s.Repo == nil {
		return &strategypb.ListStrategyTargetsRsp{RetInfo: invalid(errors.New("runner_id is required"))}, nil
	}
	if _, err := requireSpaceID(ctx); err != nil {
		return &strategypb.ListStrategyTargetsRsp{RetInfo: invalid(err)}, nil
	}
	runner, err := s.Repo.GetRunner(ctx, req.GetRunnerId())
	if err != nil {
		return &strategypb.ListStrategyTargetsRsp{RetInfo: invalid(err)}, nil
	}
	if err := ensureRunnerScope(ctx, runner); err != nil {
		return &strategypb.ListStrategyTargetsRsp{RetInfo: invalid(err)}, nil
	}
	targets, err := decodeTargets(runner.CurrentTargetsJSON)
	if err != nil {
		return &strategypb.ListStrategyTargetsRsp{RetInfo: invalid(err)}, nil
	}
	return &strategypb.ListStrategyTargetsRsp{RetInfo: success(), Targets: targets, CommandSequence: runner.CommandSequence}, nil
}

func (s *Service) compilerFor(spaceID string) *compiler.Compiler {
	if s.CompilerFactory != nil {
		if value := s.CompilerFactory(spaceID); value != nil {
			return value
		}
	}
	return s.Compiler
}

func decodeCompiled(strategy domain.Strategy) (compiler.CompiledStrategy, error) {
	if len(strategy.CompiledJSON) == 0 {
		return compiler.CompiledStrategy{}, errors.New("strategy has no compiled artifact")
	}
	var compiled compiler.CompiledStrategy
	if err := json.Unmarshal(strategy.CompiledJSON, &compiled); err != nil {
		return compiler.CompiledStrategy{}, fmt.Errorf("decode compiled strategy: %w", err)
	}
	if compiled.APIVersion != config.APIVersion || compiled.Kind != config.Kind || compiled.SpaceID == "" {
		return compiler.CompiledStrategy{}, errors.New("compiled strategy artifact is invalid")
	}
	return compiled, nil
}

func validateRunnerIdentity(value *strategypb.StrategyRunner) error {
	if strings.TrimSpace(value.GetRunnerId()) == "" || strings.TrimSpace(value.GetStrategyId()) == "" || strings.TrimSpace(value.GetSpaceId()) == "" {
		return errors.New("runner_id, strategy_id and space_id are required")
	}
	return nil
}

func (s *Service) claimRunner(ctx context.Context, runner domain.StrategyRunner) error {
	if runner.LogicalAccountID == nil {
		return nil
	}
	_, err := s.claimRunnerWithGeneration(ctx, runner)
	return err
}

func (s *Service) claimRunnerWithGeneration(ctx context.Context, runner domain.StrategyRunner) (int64, error) {
	if runner.LogicalAccountID == nil {
		return 0, nil
	}
	if s.LogicalAccounts == nil {
		return 0, errors.New("logical account owner client is unavailable")
	}
	if claimer, ok := s.LogicalAccounts.(LogicalAccountOwnerClaimer); ok {
		return claimer.ClaimWithGeneration(ctx, runner.SpaceID, *runner.LogicalAccountID, runner.ID)
	}
	return 0, s.LogicalAccounts.Claim(ctx, runner.SpaceID, *runner.LogicalAccountID, runner.ID)
}

func (s *Service) verifyRunnerDependencies(ctx context.Context, runner domain.StrategyRunner) error {
	if s.Repo == nil {
		return errors.New("strategy repository is unavailable")
	}
	strategy, err := s.Repo.GetStrategy(ctx, runner.StrategyID)
	if err != nil {
		return err
	}
	compiled, err := decodeCompiled(strategy)
	if err != nil {
		return err
	}
	selectedCompiler := s.compilerFor(runner.SpaceID)
	if selectedCompiler == nil {
		return errors.New("strategy compiler is unavailable")
	}
	return selectedCompiler.VerifyDependencies(ctx, compiled)
}

func (s *Service) releaseRunner(ctx context.Context, runner domain.StrategyRunner) error {
	if runner.LogicalAccountID == nil {
		return nil
	}
	if s.LogicalAccounts == nil {
		return errors.New("logical account owner client is unavailable")
	}
	return s.LogicalAccounts.Release(ctx, runner.SpaceID, *runner.LogicalAccountID, runner.ID)
}

func requestSpaceID(ctx context.Context) string {
	for _, key := range []string{"space_id", "X-Space-Id", "x-space-id"} {
		if value := string(trpc.GetMetaData(ctx, key)); value != "" {
			return value
		}
	}
	return ""
}

func requireSpaceID(ctx context.Context) (string, error) {
	if value := requestSpaceID(ctx); value != "" {
		return value, nil
	}
	return "", errors.New("space_id metadata is required")
}

func ensureRunnerScope(ctx context.Context, runner domain.StrategyRunner) error {
	if scoped := requestSpaceID(ctx); scoped != "" && scoped != runner.SpaceID {
		return fmt.Errorf("runner %q is outside the current space", runner.ID)
	}
	return nil
}

func ensureStrategyScope(ctx context.Context, strategy domain.Strategy) error {
	scoped := requestSpaceID(ctx)
	if scoped == "" {
		return nil
	}
	compiled, err := decodeCompiled(strategy)
	if err != nil {
		return err
	}
	if compiled.SpaceID != scoped {
		return fmt.Errorf("strategy %q is outside the current space", strategy.ID)
	}
	return nil
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func (s *Service) nowTime() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func success() *commonpb.RetInfo {
	return &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS, Msg: "success"}
}
func invalid(err error) *commonpb.RetInfo {
	return &commonpb.RetInfo{Code: commonpb.ErrorCode_INVALID_PARAM, Msg: err.Error()}
}
