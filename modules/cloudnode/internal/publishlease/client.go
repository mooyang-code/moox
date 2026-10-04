package publishlease

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const validatePath = "/api/service/publishlease/ValidateCollectorPublishLease"
const beginOperationPath = "/api/service/publishlease/BeginCollectorPublishOperation"
const renewOperationPath = "/api/service/publishlease/RenewCollectorPublishOperation"
const endOperationPath = "/api/service/publishlease/EndCollectorPublishOperation"
const acquireLeasePath = "/api/service/publishlease/AcquireCollectorPublishLease"
const renewLeasePath = "/api/service/publishlease/RenewCollectorPublishLease"
const releaseLeasePath = "/api/service/publishlease/ReleaseCollectorPublishLease"

var (
	ErrLeaseHeld       = errors.New("collector publish lease is held")
	ErrLeaseStale      = errors.New("collector publish lease is expired or fenced")
	ErrLeaseSuperseded = errors.New("collector publish lease was superseded by a newer fencing token")
)

type Lease struct {
	SpaceID      string
	LeaseID      string
	FencingToken int64
}

type Validator interface {
	Validate(context.Context, string, string, int64) error
}

type Client struct {
	baseURL     string
	targetNode  string
	credentials gatewayauth.Credentials
	httpClient  *http.Client
	initErr     error
}

func NewFromEnv() *Client {
	baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("MOOX_SERVICE_GATEWAY_HTTP_URL")), "/")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:11002"
	}
	targetNode := gatewayauth.ServiceGatewayNodeID()
	credentials := gatewayauth.CredentialsFromEnv()
	if targetNode == "" || credentials.KeyID == "" || credentials.Caller != "cloudnode" || credentials.Secret == "" {
		return &Client{initErr: fmt.Errorf("collector publish lease validation requires the cloudnode service-gateway credentials and target node")}
	}
	httpClient, err := gatewayauth.NewHTTPClient(gatewayauth.ClientOptions{
		Timeout: 3 * time.Second,
		CAFile:  strings.TrimSpace(os.Getenv("MOOX_GATEWAY_CA_FILE")),
	})
	if err != nil {
		return &Client{initErr: fmt.Errorf("create collector publish lease client: %w", err)}
	}
	return &Client{baseURL: baseURL, targetNode: targetNode, credentials: credentials, httpClient: httpClient}
}

func (c *Client) Validate(ctx context.Context, spaceID, leaseID string, fencingToken int64) error {
	if c == nil {
		return fmt.Errorf("collector publish lease validator is unavailable")
	}
	if c.initErr != nil {
		return c.initErr
	}
	if strings.TrimSpace(spaceID) == "" || strings.TrimSpace(leaseID) == "" || fencingToken < 1 {
		return fmt.Errorf("collector publish lease identity is incomplete")
	}
	request := &adminpb.ValidateCollectorPublishLeaseReq{SpaceId: spaceID, LeaseId: leaseID, FencingToken: fencingToken}
	body, err := protojson.Marshal(request)
	if err != nil {
		return fmt.Errorf("marshal collector publish lease validation: %w", err)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+validatePath, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create collector publish lease validation: %w", err)
	}
	headers, err := gatewayauth.Sign(c.credentials, gatewayauth.Request{
		Method: http.MethodPost, Path: validatePath, TargetNode: c.targetNode,
		Caller: c.credentials.Caller, Body: body,
	}, time.Now())
	if err != nil {
		return fmt.Errorf("sign collector publish lease validation: %w", err)
	}
	httpRequest.Header = headers
	httpRequest.Header.Set("Content-Type", "application/json")
	response, err := c.httpClient.Do(httpRequest)
	if err != nil {
		return fmt.Errorf("send collector publish lease validation: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return fmt.Errorf("collector publish lease control plane returned HTTP %d", response.StatusCode)
	}
	encoded, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read collector publish lease validation: %w", err)
	}
	var result adminpb.ValidateCollectorPublishLeaseRsp
	if err := protojson.Unmarshal(encoded, &result); err != nil {
		return fmt.Errorf("decode collector publish lease validation: %w", err)
	}
	if result.GetRetInfo().GetCode() != adminpb.ErrorCode_SUCCESS || !result.GetValid() || result.GetCurrentFencingToken() != fencingToken {
		return fmt.Errorf("collector publish lease is no longer current")
	}
	return nil
}

func (c *Client) BeginOperation(ctx context.Context, spaceID, leaseID string, fencingToken int64, operationID string) error {
	request := &adminpb.BeginCollectorPublishOperationReq{
		SpaceId: spaceID, LeaseId: leaseID, FencingToken: fencingToken, OperationId: operationID,
	}
	var response adminpb.CollectorPublishOperationRsp
	if err := c.call(ctx, beginOperationPath, request, &response); err != nil {
		return err
	}
	if !response.GetActive() {
		return fmt.Errorf("collector publish operation claim was not granted")
	}
	return nil
}

func (c *Client) RenewOperation(ctx context.Context, spaceID, operationID string, fencingToken int64) error {
	request := &adminpb.CollectorPublishOperationReq{SpaceId: spaceID, OperationId: operationID, FencingToken: fencingToken}
	var response adminpb.CollectorPublishOperationRsp
	if err := c.call(ctx, renewOperationPath, request, &response); err != nil {
		return err
	}
	if !response.GetActive() {
		return fmt.Errorf("collector publish operation claim is no longer active")
	}
	return nil
}

func (c *Client) EndOperation(ctx context.Context, spaceID, operationID string, fencingToken int64) error {
	request := &adminpb.CollectorPublishOperationReq{SpaceId: spaceID, OperationId: operationID, FencingToken: fencingToken}
	var response adminpb.CollectorPublishOperationRsp
	return c.call(ctx, endOperationPath, request, &response)
}

func (c *Client) AcquireLease(ctx context.Context, spaceID, holderID string, expectedFencingToken int64) (*Lease, error) {
	request := &adminpb.AcquireCollectorPublishLeaseReq{SpaceId: spaceID, HolderId: holderID, ExpectedFencingToken: expectedFencingToken}
	encoded, err := c.roundTrip(ctx, acquireLeasePath, request)
	if err != nil {
		return nil, err
	}
	var response adminpb.CollectorPublishLeaseRsp
	if err := protojson.Unmarshal(encoded, &response); err != nil {
		return nil, fmt.Errorf("decode collector publish lease acquisition: %w", err)
	}
	if err := leaseResponseError(response.GetRetInfo()); err != nil {
		return nil, err
	}
	if response.GetSpaceId() == "" || response.GetLeaseId() == "" || response.GetFencingToken() < 1 {
		return nil, fmt.Errorf("collector publish lease acquisition returned an incomplete lease")
	}
	return &Lease{SpaceID: response.GetSpaceId(), LeaseID: response.GetLeaseId(), FencingToken: response.GetFencingToken()}, nil
}

func (c *Client) RenewLease(ctx context.Context, lease *Lease) error {
	if lease == nil || lease.SpaceID == "" || lease.LeaseID == "" || lease.FencingToken < 1 {
		return fmt.Errorf("collector publish lease identity is incomplete")
	}
	request := &adminpb.RenewCollectorPublishLeaseReq{SpaceId: lease.SpaceID, LeaseId: lease.LeaseID, FencingToken: lease.FencingToken}
	encoded, err := c.roundTrip(ctx, renewLeasePath, request)
	if err != nil {
		return err
	}
	var response adminpb.CollectorPublishLeaseRsp
	if err := protojson.Unmarshal(encoded, &response); err != nil {
		return fmt.Errorf("decode collector publish lease renewal: %w", err)
	}
	if err := leaseResponseError(response.GetRetInfo()); err != nil {
		return err
	}
	if response.GetLeaseId() != lease.LeaseID || response.GetFencingToken() != lease.FencingToken {
		return ErrLeaseStale
	}
	return nil
}

func (c *Client) ReleaseLease(ctx context.Context, lease *Lease) error {
	if lease == nil || lease.SpaceID == "" || lease.LeaseID == "" || lease.FencingToken < 1 {
		return fmt.Errorf("collector publish lease identity is incomplete")
	}
	request := &adminpb.ReleaseCollectorPublishLeaseReq{SpaceId: lease.SpaceID, LeaseId: lease.LeaseID, FencingToken: lease.FencingToken}
	encoded, err := c.roundTrip(ctx, releaseLeasePath, request)
	if err != nil {
		return err
	}
	var response adminpb.ReleaseCollectorPublishLeaseRsp
	if err := protojson.Unmarshal(encoded, &response); err != nil {
		return fmt.Errorf("decode collector publish lease release: %w", err)
	}
	if response.GetRetInfo() == nil || response.GetRetInfo().GetCode() != adminpb.ErrorCode_SUCCESS {
		return leaseResponseError(response.GetRetInfo())
	}
	if !response.GetReleased() {
		return ErrLeaseStale
	}
	return nil
}

func (c *Client) call(ctx context.Context, path string, requestMessage proto.Message, responseMessage *adminpb.CollectorPublishOperationRsp) error {
	encoded, err := c.roundTrip(ctx, path, requestMessage)
	if err != nil {
		return err
	}
	if err := protojson.Unmarshal(encoded, responseMessage); err != nil {
		return fmt.Errorf("decode collector publish lease response: %w", err)
	}
	return leaseResponseError(responseMessage.GetRetInfo())
}

func (c *Client) roundTrip(ctx context.Context, path string, requestMessage proto.Message) ([]byte, error) {
	if c == nil {
		return nil, fmt.Errorf("collector publish lease validator is unavailable")
	}
	if c.initErr != nil {
		return nil, c.initErr
	}
	body, err := protojson.Marshal(requestMessage)
	if err != nil {
		return nil, fmt.Errorf("marshal collector publish lease request: %w", err)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create collector publish lease request: %w", err)
	}
	headers, err := gatewayauth.Sign(c.credentials, gatewayauth.Request{
		Method: http.MethodPost, Path: path, TargetNode: c.targetNode,
		Caller: c.credentials.Caller, Body: body,
	}, time.Now())
	if err != nil {
		return nil, fmt.Errorf("sign collector publish lease request: %w", err)
	}
	httpRequest.Header = headers
	httpRequest.Header.Set("Content-Type", "application/json")
	response, err := c.httpClient.Do(httpRequest)
	if err != nil {
		return nil, fmt.Errorf("send collector publish lease request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return nil, fmt.Errorf("collector publish lease control plane returned HTTP %d", response.StatusCode)
	}
	encoded, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read collector publish lease response: %w", err)
	}
	return encoded, nil
}

func leaseResponseError(retInfo *adminpb.RetInfo) error {
	if retInfo == nil || retInfo.GetCode() != adminpb.ErrorCode_SUCCESS {
		message := "control plane rejected the request"
		if retInfo != nil && retInfo.GetMsg() != "" {
			message = retInfo.GetMsg()
		}
		if retInfo != nil && retInfo.GetCode() == adminpb.ErrorCode_CONFLICT {
			switch {
			case strings.Contains(strings.ToLower(message), "held"):
				return fmt.Errorf("%w: %s", ErrLeaseHeld, message)
			case strings.Contains(strings.ToLower(message), "superseded"):
				return fmt.Errorf("%w: %s", ErrLeaseSuperseded, message)
			case strings.Contains(strings.ToLower(message), "expired"), strings.Contains(strings.ToLower(message), "fenced"):
				return fmt.Errorf("%w: %s", ErrLeaseStale, message)
			}
		}
		return fmt.Errorf("collector publish lease control plane: %s", message)
	}
	return nil
}
