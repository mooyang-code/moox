package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/service"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayclient"
)

// gatewayDatasets 经网关调用 Storage 的 Metadata.GetDataset、Metadata.ListDatasets 与 Metadata.GetSpace。
type gatewayDatasets struct {
	gateway gatewayclient.Invoker
}

const (
	listDatasetsPageSize = 200
	listDatasetsMaxPages = 50
)

func newGatewayDatasets(gateway gatewayclient.Invoker) *gatewayDatasets {
	return &gatewayDatasets{gateway: gateway}
}

func datasetFromPB(ds *storagepb.Dataset) service.Dataset {
	return service.Dataset{
		SpaceID: ds.GetSpaceId(), DatasetID: ds.GetDatasetId(), Name: ds.GetName(), DataKind: dataKind(ds.GetDataKind()), Freq: ds.GetFreq(),
		Status: ds.GetStatus(), Retention: ds.GetRetention(),
		Description: ds.GetDescription(), DataSourceID: ds.GetDataSourceId(), RetentionSource: ds.GetRetentionSource(), SubjectTags: ds.GetSubjectTags(),
		Attributes: ds.GetAttributes(), CreatedAt: ds.GetCreatedAt(), UpdatedAt: ds.GetUpdatedAt(), Revision: ds.GetRevision(),
	}
}

// GetDataset 读取数据集的频率、保留期、状态与元数据。
func (g *gatewayDatasets) GetDataset(ctx context.Context, spaceID, datasetID string) (service.Dataset, error) {
	if g == nil || g.gateway == nil {
		return service.Dataset{}, errors.New("archive Storage gateway client is required")
	}
	rsp := &storagepb.GetDatasetRsp{}
	req := &storagepb.GetDatasetReq{SpaceId: spaceID, DatasetId: datasetID}
	if err := g.gateway.Invoke(ctx, "trpc.moox.storage.Metadata", "GetDataset", req, rsp); err != nil {
		return service.Dataset{}, fmt.Errorf("get dataset %s/%s: %w", spaceID, datasetID, err)
	}
	switch rsp.GetRetInfo().GetCode() {
	case commonpb.ErrorCode_SUCCESS:
	case commonpb.ErrorCode_DATASET_NOT_FOUND, commonpb.ErrorCode_NOT_FOUND:
		return service.Dataset{}, fmt.Errorf("%w: %s/%s", service.ErrDatasetNotFound, spaceID, datasetID)
	default:
		return service.Dataset{}, fmt.Errorf("get dataset %s/%s: %s", spaceID, datasetID, rsp.GetRetInfo().GetMsg())
	}
	ds := rsp.GetDataset()
	if ds == nil {
		return service.Dataset{}, fmt.Errorf("get dataset %s/%s returned no dataset", spaceID, datasetID)
	}
	return datasetFromPB(ds), nil
}

// ListDatasets 分页读取 Storage 的全部数据集元数据，供维护定时器自动补齐归档任务使用。
func (g *gatewayDatasets) ListDatasets(ctx context.Context) ([]service.Dataset, error) {
	if g == nil || g.gateway == nil {
		return nil, errors.New("archive Storage gateway client is required")
	}
	var out []service.Dataset
	for page := uint32(1); page <= listDatasetsMaxPages; page++ {
		rsp := &storagepb.ListDatasetsRsp{}
		req := &storagepb.ListDatasetsReq{Page: &commonpb.Page{Page: page, Size: listDatasetsPageSize}}
		if err := g.gateway.Invoke(ctx, "trpc.moox.storage.Metadata", "ListDatasets", req, rsp); err != nil {
			return nil, fmt.Errorf("list datasets page %d: %w", page, err)
		}
		if info := rsp.GetRetInfo(); info.GetCode() != commonpb.ErrorCode_SUCCESS {
			return nil, fmt.Errorf("list datasets page %d: %s", page, info.GetMsg())
		}
		for _, ds := range rsp.GetDatasets() {
			if ds == nil {
				continue
			}
			out = append(out, datasetFromPB(ds))
		}
		if rsp.GetPageResult() == nil || !rsp.GetPageResult().GetHasMore() {
			return out, nil
		}
	}
	return nil, fmt.Errorf("list datasets exceeds %d pages", listDatasetsMaxPages)
}

// GetSpaceTimezone 从 Storage 的 Metadata.GetSpace 读取空间时区（为空时按 UTC）。
func (g *gatewayDatasets) GetSpaceTimezone(ctx context.Context, spaceID string) (string, error) {
	if g == nil || g.gateway == nil {
		return "", errors.New("archive Storage gateway client is required")
	}
	rsp := &storagepb.GetSpaceRsp{}
	req := &storagepb.GetSpaceReq{SpaceId: spaceID}
	if err := g.gateway.Invoke(ctx, "trpc.moox.storage.Metadata", "GetSpace", req, rsp); err != nil {
		return "", fmt.Errorf("get space %s: %w", spaceID, err)
	}
	switch rsp.GetRetInfo().GetCode() {
	case commonpb.ErrorCode_SUCCESS:
	case commonpb.ErrorCode_SPACE_NOT_FOUND, commonpb.ErrorCode_NOT_FOUND:
		return "", fmt.Errorf("%w: %s", service.ErrSpaceNotFound, spaceID)
	default:
		return "", fmt.Errorf("get space %s: %s", spaceID, rsp.GetRetInfo().GetMsg())
	}
	sp := rsp.GetSpace()
	if sp == nil {
		return "", fmt.Errorf("get space %s returned no space", spaceID)
	}
	tz := strings.TrimSpace(sp.GetTimezone())
	if tz == "" {
		return "UTC", nil
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return "", fmt.Errorf("space %s has invalid timezone %q: %w", spaceID, tz, err)
	}
	return tz, nil
}

func dataKind(kind storagepb.DataKind) string {
	switch kind {
	case storagepb.DataKind_DATA_KIND_TIME_SERIES:
		return "time_series"
	case storagepb.DataKind_DATA_KIND_RECORD:
		return "record"
	default:
		return "unspecified"
	}
}
