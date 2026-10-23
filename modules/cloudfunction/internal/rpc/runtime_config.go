package rpc

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	tencentscf "github.com/mooyang-code/moox/modules/cloudfunction/internal/providers/tencentscf"
	"github.com/mooyang-code/moox/modules/cloudfunction/internal/spacecontext"
	"github.com/mooyang-code/moox/modules/cloudfunction/internal/store"
	pb "github.com/mooyang-code/moox/modules/cloudfunction/proto/cloudfunctiongen"
	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
	"google.golang.org/protobuf/encoding/protojson"
)

const (
	timerTriggerName       = "moox-market-fetch-timer"
	timerTriggerMessage    = "market_fetch_timer_v1"
	timerTriggerQualifier  = "$LATEST"
	maxSCFEnvironmentBytes = 4096
)

var managedEnvironmentKeys = map[string]struct{}{
	"MOOX_MARKET_FETCH_BINDING_HASH":        {},
	"MOOX_ACCESS_ADDRESS":                   {},
	"MOOX_ACCESS_ID":                        {},
	"MOOX_CALLER":                           {},
	"MOOX_CALLER_KEY_ID":                    {},
	"MOOX_CALLER_KEY":                       {},
	"MOOX_FETCH_TIMEOUT_SECONDS":            {},
	"MOOX_MARKET_FETCH_PROVIDER":            {},
	"MOOX_MARKET_FETCH_MARKET_TYPE":         {},
	"MOOX_MARKET_FETCH_MARKET_ID":           {},
	"MOOX_MARKET_FETCH_INSTRUMENT_TYPE":     {},
	"MOOX_MARKET_FETCH_SOURCE_ID":           {},
	"MOOX_MARKET_FETCH_SERIES_TAG":          {},
	"MOOX_MARKET_FETCH_DATASET_ID":          {},
	"MOOX_MARKET_FETCH_FREQUENCY":           {},
	"MOOX_MARKET_FETCH_OUTPUT_FIELDS":       {},
	"MOOX_MARKET_FETCH_SUBJECT_COUNT":       {},
	"MOOX_MARKET_FETCH_SUBJECTS":            {},
	"MOOX_MARKET_FETCH_SYMBOLS_JSON":        {},
	"MOOX_MARKET_FETCH_ASSIGNMENT_HASH":     {},
	"MOOX_MARKET_FETCH_DNS_ROUTES_JSON":     {},
	"MOOX_MARKET_FETCH_DNS_HASH":            {},
	"MOOX_MARKET_FETCH_DNS_UPDATED_AT":      {},
	"MOOX_MARKET_FETCH_PROVIDER_CHAIN":      {},
	"MOOX_MARKET_FETCH_ROUTE_VERSION":       {},
	"MOOX_MARKET_FETCH_GROUP_ID":            {},
	"MOOX_MARKET_FETCH_GROUP_COUNT":         {},
	"MOOX_MARKET_FETCH_MODE":                {},
	"MOOX_MARKET_FETCH_PLAN":                {},
	"MOOX_METRICS_EVENTBUS_URL":             {},
	"MOOX_METRICS_EVENTBUS_CREDENTIAL_FILE": {},
}

func (s *Service) SubmitUpdateFunctionInstanceRuntimeConfigs(ctx context.Context, req *pb.SubmitUpdateFunctionInstanceRuntimeConfigsReq) (*pb.SubmitFunctionJobRsp, error) {
	spaceID, err := spacecontext.MustFromContext(ctx)
	if err != nil {
		return &pb.SubmitFunctionJobRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	if req == nil {
		return &pb.SubmitFunctionJobRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "request is required")}, nil
	}
	items := req.GetInstances()
	if ret := validateFunctionJobSize(len(items), "instances"); ret != nil {
		return &pb.SubmitFunctionJobRsp{RetInfo: ret}, nil
	}
	jobID := "function-job-" + uuid.NewString()
	creates := make([]store.FunctionJobItemCreate, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for index, item := range items {
		if ret := s.preflightRuntimeConfig(ctx, spaceID, item); ret != nil {
			return &pb.SubmitFunctionJobRsp{RetInfo: ret}, nil
		}
		instanceID := strings.TrimSpace(item.GetInstanceId())
		if _, ok := seen[instanceID]; ok {
			return &pb.SubmitFunctionJobRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "instances contains duplicate instance_id")}, nil
		}
		seen[instanceID] = struct{}{}
		lifecycleID, err := s.catalog.InstanceLifecycleID(ctx, spaceID, instanceID)
		if err != nil {
			return &pb.SubmitFunctionJobRsp{RetInfo: retFromError(err)}, nil
		}
		itemID := fmt.Sprintf("%s-%03d", jobID, index)
		item.LifecycleId = lifecycleID
		item.OperationId = itemID
		rawBytes, err := protojson.Marshal(item)
		if err != nil {
			return &pb.SubmitFunctionJobRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "invalid runtime config request")}, nil
		}
		creates = append(creates, store.FunctionJobItemCreate{ItemID: itemID, ItemIndex: index, InstanceID: instanceID, RequestJSON: string(rawBytes)})
	}
	if err := s.catalog.CreateFunctionJob(ctx, store.FunctionJobCreate{SpaceID: spaceID, JobID: jobID, Operation: functionJobOperationRuntimeConfig, Items: creates}); err != nil {
		return &pb.SubmitFunctionJobRsp{RetInfo: retFromError(err)}, nil
	}
	return &pb.SubmitFunctionJobRsp{RetInfo: retOK(), JobId: jobID, Operation: pb.FunctionJobOperation_FUNCTION_JOB_OPERATION_UPDATE_RUNTIME_CONFIGS, TotalCount: int32(len(creates))}, nil
}

func (s *Service) preflightRuntimeConfig(ctx context.Context, spaceID string, item *pb.FunctionInstanceRuntimeConfigPatch) *pb.RetInfo {
	if item == nil || strings.TrimSpace(item.GetInstanceId()) == "" {
		return retErr(pb.ErrorCode_INVALID_PARAM, "instance_id is required")
	}
	instance, err := s.catalog.GetInstance(ctx, spaceID, item.GetInstanceId())
	if err != nil {
		return retFromError(err)
	}
	if instance == nil {
		return retErr(pb.ErrorCode_NOT_FOUND, "instance not found")
	}
	if instance.PackageID != "" {
		pkg, packageErr := s.catalog.GetPackage(ctx, spaceID, instance.PackageID)
		if packageErr != nil {
			return retFromError(packageErr)
		}
		if ret := validateCollectorPublishLeaseAtSubmit(pkg, item.GetCollectorPublishLeaseId(), item.GetCollectorPublishFencingToken()); ret != nil {
			return ret
		}
	}
	if instance.FunctionType != "scf-event" || instance.TriggerType != "timer" {
		return retErr(pb.ErrorCode_INVALID_PARAM, "runtime config patch requires scf-event timer instance")
	}
	if strings.TrimSpace(item.GetTimerCron()) == "" {
		return retErr(pb.ErrorCode_INVALID_PARAM, "timer_cron is required")
	}
	if !isSupportedTimerCron(item.GetTimerCron()) {
		return retErr(pb.ErrorCode_INVALID_PARAM, "timer_cron is not supported")
	}
	for key := range item.GetManagedEnvironment() {
		if _, ok := managedEnvironmentKeys[key]; !ok {
			return retErr(pb.ErrorCode_INVALID_PARAM, "environment key is not managed: "+key)
		}
	}
	if _, err := assignmentRuntimeMetadata(item.GetManagedEnvironment()); err != nil {
		return retErr(pb.ErrorCode_INVALID_PARAM, err.Error())
	}
	if value, ok := item.GetManagedEnvironment()["MOOX_FETCH_TIMEOUT_SECONDS"]; ok && value != strconv.Itoa(tencent.CollectorTimerTimeoutSeconds) {
		return retErr(pb.ErrorCode_INVALID_PARAM, "Timer runtime timeout must be 60 seconds")
	}
	if item.GetTimerEnabled() && item.GetManagedEnvironment()["MOOX_FETCH_TIMEOUT_SECONDS"] != strconv.Itoa(tencent.CollectorTimerTimeoutSeconds) {
		return retErr(pb.ErrorCode_INVALID_PARAM, "enabled Timer runtime config requires MOOX_FETCH_TIMEOUT_SECONDS=60")
	}
	return nil
}

func isSupportedTimerCron(cron string) bool {
	fields := strings.Fields(cron)
	if len(fields) != 7 {
		return false
	}
	second, err := strconv.Atoi(fields[0])
	if err != nil || second < 0 || second > 59 {
		return false
	}
	// Hourly market-fetch shards may use a deterministic minute offset to
	// avoid every SCF invoking at :00. Keep the cadence constrained to one
	// invocation per hour; only the minute field may be a literal 0..59.
	if fields[1] != "*" {
		minute, minuteErr := strconv.Atoi(fields[1])
		if minuteErr == nil && minute >= 0 && minute <= 59 && strings.Join(fields[2:], " ") == "* * * * *" {
			return true
		}
	}
	fields[0] = "0"
	switch strings.Join(fields, " ") {
	case "0 * * * * * *", "0 */5 * * * * *", "0 */15 * * * * *", "0 */30 * * * * *", "0 0 * * * * *", "0 0 */4 * * * *", "0 0 0 * * * *":
		return true
	default:
		return false
	}
}

func (s *Service) executeRuntimeConfigItem(ctx context.Context, spaceID string, item *pb.FunctionInstanceRuntimeConfigPatch) (string, error) {
	instance, err := s.catalog.GetInstance(ctx, spaceID, item.GetInstanceId())
	if err != nil || instance == nil {
		if err != nil {
			return "", err
		}
		return "", fmt.Errorf("instance not found: %s", item.GetInstanceId())
	}
	durable := isFunctionJobDurableOperation(ctx)
	retainClaim := durable
	if durable {
		if strings.TrimSpace(item.GetLifecycleId()) == "" || strings.TrimSpace(item.GetOperationId()) == "" {
			return "", fmt.Errorf("runtime config item lifecycle identity is missing; resubmit the update")
		}
		if instance.LifecycleID != item.GetLifecycleId() {
			return "", store.ErrInstanceLifecycleMismatch
		}
		if err := s.catalog.ClaimInstanceMutation(ctx, spaceID, instance.InstanceID, item.GetLifecycleId(), item.GetOperationId()); err != nil {
			return "", err
		}
		defer func() {
			if !retainClaim {
				_ = s.catalog.ReleaseInstanceMutationClaim(context.WithoutCancel(ctx), spaceID, instance.InstanceID, item.GetLifecycleId(), item.GetOperationId())
			}
		}()
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
	unlock := lockSCFFunction(ref)
	defer unlock()
	info, err := client.GetFunction(ctx, ref)
	if err != nil {
		return "", fmt.Errorf("get scf function %s: %w", ref.FunctionName, err)
	}
	environment := copyStringMap(info.Environment)
	if environment == nil {
		environment = make(map[string]string)
	}
	needsEnvironmentUpdate := !managedEnvironmentMatches(environment, item.GetManagedEnvironment())
	for key, value := range item.GetManagedEnvironment() {
		environment[key] = value
	}
	// 缺少日志上报配置的采集函数（例如日志服务开通之前创建的）在这次更新里补齐。
	// 停用定时器的更新不顺带开通日志服务：停用必须能在任何情况下完成。
	if item.GetTimerEnabled() {
		needsEnvironmentUpdate = s.applyCLSEnvironment(ctx, *account, instance, environment) || needsEnvironmentUpdate
	}
	if item.GetTimerEnabled() {
		if environment["MOOX_FETCH_TIMEOUT_SECONDS"] != strconv.Itoa(tencent.CollectorTimerTimeoutSeconds) {
			return "", fmt.Errorf("scf function %s Timer runtime environment requires MOOX_FETCH_TIMEOUT_SECONDS=60", ref.FunctionName)
		}
		if err := tencent.ValidateCollectorTimerEnvironment(environment); err != nil {
			return "", fmt.Errorf("scf function %s Timer runtime environment: %w", ref.FunctionName, err)
		}
	}
	if err := tencent.ValidateSCFEnvironment(environment); err != nil {
		return "", fmt.Errorf("scf function %s %w", ref.FunctionName, err)
	}
	verified := info
	providerMutationStarted := false
	mutationError := func(err error) error {
		if err != nil && (providerMutationStarted || isAmbiguousSCFProviderOutcome(err)) {
			retainClaim = true
			return fmt.Errorf("%w: runtime config update for %s: %w", errInstanceMutationReconciliationRequired, instance.InstanceID, err)
		}
		return err
	}
	timeout := int64(0)
	if environment["MOOX_FETCH_TIMEOUT_SECONDS"] == strconv.Itoa(tencent.CollectorTimerTimeoutSeconds) {
		timeout = tencent.CollectorTimerTimeoutSeconds
	}
	if needsEnvironmentUpdate || (timeout > 0 && info.Timeout != timeout) {
		if _, err := client.UpdateFunctionConfiguration(ctx, tencentscf.UpdateFunctionConfigurationRequest{FunctionRef: ref, Environment: environment, Timeout: timeout}); err != nil {
			return "", mutationError(fmt.Errorf("update scf function %s environment: %w", ref.FunctionName, err))
		}
		providerMutationStarted = true
		if _, err := waitForSCFActive(ctx, client, ref, nil); err != nil {
			return "", mutationError(err)
		}
		verified, err = client.GetFunction(ctx, ref)
		if err != nil {
			return "", mutationError(fmt.Errorf("verify scf function %s environment: %w", ref.FunctionName, err))
		}
	}
	if timeout > 0 && verified.Timeout != timeout {
		return "", mutationError(fmt.Errorf("scf function %s Timer timeout did not verify", ref.FunctionName))
	}
	for key, expected := range item.GetManagedEnvironment() {
		if verified.Environment[key] != expected {
			return "", mutationError(fmt.Errorf("scf function %s environment %q did not verify", ref.FunctionName, key))
		}
	}
	trigger, err := client.EnsureTimerTrigger(ctx, tencentscf.TimerTriggerRequest{FunctionRef: ref, Name: timerTriggerName, Cron: item.GetTimerCron(), Enabled: item.GetTimerEnabled(), Qualifier: timerTriggerQualifier, Message: timerTriggerMessage})
	if err != nil {
		return "", mutationError(fmt.Errorf("ensure timer trigger for %s: %w", ref.FunctionName, err))
	}
	providerMutationStarted = true
	metadata := map[string]any{"access_address": environment["MOOX_ACCESS_ADDRESS"], "access_id": environment["MOOX_ACCESS_ID"], "access_caller": environment["MOOX_CALLER"], "access_key_id": environment["MOOX_CALLER_KEY_ID"], "fetch_timeout_seconds": environment["MOOX_FETCH_TIMEOUT_SECONDS"], "dns_hash": environment["MOOX_MARKET_FETCH_DNS_HASH"], "dns_updated_at": environment["MOOX_MARKET_FETCH_DNS_UPDATED_AT"], "timer_trigger_name": timerTriggerName, "timer_cron": item.GetTimerCron(), "timer_enabled": item.GetTimerEnabled(), "timer_actual_type": trigger.Type, "timer_actual_enabled": trigger.Enabled, "timer_actual_cron": trigger.Cron, "timer_actual_qualifier": trigger.Qualifier, "timer_actual_message": trigger.Message, "timer_available_status": trigger.AvailableStatus, "timer_status_error": nil, "managed_environment_budget_bytes": scfManagedEnvironmentBudget(verified.Environment), "runtime_config_reconciled_at": time.Now().UTC().Format(time.RFC3339Nano)}
	assignmentMetadata, err := assignmentRuntimeMetadata(item.GetManagedEnvironment())
	if err != nil {
		return "", mutationError(err)
	}
	for key, value := range assignmentMetadata {
		metadata[key] = value
	}
	if timeout > 0 {
		metadata["timeout_seconds"] = timeout
	}
	if err := s.catalog.UpdateInstanceRuntimeMetadata(ctx, spaceID, instance.InstanceID, metadata); err != nil {
		return "", mutationError(err)
	}
	if !durable {
		retainClaim = false
	}
	return fmt.Sprintf("updated runtime config and timer for %s", instance.InstanceID), nil
}

func assignmentRuntimeMetadata(managed map[string]string) (map[string]any, error) {
	hash, hasHash := managed["MOOX_MARKET_FETCH_ASSIGNMENT_HASH"]
	rawCount, hasCount := managed["MOOX_MARKET_FETCH_SUBJECT_COUNT"]
	if !hasHash && !hasCount {
		return nil, nil
	}
	if !hasHash || strings.TrimSpace(hash) == "" || !hasCount {
		return nil, fmt.Errorf("assignment runtime config requires assignment hash and subject count")
	}
	count, err := strconv.Atoi(rawCount)
	if err != nil || count < 0 {
		return nil, fmt.Errorf("assignment subject count must be a non-negative integer")
	}
	metadata := map[string]any{"assignment_hash": hash, "assignment_count": count, "binding_hash": nil}
	if bindingHash, ok := managed["MOOX_MARKET_FETCH_BINDING_HASH"]; ok && strings.TrimSpace(bindingHash) != "" {
		metadata["binding_hash"] = bindingHash
	}
	return metadata, nil
}

func scfEnvironmentBytes(values map[string]string) int {
	return tencent.SCFEnvironmentBytes(values)
}

func scfManagedEnvironmentBudget(values map[string]string) int {
	base := make(map[string]string, len(values))
	for key, value := range values {
		if _, managed := managedEnvironmentKeys[key]; managed {
			continue
		}
		base[key] = value
	}
	return maxSCFEnvironmentBytes - scfEnvironmentBytes(base)
}

func managedEnvironmentMatches(current, desired map[string]string) bool {
	for key, expected := range desired {
		if actual, ok := current[key]; !ok || actual != expected {
			return false
		}
	}
	return true
}
