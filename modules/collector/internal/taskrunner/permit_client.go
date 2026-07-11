package taskrunner

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	runtimeapp "github.com/mooyang-code/moox/modules/collector/internal/app/runtime"
	"github.com/mooyang-code/moox/modules/collector/internal/providers"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type controlRequestGate struct {
	gateway, leaseID, executionNonce, scopeKey string
	leaseEpoch                                 int64
	windows                                    []*pb.ProviderQuotaWindow
	client                                     *http.Client
}

func (g controlRequestGate) BeforeRequest(ctx context.Context, meta providers.RequestMeta) (providers.RequestPermit, error) {
	if g.gateway == "" || g.leaseID == "" || g.executionNonce == "" || len(g.windows) == 0 {
		return providers.RequestPermit{}, fmt.Errorf("control gateway, quota lease, execution nonce and quota windows are required")
	}
	req := &pb.AcquireProviderPermitReq{ProviderId: string(meta.ProviderID), ScopeKey: firstNonEmpty(meta.QuotaScopeKey, g.scopeKey), EndpointClass: meta.EndpointClass, RequestCost: meta.RequestCost, QuotaLeaseId: g.leaseID, LeaseEpoch: g.leaseEpoch, ExecutionNonce: g.executionNonce, RequestIndex: int32(meta.RequestIndex), Windows: g.windows}
	var rsp pb.AcquireProviderPermitRsp
	if err := postCollectMgr(ctx, g.httpClient(), g.gateway, "AcquireProviderPermit", req, &rsp); err != nil {
		return providers.RequestPermit{}, err
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		return providers.RequestPermit{}, fmt.Errorf("acquire provider permit: %s", rsp.GetRetInfo().GetMsg())
	}
	permit := providers.RequestPermit{PermitID: rsp.GetPermitId(), LeaseEpoch: rsp.GetLeaseEpoch(), Allowed: rsp.GetAllowed(), DenialReason: rsp.GetDenialReason()}
	permit.NotBefore, _ = time.Parse(time.RFC3339Nano, rsp.GetNotBefore())
	permit.ExpiresAt, _ = time.Parse(time.RFC3339Nano, rsp.GetExpiresAt())
	return permit, nil
}
func (g controlRequestGate) httpClient() *http.Client {
	if g.client != nil {
		return g.client
	}
	return &http.Client{Timeout: 5 * time.Second}
}

type controlLeaseGuard struct {
	gateway, leaseID, leaseType string
	leaseEpoch                  int64
	client                      *http.Client
}

func (g controlLeaseGuard) BeforeUnifiedWrite(ctx context.Context) error {
	var rsp pb.ValidateMarketLeaseRsp
	if err := postCollectMgr(ctx, g.httpClient(), g.gateway, "ValidateMarketLease", &pb.ValidateMarketLeaseReq{LeaseId: g.leaseID, LeaseType: g.leaseType, LeaseEpoch: g.leaseEpoch}, &rsp); err != nil {
		return err
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || !rsp.GetValid() {
		return fmt.Errorf("resolution lease invalid: %s", rsp.GetRetInfo().GetMsg())
	}
	return nil
}
func (g controlLeaseGuard) httpClient() *http.Client {
	if g.client != nil {
		return g.client
	}
	return &http.Client{Timeout: 5 * time.Second}
}
func postCollectMgr(ctx context.Context, httpClient *http.Client, gateway, method string, in, out proto.Message) error {
	raw, err := protojson.Marshal(in)
	if err != nil {
		return err
	}
	url := runtimeapp.ServiceURL(gateway, "collectmgr", method)
	request, err := runtimeapp.NewSignedRequestWithContext(ctx, http.MethodPost, url, raw, runtimeapp.DefaultAuthConfig())
	if err != nil {
		return err
	}
	rsp, err := httpClient.Do(request)
	if err != nil {
		return err
	}
	defer rsp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(rsp.Body, 1<<20))
	if err != nil {
		return err
	}
	if rsp.StatusCode != http.StatusOK {
		return fmt.Errorf("collectmgr %s status %d: %s", method, rsp.StatusCode, string(body))
	}
	if err := protojson.Unmarshal(body, out); err != nil {
		return err
	}
	return nil
}
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
