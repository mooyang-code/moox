package cloudfunction

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/pkg/config"
	"github.com/mooyang-code/moox/modules/collector/pkg/model"
	"github.com/tencentyun/scf-go-lib/functioncontext"
)

func TestHandleKeepaliveProbeReportsHeartbeatAfterProcessProbe(t *testing.T) {
	oldReporter := reportHeartbeatAfterProbe
	oldGlobalConfig := config.GlobalConfig
	t.Cleanup(func() {
		reportHeartbeatAfterProbe = oldReporter
		config.GlobalConfig = oldGlobalConfig
	})

	reported := false
	reportHeartbeatAfterProbe = func(ctx context.Context) error {
		reported = true
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatalf("heartbeat context has no deadline")
		}
		if remaining := time.Until(deadline); remaining <= 0 || remaining > keepaliveHeartbeatTimeout {
			t.Fatalf("heartbeat context deadline remaining = %s, want within %s", remaining, keepaliveHeartbeatTimeout)
		}
		nodeID, _ := config.GetNodeInfo()
		if nodeID != "scf-test" {
			t.Fatalf("node id before heartbeat = %q, want scf-test", nodeID)
		}
		serverIP, serverPort := config.GetServerInfo()
		if serverIP != "127.0.0.1" || serverPort != 20103 {
			t.Fatalf("server before heartbeat = %s:%d, want 127.0.0.1:20103", serverIP, serverPort)
		}
		return nil
	}

	ctx := functioncontext.NewContext(context.Background(), &functioncontext.FunctionContext{
		FunctionName:       "scf-test",
		FunctionVersion:    "$LATEST",
		TencentcloudRegion: "ap-guangzhou",
		Namespace:          "ap-guangzhou-test",
		RequestID:          "request-test",
	})
	event := model.CloudFunctionEvent{
		Action:     model.EventActionKeepalive,
		Source:     "keepalive_probe",
		ServerIP:   "127.0.0.1",
		ServerPort: 20103,
		RequestID:  "request-test",
		Data: map[string]interface{}{
			"node_id": "scf-test",
		},
	}

	resp, err := NewCloudFunctionHandler().handleKeepalive(ctx, event)
	if err != nil {
		t.Fatalf("handleKeepalive returned error: %v", err)
	}
	if resp == nil || !resp.Success {
		t.Fatalf("handleKeepalive response = %#v, want success", resp)
	}
	if !reported {
		t.Fatalf("expected keepalive probe to report heartbeat")
	}
}

func TestHandleKeepaliveProbeRunsDueTasksAfterHeartbeat(t *testing.T) {
	oldReporter := reportHeartbeatAfterProbe
	oldRunner := executeDueTasksAfterHeartbeat
	oldGlobalConfig := config.GlobalConfig
	t.Cleanup(func() {
		reportHeartbeatAfterProbe = oldReporter
		executeDueTasksAfterHeartbeat = oldRunner
		config.GlobalConfig = oldGlobalConfig
	})

	var calls []string
	reportHeartbeatAfterProbe = func(ctx context.Context) error {
		calls = append(calls, "heartbeat")
		return nil
	}
	executeDueTasksAfterHeartbeat = func(ctx context.Context) error {
		calls = append(calls, "execute")
		nodeID, _ := config.GetNodeInfo()
		if nodeID != "scf-test" {
			t.Fatalf("node id before execute = %q, want scf-test", nodeID)
		}
		serverIP, serverPort := config.GetServerInfo()
		if serverIP != "127.0.0.1" || serverPort != 20103 {
			t.Fatalf("server before execute = %s:%d, want 127.0.0.1:20103", serverIP, serverPort)
		}
		return nil
	}

	ctx := functioncontext.NewContext(context.Background(), &functioncontext.FunctionContext{
		FunctionName:       "scf-test",
		FunctionVersion:    "$LATEST",
		TencentcloudRegion: "ap-guangzhou",
		Namespace:          "ap-guangzhou-test",
		RequestID:          "request-test",
	})
	event := model.CloudFunctionEvent{
		Action:     model.EventActionKeepalive,
		Source:     "keepalive_probe",
		ServerIP:   "127.0.0.1",
		ServerPort: 20103,
		RequestID:  "request-test",
		Data: map[string]interface{}{
			"node_id": "scf-test",
		},
	}

	resp, err := NewCloudFunctionHandler().handleKeepalive(ctx, event)
	if err != nil {
		t.Fatalf("handleKeepalive returned error: %v", err)
	}
	if resp == nil || !resp.Success {
		t.Fatalf("handleKeepalive response = %#v, want success", resp)
	}
	if len(calls) != 2 || calls[0] != "heartbeat" || calls[1] != "execute" {
		t.Fatalf("calls = %v, want [heartbeat execute]", calls)
	}
}

func TestHandleRequestUpdatesRuntimeConfigForTaskEvent(t *testing.T) {
	oldGlobalConfig := config.GlobalConfig
	t.Cleanup(func() {
		config.GlobalConfig = oldGlobalConfig
	})

	ctx := functioncontext.NewContext(context.Background(), &functioncontext.FunctionContext{
		FunctionName:    "scf-task-node",
		FunctionVersion: "$LATEST",
		RequestID:       "request-task",
	})
	event := model.CloudFunctionEvent{
		Action:     model.EventAction("unknown"),
		ServerIP:   "10.0.0.8",
		ServerPort: 20103,
		RequestID:  "request-task",
		Data: map[string]interface{}{
			"task_id": "task-1",
		},
	}
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}

	_, _ = NewCloudFunctionHandler().HandleRequest(ctx, raw)

	serverIP, serverPort := config.GetServerInfo()
	if serverIP != "10.0.0.8" || serverPort != 20103 {
		t.Fatalf("server = %s:%d, want 10.0.0.8:20103", serverIP, serverPort)
	}
	nodeID, _ := config.GetNodeInfo()
	if nodeID != "scf-task-node" {
		t.Fatalf("node id = %q, want scf-task-node", nodeID)
	}
}
