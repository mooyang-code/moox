package rpc

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/compiler"
	"github.com/mooyang-code/moox/modules/strategy/internal/config"
	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/registry"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayroute"
	"gorm.io/gorm"
	trpc "trpc.group/trpc-go/trpc-go"
)

type LogicalAccountOwner interface {
	Validate(context.Context, string, string) error
	Claim(context.Context, string, string, string) error
	Release(context.Context, string, string, string) error
}

type LogicalAccountSessionOwner interface {
	ClaimSession(context.Context, string, string, string, string) error
	ReleaseSession(context.Context, string, string, string, string) error
}

type LogicalAccountSessionValidator interface {
	ValidateSession(context.Context, string, string, string, string) error
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
	PoolRegistry    *input.UDFRegistry
	LogicalAccounts LogicalAccountOwner
	Now             func() time.Time

	strategyLocks sync.Map
}

// ReconcileDisabledInstances finishes modern disable/enable handshakes left
// in the durable disabled+session state by a crash or an unknown Trade RPC
// result. The session identity is retained until Trade confirms release.
func (s *Service) ReconcileDisabledInstances(ctx context.Context) error {
	if s == nil || s.Repo == nil || s.LogicalAccounts == nil {
		return nil
	}
	owner, ok := s.LogicalAccounts.(LogicalAccountSessionOwner)
	if !ok {
		return errors.New("logical account session owner is unavailable")
	}
	instances, err := s.Repo.ListAllInstances(ctx, ptrBool(false))
	if err != nil {
		return err
	}
	var reconcileErr error
	for _, instance := range instances {
		unlock := s.lockStrategy(instance.StrategyID)
		func() {
			defer unlock()
			current, getErr := s.Repo.GetInstance(ctx, instance.InstanceID)
			if getErr != nil {
				reconcileErr = errors.Join(reconcileErr, fmt.Errorf("instance %s: reload: %w", instance.InstanceID, getErr))
				return
			}
			// Re-read under the same strategy lock used by enable/disable. An
			// enable handshake that has already claimed Trade must not be
			// mistaken for a stale disabled session and released here.
			if current.Enabled || current.SessionID == nil {
				return
			}
			instance = current
			if instance.LogicalAccountID == nil {
				if err := s.Repo.ClearInstanceSession(ctx, instance.InstanceID, *instance.SessionID, s.nowTime()); err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
					reconcileErr = errors.Join(reconcileErr, fmt.Errorf("instance %s: clear observation session: %w", instance.InstanceID, err))
				}
				return
			}
			if err := owner.ReleaseSession(ctx, instance.SpaceID, *instance.LogicalAccountID, instance.InstanceID, *instance.SessionID); err != nil && !isOwnerConflict(err) {
				reconcileErr = errors.Join(reconcileErr, fmt.Errorf("instance %s: release session: %w", instance.InstanceID, err))
				return
			}
			if err := s.Repo.ClearInstanceSession(ctx, instance.InstanceID, *instance.SessionID, s.nowTime()); err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				reconcileErr = errors.Join(reconcileErr, fmt.Errorf("instance %s: clear session: %w", instance.InstanceID, err))
			}
		}()
	}
	return reconcileErr
}

// ReconcileEnabledInstances verifies that persisted enabled instances still
// own the same Trade session before Strategy starts consuming readiness
// events. A mismatch is a startup error rather than a best-effort warning so
// the instance cannot keep calculating against an authorization it no longer
// holds.
func (s *Service) ReconcileEnabledInstances(ctx context.Context) error {
	if s == nil || s.Repo == nil || s.LogicalAccounts == nil {
		return nil
	}
	validator, ok := s.LogicalAccounts.(LogicalAccountSessionValidator)
	if !ok {
		return nil
	}
	instances, err := s.Repo.ListAllInstances(ctx, ptrBool(true))
	if err != nil {
		return err
	}
	for _, instance := range instances {
		if instance.LogicalAccountID == nil || instance.SessionID == nil || strings.TrimSpace(*instance.SessionID) == "" {
			continue
		}
		if err := validator.ValidateSession(ctx, instance.SpaceID, *instance.LogicalAccountID, instance.InstanceID, *instance.SessionID); err != nil {
			return fmt.Errorf("enabled strategy instance %s session validation: %w", instance.InstanceID, err)
		}
	}
	return nil
}

func ptrBool(value bool) *bool { return &value }

func isOwnerConflict(err error) bool {
	if err == nil {
		return false
	}
	var coded interface{ Code() int32 }
	if errors.As(err, &coded) {
		return coded.Code() == 14
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "owner conflict") || strings.Contains(message, "code=14")
}

// A failed claim with one of these codes is a definitive Trade response. The
// local pending session was never authorized and can be cleared immediately;
// transport/internal errors retain it so reconciliation can safely recover an
// outcome that may have succeeded remotely.
func isPermanentOwnerClaimError(err error) bool {
	if err == nil {
		return false
	}
	var coded interface{ Code() int32 }
	if !errors.As(err, &coded) {
		return false
	}
	switch coded.Code() {
	case 1, 2, 3, 5, 6, 7, 8, 9, 10, 12, 13, 14, 16, 17, 18:
		return true
	default:
		return false
	}
}

// lockStrategy serializes edits to a shared DSL with instance enablement. A
// definition update is allowed only while all referencing instances are
// disabled, so both RPCs must observe that state atomically.
func (s *Service) lockStrategy(strategyID string) func() {
	value, _ := s.strategyLocks.LoadOrStore(strategyID, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	lock.Lock()
	return lock.Unlock
}

func (s *Service) CreateStrategy(ctx context.Context, req *strategypb.CreateStrategyReq) (*strategypb.CreateStrategyRsp, error) {
	if req == nil || req.GetStrategy() == nil || s.Registry == nil {
		return &strategypb.CreateStrategyRsp{RetInfo: invalid(errors.New("strategy registry and dsl_yaml are required"))}, nil
	}
	value := req.GetStrategy()
	spaceID, scopeErr := requireSpaceID(ctx)
	if scopeErr != nil {
		return &strategypb.CreateStrategyRsp{RetInfo: invalid(scopeErr)}, nil
	}
	dslText := value.GetDslYaml()
	if dslText == "" {
		dslText = value.GetManifestYaml()
	}
	definition, dsl, err := s.Registry.PrepareDefinition(value.GetStrategyId(), dslText, s.nowTime())
	if err != nil {
		return &strategypb.CreateStrategyRsp{RetInfo: invalid(err)}, nil
	}
	if err := s.validatePoolUDFs(dsl); err != nil {
		return &strategypb.CreateStrategyRsp{RetInfo: invalid(err)}, nil
	}
	if selected := s.compilerFor(spaceID); selected != nil {
		if err := selected.ValidateDSL(ctx, dsl); err != nil {
			return &strategypb.CreateStrategyRsp{RetInfo: invalid(err)}, nil
		}
	}
	// Definition creation validates DSL shape and pool UDF contracts only.
	// Factor/View fields are instance bindings, so compiling them here would
	// reject valid bars[-1].factor expressions before a binding exists. Full
	// dependency compilation is performed when an instance is enabled.
	if err := s.Repo.SaveStrategyDefinition(ctx, definition); err != nil {
		return &strategypb.CreateStrategyRsp{RetInfo: invalid(err)}, nil
	}
	strategy := domain.Strategy{ID: definition.StrategyID, Name: definition.StrategyName, ManifestYAML: definition.DSLYaml, CreatedAt: definition.CreatedAt}
	return &strategypb.CreateStrategyRsp{RetInfo: success(), Strategy: strategyProto(strategy)}, nil
}

// UpdateStrategy replaces the single authoritative DSL text. Definitions are
// editable only while every referencing instance is disabled; enabling later
// performs the full dependency compile against the instance bindings.
func (s *Service) UpdateStrategy(ctx context.Context, req *strategypb.UpdateStrategyReq) (*strategypb.UpdateStrategyRsp, error) {
	if req == nil || strings.TrimSpace(req.GetStrategyId()) == "" || strings.TrimSpace(req.GetDslYaml()) == "" || s.Repo == nil || s.Registry == nil {
		return &strategypb.UpdateStrategyRsp{RetInfo: invalid(errors.New("strategy_id and dsl_yaml are required"))}, nil
	}
	unlock := s.lockStrategy(req.GetStrategyId())
	defer unlock()
	definition, dsl, err := s.Registry.PrepareDefinition(req.GetStrategyId(), req.GetDslYaml(), s.nowTime())
	if err != nil {
		return &strategypb.UpdateStrategyRsp{RetInfo: invalid(err)}, nil
	}
	if err := s.validatePoolUDFs(dsl); err != nil {
		return &strategypb.UpdateStrategyRsp{RetInfo: invalid(err)}, nil
	}
	if selected := s.compilerFor(requestSpaceID(ctx)); selected != nil {
		if err := selected.ValidateDSL(ctx, dsl); err != nil {
			return &strategypb.UpdateStrategyRsp{RetInfo: invalid(err)}, nil
		}
	}
	// As in CreateStrategy, defer binding-dependent expression compilation to
	// instance enablement; the shared DSL has no concrete Factor catalog yet.
	if err := s.Repo.UpdateStrategyDefinition(ctx, definition); err != nil {
		return &strategypb.UpdateStrategyRsp{RetInfo: invalid(err)}, nil
	}
	updated, err := s.Repo.GetStrategyDefinition(ctx, req.GetStrategyId())
	if err != nil {
		return &strategypb.UpdateStrategyRsp{RetInfo: invalid(err)}, nil
	}
	return &strategypb.UpdateStrategyRsp{RetInfo: success(), Strategy: strategyProto(domain.Strategy{ID: updated.StrategyID, Name: updated.StrategyName, ManifestYAML: updated.DSLYaml, CreatedAt: updated.UpdatedAt})}, nil
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

func (s *Service) ListStrategyResults(ctx context.Context, req *strategypb.ListStrategyResultsReq) (*strategypb.ListStrategyResultsRsp, error) {
	if s.Repo == nil {
		return &strategypb.ListStrategyResultsRsp{RetInfo: invalid(errors.New("strategy repository is unavailable"))}, nil
	}
	if req == nil || strings.TrimSpace(req.GetInstanceId()) == "" {
		return &strategypb.ListStrategyResultsRsp{RetInfo: invalid(errors.New("instance_id is required"))}, nil
	}
	if _, err := requireSpaceID(ctx); err != nil {
		return &strategypb.ListStrategyResultsRsp{RetInfo: invalid(err)}, nil
	}
	instance, err := s.Repo.GetInstance(ctx, req.GetInstanceId())
	if err != nil {
		return &strategypb.ListStrategyResultsRsp{RetInfo: invalid(err)}, nil
	}
	if err := ensureInstanceScope(ctx, instance); err != nil {
		return &strategypb.ListStrategyResultsRsp{RetInfo: invalid(err)}, nil
	}
	values, err := s.Repo.ListStrategyResults(ctx, instance.InstanceID, req.GetSessionId())
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
	result, err := s.Repo.GetStrategyResult(ctx, req.GetResultId())
	if err != nil {
		return &strategypb.GetStrategyResultRsp{RetInfo: invalid(err)}, nil
	}
	instance, err := s.Repo.GetInstance(ctx, result.InstanceID)
	if err != nil {
		return &strategypb.GetStrategyResultRsp{RetInfo: invalid(err)}, nil
	}
	if err := ensureInstanceScope(ctx, instance); err != nil {
		return &strategypb.GetStrategyResultRsp{RetInfo: invalid(err)}, nil
	}
	return &strategypb.GetStrategyResultRsp{RetInfo: success(), Result: resultProto(result)}, nil
}

func (s *Service) ListStrategyTargets(ctx context.Context, req *strategypb.ListStrategyTargetsReq) (*strategypb.ListStrategyTargetsRsp, error) {
	if req == nil || req.GetInstanceId() == "" || s.Repo == nil {
		return &strategypb.ListStrategyTargetsRsp{RetInfo: invalid(errors.New("instance_id is required"))}, nil
	}
	if _, err := requireSpaceID(ctx); err != nil {
		return &strategypb.ListStrategyTargetsRsp{RetInfo: invalid(err)}, nil
	}
	instance, err := s.Repo.GetInstance(ctx, req.GetInstanceId())
	if err != nil {
		return &strategypb.ListStrategyTargetsRsp{RetInfo: invalid(err)}, nil
	}
	if err := ensureInstanceScope(ctx, instance); err != nil {
		return &strategypb.ListStrategyTargetsRsp{RetInfo: invalid(err)}, nil
	}
	if instance.SessionID == nil {
		return &strategypb.ListStrategyTargetsRsp{RetInfo: success(), Targets: []*strategypb.InstrumentTarget{}}, nil
	}
	result, err := s.Repo.LatestResult(ctx, instance.InstanceID, *instance.SessionID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return &strategypb.ListStrategyTargetsRsp{RetInfo: success(), Targets: []*strategypb.InstrumentTarget{}}, nil
		}
		return &strategypb.ListStrategyTargetsRsp{RetInfo: invalid(err)}, nil
	}
	return &strategypb.ListStrategyTargetsRsp{RetInfo: success(), Targets: decodeTargetProto(result.TargetsJSON), SessionId: result.SessionID, BarEndTime: formatTime(result.BarEndTime), ValidUntil: formatTime(result.ValidUntil)}, nil
}

func (s *Service) CreateStrategyInstance(ctx context.Context, req *strategypb.CreateStrategyInstanceReq) (*strategypb.CreateStrategyInstanceRsp, error) {
	if req == nil || req.GetInstance() == nil || s.Repo == nil {
		return &strategypb.CreateStrategyInstanceRsp{RetInfo: invalid(errors.New("instance is required"))}, nil
	}
	v := req.GetInstance()
	scoped, err := requireSpaceID(ctx)
	if err != nil {
		return &strategypb.CreateStrategyInstanceRsp{RetInfo: invalid(err)}, nil
	}
	if scoped != v.GetSpaceId() {
		return &strategypb.CreateStrategyInstanceRsp{RetInfo: invalid(errors.New("instance is outside current space"))}, nil
	}
	unlockStrategy := s.lockStrategy(v.GetStrategyId())
	defer unlockStrategy()
	now := s.nowTime()
	instance := store.StrategyInstance{InstanceID: v.GetInstanceId(), StrategyID: v.GetStrategyId(), SpaceID: v.GetSpaceId(), InputBindingsJSON: json.RawMessage(v.GetInputBindingsJson()), LogicalAccountID: optionalString(v.GetLogicalAccountId()), Enabled: false, CreatedAt: now, UpdatedAt: now}
	if instance.InstanceID == "" || instance.StrategyID == "" {
		return &strategypb.CreateStrategyInstanceRsp{RetInfo: invalid(errors.New("instance_id and strategy_id are required"))}, nil
	}
	if existing, err := s.Repo.GetInstance(ctx, instance.InstanceID); err == nil {
		return createInstanceRetryResponse(existing, instance, v.GetEnabled()), nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return &strategypb.CreateStrategyInstanceRsp{RetInfo: invalid(err)}, nil
	}
	if _, err := s.Repo.GetStrategyDefinition(ctx, v.GetStrategyId()); err != nil {
		return &strategypb.CreateStrategyInstanceRsp{RetInfo: invalid(err)}, nil
	}
	if v.GetEnabled() {
		if instance.LogicalAccountID != nil {
			if _, ok := s.LogicalAccounts.(LogicalAccountSessionOwner); !ok {
				return &strategypb.CreateStrategyInstanceRsp{RetInfo: invalid(errors.New("logical account session owner is unavailable"))}, nil
			}
		}
		if err := validateEnabledBindings(instance.InputBindingsJSON); err != nil {
			return &strategypb.CreateStrategyInstanceRsp{RetInfo: invalid(err)}, nil
		}
		definition, defErr := s.Repo.GetStrategyDefinition(ctx, instance.StrategyID)
		if defErr != nil {
			return &strategypb.CreateStrategyInstanceRsp{RetInfo: invalid(defErr)}, nil
		}
		dsl, parseErr := config.Parse([]byte(definition.DSLYaml))
		if parseErr != nil {
			return &strategypb.CreateStrategyInstanceRsp{RetInfo: invalid(parseErr)}, nil
		}
		if err := s.validatePoolUDFs(dsl); err != nil {
			return &strategypb.CreateStrategyInstanceRsp{RetInfo: invalid(err)}, nil
		}
		selected := s.compilerFor(instance.SpaceID)
		if selected == nil {
			return &strategypb.CreateStrategyInstanceRsp{RetInfo: invalid(errors.New("strategy compiler/execution dependencies are unavailable"))}, nil
		}
		compiled, compileErr := selected.CompileWithBindings(ctx, dsl, instance.SpaceID, instance.InputBindingsJSON)
		if compileErr != nil {
			return &strategypb.CreateStrategyInstanceRsp{RetInfo: invalid(compileErr)}, nil
		}
		if verifyErr := selected.VerifyDependencies(ctx, compiled); verifyErr != nil {
			return &strategypb.CreateStrategyInstanceRsp{RetInfo: invalid(verifyErr)}, nil
		}
		session, genErr := newSessionID()
		if genErr != nil {
			return &strategypb.CreateStrategyInstanceRsp{RetInfo: invalid(genErr)}, nil
		}
		// Persist the session while still disabled before contacting Trade. If
		// the claim response is lost, startup reconciliation can release this
		// exact identity instead of losing track of the owner.
		instance.Enabled = false
		instance.SessionID = &session
	}
	if err := s.Repo.CreateInstance(ctx, instance); err != nil {
		// Another service may have inserted the ID after the initial lookup.
		// Only matching configuration may expose that durable identity.
		if existing, getErr := s.Repo.GetInstance(ctx, instance.InstanceID); getErr == nil {
			return createInstanceRetryResponse(existing, instance, v.GetEnabled()), nil
		}
		return &strategypb.CreateStrategyInstanceRsp{RetInfo: invalid(err)}, nil
	}
	// Creation is already durable. Report its identity even when the later
	// enable handshake fails, including for clients that keep only the message.
	createdFailure := func(err error) (*strategypb.CreateStrategyInstanceRsp, error) {
		current, getErr := s.Repo.GetInstance(ctx, instance.InstanceID)
		if getErr != nil {
			return &strategypb.CreateStrategyInstanceRsp{
				RetInfo:  invalid(fmt.Errorf("strategy instance %q was created; state unavailable, inspect the instance before using SetStrategyInstanceEnabled: %w", instance.InstanceID, errors.Join(err, fmt.Errorf("read created instance: %w", getErr)))),
				Instance: &strategypb.StrategyInstance{InstanceId: instance.InstanceID, StrategyId: instance.StrategyID, SpaceId: instance.SpaceID},
			}, nil
		}
		return &strategypb.CreateStrategyInstanceRsp{
			RetInfo:  invalid(fmt.Errorf("strategy instance %q was created; inspect the instance and use SetStrategyInstanceEnabled to change enabled state: %w", instance.InstanceID, err)),
			Instance: instanceProto(current),
		}, nil
	}
	if v.GetEnabled() {
		if instance.LogicalAccountID != nil {
			owner, ok := s.LogicalAccounts.(LogicalAccountSessionOwner)
			if !ok {
				return createdFailure(errors.New("logical account session owner is unavailable"))
			}
			if err := owner.ClaimSession(ctx, instance.SpaceID, *instance.LogicalAccountID, instance.InstanceID, *instance.SessionID); err != nil {
				if isPermanentOwnerClaimError(err) {
					if clearErr := s.Repo.ClearInstanceSession(ctx, instance.InstanceID, *instance.SessionID, now); clearErr == nil {
						instance.SessionID = nil
					} else {
						err = errors.Join(err, fmt.Errorf("clear rejected session: %w", clearErr))
					}
				}
				return createdFailure(err)
			}
		}
		if err := s.Repo.SetInstanceEnabled(ctx, instance.InstanceID, true, instance.SessionID, now); err != nil {
			return createdFailure(err)
		}
		instance.Enabled = true
	}
	created, err := s.Repo.GetInstance(ctx, instance.InstanceID)
	if err != nil {
		return createdFailure(err)
	}
	return &strategypb.CreateStrategyInstanceRsp{RetInfo: success(), Instance: instanceProto(created)}, nil
}

func createInstanceRetryResponse(existing, requested store.StrategyInstance, desiredEnabled bool) *strategypb.CreateStrategyInstanceRsp {
	if existing.SpaceID != requested.SpaceID || existing.StrategyID != requested.StrategyID ||
		!reflect.DeepEqual(existing.LogicalAccountID, requested.LogicalAccountID) || !equalInstanceBindings(existing.InputBindingsJSON, requested.InputBindingsJSON) {
		return &strategypb.CreateStrategyInstanceRsp{RetInfo: invalid(errors.New("instance_id conflicts with an existing instance configuration"))}
	}
	response := &strategypb.CreateStrategyInstanceRsp{RetInfo: success(), Instance: instanceProto(existing)}
	// Enabled is current lifecycle state, not an original-request fingerprint.
	// Create retries never claim ownership or silently undo a later disable.
	if existing.Enabled != desiredEnabled {
		response.RetInfo = invalid(fmt.Errorf("strategy instance %q already exists with enabled=%t; use SetStrategyInstanceEnabled to change enabled state", existing.InstanceID, existing.Enabled))
	}
	return response
}

func equalInstanceBindings(left, right json.RawMessage) bool {
	decode := func(raw json.RawMessage) (any, error) {
		if len(raw) == 0 {
			raw = json.RawMessage(`{}`)
		}
		if !json.Valid(raw) {
			return nil, errors.New("invalid input bindings JSON")
		}
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		// Do not collapse distinct large integer inputs through float64.
		decoder.UseNumber()
		var value any
		err := decoder.Decode(&value)
		return value, err
	}
	l, leftErr := decode(left)
	r, rightErr := decode(right)
	return leftErr == nil && rightErr == nil && reflect.DeepEqual(l, r)
}

func (s *Service) GetStrategyInstance(ctx context.Context, req *strategypb.GetStrategyInstanceReq) (*strategypb.GetStrategyInstanceRsp, error) {
	if req == nil || req.GetInstanceId() == "" || s.Repo == nil {
		return &strategypb.GetStrategyInstanceRsp{RetInfo: invalid(errors.New("instance_id is required"))}, nil
	}
	scoped, scopeErr := requireSpaceID(ctx)
	if scopeErr != nil {
		return &strategypb.GetStrategyInstanceRsp{RetInfo: invalid(scopeErr)}, nil
	}
	instance, err := s.Repo.GetInstance(ctx, req.GetInstanceId())
	if err != nil {
		return &strategypb.GetStrategyInstanceRsp{RetInfo: invalid(err)}, nil
	}
	if scoped != instance.SpaceID {
		return &strategypb.GetStrategyInstanceRsp{RetInfo: invalid(errors.New("instance is outside current space"))}, nil
	}
	return &strategypb.GetStrategyInstanceRsp{RetInfo: success(), Instance: instanceProto(instance)}, nil
}

func (s *Service) ListStrategyInstances(ctx context.Context, req *strategypb.ListStrategyInstancesReq) (*strategypb.ListStrategyInstancesRsp, error) {
	if s.Repo == nil {
		return &strategypb.ListStrategyInstancesRsp{RetInfo: invalid(errors.New("strategy repository is unavailable"))}, nil
	}
	scoped, err := requireSpaceID(ctx)
	if err != nil {
		return &strategypb.ListStrategyInstancesRsp{RetInfo: invalid(err)}, nil
	}
	var enabled *bool
	if req != nil && req.Enabled != nil {
		enabled = req.Enabled
	}
	values, err := s.Repo.ListInstances(ctx, scoped, enabled)
	if err != nil {
		return &strategypb.ListStrategyInstancesRsp{RetInfo: invalid(err)}, nil
	}
	if req != nil && req.GetStrategyId() != "" {
		filtered := values[:0]
		for _, value := range values {
			if value.StrategyID == req.GetStrategyId() {
				filtered = append(filtered, value)
			}
		}
		values = filtered
	}
	page, size, start, end := pageBounds(req.GetPage(), len(values))
	items := make([]*strategypb.StrategyInstance, 0, end-start)
	for _, value := range values[start:end] {
		items = append(items, instanceProto(value))
	}
	return &strategypb.ListStrategyInstancesRsp{RetInfo: success(), Instances: items, Total: int64(len(values)), Page: int32(page), PageSize: int32(size)}, nil
}

func (s *Service) SetStrategyInstanceEnabled(ctx context.Context, req *strategypb.SetStrategyInstanceEnabledReq) (*strategypb.SetStrategyInstanceEnabledRsp, error) {
	if req == nil || req.GetInstanceId() == "" || s.Repo == nil {
		return &strategypb.SetStrategyInstanceEnabledRsp{RetInfo: invalid(errors.New("instance_id is required"))}, nil
	}
	scoped, scopeErr := requireSpaceID(ctx)
	if scopeErr != nil {
		return &strategypb.SetStrategyInstanceEnabledRsp{RetInfo: invalid(scopeErr)}, nil
	}
	instance, err := s.Repo.GetInstance(ctx, req.GetInstanceId())
	if err != nil {
		return &strategypb.SetStrategyInstanceEnabledRsp{RetInfo: invalid(err)}, nil
	}
	if scoped != instance.SpaceID {
		return &strategypb.SetStrategyInstanceEnabledRsp{RetInfo: invalid(errors.New("instance is outside current space"))}, nil
	}
	unlock := s.lockStrategy(instance.StrategyID)
	defer unlock()
	// The initial read only chooses the strategy lock. Re-read after acquiring
	// it so a concurrent enable/disable cannot apply its transition to a stale
	// snapshot and skip the matching Trade release.
	instance, err = s.Repo.GetInstance(ctx, req.GetInstanceId())
	if err != nil {
		return &strategypb.SetStrategyInstanceEnabledRsp{RetInfo: invalid(err)}, nil
	}
	if scoped != instance.SpaceID {
		return &strategypb.SetStrategyInstanceEnabledRsp{RetInfo: invalid(errors.New("instance is outside the current space"))}, nil
	}
	if req.GetEnabled() && instance.Enabled {
		// Repeated enable is idempotent and must not allocate a new session or
		// contend with Trade's existing claim. When a logical account is
		// configured, verify that the persisted session is still the live Trade
		// owner instead of returning a false-success after an external rebind.
		if instance.LogicalAccountID != nil && instance.SessionID != nil {
			if validator, ok := s.LogicalAccounts.(LogicalAccountSessionValidator); ok {
				if err := validator.ValidateSession(ctx, instance.SpaceID, *instance.LogicalAccountID, instance.InstanceID, *instance.SessionID); err != nil {
					return &strategypb.SetStrategyInstanceEnabledRsp{RetInfo: invalid(fmt.Errorf("strategy instance session is no longer authorized: %w", err))}, nil
				}
			}
		}
		return &strategypb.SetStrategyInstanceEnabledRsp{RetInfo: success(), Instance: instanceProto(instance)}, nil
	}
	if req.GetEnabled() {
		if instance.LogicalAccountID != nil {
			if _, ok := s.LogicalAccounts.(LogicalAccountSessionOwner); !ok {
				return &strategypb.SetStrategyInstanceEnabledRsp{RetInfo: invalid(errors.New("logical account session owner is unavailable"))}, nil
			}
		}
		if err := validateEnabledBindings(instance.InputBindingsJSON); err != nil {
			return &strategypb.SetStrategyInstanceEnabledRsp{RetInfo: invalid(err)}, nil
		}
		definition, defErr := s.Repo.GetStrategyDefinition(ctx, instance.StrategyID)
		if defErr != nil {
			return &strategypb.SetStrategyInstanceEnabledRsp{RetInfo: invalid(defErr)}, nil
		}
		dsl, parseErr := config.Parse([]byte(definition.DSLYaml))
		if parseErr != nil {
			return &strategypb.SetStrategyInstanceEnabledRsp{RetInfo: invalid(parseErr)}, nil
		}
		if err := s.validatePoolUDFs(dsl); err != nil {
			return &strategypb.SetStrategyInstanceEnabledRsp{RetInfo: invalid(err)}, nil
		}
		selected := s.compilerFor(instance.SpaceID)
		if selected == nil {
			return &strategypb.SetStrategyInstanceEnabledRsp{RetInfo: invalid(errors.New("strategy compiler/execution dependencies are unavailable"))}, nil
		}
		compiled, compileErr := selected.CompileWithBindings(ctx, dsl, instance.SpaceID, instance.InputBindingsJSON)
		if compileErr != nil {
			return &strategypb.SetStrategyInstanceEnabledRsp{RetInfo: invalid(compileErr)}, nil
		}
		if verifyErr := selected.VerifyDependencies(ctx, compiled); verifyErr != nil {
			return &strategypb.SetStrategyInstanceEnabledRsp{RetInfo: invalid(verifyErr)}, nil
		}
	}
	var session *string
	if req.GetEnabled() {
		if instance.SessionID != nil && strings.TrimSpace(*instance.SessionID) != "" {
			value := strings.TrimSpace(*instance.SessionID)
			session = &value
		} else {
			value, genErr := newSessionID()
			if genErr != nil {
				return &strategypb.SetStrategyInstanceEnabledRsp{RetInfo: invalid(genErr)}, nil
			}
			session = &value
		}
		if err := s.Repo.SetInstanceEnabled(ctx, instance.InstanceID, false, session, s.nowTime()); err != nil {
			return &strategypb.SetStrategyInstanceEnabledRsp{RetInfo: invalid(err)}, nil
		}
		if instance.LogicalAccountID != nil {
			owner, ok := s.LogicalAccounts.(LogicalAccountSessionOwner)
			if !ok {
				return &strategypb.SetStrategyInstanceEnabledRsp{RetInfo: invalid(errors.New("logical account session owner is unavailable"))}, nil
			}
			if err := owner.ClaimSession(ctx, instance.SpaceID, *instance.LogicalAccountID, instance.InstanceID, *session); err != nil {
				if isPermanentOwnerClaimError(err) {
					_ = s.Repo.ClearInstanceSession(ctx, instance.InstanceID, *session, s.nowTime())
				}
				return &strategypb.SetStrategyInstanceEnabledRsp{RetInfo: invalid(err)}, nil
			}
		}
	}
	releaseNeeded := !req.GetEnabled() && instance.SessionID != nil && instance.LogicalAccountID != nil
	if !req.GetEnabled() {
		// Keep the old session durable until Trade confirms the matching
		// release. A crash or timeout must be recoverable by the next
		// reconciliation pass; clearing it before ReleaseSession would leak
		// the cross-service owner permanently.
		var pendingSession *string
		if releaseNeeded {
			pendingSession = instance.SessionID
		}
		if err := s.Repo.SetInstanceEnabled(ctx, instance.InstanceID, false, pendingSession, s.nowTime()); err != nil {
			return &strategypb.SetStrategyInstanceEnabledRsp{RetInfo: invalid(err)}, nil
		}
	}
	if req.GetEnabled() {
		if err := s.Repo.SetInstanceEnabled(ctx, instance.InstanceID, true, session, s.nowTime()); err != nil {
			if req.GetEnabled() && instance.LogicalAccountID != nil && session != nil {
				if owner, ok := s.LogicalAccounts.(LogicalAccountSessionOwner); ok {
					_ = owner.ReleaseSession(ctx, instance.SpaceID, *instance.LogicalAccountID, instance.InstanceID, *session)
				}
			}
			return &strategypb.SetStrategyInstanceEnabledRsp{RetInfo: invalid(err)}, nil
		}
	}
	if releaseNeeded {
		owner, ok := s.LogicalAccounts.(LogicalAccountSessionOwner)
		if !ok {
			return &strategypb.SetStrategyInstanceEnabledRsp{RetInfo: invalid(errors.New("logical account session owner is unavailable"))}, nil
		}
		if err := owner.ReleaseSession(ctx, instance.SpaceID, *instance.LogicalAccountID, instance.InstanceID, *instance.SessionID); err != nil {
			return &strategypb.SetStrategyInstanceEnabledRsp{RetInfo: invalid(err)}, nil
		}
		if err := s.Repo.ClearInstanceSession(ctx, instance.InstanceID, *instance.SessionID, s.nowTime()); err != nil {
			return &strategypb.SetStrategyInstanceEnabledRsp{RetInfo: invalid(err)}, nil
		}
	}
	updated, err := s.Repo.GetInstance(ctx, instance.InstanceID)
	if err != nil {
		return &strategypb.SetStrategyInstanceEnabledRsp{RetInfo: invalid(err)}, nil
	}
	return &strategypb.SetStrategyInstanceEnabledRsp{RetInfo: success(), Instance: instanceProto(updated)}, nil
}

func newSessionID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate strategy session id: %w", err)
	}
	// UUIDv4 layout keeps the identifier opaque and avoids ordering semantics
	// that could accidentally turn session IDs into a clock-based sequence.
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16]), nil
}

func validateEnabledBindings(raw json.RawMessage) error {
	if len(raw) == 0 || string(raw) == "null" {
		return errors.New("strategy instance input_bindings_json must set source_view_id before enabling")
	}
	if !json.Valid(raw) {
		return errors.New("strategy instance input_bindings_json must be valid JSON")
	}
	var binding struct {
		SourceViewID string `json:"source_view_id"`
		ViewID       string `json:"view_id"`
		Factors      []struct {
			FactorID     string `json:"factor_id"`
			SetID        string `json:"set_id"`
			ResultViewID string `json:"result_view_id"`
			Output       string `json:"output"`
			ColumnName   string `json:"column_name"`
		} `json:"factors"`
	}
	if err := json.Unmarshal(raw, &binding); err != nil {
		return fmt.Errorf("strategy instance input_bindings_json must be an object: %w", err)
	}
	if strings.TrimSpace(binding.SourceViewID) == "" && strings.TrimSpace(binding.ViewID) == "" {
		return errors.New("strategy instance input_bindings_json must set source_view_id")
	}
	for index, factor := range binding.Factors {
		if strings.TrimSpace(factor.FactorID) == "" || strings.TrimSpace(factor.SetID) == "" || strings.TrimSpace(factor.ResultViewID) == "" || strings.TrimSpace(factor.Output) == "" || strings.TrimSpace(factor.ColumnName) == "" {
			return fmt.Errorf("strategy instance input_bindings_json factors[%d] requires factor_id, set_id, result_view_id, output and column_name", index)
		}
	}
	return nil
}

func (s *Service) compilerFor(spaceID string) *compiler.Compiler {
	if s.CompilerFactory != nil {
		if value := s.CompilerFactory(spaceID); value != nil {
			return value
		}
	}
	return s.Compiler
}

func (s *Service) validatePoolUDFs(dsl config.DSL) error {
	for name, rule := range dsl.Rules {
		if rule.Pool.UDF == nil {
			continue
		}
		if err := s.PoolRegistry.Validate(rule.Pool); err != nil {
			return fmt.Errorf("strategy DSL rules.%s.pool: %w", name, err)
		}
	}
	return nil
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

// requestSpaceID 读取调用方在 tRPC 元数据 x-space-id 中声明的空间（控制台转发时写入，主机网关原样透传）。
func requestSpaceID(ctx context.Context) string {
	return strings.TrimSpace(string(trpc.GetMetaData(ctx, gatewayroute.MetadataSpaceID)))
}

func requireSpaceID(ctx context.Context) (string, error) {
	if value := requestSpaceID(ctx); value != "" {
		return value, nil
	}
	return "", errors.New("space_id metadata is required")
}

func ensureInstanceScope(ctx context.Context, instance store.StrategyInstance) error {
	if scoped := requestSpaceID(ctx); scoped != "" && scoped != instance.SpaceID {
		return fmt.Errorf("instance %q is outside the current space", instance.InstanceID)
	}
	return nil
}

func ensureStrategyScope(ctx context.Context, strategy domain.Strategy) error {
	// Strategy definitions are shared across spaces. Their persisted contract
	// contains no compiled space binding; scope is enforced on instances.
	if len(strategy.CompiledJSON) == 0 {
		return nil
	}
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
