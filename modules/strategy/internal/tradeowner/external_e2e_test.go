//go:build e2e_external

package tradeowner

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
	thttp "trpc.group/trpc-go/trpc-go/http"
)

// reachable 用会话校验探测 Trade 是否可达：账户存在但所有者不是本会话属于"可达"。
func reachable(ctx context.Context, owner *Client, space, account string) error {
	err := owner.ValidateSession(ctx, space, account, "probe-instance", "probe-session")
	if err == nil {
		return nil
	}
	var coded *ResponseError
	if errors.As(err, &coded) {
		return err
	}
	if errors.Is(err, ErrSessionNotOwned) {
		return nil
	}
	// 传输错误对调用方只给概述，断言具体的 TLS 失败要看原始原因。
	var transport *TransportError
	if errors.As(err, &transport) {
		return transport.Err
	}
	return err
}

// 直连 TradeConsoleService：会话认领幂等、冲突返回 14、释放后校验失败。
func TestExternalStrategyClaimsLogicalAccountFromTrade(t *testing.T) {
	target := strings.TrimSpace(os.Getenv("MOOX_STRATEGY_TRADE_RPC_E2E_TARGET"))
	if target == "" {
		t.Fatal("需要 MOOX_STRATEGY_TRADE_RPC_E2E_TARGET")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	header := &thttp.ClientReqHeader{Header: make(http.Header)}
	header.Header.Set("X-Space-Id", "space-e2e")
	proxy := tradepb.NewTradeConsoleServiceClientProxy(client.WithTarget(target), client.WithNetwork("tcp"), client.WithProtocol("http"), client.WithTimeout(3*time.Second))
	created, err := proxy.CreateLogicalAccount(ctx, &tradepb.CreateLogicalAccountReq{Name: "strategy-rpc-e2e", ExecutionMode: tradepb.ExecutionMode_EXECUTION_MODE_PAPER, MarketType: tradepb.MarketType_MARKET_TYPE_SPOT, SettlementAsset: "USDT"}, client.WithReqHead(header))
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_SUCCESS, created.GetRetInfo().GetCode())
	account := created.GetLogicalAccount().GetLogicalAccountId()
	require.NotEmpty(t, account)
	owner := &Client{proxy: proxy, timeout: 3 * time.Second}
	require.NoError(t, reachable(ctx, owner, "space-e2e", account))
	for range 2 {
		require.NoError(t, owner.ClaimSession(ctx, "space-e2e", account, "instance-e2e", "session-e2e"))
	}
	require.NoError(t, owner.ValidateSession(ctx, "space-e2e", account, "instance-e2e", "session-e2e"))
	conflict := owner.ClaimSession(ctx, "space-e2e", account, "instance-other", "session-other")
	require.Error(t, conflict)
	require.True(t, IsOwnerConflict(conflict), "冲突的认领应返回所有权冲突：%v", conflict)
	for range 2 {
		require.NoError(t, owner.ReleaseSession(ctx, "space-e2e", account, "instance-e2e", "session-e2e"))
	}
	require.Error(t, owner.ValidateSession(ctx, "space-e2e", account, "instance-e2e", "session-e2e"))
}

// 生产客户端 → 可信 TLS 网关 → Trade：认领、校验、释放；缺失或无关的 CA、错误节点、错误密钥、跨空间与 SubmitOrder 都被拒绝。
func TestExternalStrategyGatewayOwnerClient(t *testing.T) {
	coord := os.Getenv("MOOX_GATEWAY_OWNER_E2E_COORD")
	require.NotEmpty(t, coord)
	endpoint, err := os.ReadFile(filepath.Join(coord, "gateway-ready"))
	require.NoError(t, err)
	u, err := url.Parse(string(endpoint))
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1", u.Hostname())
	require.Equal(t, "https", u.Scheme)
	id, err := os.ReadFile(filepath.Join(coord, "logical-id"))
	require.NoError(t, err)
	cfg := Config{GatewayURL: string(endpoint), TargetNode: "gateway-owner-e2e", CAFile: filepath.Join(coord, "gateway-ca.pem"), Timeout: 3 * time.Second}
	owner := New(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	const space, instance, session = "space-gateway-e2e", "gateway-instance", "gateway-session"
	require.NoError(t, reachable(ctx, owner, space, string(id)))
	for name, caFile := range map[string]string{"缺少 CA": "", "无关 CA": filepath.Join(coord, "unrelated-ca.pem")} {
		t.Run(name, func(t *testing.T) {
			untrusted := cfg
			untrusted.CAFile = caFile
			require.ErrorContains(t, reachable(ctx, New(untrusted), space, string(id)), "x509:")
		})
	}
	require.NoError(t, owner.ClaimSession(ctx, space, string(id), instance, session))
	require.NoError(t, owner.ValidateSession(ctx, space, string(id), instance, session))
	require.Error(t, owner.ValidateSession(ctx, space, string(id), instance, "wrong-session"))
	require.Error(t, reachable(ctx, owner, "another-space", string(id)))
	require.Error(t, owner.ClaimSession(ctx, "another-space", string(id), instance, session))
	wrongNode := cfg
	wrongNode.TargetNode = "wrong-node"
	require.ErrorContains(t, reachable(ctx, New(wrongNode), space, string(id)), "HTTP 401")
	t.Run("错误密钥", func(t *testing.T) {
		t.Setenv("MOOX_GATEWAY_SERVICE_SECRET_KEY", "wrong-e2e-secret")
		require.ErrorContains(t, reachable(ctx, New(cfg), space, string(id)), "HTTP 401")
	})
	path := "/api/service/trade_owner/SubmitOrder"
	body := []byte(`{}`)
	headers, err := gatewayauth.Sign(gatewayauth.CredentialsFromEnv(), gatewayauth.Request{Method: http.MethodPost, Path: path, TargetNode: cfg.TargetNode, Body: body}, time.Now())
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.GatewayURL+path, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header = headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Space-Id", space)
	httpClient, err := gatewayauth.NewHTTPClient(gatewayauth.ClientOptions{Timeout: 3 * time.Second, CAFile: cfg.CAFile})
	require.NoError(t, err)
	defer httpClient.CloseIdleConnections()
	rsp, err := httpClient.Do(req)
	require.NoError(t, err)
	rsp.Body.Close()
	require.Equal(t, http.StatusNotFound, rsp.StatusCode, "账户所有权路由必须拒绝 SubmitOrder")
	require.NoError(t, owner.ValidateSession(ctx, space, string(id), instance, session))
	require.NoError(t, owner.ReleaseSession(ctx, space, string(id), instance, session))
	require.Error(t, owner.ValidateSession(ctx, space, string(id), instance, session))
	require.NoError(t, os.WriteFile(filepath.Join(coord, "strategy-done"), []byte("passed"), 0o600))
	t.Log("生产客户端 → 可信 TLS 网关 → Trade：ClaimSession/ValidateSession/ReleaseSession 通过；缺失或无关 CA、错误节点或密钥、跨空间与 SubmitOrder 被拒绝")
}
