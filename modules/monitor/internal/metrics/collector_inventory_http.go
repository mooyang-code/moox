package metrics

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"google.golang.org/protobuf/encoding/protojson"
	"trpc.group/trpc-go/trpc-go/client"
)

const (
	collectorInventoryServiceID = "collectmgr"
	maxCollectorInventoryBody   = 8 << 20
)

// CollectorInventoryHTTPClient fetches the collector task-result inventory
// through the Service Gateway's HTTP entry. CollectMgr serves HTTP only, so
// the native (tRPC) entry cannot reach it: a tRPC frame sent to its HTTP port
// is dropped and the refresh failed on every attempt.
type CollectorInventoryHTTPClient struct {
	baseURL     string
	targetNode  string
	credentials gatewayauth.Credentials
	http        *http.Client
}

func NewCollectorInventoryHTTPClient(baseURL, targetNode string, credentials gatewayauth.Credentials, caFile string, timeout time.Duration) (*CollectorInventoryHTTPClient, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" || strings.TrimSpace(targetNode) == "" {
		return nil, errors.New("collector inventory gateway URL and target node are required")
	}
	httpClient, err := gatewayauth.NewHTTPClient(gatewayauth.ClientOptions{Timeout: timeout, CAFile: caFile})
	if err != nil {
		return nil, fmt.Errorf("collector inventory gateway client: %w", err)
	}
	return &CollectorInventoryHTTPClient{baseURL: baseURL, targetNode: strings.TrimSpace(targetNode), credentials: credentials, http: httpClient}, nil
}

func (c *CollectorInventoryHTTPClient) GetTaskResultInventory(ctx context.Context, req *collectorpb.GetTaskResultInventoryReq, _ ...client.Option) (*collectorpb.GetTaskResultInventoryRsp, error) {
	body, err := protojson.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("encode inventory request: %w", err)
	}
	path := "/api/service/" + collectorInventoryServiceID + "/GetTaskResultInventory"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	headers, err := gatewayauth.Sign(c.credentials, gatewayauth.Request{Method: http.MethodPost, Path: path, TargetNode: c.targetNode, Body: body}, time.Now())
	if err != nil {
		return nil, fmt.Errorf("sign inventory request: %w", err)
	}
	request.Header = headers
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Space-Id", req.GetSpaceId())
	response, err := c.http.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxCollectorInventoryBody+1))
	if err != nil {
		return nil, fmt.Errorf("read inventory response: %w", err)
	}
	if len(raw) > maxCollectorInventoryBody {
		return nil, fmt.Errorf("inventory response exceeds %d bytes", maxCollectorInventoryBody)
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("inventory request returned HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(raw[:min(len(raw), 200)])))
	}
	rsp := &collectorpb.GetTaskResultInventoryRsp{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(raw, rsp); err != nil {
		return nil, fmt.Errorf("decode inventory response: %w", err)
	}
	return rsp, nil
}
