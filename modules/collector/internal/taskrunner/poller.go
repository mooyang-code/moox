// Package taskrunner adapts CloudNode JobItems to collector workloads.
package taskrunner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	runtimeapp "github.com/mooyang-code/moox/modules/collector/internal/app/runtime"
	"github.com/mooyang-code/moox/modules/collector/internal/executor"
	"github.com/mooyang-code/moox/modules/collector/internal/jobs"
	"github.com/mooyang-code/moox/modules/collector/internal/model"
	nodeRuntime "github.com/mooyang-code/moox/packages/cloudruntime"
	"trpc.group/trpc-go/trpc-go/log"
)

var registerHandlersOnce sync.Once

// PollAndExecuteJobItems polls CloudNode JobItems and executes them in the current runtime.
func PollAndExecuteJobItems(ctx context.Context) error {
	serviceGatewayTarget := runtimeapp.GetServiceGatewayTarget()
	nodeID, _ := runtimeapp.GetNodeInfo()
	if serviceGatewayTarget == "" || nodeID == "" {
		log.DebugContextf(ctx, "[CloudRuntime] skip poll job items service_gateway_target=%s node_id=%s", serviceGatewayTarget, nodeID)
		return nil
	}
	spaceID := runtimeSpaceID()
	if spaceID == "" {
		return fmt.Errorf("MOOX_SPACE_ID is required for collector SCF runtime")
	}
	auth := runtimeapp.GetServiceAuthConfig()
	registerCollectorHandlers()
	return nodeRuntime.Run(ctx, nodeRuntime.Config{
		ServiceGatewayTarget: serviceGatewayTarget,
		SpaceID:              spaceID,
		NodeID:               nodeID,
		SupportedJobTypes: []string{
			jobs.JobTypeCollectKline,
			jobs.JobTypeCollectSymbol,
		},
		// A Market JobItem owns one logical shard and its quota/resolve leases.
		// Processing more than one in one SCF invocation violates that boundary.
		Limit: 1,
		Auth: nodeRuntime.AuthConfig{
			Version:   auth.Version,
			AccessKey: auth.AccessKey,
			SecretKey: auth.SecretKey,
			ExpireSec: auth.ExpireSec,
		},
	})
}

func registerCollectorHandlers() {
	registerHandlersOnce.Do(func() {
		nodeRuntime.Register(jobs.JobTypeCollectKline, nodeRuntime.HandlerFunc(executeCollectorJobItem))
		nodeRuntime.Register(jobs.JobTypeCollectSymbol, nodeRuntime.HandlerFunc(executeCollectorJobItem))
	})
}

func runtimeSpaceID() string {
	return strings.TrimSpace(os.Getenv("MOOX_SPACE_ID"))
}

func executeCollectorJobItem(ctx context.Context, item nodeRuntime.JobItem) (nodeRuntime.Result, error) {
	if strings.TrimSpace(stringValue(item.Params, "market_id")) != "" {
		return executeMarketKlineJobItem(ctx, item)
	}
	taskEvent, err := taskEventFromJobItem(item)
	if err != nil {
		return nodeRuntime.Result{}, nodeRuntime.Permanent(err, "INVALID_JOB_ITEM")
	}
	result, err := executor.ExecuteTaskImmediately(ctx, taskEvent)
	summary := map[string]any{}
	if strings.TrimSpace(result) != "" {
		_ = json.Unmarshal([]byte(result), &summary)
	}
	if err != nil {
		return nodeRuntime.Result{Summary: summary}, nodeRuntime.Retryable(err, "COLLECT_FAILED")
	}
	return nodeRuntime.Result{Summary: summary}, nil
}

func taskEventFromJobItem(item nodeRuntime.JobItem) (*model.TaskExecuteEvent, error) {
	payload := item.Params
	taskID := firstString(stringValue(payload, "task_id"), item.JobItemID)
	dataType := strings.ToLower(firstString(stringValue(payload, "data_type"), "kline"))
	market := strings.ToLower(firstString(stringValue(payload, "market"), "spot"))
	symbol := stringValue(payload, "symbol")
	subjectID := firstString(stringValue(payload, "subject_id"), symbol)
	interval := stringValue(payload, "interval")
	if taskID == "" {
		return nil, fmt.Errorf("task_id is required")
	}
	if dataType != "symbol" && symbol == "" {
		return nil, fmt.Errorf("symbol is required")
	}
	if dataType != "symbol" && interval == "" {
		interval = "1m"
	}
	intervals := []string{interval}
	if dataType == "symbol" {
		intervals = []string{""}
	}
	return &model.TaskExecuteEvent{
		SpaceID:    stringValue(payload, "space_id"),
		TaskID:     taskID,
		DataType:   dataType,
		DataSource: firstString(stringValue(payload, "exchange"), "binance"),
		Market:     market,
		InstType:   strings.ToUpper(market),
		SubjectID:  subjectID,
		Symbol:     symbol,
		Intervals:  intervals,
		Immediate:  true,
	}, nil
}

func stringValue(payload map[string]any, key string) string {
	if value, ok := payload[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func firstString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
