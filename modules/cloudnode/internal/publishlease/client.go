// Package publishlease 是 CloudNode 调用 Admin 发布租约服务的客户端：校验 Collector 发布租约、认领发布操作，
// 以及恢复任务自己申请的租约。经 gatewayclient 以 cloudnode 身份发送 tRPC 请求。
package publishlease

import (
	"context"
	"errors"
	"fmt"
	"strings"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"trpc.group/trpc-go/trpc-go/client"
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

// leaseAPI 是 CloudNode 用到的 CollectorPublishLease 方法。
type leaseAPI interface {
	ValidateCollectorPublishLease(context.Context, *adminpb.ValidateCollectorPublishLeaseReq, ...client.Option) (*adminpb.ValidateCollectorPublishLeaseRsp, error)
	BeginCollectorPublishOperation(context.Context, *adminpb.BeginCollectorPublishOperationReq, ...client.Option) (*adminpb.CollectorPublishOperationRsp, error)
	RenewCollectorPublishOperation(context.Context, *adminpb.CollectorPublishOperationReq, ...client.Option) (*adminpb.CollectorPublishOperationRsp, error)
	EndCollectorPublishOperation(context.Context, *adminpb.CollectorPublishOperationReq, ...client.Option) (*adminpb.CollectorPublishOperationRsp, error)
	AcquireCollectorPublishLease(context.Context, *adminpb.AcquireCollectorPublishLeaseReq, ...client.Option) (*adminpb.CollectorPublishLeaseRsp, error)
	RenewCollectorPublishLease(context.Context, *adminpb.RenewCollectorPublishLeaseReq, ...client.Option) (*adminpb.CollectorPublishLeaseRsp, error)
	ReleaseCollectorPublishLease(context.Context, *adminpb.ReleaseCollectorPublishLeaseReq, ...client.Option) (*adminpb.ReleaseCollectorPublishLeaseRsp, error)
}

type Client struct {
	api leaseAPI
}

// New 用给定的 tRPC 客户端选项（gatewayclient）创建客户端。
func New(options []client.Option) *Client {
	return &Client{api: adminpb.NewCollectorPublishLeaseClientProxy(options...)}
}

func (c *Client) ready() error {
	if c == nil || c.api == nil {
		return fmt.Errorf("collector publish lease validator is unavailable")
	}
	return nil
}

func (c *Client) Validate(ctx context.Context, spaceID, leaseID string, fencingToken int64) error {
	if err := c.ready(); err != nil {
		return err
	}
	if strings.TrimSpace(spaceID) == "" || strings.TrimSpace(leaseID) == "" || fencingToken < 1 {
		return fmt.Errorf("collector publish lease identity is incomplete")
	}
	result, err := c.api.ValidateCollectorPublishLease(ctx, &adminpb.ValidateCollectorPublishLeaseReq{SpaceId: spaceID, LeaseId: leaseID, FencingToken: fencingToken})
	if err != nil {
		return fmt.Errorf("send collector publish lease validation: %w", err)
	}
	if result.GetRetInfo().GetCode() != adminpb.ErrorCode_SUCCESS || !result.GetValid() || result.GetCurrentFencingToken() != fencingToken {
		return fmt.Errorf("collector publish lease is no longer current")
	}
	return nil
}

func (c *Client) BeginOperation(ctx context.Context, spaceID, leaseID string, fencingToken int64, operationID string) error {
	if err := c.ready(); err != nil {
		return err
	}
	response, err := c.api.BeginCollectorPublishOperation(ctx, &adminpb.BeginCollectorPublishOperationReq{
		SpaceId: spaceID, LeaseId: leaseID, FencingToken: fencingToken, OperationId: operationID,
	})
	if err := operationError(response, err); err != nil {
		return err
	}
	if !response.GetActive() {
		return fmt.Errorf("collector publish operation claim was not granted")
	}
	return nil
}

func (c *Client) RenewOperation(ctx context.Context, spaceID, operationID string, fencingToken int64) error {
	if err := c.ready(); err != nil {
		return err
	}
	response, err := c.api.RenewCollectorPublishOperation(ctx, &adminpb.CollectorPublishOperationReq{SpaceId: spaceID, OperationId: operationID, FencingToken: fencingToken})
	if err := operationError(response, err); err != nil {
		return err
	}
	if !response.GetActive() {
		return fmt.Errorf("collector publish operation claim is no longer active")
	}
	return nil
}

func (c *Client) EndOperation(ctx context.Context, spaceID, operationID string, fencingToken int64) error {
	if err := c.ready(); err != nil {
		return err
	}
	response, err := c.api.EndCollectorPublishOperation(ctx, &adminpb.CollectorPublishOperationReq{SpaceId: spaceID, OperationId: operationID, FencingToken: fencingToken})
	return operationError(response, err)
}

func operationError(response *adminpb.CollectorPublishOperationRsp, err error) error {
	if err != nil {
		return fmt.Errorf("send collector publish lease request: %w", err)
	}
	return leaseResponseError(response.GetRetInfo())
}

func (c *Client) AcquireLease(ctx context.Context, spaceID, holderID string, expectedFencingToken int64) (*Lease, error) {
	if err := c.ready(); err != nil {
		return nil, err
	}
	response, err := c.api.AcquireCollectorPublishLease(ctx, &adminpb.AcquireCollectorPublishLeaseReq{SpaceId: spaceID, HolderId: holderID, ExpectedFencingToken: expectedFencingToken})
	if err != nil {
		return nil, fmt.Errorf("send collector publish lease acquisition: %w", err)
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
	if err := c.ready(); err != nil {
		return err
	}
	if lease == nil || lease.SpaceID == "" || lease.LeaseID == "" || lease.FencingToken < 1 {
		return fmt.Errorf("collector publish lease identity is incomplete")
	}
	response, err := c.api.RenewCollectorPublishLease(ctx, &adminpb.RenewCollectorPublishLeaseReq{SpaceId: lease.SpaceID, LeaseId: lease.LeaseID, FencingToken: lease.FencingToken})
	if err != nil {
		return fmt.Errorf("send collector publish lease renewal: %w", err)
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
	if err := c.ready(); err != nil {
		return err
	}
	if lease == nil || lease.SpaceID == "" || lease.LeaseID == "" || lease.FencingToken < 1 {
		return fmt.Errorf("collector publish lease identity is incomplete")
	}
	response, err := c.api.ReleaseCollectorPublishLease(ctx, &adminpb.ReleaseCollectorPublishLeaseReq{SpaceId: lease.SpaceID, LeaseId: lease.LeaseID, FencingToken: lease.FencingToken})
	if err != nil {
		return fmt.Errorf("send collector publish lease release: %w", err)
	}
	if response.GetRetInfo() == nil || response.GetRetInfo().GetCode() != adminpb.ErrorCode_SUCCESS {
		return leaseResponseError(response.GetRetInfo())
	}
	if !response.GetReleased() {
		return ErrLeaseStale
	}
	return nil
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
