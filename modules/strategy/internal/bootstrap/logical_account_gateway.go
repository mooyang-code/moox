package bootstrap

import (
	"bytes"
	"context"
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

type logicalAccountProxy interface {
	GetLogicalAccount(context.Context, *tradepb.GetLogicalAccountReq, ...client.Option) (*tradepb.GetLogicalAccountRsp, error)
	ClaimLogicalAccountOwner(context.Context, *tradepb.ClaimLogicalAccountOwnerReq, ...client.Option) (*tradepb.ClaimLogicalAccountOwnerRsp, error)
	ReleaseLogicalAccountOwner(context.Context, *tradepb.ReleaseLogicalAccountOwnerReq, ...client.Option) (*tradepb.ReleaseLogicalAccountOwnerRsp, error)
	RebindLogicalAccountOwner(context.Context, *tradepb.RebindLogicalAccountOwnerReq, ...client.Option) (*tradepb.RebindLogicalAccountOwnerRsp, error)
}

type logicalAccountSpaceKey struct{}

type logicalAccountGateway struct {
	config      TradeConfig
	credentials gatewayauth.Credentials
	client      *http.Client
	initErr     error
}

func newLogicalAccountGateway(cfg TradeConfig) *logicalAccountGateway {
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultLogicalAccountTimeout
	}
	g := &logicalAccountGateway{config: cfg, credentials: gatewayauth.CredentialsFromEnv()}
	g.initErr = cfg.validate()
	if g.initErr == nil {
		g.client, g.initErr = gatewayauth.NewHTTPClient(gatewayauth.ClientOptions{Timeout: cfg.Timeout, CAFile: cfg.CAFile})
	}
	return g
}

func (g *logicalAccountGateway) post(ctx context.Context, method string, request, response proto.Message) error {
	if g.initErr != nil {
		return g.initErr
	}
	if g.config.GatewayURL == "" {
		return fmt.Errorf("Trade gateway_url is not configured")
	}
	if g.credentials.Caller != "strategy" {
		return fmt.Errorf("Trade gateway caller must be strategy")
	}
	spaceID, _ := ctx.Value(logicalAccountSpaceKey{}).(string)
	if spaceID == "" {
		return fmt.Errorf("Trade gateway space is required")
	}
	body, err := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(request)
	if err != nil {
		return err
	}
	path := "/api/service/trade_owner/" + method
	headers, err := gatewayauth.Sign(g.credentials, gatewayauth.Request{Method: http.MethodPost, Path: path, TargetNode: g.config.TargetNode, Body: body}, time.Now())
	if err != nil {
		return err
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
		return fmt.Errorf("Trade gateway %s HTTP %d", method, httpRsp.StatusCode)
	}
	const maxResponseBytes = 1 << 20
	data, err := io.ReadAll(io.LimitReader(httpRsp.Body, maxResponseBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxResponseBytes {
		return fmt.Errorf("Trade gateway response exceeds limit")
	}
	return protojson.Unmarshal(data, response)
}

func (g *logicalAccountGateway) GetLogicalAccount(ctx context.Context, req *tradepb.GetLogicalAccountReq, _ ...client.Option) (*tradepb.GetLogicalAccountRsp, error) {
	rsp := new(tradepb.GetLogicalAccountRsp)
	return rsp, g.post(ctx, "GetLogicalAccount", req, rsp)
}

func (g *logicalAccountGateway) ClaimLogicalAccountOwner(ctx context.Context, req *tradepb.ClaimLogicalAccountOwnerReq, _ ...client.Option) (*tradepb.ClaimLogicalAccountOwnerRsp, error) {
	rsp := new(tradepb.ClaimLogicalAccountOwnerRsp)
	return rsp, g.post(ctx, "ClaimLogicalAccountOwner", req, rsp)
}

func (g *logicalAccountGateway) ReleaseLogicalAccountOwner(ctx context.Context, req *tradepb.ReleaseLogicalAccountOwnerReq, _ ...client.Option) (*tradepb.ReleaseLogicalAccountOwnerRsp, error) {
	rsp := new(tradepb.ReleaseLogicalAccountOwnerRsp)
	return rsp, g.post(ctx, "ReleaseLogicalAccountOwner", req, rsp)
}

func (g *logicalAccountGateway) RebindLogicalAccountOwner(ctx context.Context, req *tradepb.RebindLogicalAccountOwnerReq, _ ...client.Option) (*tradepb.RebindLogicalAccountOwnerRsp, error) {
	rsp := new(tradepb.RebindLogicalAccountOwnerRsp)
	return rsp, g.post(ctx, "RebindLogicalAccountOwner", req, rsp)
}
