package subjectsync

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/modules/collector/internal/marketstorage"
	"github.com/mooyang-code/moox/modules/collector/internal/storageio"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"trpc.group/trpc-go/trpc-go/client"
)

const storagePageSize = 1000

type metadataClient interface {
	ListTags(context.Context, *storagepb.ListTagsReq, ...client.Option) (*storagepb.ListTagsRsp, error)
	ApplyTagSnapshot(context.Context, *storagepb.ApplyTagSnapshotReq, ...client.Option) (*storagepb.ApplyTagSnapshotRsp, error)
	UpdateSubjectAttributes(context.Context, *storagepb.UpdateSubjectAttributesReq, ...client.Option) (*storagepb.UpdateSubjectAttributesRsp, error)
}

// StorageClient is the narrow Storage Metadata client used by Collector's
// subject synchronizer. Keeping the RPC surface small makes the two schedulers
// independently testable and prevents accidental writes outside metadata.
type StorageClient struct {
	client metadataClient
	auth   *storagepb.AuthInfo
}

func NewStorageClient(gateway gatewayclient.Invoker) (*StorageClient, error) {
	auth, err := marketstorage.ResolveStorageAuthInfo(marketstorage.InstTypeSPOT)
	if err != nil {
		return nil, fmt.Errorf("resolve storage auth: %w", err)
	}
	if gateway == nil {
		return nil, fmt.Errorf("collector gateway client is required")
	}
	return &StorageClient{client: storageio.NewGatewayClient(gateway), auth: auth}, nil
}

func (c *StorageClient) ListTags(ctx context.Context) ([]*storagepb.Tag, error) {
	if err := c.checkReady(); err != nil {
		return nil, err
	}
	all := make([]*storagepb.Tag, 0)
	for page := uint32(1); ; page++ {
		rsp, err := c.client.ListTags(ctx, &storagepb.ListTagsReq{AuthInfo: c.auth, Page: &storagepb.Page{Page: page, Size: storagePageSize}})
		if err != nil {
			return nil, fmt.Errorf("list tags: %w", err)
		}
		if err := storageOK("list tags", rsp.GetRetInfo()); err != nil {
			return nil, err
		}
		all = append(all, rsp.GetTags()...)
		pageResult := rsp.GetPageResult()
		if len(rsp.GetTags()) == 0 || (pageResult != nil && !pageResult.GetHasMore() &&
			(uint64(page)*uint64(pageResult.GetSize()) >= uint64(pageResult.GetTotal()) || len(rsp.GetTags()) < storagePageSize)) {
			return all, nil
		}
	}
}

func (c *StorageClient) ApplyTagSnapshot(ctx context.Context, spaceID, tagID string, items []*storagepb.TagSnapshotItem) error {
	if err := c.checkReady(); err != nil {
		return err
	}
	rsp, err := c.client.ApplyTagSnapshot(ctx, &storagepb.ApplyTagSnapshotReq{AuthInfo: c.auth, SpaceId: spaceID, TagId: tagID, Items: items})
	if err != nil {
		return fmt.Errorf("apply tag snapshot %s/%s: %w", spaceID, tagID, err)
	}
	return storageOK("apply tag snapshot", rsp.GetRetInfo())
}

func (c *StorageClient) UpdateSubjectAttributes(ctx context.Context, spaceID string, items []*storagepb.SubjectAttributes) (int, int, error) {
	if err := c.checkReady(); err != nil {
		return 0, 0, err
	}
	rsp, err := c.client.UpdateSubjectAttributes(ctx, &storagepb.UpdateSubjectAttributesReq{AuthInfo: c.auth, SpaceId: spaceID, Items: items})
	if err != nil {
		return 0, 0, fmt.Errorf("update subject attributes %s: %w", spaceID, err)
	}
	if err := storageOK("update subject attributes", rsp.GetRetInfo()); err != nil {
		return 0, 0, err
	}
	return int(rsp.GetUpdated()), int(rsp.GetSkipped()), nil
}

func (c *StorageClient) checkReady() error {
	if c == nil || c.client == nil || c.auth == nil {
		return fmt.Errorf("storage metadata client is not initialized")
	}
	return nil
}

func storageOK(action string, ret *storagepb.RetInfo) error {
	if ret == nil {
		return fmt.Errorf("%s: empty ret_info", action)
	}
	if ret.GetCode() != storagepb.ErrorCode_SUCCESS {
		return fmt.Errorf("%s: %s", action, ret.GetMsg())
	}
	return nil
}
