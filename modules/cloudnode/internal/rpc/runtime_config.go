package rpc

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	tencentscf "github.com/mooyang-code/moox/modules/cloudnode/internal/providers/tencentscf"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/spacecontext"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/store"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"google.golang.org/protobuf/encoding/protojson"
)

const (
	timerTriggerName       = "moox-market-fetch-timer"
	timerTriggerMessage    = "market_fetch_timer_v1"
	timerTriggerQualifier  = "$LATEST"
	maxSCFEnvironmentBytes = 4096
)

var managedEnvironmentKeys = map[string]struct{}{
	"MOOX_MARKET_FETCH_PROVIDER":        {},
	"MOOX_MARKET_FETCH_MARKET_TYPE":     {},
	"MOOX_MARKET_FETCH_DATASET_ID":      {},
	"MOOX_MARKET_FETCH_FREQUENCY":       {},
	"MOOX_MARKET_FETCH_SUBJECTS":        {},
	"MOOX_MARKET_FETCH_SYMBOLS_JSON":    {},
	"MOOX_MARKET_FETCH_ASSIGNMENT_HASH": {},
	"MOOX_MARKET_FETCH_DNS_ROUTES_JSON": {},
	"MOOX_MARKET_FETCH_DNS_HASH":        {},
	"MOOX_MARKET_FETCH_DNS_UPDATED_AT":  {},
	"MOOX_MARKET_FETCH_PROVIDER_CHAIN":  {},
	"MOOX_MARKET_FETCH_ROUTE_VERSION":   {},
	"MOOX_MARKET_FETCH_GROUP_ID":        {},
}

func (s *Service) SubmitUpdateNodeRuntimeConfigs(ctx context.Context, req *pb.BatchUpdateNodeRuntimeConfigsReq) (*pb.SubmitNodeBatchRsp, error) {
	spaceID, err := spacecontext.MustFromContext(ctx)
	if err != nil {
		return &pb.SubmitNodeBatchRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	if req == nil {
		return &pb.SubmitNodeBatchRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "request is required")}, nil
	}
	items := req.GetNodes()
	if ret := validateNodeBatchSize(len(items), "nodes"); ret != nil {
		return &pb.SubmitNodeBatchRsp{RetInfo: ret}, nil
	}
	jobID := "node-batch-" + uuid.NewString()
	creates := make([]store.NodeBatchItemCreate, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for index, item := range items {
		if ret := s.preflightRuntimeConfig(ctx, spaceID, item); ret != nil {
			return &pb.SubmitNodeBatchRsp{RetInfo: ret}, nil
		}
		nodeID := strings.TrimSpace(item.GetNodeId())
		if _, ok := seen[nodeID]; ok {
			return &pb.SubmitNodeBatchRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "nodes contains duplicate node_id")}, nil
		}
		seen[nodeID] = struct{}{}
		rawBytes, err := protojson.Marshal(item)
		if err != nil {
			return &pb.SubmitNodeBatchRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "invalid runtime config request")}, nil
		}
		creates = append(creates, store.NodeBatchItemCreate{ItemID: fmt.Sprintf("%s-%03d", jobID, index), ItemIndex: index, NodeID: nodeID, RequestJSON: string(rawBytes)})
	}
	if err := s.catalog.CreateNodeBatch(ctx, store.NodeBatchCreate{SpaceID: spaceID, JobID: jobID, Operation: nodeBatchOperationRuntimeConfig, Items: creates}); err != nil {
		return &pb.SubmitNodeBatchRsp{RetInfo: retFromError(err)}, nil
	}
	return &pb.SubmitNodeBatchRsp{RetInfo: retOK(), JobId: jobID, Operation: pb.NodeBatchOperation_NODE_BATCH_OPERATION_UPDATE_RUNTIME_CONFIGS, TotalCount: int32(len(creates))}, nil
}

func (s *Service) preflightRuntimeConfig(ctx context.Context, spaceID string, item *pb.NodeRuntimeConfigPatch) *pb.RetInfo {
	if item == nil || strings.TrimSpace(item.GetNodeId()) == "" {
		return retErr(pb.ErrorCode_INVALID_PARAM, "node_id is required")
	}
	node, err := s.catalog.GetNode(ctx, spaceID, item.GetNodeId())
	if err != nil {
		return retFromError(err)
	}
	if node == nil {
		return retErr(pb.ErrorCode_NOT_FOUND, "node not found")
	}
	if node.NodeType != "scf-event" || node.TriggerType != "timer" {
		return retErr(pb.ErrorCode_INVALID_PARAM, "runtime config patch requires scf-event timer node")
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
	fields[0] = "0"
	switch strings.Join(fields, " ") {
	case "0 * * * * * *", "0 */5 * * * * *", "0 */15 * * * * *", "0 */30 * * * * *", "0 0 * * * * *", "0 0 */4 * * * *", "0 0 0 * * * *":
		return true
	default:
		return false
	}
}

func (s *Service) executeRuntimeConfigItem(ctx context.Context, spaceID string, item *pb.NodeRuntimeConfigPatch) (string, error) {
	node, err := s.catalog.GetNode(ctx, spaceID, item.GetNodeId())
	if err != nil || node == nil {
		if err != nil {
			return "", err
		}
		return "", fmt.Errorf("node not found: %s", item.GetNodeId())
	}
	account, err := s.catalog.GetAccount(ctx, node.CloudAccountID)
	if err != nil || account == nil {
		if err != nil {
			return "", err
		}
		return "", fmt.Errorf("cloud account unavailable for node %s", node.NodeID)
	}
	client, err := s.scfClient(ctx, *account)
	if err != nil {
		return "", err
	}
	ref := tencentscf.FunctionRef{Region: node.Region, FunctionName: firstString(node.FunctionName, node.NodeID), Namespace: firstString(node.Namespace, "default")}
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
	if size := scfEnvironmentBytes(environment); size > maxSCFEnvironmentBytes {
		return "", fmt.Errorf("scf function %s environment is %d bytes; limit is %d", ref.FunctionName, size, maxSCFEnvironmentBytes)
	}
	verified := info
	if needsEnvironmentUpdate {
		if _, err := client.UpdateFunctionConfiguration(ctx, tencentscf.UpdateFunctionConfigurationRequest{FunctionRef: ref, Environment: environment}); err != nil {
			return "", fmt.Errorf("update scf function %s environment: %w", ref.FunctionName, err)
		}
		if _, err := waitForSCFActive(ctx, client, ref, nil); err != nil {
			return "", err
		}
		verified, err = client.GetFunction(ctx, ref)
		if err != nil {
			return "", fmt.Errorf("verify scf function %s environment: %w", ref.FunctionName, err)
		}
	}
	for key, expected := range item.GetManagedEnvironment() {
		if verified.Environment[key] != expected {
			return "", fmt.Errorf("scf function %s environment %q did not verify", ref.FunctionName, key)
		}
	}
	trigger, err := client.EnsureTimerTrigger(ctx, tencentscf.TimerTriggerRequest{FunctionRef: ref, Name: timerTriggerName, Cron: item.GetTimerCron(), Enabled: item.GetTimerEnabled(), Qualifier: timerTriggerQualifier, Message: timerTriggerMessage})
	if err != nil {
		return "", fmt.Errorf("ensure timer trigger for %s: %w", ref.FunctionName, err)
	}
	metadata := map[string]any{"assignment_hash": environment["MOOX_MARKET_FETCH_ASSIGNMENT_HASH"], "assignment_count": strings.Count(environment["MOOX_MARKET_FETCH_SUBJECTS"], "|") + boolToInt(environment["MOOX_MARKET_FETCH_SUBJECTS"] != ""), "dns_hash": environment["MOOX_MARKET_FETCH_DNS_HASH"], "dns_updated_at": environment["MOOX_MARKET_FETCH_DNS_UPDATED_AT"], "timer_trigger_name": timerTriggerName, "timer_cron": item.GetTimerCron(), "timer_enabled": item.GetTimerEnabled(), "timer_actual_type": trigger.Type, "timer_actual_enabled": trigger.Enabled, "timer_actual_cron": trigger.Cron, "timer_actual_qualifier": trigger.Qualifier, "timer_actual_message": trigger.Message, "timer_available_status": trigger.AvailableStatus, "timer_status_error": nil, "managed_environment_budget_bytes": scfManagedEnvironmentBudget(verified.Environment), "runtime_config_reconciled_at": time.Now().UTC().Format(time.RFC3339Nano)}
	if err := s.catalog.UpdateNodeRuntimeMetadata(ctx, spaceID, node.NodeID, metadata); err != nil {
		return "", err
	}
	return fmt.Sprintf("updated runtime config and timer for %s", node.NodeID), nil
}

func scfEnvironmentBytes(values map[string]string) int {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	total := 0
	for _, key := range keys {
		total += len(key) + 1 + len(values[key]) + 1
	}
	return total
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

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
