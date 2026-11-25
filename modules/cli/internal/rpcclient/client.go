// Package rpcclient 是 moox-cli 经 SSH 网关以 JSON 调各模块的客户端：Collector 的采集任务、云账号、代码包与云函数，Admin 的密钥读取。
package rpcclient

import (
	"strings"
)

// Client borrows the command-owned SSH gateway.
type Client struct {
	Gateway GatewayForwarder
	SpaceID string
	// HostID 非空时把调用发到这台主机，用于只在个别主机上部署的服务（如 HostAgentMgr）。
	HostID string
}

type retInfo struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

func isRetInfoSuccess(code int) bool {
	return code == 0
}

func isRetInfoNotFound(info retInfo) bool {
	if info.Code == 404 {
		return true
	}
	message := strings.ToLower(strings.TrimSpace(info.Msg))
	return strings.Contains(message, "not found") || strings.Contains(message, "record not found")
}
