package rpc

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/mooyang-code/moox/modules/cloudfunction/internal/cloudcredential"
	"github.com/mooyang-code/moox/modules/cloudfunction/internal/clsprovision"
	tencentscf "github.com/mooyang-code/moox/modules/cloudfunction/internal/providers/tencentscf"
	"github.com/mooyang-code/moox/modules/cloudfunction/internal/spacecontext"
	"github.com/mooyang-code/moox/modules/cloudfunction/internal/store"
	pb "github.com/mooyang-code/moox/modules/cloudfunction/proto/cloudfunctiongen"
	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
	"google.golang.org/protobuf/types/known/structpb"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	defaultSCFTimeoutSeconds      = 120
	scfOperationTimeout           = 5 * time.Minute
	scfCreateAttemptTimeout       = 4 * time.Minute
	scfCreateReconcileTimeout     = 10 * time.Second
	timerReadbackInterval         = 5 * time.Minute
	timerReadbackBatchSize        = 4
	timerReadbackMinRequestBudget = time.Minute
)

func (s *Service) ListFunctionInstances(ctx context.Context, req *pb.ListFunctionInstancesReq) (*pb.ListFunctionInstancesRsp, error) {
	spaceID, err := spacecontext.MustFromContext(ctx)
	if err != nil {
		return &pb.ListFunctionInstancesRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	instances, total, err := s.catalog.ListInstances(ctx, spaceID, req)
	if err != nil {
		return &pb.ListFunctionInstancesRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	// Tencent API has a per-account request rate limit. ListFunctionInstances is called
	// by Collector every few seconds, so refreshing timer instances in the listing
	// path would turn a read-only snapshot into a blocking SCF API call. The
	// Collector only needs the catalog identity here; runtime metadata is
	// refreshed by FunctionInstance's write path and by an unbounded admin listing.
	type timerRefreshCandidate struct {
		index int
		at    time.Time
	}
	candidates := make([]timerRefreshCandidate, 0)
	explicitRefresh := req.GetRefreshReadback()
	if explicitRefresh || shouldRefreshTimerReadback(ctx, req) {
		now := time.Now().UTC()
		for index := range instances {
			if instances[index].TriggerType != "timer" {
				continue
			}
			last, _ := time.Parse(time.RFC3339Nano, metadataString(parseJSONMap(instances[index].Metadata), "timer_last_readback_at"))
			if !explicitRefresh && !last.IsZero() && now.Sub(last) < timerReadbackInterval {
				continue
			}
			candidates = append(candidates, timerRefreshCandidate{index: index, at: last})
		}
		sort.SliceStable(candidates, func(i, j int) bool {
			if candidates[i].at.IsZero() != candidates[j].at.IsZero() {
				return candidates[i].at.IsZero()
			}
			if candidates[i].at.Equal(candidates[j].at) {
				return candidates[i].index < candidates[j].index
			}
			return candidates[i].at.Before(candidates[j].at)
		})
		// 显式要求读回时不截断：调用方要对本页每个实例都拿到新的云端状态。
		if !explicitRefresh && len(candidates) > timerReadbackBatchSize {
			candidates = candidates[:timerReadbackBatchSize]
		}
	}
	var refreshWG sync.WaitGroup
	refreshSlots := make(chan struct{}, timerReadbackBatchSize*2)
	for _, candidate := range candidates {
		index := candidate.index
		refreshWG.Add(1)
		go func(index int) {
			defer refreshWG.Done()
			select {
			case refreshSlots <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-refreshSlots }()
			// Refresh the provider readback on every timer-fleet listing. This is
			// a read-only health check, not an Ensure call, so manual Tencent-side
			// trigger drift is visible even when the assignment hash is unchanged.
			// The bounded slots avoid making a 50-instance fleet serial or creating an
			// unbounded Tencent API burst.
			if err := s.refreshTimerTriggerMetadata(ctx, spaceID, &instances[index]); err != nil {
				log.WarnContextf(ctx, "[CloudFunction-TencentSCF] timer trigger readback failed instance=%s: %v", instances[index].InstanceID, err)
			}
		}(index)
	}
	refreshWG.Wait()
	out := make([]*pb.FunctionInstance, 0, len(instances))
	for _, instance := range instances {
		out = append(out, toPBInstance(instance))
	}
	page, size := pageFromCommon(req.GetPage())
	return &pb.ListFunctionInstancesRsp{
		RetInfo: retOK(),
		Items:   out,
		Page:    pageResult(page, size, total),
	}, nil
}

func shouldRefreshTimerReadback(ctx context.Context, req *pb.ListFunctionInstancesReq) bool {
	// Collector reconciliation is a control-plane liveness path. It must not
	// wait for Tencent readback of up to four functions before receiving the
	// catalog snapshot. The normal admin listing remains the readback path.
	if req != nil && strings.EqualFold(strings.TrimSpace(req.GetWorkloadType()), "market_fetcher") {
		return false
	}
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < timerReadbackMinRequestBudget {
		return false
	}
	return true
}

func (s *Service) refreshTimerTriggerMetadata(ctx context.Context, spaceID string, instance *store.FunctionInstance) error {
	if s == nil || instance == nil || instance.TriggerType != "timer" {
		return nil
	}
	metadata := parseJSONMap(instance.Metadata)
	readbackAt := time.Now().UTC().Format(time.RFC3339Nano)
	patch := map[string]any{"timer_available_status": "Unknown", "timer_actual_type": nil, "timer_actual_enabled": nil, "timer_actual_cron": nil, "timer_actual_qualifier": nil, "timer_actual_message": nil, "timer_last_readback_at": readbackAt}
	if strings.TrimSpace(instance.PackageID) == "" || (strings.TrimSpace(instance.DeploymentID) == "" && !metadataBool(metadata, "deployment_ready")) {
		patch["timer_available_status"] = "Unknown"
		err := s.catalog.UpdateInstanceRuntimeMetadata(ctx, spaceID, instance.InstanceID, patch)
		for key, value := range patch {
			if value == nil {
				delete(metadata, key)
				continue
			}
			metadata[key] = value
		}
		instance.Metadata = jsonString(metadata)
		return err
	}
	account, err := s.catalog.GetAccount(ctx, instance.CloudAccountID)
	if err != nil || account == nil {
		if err == nil {
			err = fmt.Errorf("cloud account unavailable")
		}
		patch["timer_status_error"] = err.Error()
	} else {
		client, clientErr := s.scfClient(ctx, *account)
		if clientErr != nil {
			err = clientErr
			patch["timer_status_error"] = clientErr.Error()
		} else {
			ref := tencentscf.FunctionRef{Region: instance.Region, FunctionName: firstString(instance.FunctionName, instance.InstanceID), Namespace: firstString(instance.Namespace, "default")}
			var function *tencentscf.FunctionInfo
			var functionErr error
			var info *tencentscf.TimerTriggerInfo
			var lookupErr error
			var readbackWG sync.WaitGroup
			readbackWG.Add(2)
			go func() {
				defer readbackWG.Done()
				function, functionErr = client.GetFunction(ctx, ref)
			}()
			go func() {
				defer readbackWG.Done()
				info, lookupErr = client.GetTimerTrigger(ctx, ref, timerTriggerName)
			}()
			readbackWG.Wait()
			if functionErr != nil || function == nil {
				if functionErr == nil {
					functionErr = fmt.Errorf("empty SCF function readback")
				}
				err = functionErr
				patch["timer_status_error"] = functionErr.Error()
			} else {
				patch["managed_environment_budget_bytes"] = scfManagedEnvironmentBudget(function.Environment)
				// 记录云端函数环境里实际生效的分配与访问入口，供 Collector 比对环境是否被手工改动。
				patch["actual_assignment_hash"] = function.Environment["MOOX_MARKET_FETCH_ASSIGNMENT_HASH"]
				patch["actual_access_address"] = function.Environment["MOOX_ACCESS_ADDRESS"]
				patch["actual_access_id"] = function.Environment["MOOX_ACCESS_ID"]
				patch["actual_access_key_id"] = function.Environment["MOOX_CALLER_KEY_ID"]
				if lookupErr != nil {
					err = lookupErr
					patch["timer_status_error"] = lookupErr.Error()
				} else if info == nil {
					patch["timer_available_status"] = "Missing"
					patch["timer_status_error"] = "timer trigger is missing"
				} else {
					patch["timer_available_status"] = firstString(info.AvailableStatus, "Unknown")
					patch["timer_actual_type"] = info.Type
					patch["timer_actual_enabled"] = info.Enabled
					patch["timer_actual_cron"] = info.Cron
					patch["timer_actual_qualifier"] = info.Qualifier
					patch["timer_actual_message"] = info.Message
					patch["timer_status_error"] = nil
				}
			}
		}
	}
	if err != nil && patch["timer_available_status"] == "Unknown" {
		// Keep Unknown rather than preserving an old Available value.
		patch["timer_available_status"] = "Unknown"
	}
	updateErr := s.catalog.UpdateInstanceRuntimeMetadata(ctx, spaceID, instance.InstanceID, patch)
	for key, value := range patch {
		if value == nil {
			delete(metadata, key)
			continue
		}
		metadata[key] = value
	}
	instance.Metadata = jsonString(metadata)
	if updateErr != nil {
		return updateErr
	}
	return err
}

func (s *Service) executeCreateInstanceItem(
	ctx context.Context,
	spaceID string,
	item *pb.FunctionInstanceCreateItem,
	index int,
) (string, error) {
	if item == nil {
		return "", fmt.Errorf("instance item is required")
	}
	if strings.TrimSpace(item.GetCreateReservationId()) == "" {
		item.CreateReservationId = uuid.NewString()
	}
	instance := functionInstanceFromCreateItem(spaceID, item, index)
	instance.LifecycleID = item.GetCreateReservationId()
	unlockInstance := lockFunctionInstance(spaceID, instance.InstanceID)
	defer unlockInstance()
	pkg, err := s.catalog.GetPackage(ctx, spaceID, instance.PackageID)
	if err != nil {
		return "", err
	}
	if requiresCollectorPublishLease(pkg) {
		metadata := parseJSONMap(instance.Metadata)
		metadata["workload_type"] = "market_fetcher"
		metadata["collector_publish_fenced"] = true
		instance.Metadata = jsonString(metadata)
	}
	if err := validateTriggerType(instance.FunctionType, item.GetTriggerType()); err != nil {
		return "", err
	}
	if strings.TrimSpace(instance.CloudAccountID) == "" {
		return "", fmt.Errorf("cloud_account_id is required")
	}
	if strings.TrimSpace(instance.Region) == "" {
		return "", fmt.Errorf("region is required")
	}
	if strings.TrimSpace(instance.PackageID) == "" {
		return "", fmt.Errorf("package_id is required")
	}
	metadata := parseJSONMap(instance.Metadata)
	metadata["deployment_ready"] = false
	metadata["create_reservation_id"] = item.GetCreateReservationId()
	instance.Metadata = jsonString(metadata)
	if err := s.catalog.ReserveInstanceCreate(ctx, instance, item.GetCreateReservationId()); err != nil {
		return "", fmt.Errorf("persist SCF create reservation: %w", err)
	}
	if err := s.ensureSCFFunction(ctx, &instance, item); err != nil {
		return "", err
	}
	metadata = parseJSONMap(instance.Metadata)
	metadata["deployment_ready"] = true
	instance.Metadata = jsonString(metadata)
	persistCtx, persistCancel := acceptedSCFPersistenceContext(ctx)
	defer persistCancel()
	if err := s.catalog.UpdateInstanceCreateReservation(persistCtx, instance, item.GetCreateReservationId()); err != nil {
		return "", err
	}
	return fmt.Sprintf("created function %s", instance.FunctionName), nil
}

func modernMarketFetchInstance(instance *store.FunctionInstance, pkg store.FunctionPackage) bool {
	return strings.EqualFold(strings.TrimSpace(pkg.WorkloadType), "market_fetcher") && metadataString(parseJSONMap(instance.Metadata), "function_mode") != "instrument_snapshot"
}

func durableMarketFetchTimer(instance *store.FunctionInstance, pkg store.FunctionPackage) bool {
	return modernMarketFetchInstance(instance, pkg) && instance.TriggerType == "timer"
}

func validateMarketFetchTimerTimeout(instance *store.FunctionInstance, pkg store.FunctionPackage, environment map[string]string, outerTimeout int64) error {
	if !durableMarketFetchTimer(instance, pkg) {
		return nil
	}
	runtimeValue := strings.TrimSpace(environment["MOOX_FETCH_TIMEOUT_SECONDS"])
	if runtimeValue == "" {
		runtimeValue = strconv.Itoa(tencent.CollectorTimerTimeoutSeconds)
	}
	runtimeTimeout, err := strconv.ParseInt(runtimeValue, 10, 64)
	if err != nil || runtimeTimeout != tencent.CollectorTimerTimeoutSeconds || outerTimeout != tencent.CollectorTimerTimeoutSeconds {
		return fmt.Errorf("Timer runtime timeout MOOX_FETCH_TIMEOUT_SECONDS and config.timeout must both equal %d", tencent.CollectorTimerTimeoutSeconds)
	}
	return nil
}

func validateMarketFetchInvokeTimeout(environment, config map[string]string, outerTimeout int64) error {
	if value, ok := config["timeout"]; ok {
		parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil || parsed <= 0 {
			return fmt.Errorf("Invoke runtime timeout requires a positive config.timeout")
		}
		outerTimeout = parsed
	}
	// Match executionContext's envInt(primary, envInt(alias, 60)) fallback.
	runtimeTimeout := int64(tencent.CollectorTimerTimeoutSeconds)
	for _, key := range []string{"MOOX_FETCH_TIMEOUT_SECONDS", "MOOX_MARKET_FETCH_TIMEOUT_SECONDS"} {
		if value, ok := environment[key]; ok {
			parsed, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || parsed <= 0 {
				continue
			}
			runtimeTimeout = int64(parsed)
			break
		}
	}
	if outerTimeout <= 0 || runtimeTimeout != outerTimeout {
		return fmt.Errorf("Invoke runtime timeout must equal effective config.timeout")
	}
	return nil
}

func validateMarketFetchInvokeReadback(instance *store.FunctionInstance, pkg store.FunctionPackage, info *tencentscf.FunctionInfo) error {
	if !modernMarketFetchInstance(instance, pkg) || instance.TriggerType == "timer" {
		return nil
	}
	if info == nil {
		return fmt.Errorf("Invoke runtime timeout configuration readback is empty")
	}
	return validateMarketFetchInvokeTimeout(info.Environment, nil, info.Timeout)
}

var errInstanceMutationReconciliationRequired = errors.New("instance mutation requires reconciliation")

type functionJobDurableOperationContextKey struct{}

func withFunctionJobDurableOperation(ctx context.Context) context.Context {
	return context.WithValue(ctx, functionJobDurableOperationContextKey{}, true)
}

func isFunctionJobDurableOperation(ctx context.Context) bool {
	durable, _ := ctx.Value(functionJobDurableOperationContextKey{}).(bool)
	return durable
}

func (s *Service) executeDeleteInstanceItem(ctx context.Context, spaceID string, item *pb.FunctionInstanceDeleteItem) (string, error) {
	if item == nil || strings.TrimSpace(item.GetInstanceId()) == "" {
		return "", fmt.Errorf("instance_id is required")
	}
	if strings.TrimSpace(item.GetLifecycleId()) == "" || strings.TrimSpace(item.GetOperationId()) == "" {
		return "", fmt.Errorf("delete item lifecycle identity and operation id are required")
	}
	// Lock before reloading; an old durable delete must not observe a stale row,
	// then race with same-ID recreation before it reaches the provider.
	unlockInstance := lockFunctionInstance(spaceID, item.GetInstanceId())
	defer unlockInstance()
	instance, err := s.catalog.GetInstance(ctx, spaceID, item.GetInstanceId())
	if err != nil {
		return "", err
	}
	if instance == nil || instance.IsDeleted {
		instance, err = s.catalog.GetInstanceIncludingDeleted(ctx, spaceID, item.GetInstanceId())
		if err != nil {
			return "", err
		}
		if instance == nil {
			if collectorPublishLeaseFieldsPresent(item.GetCollectorPublishLeaseId(), item.GetCollectorPublishFencingToken()) {
				return fmt.Sprintf("instance %s was already absent when the fenced delete ran", item.GetInstanceId()), nil
			}
			return "", fmt.Errorf("instance not found: %s", item.GetInstanceId())
		}
	}
	if instance.LifecycleID == "" || instance.LifecycleID != item.GetLifecycleId() {
		return "", store.ErrInstanceLifecycleMismatch
	}
	if err := s.catalog.ClaimInstanceDelete(ctx, spaceID, instance.InstanceID, item.GetLifecycleId(), item.GetOperationId()); err != nil {
		return "", err
	}
	retainClaim := isFunctionJobDurableOperation(ctx)
	defer func() {
		if !retainClaim {
			_ = s.catalog.ReleaseInstanceDeleteClaim(context.WithoutCancel(ctx), spaceID, instance.InstanceID, item.GetLifecycleId(), item.GetOperationId())
		}
	}()
	if instance.IsDeleted {
		return fmt.Sprintf("instance %s was already deleted", item.GetInstanceId()), nil
	}
	account, err := s.catalog.GetAccount(ctx, instance.CloudAccountID)
	if err != nil || account == nil {
		if err != nil {
			return "", err
		}
		return "", fmt.Errorf("cloud account unavailable for instance %s", instance.InstanceID)
	}
	client, err := s.scfClient(ctx, *account)
	if err != nil {
		return "", err
	}
	ref := tencentscf.FunctionRef{Region: instance.Region, FunctionName: firstString(instance.FunctionName, instance.InstanceID), Namespace: firstString(instance.Namespace, "default")}
	unlockFunction := lockSCFFunction(ref)
	defer unlockFunction()
	deleteRemote := true
	metadata := parseJSONMap(instance.Metadata)
	deploymentReady, readinessMarkerPresent := metadata["deployment_ready"].(bool)
	reservation := readinessMarkerPresent && !deploymentReady && strings.TrimSpace(instance.DeploymentID) == ""
	if reservation {
		info, readErr := client.GetFunction(ctx, ref)
		if isSCFNotFound(readErr) {
			deleteRemote = false
		} else if readErr != nil {
			return "", fmt.Errorf("verify SCF create reservation ownership for %s: %w", instance.InstanceID, readErr)
		} else if info == nil {
			return "", fmt.Errorf("verify SCF create reservation ownership for %s: empty function readback", instance.InstanceID)
		} else if strings.TrimSpace(info.Environment["MOOX_CODE_PACKAGE_ID"]) != strings.TrimSpace(instance.PackageID) ||
			strings.TrimSpace(info.Environment["MOOX_CREATE_RESERVATION_ID"]) != strings.TrimSpace(instance.LifecycleID) {
			deleteRemote = false
		}
	}
	if deleteRemote {
		if instance.TriggerType == "timer" {
			if err := client.DeleteTimerTrigger(ctx, tencentscf.TimerTriggerRequest{FunctionRef: ref, Name: timerTriggerName, Qualifier: timerTriggerQualifier}); err != nil && !isSCFNotFound(err) {
				retainClaim = retainClaim || isAmbiguousSCFProviderOutcome(err)
				return "", fmt.Errorf("delete timer trigger for %s: %w", instance.InstanceID, err)
			}
			retainClaim = true
		}
		if err := client.DeleteFunction(ctx, ref); err != nil && !isSCFNotFound(err) {
			retainClaim = retainClaim || isAmbiguousSCFProviderOutcome(err)
			return "", fmt.Errorf("delete scf function %s: %w", instance.InstanceID, err)
		}
		if err := waitForSCFFunctionDeleted(ctx, client, ref); err != nil {
			retainClaim = true
			return "", fmt.Errorf("%w: verify scf function %s deletion: %w", errInstanceMutationReconciliationRequired, ref.FunctionName, err)
		}
	}
	if err := s.catalog.CompleteInstanceDelete(ctx, spaceID, instance.InstanceID, item.GetLifecycleId(), item.GetOperationId()); err != nil {
		if deleteRemote {
			retainClaim = true
			return "", fmt.Errorf("%w: complete catalog deletion after provider delete: %v", errInstanceMutationReconciliationRequired, err)
		}
		return "", err
	}
	if !isFunctionJobDurableOperation(ctx) {
		retainClaim = false
	}
	if reservation && !deleteRemote {
		return fmt.Sprintf("removed SCF create reservation %s without deleting an unverified function", ref.FunctionName), nil
	}
	return fmt.Sprintf("deleted function %s", ref.FunctionName), nil
}

func (s *Service) executeDeployInstanceItem(
	ctx context.Context,
	spaceID string,
	item *pb.FunctionInstanceDeployItem,
) (string, error) {
	if item == nil || strings.TrimSpace(item.GetInstanceId()) == "" || strings.TrimSpace(item.GetPackageId()) == "" {
		return "", fmt.Errorf("instance_id and package_id are required")
	}
	unlockInstance := lockFunctionInstance(spaceID, item.GetInstanceId())
	defer unlockInstance()
	// Reload only after acquiring the instance lock: synchronous catalog deletion and
	// same-process deploys must agree on the exact row that is about to be used.
	instance, err := s.catalog.GetInstance(ctx, spaceID, item.GetInstanceId())
	if err != nil {
		return "", err
	}
	if instance == nil || instance.IsDeleted {
		return "", fmt.Errorf("instance not found: %s", item.GetInstanceId())
	}
	lifecycleID, err := s.catalog.InstanceLifecycleID(ctx, spaceID, instance.InstanceID)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(item.GetLifecycleId()) != "" && item.GetLifecycleId() != lifecycleID {
		return "", store.ErrInstanceLifecycleMismatch
	}
	instance.LifecycleID = lifecycleID
	if strings.TrimSpace(item.GetOperationId()) == "" {
		item.OperationId = uuid.NewString()
	}
	if err := s.catalog.ClaimInstanceMutation(ctx, spaceID, instance.InstanceID, lifecycleID, item.GetOperationId()); err != nil {
		return "", err
	}
	retainClaim := isFunctionJobDurableOperation(ctx)
	defer func() {
		if !retainClaim {
			_ = s.catalog.ReleaseInstanceMutationClaim(context.WithoutCancel(ctx), spaceID, instance.InstanceID, lifecycleID, item.GetOperationId())
		}
	}()
	pkg, err := s.catalog.GetPackage(ctx, spaceID, item.GetPackageId())
	if err != nil {
		return "", err
	}
	if pkg == nil {
		return "", fmt.Errorf("package not found: %s", item.GetPackageId())
	}
	if pkg.Status != "available" {
		return "", fmt.Errorf("package %s is not available", pkg.PackageID)
	}
	if err := s.updateSCFFunctionCode(ctx, *instance, *pkg, item.GetEnvironment(), item.GetConfig()); err != nil {
		if errors.Is(err, errInstanceMutationReconciliationRequired) || isAmbiguousSCFProviderOutcome(err) {
			retainClaim = true
			return "", fmt.Errorf("%w: deploy %s: %w", errInstanceMutationReconciliationRequired, instance.InstanceID, err)
		}
		return "", err
	}
	retainClaim = true
	persistCtx, persistCancel := acceptedSCFPersistenceContext(ctx)
	defer persistCancel()
	if err := s.catalog.UpdateInstanceDeployment(persistCtx, spaceID, item.GetInstanceId(), item.GetPackageId(), pkg.Version, item.GetConfig(), item.GetEnvironment(), isFencedCollectorInstance(instance, pkg), lifecycleID, item.GetOperationId()); err != nil {
		retainClaim = true
		return "", fmt.Errorf("%w: persist deployment for %s: %v", errInstanceMutationReconciliationRequired, instance.InstanceID, err)
	}
	// The durable runner releases the claim with the terminal item transition.
	if !isFunctionJobDurableOperation(ctx) {
		retainClaim = false
	}
	return fmt.Sprintf("deployed package %s to %s", pkg.PackageID, instance.InstanceID), nil
}

func (s *Service) ensureSCFFunction(ctx context.Context, instance *store.FunctionInstance, item *pb.FunctionInstanceCreateItem) error {
	if strings.EqualFold(strings.TrimSpace(instance.Region), "local") {
		return s.ensureLocalPackage(ctx, instance, item)
	}
	pkg, account, err := s.packageAndAccount(ctx, instance.SpaceID, instance.PackageID, instance.CloudAccountID)
	if err != nil {
		return err
	}
	if pkg.Status != "available" {
		return fmt.Errorf("package %s is not available", pkg.PackageID)
	}
	reservationID := strings.TrimSpace(item.GetCreateReservationId())
	if reservationID == "" {
		return fmt.Errorf("create reservation identity is required")
	}
	instance.PackageVersion = pkg.Version
	// 日志服务（CLS）环境必须在环境校验之前补齐：Timer 采集函数的环境校验要求带 CLS 配置，
	// 之后的校验、创建与读回比对都基于补齐后的环境。
	if environment := copyStringMap(item.GetEnvironment()); s.applyCLSEnvironment(ctx, *account, instance, environment) {
		item.Environment = environment
	}
	metadata := parseJSONMap(instance.Metadata)
	config := item.GetConfig()
	memorySize := configInt64(config, "memory_size", 256)
	timeoutSeconds := configInt64(config, "timeout", defaultSCFTimeoutSeconds)
	effectiveConfig := config
	if durableMarketFetchTimer(instance, *pkg) {
		timeoutSeconds = configInt64(config, "timeout", tencent.CollectorTimerTimeoutSeconds)
		if err := validateMarketFetchTimerTimeout(instance, *pkg, item.GetEnvironment(), timeoutSeconds); err != nil {
			return err
		}
		effectiveConfig = copyStringMap(config)
		if effectiveConfig == nil {
			effectiveConfig = make(map[string]string)
		}
		effectiveConfig["timeout"] = strconv.FormatInt(timeoutSeconds, 10)
		environment := copyStringMap(item.GetEnvironment())
		if environment == nil {
			environment = make(map[string]string)
		}
		environment["MOOX_CODE_PACKAGE_ID"] = pkg.PackageID
		if err := tencent.ValidateCollectorTimerEnvironment(environment); err != nil {
			return err
		}
	} else if modernMarketFetchInstance(instance, *pkg) {
		if err := validateMarketFetchInvokeTimeout(item.GetEnvironment(), config, timeoutSeconds); err != nil {
			return err
		}
		environment := copyStringMap(item.GetEnvironment())
		if environment == nil {
			environment = make(map[string]string)
		}
		environment["MOOX_CODE_PACKAGE_ID"] = pkg.PackageID
		if err := tencent.ValidateCollectorMarketFetchEnvironment(environment); err != nil {
			return err
		}
	}
	// 行情定时函数内存默认 64MB，可由 config.memory_size 指定。
	if isMarketFetchInstance(instance) && instance.TriggerType == "timer" {
		memorySize = configInt64(config, "memory_size", 64)
	}
	metadata["runtime"] = firstString(item.GetRuntime(), pkg.Runtime, metadataString(metadata, "runtime"))
	metadata["handler"] = firstString(item.GetHandler(), metadataString(metadata, "handler"), "main")
	instance.Metadata = jsonString(metadata)

	client, err := s.scfClient(ctx, *account)
	if err != nil {
		return err
	}
	ref := tencentscf.FunctionRef{
		Region:       instance.Region,
		FunctionName: firstString(instance.FunctionName, instance.InstanceID),
		Namespace:    firstString(instance.Namespace, "default"),
	}
	if err := client.EnsureNamespace(ctx, ref.Region, ref.Namespace); err != nil {
		return fmt.Errorf("ensure SCF namespace %s in %s: %w", ref.Namespace, ref.Region, err)
	}
	unlockFunction := lockSCFFunction(ref)
	defer unlockFunction()
	info, err := client.GetFunction(ctx, ref)
	if err == nil {
		remotePackageID := strings.TrimSpace(info.Environment["MOOX_CODE_PACKAGE_ID"])
		if remotePackageID != pkg.PackageID {
			return fmt.Errorf(
				"scf function %s already exists with code package %q; expected %q",
				ref.FunctionName,
				remotePackageID,
				pkg.PackageID,
			)
		}
		remoteReservationID := strings.TrimSpace(info.Environment["MOOX_CREATE_RESERVATION_ID"])
		if remoteReservationID != reservationID {
			return fmt.Errorf(
				"scf function %s already exists with create reservation %q; expected %q",
				ref.FunctionName,
				remoteReservationID,
				reservationID,
			)
		}
		info, err = waitForSCFActive(ctx, client, ref, info)
		if err != nil {
			return err
		}
		if err := reconcileSCFPublicNetwork(ctx, client, ref, info, effectiveConfig); err != nil {
			return err
		}
		if err := validateMarketFetchInvokeReadback(instance, *pkg, info); err != nil {
			return err
		}
		if err := verifySCFFunctionConfiguration(info, effectiveConfig, item.GetEnvironment(), ref.FunctionName); err != nil {
			return err
		}
		if err := ensureSCFAsyncRetryConfig(ctx, client, ref, isMarketFetchInstance(instance)); err != nil {
			return err
		}
		if err := ensureReservedConcurrency(ctx, client, ref, effectiveConfig); err != nil {
			return err
		}
		if err := ensureInitialTimerTrigger(ctx, client, instance, ref); err != nil {
			return err
		}
		// A previous attempt may have created the function before FunctionInstance was
		// interrupted. The reservation nonce proves ownership of this request.
		return nil
	}
	if !isSCFNotFound(err) {
		return fmt.Errorf("get scf function %s: %w", ref.FunctionName, err)
	}
	environment := copyStringMap(item.GetEnvironment())
	if environment == nil {
		environment = make(map[string]string)
	}
	environment["MOOX_CODE_PACKAGE_ID"] = pkg.PackageID
	environment["MOOX_CREATE_RESERVATION_ID"] = reservationID
	s.applyCLSEnvironment(ctx, *account, instance, environment)
	if err := tencent.ValidateSCFEnvironment(environment); err != nil {
		return fmt.Errorf("scf function %s %w", ref.FunctionName, err)
	}
	createCtx, createCancel := context.WithTimeout(ctx, scfCreateAttemptTimeout)
	_, err = client.CreateFunction(createCtx, tencentscf.CreateFunctionRequest{
		FunctionRef:            ref,
		Runtime:                firstString(item.GetRuntime(), pkg.Runtime, "CustomRuntime"),
		Handler:                firstString(item.GetHandler(), "main"),
		Description:            fmt.Sprintf("MooX cloud function instance %s", instance.InstanceID),
		MemorySize:             memorySize,
		Timeout:                timeoutSeconds,
		MaxInstanceConcurrency: configInt64(effectiveConfig, "max_instance_concurrency", 0),
		Environment:            environment,
		COSBucket:              pkg.COSBucket,
		COSRegion:              firstString(pkg.COSRegion, account.COSRegion),
		COSObject:              strings.TrimPrefix(pkg.COSPath, "/"),
		Type:                   firstString(config["function_type"], "Event"),
		PublicNetStatus:        desiredSCFPublicNetStatus(effectiveConfig),
		VpcID:                  strings.TrimSpace(effectiveConfig["vpc_id"]),
		SubnetID:               strings.TrimSpace(effectiveConfig["subnet_id"]),
	})
	createCancel()
	if err != nil {
		createErr := err
		if errors.Is(ctx.Err(), context.Canceled) {
			return fmt.Errorf("create scf function %s: %w", ref.FunctionName, createErr)
		}
		reconcileCtx := ctx
		reconcileCancel := func() {}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			reconcileCtx, reconcileCancel = context.WithTimeout(
				context.WithoutCancel(ctx),
				scfCreateReconcileTimeout,
			)
		}
		defer reconcileCancel()
		info, err = client.GetFunction(reconcileCtx, ref)
		if err != nil {
			return fmt.Errorf("create scf function %s: %w", ref.FunctionName, createErr)
		}
		remotePackageID := strings.TrimSpace(info.Environment["MOOX_CODE_PACKAGE_ID"])
		remoteReservationID := strings.TrimSpace(info.Environment["MOOX_CREATE_RESERVATION_ID"])
		if remotePackageID != pkg.PackageID || remoteReservationID != reservationID {
			return fmt.Errorf(
				"create scf function %s returned an ambiguous error and remote reservation identity/package is %q/%q, expected %q/%q: %w",
				ref.FunctionName,
				remoteReservationID,
				remotePackageID,
				reservationID,
				pkg.PackageID,
				createErr,
			)
		}
		info, err = waitForSCFActive(reconcileCtx, client, ref, info)
		if err != nil {
			return err
		}
		if err := reconcileSCFPublicNetwork(reconcileCtx, client, ref, info, effectiveConfig); err != nil {
			return err
		}
		if err := validateMarketFetchInvokeReadback(instance, *pkg, info); err != nil {
			return err
		}
		if err := verifySCFFunctionConfiguration(info, effectiveConfig, item.GetEnvironment(), ref.FunctionName); err != nil {
			return err
		}
		if err := ensureSCFAsyncRetryConfig(reconcileCtx, client, ref, isMarketFetchInstance(instance)); err != nil {
			return err
		}
		if err := ensureReservedConcurrency(reconcileCtx, client, ref, effectiveConfig); err != nil {
			return err
		}
		if err := ensureInitialTimerTrigger(reconcileCtx, client, instance, ref); err != nil {
			return err
		}
		return nil
	}
	info, err = waitForSCFActive(ctx, client, ref, nil)
	if err != nil {
		return err
	}
	if err := reconcileSCFPublicNetwork(ctx, client, ref, info, effectiveConfig); err != nil {
		return err
	}
	if err := validateMarketFetchInvokeReadback(instance, *pkg, info); err != nil {
		return err
	}
	if err := verifySCFFunctionConfiguration(info, effectiveConfig, item.GetEnvironment(), ref.FunctionName); err != nil {
		return err
	}
	if err := ensureSCFAsyncRetryConfig(ctx, client, ref, isMarketFetchInstance(instance)); err != nil {
		return err
	}
	if err := ensureReservedConcurrency(ctx, client, ref, effectiveConfig); err != nil {
		return err
	}
	if err := ensureInitialTimerTrigger(ctx, client, instance, ref); err != nil {
		return err
	}
	return nil
}

func ensureInitialTimerTrigger(ctx context.Context, client scfProvisioner, instance *store.FunctionInstance, ref tencentscf.FunctionRef) error {
	if instance == nil || instance.TriggerType != "timer" {
		return nil
	}
	// 已存在的定时器保持原样：部署新代码时不能把正在运行的定时器重置为停用，
	// 只有函数还没有定时器时才创建一个停用状态的初始定时器，由运行时配置再启用。
	existing, err := client.GetTimerTrigger(ctx, ref, timerTriggerName)
	if err != nil && !isSCFNotFound(err) {
		return fmt.Errorf("read timer trigger for %s: %w", ref.FunctionName, err)
	}
	if err == nil && existing != nil {
		return nil
	}
	if _, err := client.EnsureTimerTrigger(ctx, tencentscf.TimerTriggerRequest{FunctionRef: ref, Name: timerTriggerName, Cron: "0 * * * * * *", Enabled: false, Qualifier: timerTriggerQualifier, Message: timerTriggerMessage}); err != nil {
		return fmt.Errorf("ensure initial timer trigger for %s: %w", ref.FunctionName, err)
	}
	return nil
}

// ensureReservedConcurrency 按 config.reserved_concurrency_mem 设置函数的预留并发配额；未配置时不处理。
// 预留配额同时是该函数的并发上限，补采函数借此由平台硬性限制实际并发实例数。
func ensureReservedConcurrency(ctx context.Context, client scfProvisioner, ref tencentscf.FunctionRef, config map[string]string) error {
	quota := configInt64(config, "reserved_concurrency_mem", 0)
	if quota <= 0 {
		return nil
	}
	configurer, ok := client.(interface {
		PutReservedConcurrencyConfig(context.Context, tencentscf.PutReservedConcurrencyConfigRequest) error
	})
	if !ok {
		return nil
	}
	if err := configurer.PutReservedConcurrencyConfig(ctx, tencentscf.PutReservedConcurrencyConfigRequest{FunctionRef: ref, ReservedConcurrencyMem: quota}); err != nil {
		return fmt.Errorf("configure reserved concurrency for %s: %w", ref.FunctionName, err)
	}
	return nil
}

func ensureSCFAsyncRetryConfig(ctx context.Context, client scfProvisioner, ref tencentscf.FunctionRef, enabled bool) error {
	if !enabled {
		return nil
	}
	configurer, ok := client.(interface {
		UpdateFunctionEventInvokeConfig(context.Context, tencentscf.UpdateFunctionEventInvokeConfigRequest) (*tencentscf.UpdateFunctionEventInvokeConfigResponse, error)
	})
	if !ok {
		return nil
	}
	// Tencent requires a minimum 60-second async retention period. Keep it
	// close to the Collector deadline; retries are owned by MooX, not SCF.
	if _, err := configurer.UpdateFunctionEventInvokeConfig(ctx, tencentscf.UpdateFunctionEventInvokeConfigRequest{FunctionRef: ref, RetryNum: 0, MsgTTL: 60}); err != nil {
		return fmt.Errorf("configure scf async retry for %s: %w", ref.FunctionName, err)
	}
	return nil
}

// ensureLocalPackage lets local-region instances (used by E2E tests) run without
// Tencent credentials or COS: they need only an available package descriptor,
// not a remote SCF function.
func (s *Service) ensureLocalPackage(ctx context.Context, instance *store.FunctionInstance, item *pb.FunctionInstanceCreateItem) error {
	pkg, err := s.catalog.GetPackage(ctx, instance.SpaceID, instance.PackageID)
	if err != nil {
		return err
	}
	if pkg == nil {
		pkg = &store.FunctionPackage{
			SpaceID:        instance.SpaceID,
			PackageID:      instance.PackageID,
			PackageName:    instance.PackageID,
			Version:        firstString(item.GetRuntime(), "local"),
			Description:    "local runtime package",
			Runtime:        firstString(item.GetRuntime(), "go1"),
			PackageType:    "collector",
			WorkloadType:   "collect.kline",
			OriginalName:   instance.PackageID,
			FileSize:       1,
			FileMD5:        "local",
			CloudAccountID: instance.CloudAccountID,
			Status:         "available",
		}
	} else {
		pkg.Status = "available"
		if pkg.Version == "" {
			pkg.Version = firstString(item.GetRuntime(), "local")
		}
	}
	if err := s.catalog.UpsertPackage(ctx, *pkg); err != nil {
		return err
	}
	instance.PackageVersion = pkg.Version
	return nil
}

func (s *Service) updateSCFFunctionCode(
	ctx context.Context,
	instance store.FunctionInstance,
	pkg store.FunctionPackage,
	desiredEnvironment map[string]string,
	desiredConfig map[string]string,
) error {
	mutationStarted := false
	requireReconciliationAfterMutation := func(err error) error {
		if err == nil || !mutationStarted || errors.Is(err, errInstanceMutationReconciliationRequired) {
			return err
		}
		return fmt.Errorf("%w: %w", errInstanceMutationReconciliationRequired, err)
	}
	account, err := s.catalog.GetAccount(ctx, instance.CloudAccountID)
	if err != nil {
		return err
	}
	if account == nil {
		return fmt.Errorf("cloud account not found: %s", instance.CloudAccountID)
	}
	if !isTencentProvider(account.Provider) {
		return fmt.Errorf("unsupported cloud provider: %s", account.Provider)
	}
	metadata := parseJSONMap(instance.Metadata)
	client, err := s.scfClient(ctx, *account)
	if err != nil {
		return err
	}
	ref := tencentscf.FunctionRef{
		Region:       instance.Region,
		FunctionName: firstString(instance.FunctionName, instance.InstanceID),
		Namespace:    firstString(instance.Namespace, "default"),
	}
	unlock := lockSCFFunction(ref)
	defer unlock()
	info, err := client.GetFunction(ctx, ref)
	if err != nil {
		return fmt.Errorf("get scf function %s before deploy: %w", ref.FunctionName, err)
	}
	// MOOX_CODE_PACKAGE_ID is written only after the code update has completed
	// and the desired configuration update has been accepted. It therefore
	// doubles as the idempotency marker when the caller times out while Tencent
	// is still returning Updating.
	codeCurrent := strings.TrimSpace(info.Environment["MOOX_CODE_PACKAGE_ID"]) == pkg.PackageID
	info, err = waitForSCFActive(ctx, client, ref, info)
	if err != nil {
		return err
	}
	// Deployment callers own only the base runtime variables. Preserve the
	// Collector-managed timer assignment when a function is republished; the
	// next reconciliation will replace it if the desired assignment changed.
	// Validate the complete merged environment before uploading code so an
	// oversized timer deployment cannot leave a new package with old config.
	environment := copyStringMap(info.Environment)
	if environment == nil {
		environment = make(map[string]string)
	}
	for key, value := range desiredEnvironment {
		environment[key] = value
	}
	if strings.TrimSpace(environment["MOOX_EVENTBUS_NATS_TLS_CA_FILE"]) != "" {
		delete(environment, "MOOX_EVENTBUS_NATS_TLS_CA_PEM_B64")
	}
	environment["MOOX_CODE_PACKAGE_ID"] = pkg.PackageID
	if err := tencent.ValidateSCFEnvironment(environment); err != nil {
		return fmt.Errorf("scf function %s %w", ref.FunctionName, err)
	}
	if durableMarketFetchTimer(&instance, pkg) {
		if err := validateMarketFetchTimerTimeout(&instance, pkg, environment, configInt64(desiredConfig, "timeout", info.Timeout)); err != nil {
			return err
		}
		if err := tencent.ValidateCollectorTimerEnvironment(environment); err != nil {
			return err
		}
	} else if modernMarketFetchInstance(&instance, pkg) {
		if err := validateMarketFetchInvokeTimeout(environment, desiredConfig, info.Timeout); err != nil {
			return err
		}
		if err := tencent.ValidateCollectorMarketFetchEnvironment(environment); err != nil {
			return err
		}
	}
	if !codeCurrent {
		// Treat every error from this point as an uncertain partial deployment:
		// even provider errors that look deterministic may follow a committed mutation.
		mutationStarted = true
		_, err = client.UpdateFunctionCode(ctx, tencentscf.UpdateFunctionCodeRequest{
			FunctionRef: ref,
			Handler:     firstString(metadataString(metadata, "handler"), "main"),
			COSBucket:   pkg.COSBucket,
			COSRegion:   firstString(pkg.COSRegion, account.COSRegion),
			COSObject:   strings.TrimPrefix(pkg.COSPath, "/"),
		})
		if err != nil {
			return requireReconciliationAfterMutation(fmt.Errorf("update scf function %s: %w", firstString(instance.FunctionName, instance.InstanceID), err))
		}
		if _, err := waitForSCFActive(ctx, client, ref, nil); err != nil {
			return requireReconciliationAfterMutation(err)
		}
	}
	mutationStarted = true
	if _, err := client.UpdateFunctionConfiguration(ctx, tencentscf.UpdateFunctionConfigurationRequest{
		FunctionRef:            ref,
		Environment:            environment,
		MemorySize:             configInt64(desiredConfig, "memory_size", 0),
		Timeout:                configInt64(desiredConfig, "timeout", 0),
		MaxInstanceConcurrency: configInt64(desiredConfig, "max_instance_concurrency", 0),
		PublicNetStatus:        desiredSCFPublicNetStatus(desiredConfig),
		VpcID:                  strings.TrimSpace(desiredConfig["vpc_id"]),
		SubnetID:               strings.TrimSpace(desiredConfig["subnet_id"]),
		ClearVPC:               desiredSCFClearVPC(desiredConfig),
		ClearNativeCLS:         true,
	}); err != nil {
		return requireReconciliationAfterMutation(fmt.Errorf("update scf function %s configuration: %w", ref.FunctionName, err))
	}
	if _, err = waitForSCFActive(ctx, client, ref, nil); err != nil {
		return requireReconciliationAfterMutation(err)
	}
	verified, err := client.GetFunction(ctx, ref)
	if err != nil {
		return requireReconciliationAfterMutation(fmt.Errorf("verify scf function %s configuration: %w", ref.FunctionName, err))
	}
	if err := verifySCFFunctionConfiguration(verified, desiredConfig, desiredEnvironment, ref.FunctionName); err != nil {
		return requireReconciliationAfterMutation(err)
	}
	if err := validateMarketFetchInvokeReadback(&instance, pkg, verified); err != nil {
		return requireReconciliationAfterMutation(err)
	}
	if err := ensureSCFAsyncRetryConfig(ctx, client, ref, isMarketFetchInstance(&instance)); err != nil {
		return requireReconciliationAfterMutation(err)
	}
	return requireReconciliationAfterMutation(ensureInitialTimerTrigger(ctx, client, &instance, ref))
}

func verifySCFFunctionConfiguration(info *tencentscf.FunctionInfo, desiredConfig, desiredEnvironment map[string]string, functionName string) error {
	if info == nil {
		return fmt.Errorf("scf function %s configuration readback is empty", functionName)
	}
	if expected := configInt64(desiredConfig, "memory_size", 0); expected > 0 {
		if info.MemorySize <= 0 || info.MemorySize != expected {
			return fmt.Errorf("scf function %s memory_size=%d; expected %d", functionName, info.MemorySize, expected)
		}
	}
	if expected := configInt64(desiredConfig, "timeout", 0); expected > 0 {
		if info.Timeout <= 0 || info.Timeout != expected {
			return fmt.Errorf("scf function %s timeout=%d; expected %d", functionName, info.Timeout, expected)
		}
	}
	if expected := configInt64(desiredConfig, "max_instance_concurrency", 0); expected > 0 {
		if info.MaxInstanceConcurrency <= 0 || info.MaxInstanceConcurrency != expected {
			return fmt.Errorf("scf function %s max_instance_concurrency=%d; expected %d (SCF readback unavailable or mismatched)", functionName, info.MaxInstanceConcurrency, expected)
		}
	}
	if expected := desiredSCFPublicNetStatus(desiredConfig); expected != "" && !strings.EqualFold(info.PublicNetStatus, expected) {
		return fmt.Errorf("scf function %s public_net_status=%q; expected %q", functionName, info.PublicNetStatus, expected)
	}
	if expected := strings.TrimSpace(desiredConfig["vpc_id"]); expected != "" && info.VpcID != expected {
		return fmt.Errorf("scf function %s vpc_id=%q; expected %q", functionName, info.VpcID, expected)
	}
	if expected := strings.TrimSpace(desiredConfig["subnet_id"]); expected != "" && info.SubnetID != expected {
		return fmt.Errorf("scf function %s subnet_id=%q; expected %q", functionName, info.SubnetID, expected)
	}
	if desiredSCFClearVPC(desiredConfig) && (strings.TrimSpace(info.VpcID) != "" || strings.TrimSpace(info.SubnetID) != "") {
		return fmt.Errorf("scf function %s remains attached to vpc_id=%q subnet_id=%q; expected unbound", functionName, info.VpcID, info.SubnetID)
	}
	for key, expected := range desiredEnvironment {
		if actual, ok := info.Environment[key]; !ok || actual != expected {
			return fmt.Errorf("scf function %s environment %q does not match desired configuration", functionName, key)
		}
	}
	return nil
}

func desiredSCFPublicNetStatus(config map[string]string) string {
	return strings.ToUpper(strings.TrimSpace(config["public_net_status"]))
}

func desiredSCFClearVPC(config map[string]string) bool {
	return strings.EqualFold(strings.TrimSpace(config["clear_vpc"]), "true")
}

func reconcileSCFPublicNetwork(ctx context.Context, client scfProvisioner, ref tencentscf.FunctionRef, info *tencentscf.FunctionInfo, desiredConfig map[string]string) error {
	expected := desiredSCFPublicNetStatus(desiredConfig)
	expectedVPC := strings.TrimSpace(desiredConfig["vpc_id"])
	expectedSubnet := strings.TrimSpace(desiredConfig["subnet_id"])
	clearVPC := desiredSCFClearVPC(desiredConfig)
	publicMismatch := expected != "" && (info == nil || !strings.EqualFold(info.PublicNetStatus, expected))
	vpcMismatch := (expectedVPC != "" && (info == nil || info.VpcID != expectedVPC || info.SubnetID != expectedSubnet)) ||
		(clearVPC && info != nil && (strings.TrimSpace(info.VpcID) != "" || strings.TrimSpace(info.SubnetID) != ""))
	if info == nil || (!publicMismatch && !vpcMismatch) {
		return nil
	}
	if _, err := client.UpdateFunctionConfiguration(ctx, tencentscf.UpdateFunctionConfigurationRequest{
		FunctionRef: ref, Environment: info.Environment, PublicNetStatus: expected,
		VpcID: expectedVPC, SubnetID: expectedSubnet,
		ClearVPC: clearVPC,
	}); err != nil {
		return fmt.Errorf("update scf function %s public network: %w", ref.FunctionName, err)
	}
	updated, err := waitForSCFActive(ctx, client, ref, nil)
	if err != nil {
		return err
	}
	*info = *updated
	return nil
}

func isMarketFetchInstance(instance *store.FunctionInstance) bool {
	return instance != nil && strings.EqualFold(strings.TrimSpace(metadataString(parseJSONMap(instance.Metadata), "workload_type")), "market_fetcher")
}

func acceptedSCFPersistenceContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return context.WithTimeout(context.WithoutCancel(ctx), functionJobCompletionTimeout)
	}
	return context.WithCancel(ctx)
}

func waitForSCFActive(ctx context.Context, client scfProvisioner, ref tencentscf.FunctionRef, current *tencentscf.FunctionInfo) (*tencentscf.FunctionInfo, error) {
	waitCtx, cancel := context.WithTimeout(ctx, scfOperationTimeout)
	defer cancel()
	for {
		if current == nil {
			info, err := client.GetFunction(waitCtx, ref)
			if err != nil {
				if !isSCFNotFound(err) && !isTransientSCFProviderError(err) {
					return nil, fmt.Errorf("get scf function %s status: %w", ref.FunctionName, err)
				}
				timer := time.NewTimer(time.Second)
				select {
				case <-waitCtx.Done():
					timer.Stop()
					return nil, fmt.Errorf("wait for scf function %s active: %w", ref.FunctionName, waitCtx.Err())
				case <-timer.C:
					continue
				}
			}
			current = info
		}
		status := strings.ToLower(strings.TrimSpace(current.Status))
		if status == "active" {
			return current, nil
		}
		if status == "" || strings.Contains(status, "failed") {
			return nil, fmt.Errorf("scf function %s entered status %q", ref.FunctionName, current.Status)
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-waitCtx.Done():
			timer.Stop()
			return nil, fmt.Errorf("wait for scf function %s active: %w", ref.FunctionName, waitCtx.Err())
		case <-timer.C:
			current = nil
		}
	}
}

func waitForSCFFunctionDeleted(ctx context.Context, client scfProvisioner, ref tencentscf.FunctionRef) error {
	waitCtx, cancel := context.WithTimeout(ctx, scfOperationTimeout)
	defer cancel()
	for {
		_, err := client.GetFunction(waitCtx, ref)
		if isSCFNotFound(err) {
			return nil
		}
		if err != nil && !isTransientSCFProviderError(err) {
			return fmt.Errorf("read function after delete: %w", err)
		}
		if waitCtx.Err() != nil {
			return fmt.Errorf("wait for scf function deletion: %w", waitCtx.Err())
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-waitCtx.Done():
			timer.Stop()
			return fmt.Errorf("wait for scf function deletion: %w", waitCtx.Err())
		case <-timer.C:
		}
	}
}

func (s *Service) packageAndAccount(ctx context.Context, spaceID string, packageID string, accountID string) (*store.FunctionPackage, *store.CloudAccount, error) {
	pkg, err := s.catalog.GetPackage(ctx, spaceID, packageID)
	if err != nil {
		return nil, nil, err
	}
	if pkg == nil {
		return nil, nil, fmt.Errorf("package not found: %s", packageID)
	}
	account, err := s.catalog.GetAccount(ctx, accountID)
	if err != nil {
		return nil, nil, err
	}
	if account == nil {
		return nil, nil, fmt.Errorf("cloud account not found: %s", accountID)
	}
	if !isTencentProvider(account.Provider) {
		return nil, nil, fmt.Errorf("unsupported cloud provider: %s", account.Provider)
	}
	return pkg, account, nil
}

func (s *Service) scfClient(ctx context.Context, account store.CloudAccount) (scfProvisioner, error) {
	credential, err := s.resolveCloudCredential(ctx, account)
	if err != nil {
		return nil, err
	}
	factory := s.scfClientFactory
	if factory == nil {
		factory = defaultSCFClientFactory
	}
	return factory(credential), nil
}

// applyCLSEnvironment 在采集函数的环境里补齐日志服务（CLS）上报配置，返回是否有改动。
// 首次使用时幂等开通 CLS。这是尽力而为的附加动作：开通失败、补齐后环境超过平台上限、凭证取不到，
// 都只记日志并保持环境原样，绝不让它影响函数的创建、升级或停用。
func (s *Service) applyCLSEnvironment(ctx context.Context, account store.CloudAccount, instance *store.FunctionInstance, environment map[string]string) bool {
	if s.clsEnvironment == nil || !isMarketFetchInstance(instance) {
		return false
	}
	credential, err := s.resolveCloudCredential(ctx, account)
	if err != nil {
		return false
	}
	// 在副本上补齐并校验容量：放不下就放弃注入，原环境不变。
	candidate := copyStringMap(environment)
	if candidate == nil {
		candidate = make(map[string]string)
	}
	if !s.clsEnvironment.Apply(ctx, account.AccountID, clsprovision.Credential{SecretID: credential.SecretID, SecretKey: credential.SecretKey}, candidate) {
		return false
	}
	if err := tencent.ValidateSCFEnvironment(candidate); err != nil {
		log.WarnContextf(ctx, "cloudfunction skip cls environment for instance %s: %v", instance.InstanceID, err)
		return false
	}
	for key, value := range candidate {
		environment[key] = value
	}
	return true
}

func (s *Service) resolveCloudCredential(ctx context.Context, account store.CloudAccount) (cloudcredential.TencentCredential, error) {
	if s.credentialResolver == nil {
		return cloudcredential.TencentCredential{}, fmt.Errorf("cloud credential resolver is not configured")
	}
	credential, err := s.credentialResolver.Resolve(ctx, account)
	if err != nil {
		return cloudcredential.TencentCredential{}, err
	}
	return credential, nil
}

func isTencentProvider(provider string) bool {
	return strings.TrimSpace(provider) == "tencent"
}

func normalizeTriggerType(functionType, triggerType string) string {
	functionType = strings.TrimSpace(functionType)
	triggerType = strings.ToLower(strings.TrimSpace(triggerType))
	if functionType != "scf-event" {
		return ""
	}
	if triggerType == "timer" {
		return "timer"
	}
	return "invoke"
}

func validateTriggerType(functionType, triggerType string) error {
	functionType = strings.TrimSpace(functionType)
	triggerType = strings.ToLower(strings.TrimSpace(triggerType))
	if functionType != "scf-event" {
		if triggerType != "" {
			return fmt.Errorf("trigger_type is only valid for scf-event instances")
		}
		return nil
	}
	if triggerType != "" && triggerType != "invoke" && triggerType != "timer" {
		return fmt.Errorf("trigger_type must be invoke or timer")
	}
	return nil
}

func isSCFNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "resourcenotfound") || strings.Contains(msg, "not found")
}

func configInt64(values map[string]string, key string, fallback int64) int64 {
	raw := strings.TrimSpace(values[key])
	if raw == "" {
		return fallback
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

func copyStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func functionInstanceFromCreateItem(spaceID string, item *pb.FunctionInstanceCreateItem, index int) store.FunctionInstance {
	metadata := structMap(item.GetMetadata())
	if _, ok := metadata["index"]; !ok {
		metadata["index"] = index
	}
	if item.GetRuntime() != "" {
		metadata["runtime"] = item.GetRuntime()
	}
	if item.GetHandler() != "" {
		metadata["handler"] = item.GetHandler()
	}
	if len(item.GetConfig()) > 0 {
		metadata["config"] = item.GetConfig()
	}
	prefix := firstString(metadataString(metadata, "function_name_prefix"), "moox-cloudfunction")
	indexSuffix := firstString(metadataString(metadata, "index"), strconv.Itoa(index))
	functionName := firstString(
		metadataString(metadata, "function_name"),
		fmt.Sprintf(
			"%s-%s-%s",
			prefix,
			firstString(item.GetRegion(), "region"),
			indexSuffix,
		),
	)
	instanceID := firstString(metadataString(metadata, "instance_id"), functionName)
	return store.FunctionInstance{
		SpaceID:        spaceID,
		InstanceID:     instanceID,
		CloudAccountID: item.GetCloudAccountId(),
		PackageID:      item.GetPackageId(),
		DeploymentID:   firstString(item.GetDeploymentId(), metadataString(metadata, "deployment_id")),
		FunctionType:   firstString(item.GetFunctionType(), "scf-event"),
		TriggerType:    normalizeTriggerType(firstString(item.GetFunctionType(), "scf-event"), item.GetTriggerType()),
		Provider:       "tencent-scf",
		Region:         item.GetRegion(),
		Namespace:      firstString(item.GetNamespace(), "default"),
		FunctionName:   functionName,
		Metadata:       jsonString(metadata),
		IsDeleted:      false,
	}
}

func toPBInstance(instance store.FunctionInstance) *pb.FunctionInstance {
	metadata := parseJSONMap(instance.Metadata)
	st, _ := structpb.NewStruct(metadata)
	if st == nil {
		st = &structpb.Struct{}
	}
	return &pb.FunctionInstance{
		Id:             int32(instance.ID),
		SpaceId:        instance.SpaceID,
		InstanceId:     instance.InstanceID,
		CloudAccountId: instance.CloudAccountID,
		PackageId:      instance.PackageID,
		PackageVersion: instance.PackageVersion,
		DeploymentId:   instance.DeploymentID,
		Namespace:      instance.Namespace,
		FunctionType:   instance.FunctionType,
		TriggerType:    instance.TriggerType,
		Provider:       instance.Provider,
		FunctionName:   instance.FunctionName,
		WorkloadType:   metadataString(metadata, "workload_type"),
		Region:         instance.Region,
		Metadata:       st,
		IsDeleted:      instance.IsDeleted,
		CreateTime:     formatTime(instance.CreateTime),
		ModifyTime:     formatTime(instance.ModifyTime),
	}
}
