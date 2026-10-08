package tradeowner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/client"
)

// logicalAccountProxy 是 Trade 账户所有权接口的最小集合，便于测试替换。
type logicalAccountProxy interface {
	GetLogicalAccount(context.Context, *tradepb.GetLogicalAccountReq, ...client.Option) (*tradepb.GetLogicalAccountRsp, error)
	ClaimLogicalAccountOwner(context.Context, *tradepb.ClaimLogicalAccountOwnerReq, ...client.Option) (*tradepb.ClaimLogicalAccountOwnerRsp, error)
	ReleaseLogicalAccountOwner(context.Context, *tradepb.ReleaseLogicalAccountOwnerReq, ...client.Option) (*tradepb.ReleaseLogicalAccountOwnerRsp, error)
}

type spaceKey struct{}

// gateway 经节点网关的 HMAC 鉴权 HTTP 路由调用 Trade。
type gateway struct {
	config      Config
	credentials gatewayauth.Credentials
	client      *http.Client
	initErr     error
}

func newGateway(cfg Config) *gateway {
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	g := &gateway{config: cfg, credentials: gatewayauth.CredentialsFromEnv()}
	g.initErr = cfg.Validate()
	if g.initErr == nil {
		var err error
		g.client, err = gatewayauth.NewHTTPClient(gatewayauth.ClientOptions{Timeout: cfg.Timeout, CAFile: cfg.CAFile})
		if err != nil {
			g.initErr = &hiddenCause{message: "Trade 网关的 TLS 配置无效（CA 文件无法读取或格式错误）", cause: err}
		}
	}
	return g
}

// hiddenCause 对调用方只给出概述（例如不暴露 CA 文件路径），原始错误经 Unwrap 留给日志。
type hiddenCause struct {
	message string
	cause   error
}

func (e *hiddenCause) Error() string { return e.message }
func (e *hiddenCause) Unwrap() error { return e.cause }

// visibleError 标记不含远端地址、可以原样返回给调用方的网关错误：配置或调用前提缺失、网关返回的 HTTP 状态。
// 其余错误（连接、TLS 等）可能带有网关地址，由 Client 包装为 TransportError。
type visibleError interface{ visibleToCaller() }

// configError 是网关配置或调用前提缺失。
type configError struct{ error }

func (e configError) Unwrap() error  { return e.error }
func (configError) visibleToCaller() {}

// statusError 是网关返回的非 200 状态：请求已到达网关，多为鉴权或路由配置问题，不是"不可达"。
type statusError struct {
	method string
	status int
}

func (e statusError) Error() string {
	return fmt.Sprintf("Trade 网关 %s 返回 HTTP %d", e.method, e.status)
}
func (statusError) visibleToCaller() {}

func (g *gateway) post(ctx context.Context, method string, request, response proto.Message) error {
	if g.initErr != nil {
		return configError{g.initErr}
	}
	if g.config.GatewayURL == "" {
		return configError{errors.New("Trade gateway_url 未配置")}
	}
	if g.credentials.Caller != "strategy" {
		return configError{errors.New("Trade 网关调用方必须是 strategy")}
	}
	spaceID, _ := ctx.Value(spaceKey{}).(string)
	if spaceID == "" {
		return configError{errors.New("Trade 网关调用需要空间 ID")}
	}
	body, err := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(request)
	if err != nil {
		return err
	}
	path := "/api/service/trade_owner/" + method
	headers, err := gatewayauth.Sign(g.credentials, gatewayauth.Request{Method: http.MethodPost, Path: path, TargetNode: g.config.TargetNode, Body: body}, time.Now())
	if err != nil {
		// 签名失败（凭据缺失或无效）是确定的配置问题，不是"不可达"。
		return configError{&hiddenCause{message: "Trade 网关调用签名失败：Strategy 的网关凭据缺失或无效", cause: err}}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(g.config.GatewayURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header = headers
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Space-Id", spaceID)
	httpRsp, err := g.client.Do(httpReq)
	if err != nil {
		return err
	}
	defer httpRsp.Body.Close()
	if httpRsp.StatusCode != http.StatusOK {
		return statusError{method: method, status: httpRsp.StatusCode}
	}
	const maxResponseBytes = 1 << 20
	data, err := io.ReadAll(io.LimitReader(httpRsp.Body, maxResponseBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxResponseBytes {
		return fmt.Errorf("Trade 网关响应超过大小限制")
	}
	return protojson.Unmarshal(data, response)
}

func (g *gateway) GetLogicalAccount(ctx context.Context, req *tradepb.GetLogicalAccountReq, _ ...client.Option) (*tradepb.GetLogicalAccountRsp, error) {
	rsp := new(tradepb.GetLogicalAccountRsp)
	return rsp, g.post(ctx, "GetLogicalAccount", req, rsp)
}

func (g *gateway) ClaimLogicalAccountOwner(ctx context.Context, req *tradepb.ClaimLogicalAccountOwnerReq, _ ...client.Option) (*tradepb.ClaimLogicalAccountOwnerRsp, error) {
	rsp := new(tradepb.ClaimLogicalAccountOwnerRsp)
	return rsp, g.post(ctx, "ClaimLogicalAccountOwner", req, rsp)
}

func (g *gateway) ReleaseLogicalAccountOwner(ctx context.Context, req *tradepb.ReleaseLogicalAccountOwnerReq, _ ...client.Option) (*tradepb.ReleaseLogicalAccountOwnerRsp, error) {
	rsp := new(tradepb.ReleaseLogicalAccountOwnerRsp)
	return rsp, g.post(ctx, "ReleaseLogicalAccountOwner", req, rsp)
}
