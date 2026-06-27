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
		if serverIP != "127.0.0.1" || serverPort != 11000 {
			t.Fatalf("server before heartbeat = %s:%d, want 127.0.0.1:11000", serverIP, serverPort)
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
		ServerPort: 11000,
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
		if serverIP != "127.0.0.1" || serverPort != 11000 {
			t.Fatalf("server before execute = %s:%d, want 127.0.0.1:11000", serverIP, serverPort)
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
		ServerPort: 11000,
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
		ServerPort: 11000,
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
	if serverIP != "10.0.0.8" || serverPort != 11000 {
		t.Fatalf("server = %s:%d, want 10.0.0.8:11000", serverIP, serverPort)
	}
	nodeID, _ := config.GetNodeInfo()
	if nodeID != "scf-task-node" {
		t.Fatalf("node id = %q, want scf-task-node", nodeID)
	}
}

func TestParseServerFromMooxURL(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		wantIP  string
		wantPort int
		wantOK  bool
	}{
		{"normal", "http://106.53.107.122:11000", "106.53.107.122", 11000, true},
		{"https", "https://10.0.0.8:443", "10.0.0.8", 443, true},
		{"empty", "", "", 0, false},
		{"no_port", "http://10.0.0.8", "", 0, false},
		{"invalid_port", "http://10.0.0.8:abc", "", 0, false},
		{"no_host", "http://:11000", "", 0, false},
		{"garbage", "not-a-url", "", 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ip, port, ok := parseServerFromMooxURL(c.url)
			if ip != c.wantIP || port != c.wantPort || ok != c.wantOK {
				t.Fatalf("parseServerFromMooxURL(%q) = %s,%d,%v, want %s,%d,%v",
					c.url, ip, port, ok, c.wantIP, c.wantPort, c.wantOK)
			}
		})
	}
}

// TestHandleKeepaliveProbeRecoversServerInfoFromMooxURL 覆盖历史故障场景：
// 控制面 keepalive 事件只下发 moox_server_url 而未带 server_ip/server_port，
// SCF 冷启动后应从 moox_server_url 解析出控制面地址，恢复 ServerInfo 并完成心跳上报。
func TestHandleKeepaliveProbeRecoversServerInfoFromMooxURL(t *testing.T) {
	oldReporter := reportHeartbeatAfterProbe
	oldGlobalConfig := config.GlobalConfig
	t.Cleanup(func() {
		reportHeartbeatAfterProbe = oldReporter
		config.GlobalConfig = oldGlobalConfig
	})

	reported := false
	reportHeartbeatAfterProbe = func(ctx context.Context) error {
		reported = true
		serverIP, serverPort := config.GetServerInfo()
		if serverIP != "106.53.107.122" || serverPort != 11000 {
			t.Fatalf("server recovered from moox_server_url = %s:%d, want 106.53.107.122:11000", serverIP, serverPort)
		}
		return nil
	}

	ctx := functioncontext.NewContext(context.Background(), &functioncontext.FunctionContext{
		FunctionName: "scf-coldstart-node",
	})
	// 模拟冷启动后 ServerInfo 为空：仅带 moox_server_url，不带 server_ip/server_port。
	// 走 HandleRequest 全链路，使 applyRuntimeConfig 的 fallback 生效。
	event := model.CloudFunctionEvent{
		Action:        model.EventActionKeepalive,
		Source:        "keepalive_probe",
		MooxServerURL: "http://106.53.107.122:11000",
		RequestID:     "request-coldstart",
		Data: map[string]interface{}{
			"node_id": "scf-coldstart-node",
		},
	}
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := NewCloudFunctionHandler().HandleRequest(ctx, raw)
	if err != nil {
		t.Fatalf("HandleRequest returned error: %v", err)
	}
	if resp == nil {
		t.Fatalf("HandleRequest response is nil")
	}
	r, ok := resp.(*model.Response)
	if !ok || !r.Success {
		t.Fatalf("HandleRequest response = %#v, want success", resp)
	}
	if !reported {
		t.Fatalf("expected keepalive probe to report heartbeat after recovering server info from moox_server_url")
	}
}

// TestApplyRuntimeConfigPrefersServerIPOverMooxURL 验证 server_ip/server_port 优先于 moox_server_url，
// 两者同时存在时以显式字段为准。
func TestApplyRuntimeConfigPrefersServerIPOverMooxURL(t *testing.T) {
	oldGlobalConfig := config.GlobalConfig
	t.Cleanup(func() {
		config.GlobalConfig = oldGlobalConfig
	})

	h := NewCloudFunctionHandler()
	h.applyRuntimeConfig(context.Background(), model.CloudFunctionEvent{
		ServerIP:      "10.0.0.8",
		ServerPort:    11000,
		MooxServerURL: "http://106.53.107.122:9999",
	}, nil)

	ip, port := config.GetServerInfo()
	if ip != "10.0.0.8" || port != 11000 {
		t.Fatalf("server = %s:%d, want 10.0.0.8:11000 (server_ip should win)", ip, port)
	}
}
