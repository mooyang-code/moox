package subjectsync

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketstorage"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/client"
)

const storagePageSize = 1000

type metadataClient interface {
	ListTags(context.Context, *storagepb.ListTagsReq, ...client.Option) (*storagepb.ListTagsRsp, error)
	ApplyTagSnapshot(context.Context, *storagepb.ApplyTagSnapshotReq, ...client.Option) (*storagepb.ApplyTagSnapshotRsp, error)
	ReportTagRunFailure(context.Context, *storagepb.ReportTagRunFailureReq, ...client.Option) (*storagepb.ReportTagRunFailureRsp, error)
	UpdateSubjectAttributes(context.Context, *storagepb.UpdateSubjectAttributesReq, ...client.Option) (*storagepb.UpdateSubjectAttributesRsp, error)
	GetDataSource(context.Context, *storagepb.GetDataSourceReq, ...client.Option) (*storagepb.GetDataSourceRsp, error)
	UpdateDataSource(context.Context, *storagepb.UpdateDataSourceReq, ...client.Option) (*storagepb.UpdateDataSourceRsp, error)
}

// StorageClient is the narrow Storage Metadata client used by the standalone
// subject synchronizer. Keeping the RPC surface small makes the two schedulers
// independently testable and prevents accidental writes outside metadata.
type StorageClient struct {
	client metadataClient
	auth   *storagepb.AuthInfo
}

func NewStorageClient(target string) (*StorageClient, error) {
	auth, err := marketstorage.ResolveStorageAuthInfo(marketstorage.InstTypeSPOT)
	if err != nil {
		return nil, fmt.Errorf("resolve storage auth: %w", err)
	}
	target = marketstorage.NormalizeStorageTarget(target, "11003")
	options := gatewayauth.NewTRPCClientOptions(target, marketstorage.StorageGatewayNodeID(), gatewayauth.CredentialsFromEnv())
	return &StorageClient{client: storagepb.NewMetadataClientProxy(options...), auth: auth}, nil
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

func (c *StorageClient) ApplyTagSnapshot(ctx context.Context, spaceID, tagID string, runAt time.Time, items []*storagepb.TagSnapshotItem) error {
	if err := c.checkReady(); err != nil {
		return err
	}
	rsp, err := c.client.ApplyTagSnapshot(ctx, &storagepb.ApplyTagSnapshotReq{AuthInfo: c.auth, SpaceId: spaceID, TagId: tagID, RunAt: runAt.UTC().Format(time.RFC3339Nano), Items: items})
	if err != nil {
		return fmt.Errorf("apply tag snapshot %s/%s: %w", spaceID, tagID, err)
	}
	return storageOK("apply tag snapshot", rsp.GetRetInfo())
}

func (c *StorageClient) ReportTagRunFailure(ctx context.Context, spaceID, tagID string, runAt time.Time, message string) error {
	if err := c.checkReady(); err != nil {
		return err
	}
	rsp, err := c.client.ReportTagRunFailure(ctx, &storagepb.ReportTagRunFailureReq{AuthInfo: c.auth, SpaceId: spaceID, TagId: tagID, RunAt: runAt.UTC().Format(time.RFC3339Nano), Error: message})
	if err != nil {
		return fmt.Errorf("report tag run failure %s/%s: %w", spaceID, tagID, err)
	}
	return storageOK("report tag run failure", rsp.GetRetInfo())
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

func (c *StorageClient) RegisterSubjectListing(ctx context.Context, supported map[string][]string) error {
	if err := c.checkReady(); err != nil {
		return err
	}
	spaces := map[string]string{"binance": "crypto", "sina": "stockcn", "eastmoney": "stockcn", "baidu": "stockcn", "tencent": "stockcn", "tdx": "stockcn"}
	sources := make([]string, 0, len(supported))
	for source := range supported {
		sources = append(sources, source)
	}
	sort.Strings(sources)
	for _, source := range sources {
		types := supported[source]
		normalizedSource := strings.ToLower(strings.TrimSpace(source))
		spaceID, ok := spaces[normalizedSource]
		if !ok || len(types) == 0 {
			continue
		}
		rsp, err := c.client.GetDataSource(ctx, &storagepb.GetDataSourceReq{AuthInfo: c.auth, SpaceId: spaceID, DataSourceId: source})
		if err != nil {
			return fmt.Errorf("get subject listing data source %s/%s: %w", spaceID, normalizedSource, err)
		}
		if err := storageOK("get subject listing data source", rsp.GetRetInfo()); err != nil {
			return err
		}
		if rsp.GetDataSource() == nil {
			return fmt.Errorf("get subject listing data source %s/%s: empty data source", spaceID, normalizedSource)
		}
		item := proto.Clone(rsp.GetDataSource()).(*storagepb.DataSource)
		if item.Attributes == nil {
			item.Attributes = map[string]string{}
		}
		payload, err := json.Marshal(struct {
			InstrumentTypes []string `json:"instrument_types"`
		}{InstrumentTypes: append([]string(nil), types...)})
		if err != nil {
			return fmt.Errorf("encode subject listing %s: %w", normalizedSource, err)
		}
		item.Attributes["subject_listing"] = string(payload)
		updated, err := c.client.UpdateDataSource(ctx, &storagepb.UpdateDataSourceReq{AuthInfo: c.auth, DataSource: item})
		if err != nil {
			return fmt.Errorf("update subject listing %s/%s: %w", spaceID, normalizedSource, err)
		}
		if err := storageOK("update subject listing", updated.GetRetInfo()); err != nil {
			return err
		}
	}
	return nil
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
