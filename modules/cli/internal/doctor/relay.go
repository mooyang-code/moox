package doctor

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"strconv"
)

// RelayProber 对只能出站的主机经 control 中转探测健康端口：用 moox-cli 身份拨号，
// 中转只放行目录里声明的健康端口，探测请求仍带健康签名。
type RelayProber struct {
	Auth HealthAuth
	// Outbound 判断节点是否只能出站（只有这些节点走中转）。
	Outbound func(nodeID string) bool
	// Dial 经中转连接节点的本机端口。
	Dial func(ctx context.Context, hostID string, port int) (net.Conn, error)
}

// Get 探测节点的健康地址。handled 为 false 表示该节点不是出站主机，调用方应改用直连探测。
func (p RelayProber) Get(ctx context.Context, nodeID, rawURL string) (result ProbeResult, handled bool, err error) {
	if p.Outbound == nil || p.Dial == nil || !p.Outbound(nodeID) {
		return ProbeResult{}, false, nil
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ProbeResult{}, true, err
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		return ProbeResult{}, true, err
	}
	client := &http.Client{
		Timeout: probeTimeout,
		Transport: &http.Transport{
			// 地址只用来拼请求，连接一律走中转。
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return p.Dial(ctx, nodeID, port) },
		},
	}
	probe := HTTPProber{Client: client, Auth: p.Auth, AllowHost: func(string) bool { return true }}
	result, err = probe.Get(ctx, rawURL)
	return result, true, err
}
