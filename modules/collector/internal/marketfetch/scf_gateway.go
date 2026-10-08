package marketfetch

import (
	"net/url"
	"os"
	"strings"

	"github.com/mooyang-code/moox/packages/gatewayauth"
	"trpc.group/trpc-go/trpc-go/client"
)

// scfGatewayOptions 是 SCF 函数访问 Storage 的 tRPC 选项：发往调用载荷中给出的网关地址，
// 用函数环境变量中的凭据签名。Collector 进程自己不走这里，而是用 gatewayclient。
func scfGatewayOptions(target string) []client.Option {
	return gatewayauth.NewTRPCClientOptions(normalizeSCFGatewayTarget(target), scfGatewayNodeID(), gatewayauth.CredentialsFromEnv())
}

func scfGatewayNodeID() string {
	if nodeID := strings.TrimSpace(os.Getenv("MOOX_COLLECTOR_STORAGE_RPC_GATEWAY_NODE_ID")); nodeID != "" {
		return nodeID
	}
	return strings.TrimSpace(os.Getenv("MOOX_GATEWAY_TARGET_NODE"))
}

func normalizeSCFGatewayTarget(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return "ip://127.0.0.1:11003"
	}
	if strings.Contains(raw, "://") {
		return raw
	}
	if parsed, err := url.Parse(raw); err == nil && parsed.Host != "" {
		return "ip://" + parsed.Host
	}
	if strings.Contains(raw, ":") {
		return "ip://" + raw
	}
	return raw
}
