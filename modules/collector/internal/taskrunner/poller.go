// Package taskrunner adapts CloudNode work items to collector workloads.
package taskrunner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	runtimeapp "github.com/mooyang-code/moox/modules/collector/internal/app/runtime"
	"github.com/mooyang-code/moox/modules/collector/internal/executor"
	"github.com/mooyang-code/moox/modules/collector/internal/model"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/binance"
	nodeRuntime "github.com/mooyang-code/moox/packages/cloudruntime"
	"trpc.group/trpc-go/trpc-go/log"
)

// PollAndExecuteWorkItems leases CloudNode work items and executes them in the current runtime.
func PollAndExecuteWorkItems(ctx context.Context) error {
	serverIP, serverPort := runtimeapp.GetServerInfo()
	nodeID, _ := runtimeapp.GetNodeInfo()
	if serverIP == "" || serverPort <= 0 || nodeID == "" {
		log.DebugContextf(ctx, "[CloudRuntime] skip poll work items server=%s:%d node_id=%s", serverIP, serverPort, nodeID)
		return nil
	}
	spaceID := runtimeSpaceID()
	if spaceID == "" {
		log.DebugContextf(ctx, "[CloudRuntime] skip poll work items: space_id is empty")
		return nil
	}
	auth := runtimeapp.GetServiceAuthConfig()
	return nodeRuntime.PollAndExecuteWorkItems(ctx, nodeRuntime.Config{
		ServerIP:   serverIP,
		ServerPort: serverPort,
		SpaceID:    spaceID,
		NodeID:     nodeID,
		SupportedWorkloads: []string{
			"collector.binance.spot.kline",
			"collector.binance.swap.kline",
			"collector.binance.spot.symbol",
			"collector.binance.swap.symbol",
		},
		Limit: 8,
		Auth: nodeRuntime.AuthConfig{
			Version:   auth.Version,
			AccessKey: auth.AccessKey,
			SecretKey: auth.SecretKey,
			ExpireSec: auth.ExpireSec,
		},
	}, executeCollectorWorkItem)
}

func runtimeSpaceID() string {
	if value := strings.TrimSpace(os.Getenv("MOOX_SPACE_ID")); value != "" {
		return value
	}
	binding, err := binance.ResolveStorageBinding(binance.InstTypeSPOT)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(binding.SpaceID)
}

func executeCollectorWorkItem(ctx context.Context, item nodeRuntime.WorkItemLease) (string, error) {
	taskEvent, err := taskEventFromWorkItem(item)
	if err != nil {
		return "", err
	}
	return executor.ExecuteTaskImmediately(ctx, taskEvent)
}

func taskEventFromWorkItem(item nodeRuntime.WorkItemLease) (*model.TaskExecuteEvent, error) {
	var payload map[string]any
	if err := json.Unmarshal([]byte(item.Payload), &payload); err != nil {
		return nil, fmt.Errorf("parse work item payload: %w", err)
	}
	taskID := firstString(item.OwnerRef, stringValue(payload, "task_id"))
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
