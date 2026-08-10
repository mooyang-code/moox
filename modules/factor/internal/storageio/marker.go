package storageio

import (
	"context"
	"fmt"
	"time"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"trpc.group/trpc-go/trpc-go/client"
)

func (c *Client) WaitViewSyncPoint(ctx context.Context, spaceID, viewID, requestID string, datasetIDs []string) error {
	storage, ok := c.access.(interface {
		WaitViewSyncPoint(context.Context, *storagepb.WaitViewSyncPointReq, ...client.Option) (*storagepb.WaitViewSyncPointRsp, error)
	})
	if !ok {
		return fmt.Errorf("storage client does not support View sync-point waits")
	}
	for {
		rsp, err := storage.WaitViewSyncPoint(ctx, &storagepb.WaitViewSyncPointReq{AuthInfo: c.auth, SpaceId: spaceID, ViewId: viewID, RequestId: requestID, DatasetIds: append([]string(nil), datasetIDs...), WaitTimeoutMs: 30000})
		if err != nil {
			return fmt.Errorf("wait View sync point: %w", err)
		}
		if err := ensureStorageOK("wait View sync point", rsp.GetRetInfo()); err != nil {
			return err
		}
		if rsp.GetReady() {
			return nil
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (c *Client) FactorPeriodComputed(ctx context.Context, spaceID, sourceViewID, triggerEventID string, periodTime int64) (bool, error) {
	client, ok := c.access.(interface {
		GetFactorPeriodComputed(context.Context, *storagepb.GetFactorPeriodComputedReq, ...client.Option) (*storagepb.GetFactorPeriodComputedRsp, error)
	})
	if !ok {
		return false, fmt.Errorf("storage client does not support factor marker preflight")
	}
	rsp, err := client.GetFactorPeriodComputed(ctx, &storagepb.GetFactorPeriodComputedReq{
		AuthInfo: c.auth, SpaceId: spaceID, SourceViewId: sourceViewID,
		TriggerEventId: triggerEventID, PeriodTime: periodTime,
	})
	if err != nil {
		return false, fmt.Errorf("get factor period computed: %w", err)
	}
	if err := ensureStorageOK("get factor period computed", rsp.GetRetInfo()); err != nil {
		return false, err
	}
	return rsp.GetFound(), nil
}

func (c *Client) ReportFactorPeriodComputed(ctx context.Context, spaceID string, marker *storagepb.FactorPeriodComputedMarker) error {
	client, ok := c.access.(interface {
		ReportFactorPeriodComputed(context.Context, *storagepb.ReportFactorPeriodComputedReq, ...client.Option) (*storagepb.ReportFactorPeriodComputedRsp, error)
	})
	if !ok {
		return fmt.Errorf("storage client does not support factor marker report")
	}
	rsp, err := client.ReportFactorPeriodComputed(ctx, &storagepb.ReportFactorPeriodComputedReq{AuthInfo: c.auth, SpaceId: spaceID, Marker: marker})
	if err != nil {
		return fmt.Errorf("report factor period computed: %w", err)
	}
	return ensureStorageOK("report factor period computed", rsp.GetRetInfo())
}
