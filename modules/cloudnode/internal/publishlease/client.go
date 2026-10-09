package publishlease

import (
	"context"
	"errors"
	"fmt"
	"strings"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"google.golang.org/protobuf/proto"
)

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

// Client borrows CloudNode's process gateway. Mutations are sent once.
type Client struct{ gateway gatewayclient.Invoker }

func New(gateway gatewayclient.Invoker) *Client { return &Client{gateway: gateway} }

func (c *Client) Validate(ctx context.Context, spaceID, leaseID string, fencingToken int64) error {
	if strings.TrimSpace(spaceID) == "" || strings.TrimSpace(leaseID) == "" || fencingToken < 1 {
		return fmt.Errorf("collector publish lease identity is incomplete")
	}
	var response adminpb.ValidateCollectorPublishLeaseRsp
	if err := c.invoke(ctx, spaceID, "ValidateCollectorPublishLease", &adminpb.ValidateCollectorPublishLeaseReq{
		SpaceId: spaceID, LeaseId: leaseID, FencingToken: fencingToken,
	}, &response); err != nil {
		return err
	}
	if err := leaseResponseError(response.GetRetInfo()); err != nil {
		return err
	}
	if !response.GetValid() || response.GetCurrentFencingToken() != fencingToken {
		return ErrLeaseStale
	}
	return nil
}

func (c *Client) BeginOperation(ctx context.Context, spaceID, leaseID string, fencingToken int64, operationID string) error {
	var response adminpb.CollectorPublishOperationRsp
	if err := c.operation(ctx, spaceID, "BeginCollectorPublishOperation", &adminpb.BeginCollectorPublishOperationReq{
		SpaceId: spaceID, LeaseId: leaseID, FencingToken: fencingToken, OperationId: operationID,
	}, &response); err != nil {
		return err
	}
	if !response.GetActive() {
		return fmt.Errorf("collector publish operation claim was not granted")
	}
	return nil
}

func (c *Client) RenewOperation(ctx context.Context, spaceID, operationID string, fencingToken int64) error {
	var response adminpb.CollectorPublishOperationRsp
	if err := c.operation(ctx, spaceID, "RenewCollectorPublishOperation", &adminpb.CollectorPublishOperationReq{
		SpaceId: spaceID, OperationId: operationID, FencingToken: fencingToken,
	}, &response); err != nil {
		return err
	}
	if !response.GetActive() {
		return fmt.Errorf("collector publish operation claim is no longer active")
	}
	return nil
}

func (c *Client) EndOperation(ctx context.Context, spaceID, operationID string, fencingToken int64) error {
	var response adminpb.CollectorPublishOperationRsp
	return c.operation(ctx, spaceID, "EndCollectorPublishOperation", &adminpb.CollectorPublishOperationReq{
		SpaceId: spaceID, OperationId: operationID, FencingToken: fencingToken,
	}, &response)
}

func (c *Client) AcquireLease(ctx context.Context, spaceID, holderID string, expectedFencingToken int64) (*Lease, error) {
	var response adminpb.CollectorPublishLeaseRsp
	if err := c.invoke(ctx, spaceID, "AcquireCollectorPublishLease", &adminpb.AcquireCollectorPublishLeaseReq{
		SpaceId: spaceID, HolderId: holderID, ExpectedFencingToken: expectedFencingToken,
	}, &response); err != nil {
		return nil, err
	}
	if err := leaseResponseError(response.GetRetInfo()); err != nil {
		return nil, err
	}
	if response.GetSpaceId() != spaceID || spaceID == "" || response.GetLeaseId() == "" || response.GetFencingToken() < 1 {
		return nil, fmt.Errorf("collector publish lease acquisition returned an incomplete or inconsistent lease")
	}
	return &Lease{SpaceID: spaceID, LeaseID: response.GetLeaseId(), FencingToken: response.GetFencingToken()}, nil
}

func (c *Client) RenewLease(ctx context.Context, lease *Lease) error {
	if lease == nil || lease.SpaceID == "" || lease.LeaseID == "" || lease.FencingToken < 1 {
		return fmt.Errorf("collector publish lease identity is incomplete")
	}
	var response adminpb.CollectorPublishLeaseRsp
	if err := c.invoke(ctx, lease.SpaceID, "RenewCollectorPublishLease", &adminpb.RenewCollectorPublishLeaseReq{
		SpaceId: lease.SpaceID, LeaseId: lease.LeaseID, FencingToken: lease.FencingToken,
	}, &response); err != nil {
		return err
	}
	if err := leaseResponseError(response.GetRetInfo()); err != nil {
		return err
	}
	if response.GetSpaceId() != lease.SpaceID || response.GetLeaseId() != lease.LeaseID || response.GetFencingToken() != lease.FencingToken {
		return ErrLeaseStale
	}
	return nil
}

func (c *Client) ReleaseLease(ctx context.Context, lease *Lease) error {
	if lease == nil || lease.SpaceID == "" || lease.LeaseID == "" || lease.FencingToken < 1 {
		return fmt.Errorf("collector publish lease identity is incomplete")
	}
	var response adminpb.ReleaseCollectorPublishLeaseRsp
	if err := c.invoke(ctx, lease.SpaceID, "ReleaseCollectorPublishLease", &adminpb.ReleaseCollectorPublishLeaseReq{
		SpaceId: lease.SpaceID, LeaseId: lease.LeaseID, FencingToken: lease.FencingToken,
	}, &response); err != nil {
		return err
	}
	if err := leaseResponseError(response.GetRetInfo()); err != nil {
		return err
	}
	if !response.GetReleased() {
		return ErrLeaseStale
	}
	return nil
}

func (c *Client) operation(ctx context.Context, spaceID, method string, request proto.Message, response *adminpb.CollectorPublishOperationRsp) error {
	if err := c.invoke(ctx, spaceID, method, request, response); err != nil {
		return err
	}
	return leaseResponseError(response.GetRetInfo())
}

func (c *Client) invoke(ctx context.Context, spaceID, method string, request, response proto.Message) error {
	if c == nil || c.gateway == nil {
		return fmt.Errorf("collector publish lease requires the process gateway client")
	}
	metadata := gatewayclient.CallMetadataFromContext(ctx)
	metadata.SpaceID = spaceID
	return c.gateway.Invoke(gatewayclient.WithCallMetadata(ctx, metadata), "trpc.moox.admin.CollectorPublishLease", method, request, response)
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
