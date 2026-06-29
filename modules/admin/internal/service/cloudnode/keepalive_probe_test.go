package cloudnode

import (
	"strconv"
	"testing"
)

// TestBuildKeepaliveEventDataCarriesServerFields 覆盖历史故障回归：
// keepalive 探测事件必须携带 server_ip/server_port 顶层字段，否则 SCF collector
// 冷启动后无法建立心跳通道，导致任务不下发、K线停采。
func TestBuildKeepaliveEventDataCarriesServerFields(t *testing.T) {
	const serverIP = "106.53.107.122"
	const serverPort = 11000
	const nodeID = "scfh2jq-DataCollector-Master-1782437625"

	event := buildKeepaliveEventData(serverIP, serverPort, nodeID)

	// server_ip / server_port 顶层字段必须存在且类型正确
	gotIP, ok := event["server_ip"].(string)
	if !ok || gotIP != serverIP {
		t.Fatalf("event[server_ip] = %v, want %q", event["server_ip"], serverIP)
	}
	gotPort, ok := event["server_port"].(int)
	if !ok || gotPort != serverPort {
		t.Fatalf("event[server_port] = %v, want %d", event["server_port"], serverPort)
	}

	// moox_server_url 应与 server_ip:server_port 一致（保留兼容字段）
	wantURL := "http://" + serverIP + ":" + strconv.Itoa(serverPort)
	gotURL, ok := event["moox_server_url"].(string)
	if !ok || gotURL != wantURL {
		t.Fatalf("event[moox_server_url] = %v, want %s", event["moox_server_url"], wantURL)
	}
	if _, ok := event["storage_server_rpc"]; ok {
		t.Fatalf("event[storage_server_rpc] should not be emitted; collector storage calls use direct HTTP RPC config")
	}

	// data.node_id 必须回传，SCF 侧用于识别目标节点
	data, ok := event["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("event[data] is not a map: %#v", event["data"])
	}
	if gotNodeID, _ := data["node_id"].(string); gotNodeID != nodeID {
		t.Fatalf("event[data][node_id] = %q, want %q", gotNodeID, nodeID)
	}
}

func TestApplyRuntimeDeploymentOverridesCarriesServiceDeployments(t *testing.T) {
	payload := buildKeepaliveEventData("127.0.0.1", 11000, "node-1")
	deployments := map[string]interface{}{
		"service_gateway": map[string]interface{}{"base_url": "http://106.53.107.122:11000"},
		"storage_access":  map[string]interface{}{"base_url": "http://106.53.107.122:20201"},
	}

	applyRuntimeDeploymentOverrides(payload, deployments)

	if got := payload["moox_server_url"]; got != "http://106.53.107.122:11000" {
		t.Fatalf("moox_server_url = %v, want service_gateway URL", got)
	}
	if got := payload["server_ip"]; got != "106.53.107.122" {
		t.Fatalf("server_ip = %v, want service_gateway host", got)
	}
	if got := payload["server_port"]; got != 11000 {
		t.Fatalf("server_port = %v, want service_gateway port", got)
	}
	if got := payload["storage_server_url"]; got != "http://106.53.107.122:20201" {
		t.Fatalf("storage_server_url = %v, want storage_access URL", got)
	}
}
