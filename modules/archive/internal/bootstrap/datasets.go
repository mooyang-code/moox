package bootstrap

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"context"

	"github.com/mooyang-code/moox/modules/archive/internal/service"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
)

// storageAppID 是 mgr 在 Storage 请求体角色认证里的 AppId，只用于识别调用方。
const storageAppID = "archive"

// gatewayDatasets 经网关调用 Storage 的 Metadata.GetDataset。
type gatewayDatasets struct {
	gateway gatewayclient.Invoker
	secret  string
}

func newGatewayDatasets(gateway gatewayclient.Invoker) *gatewayDatasets {
	return &gatewayDatasets{gateway: gateway, secret: strings.TrimSpace(os.Getenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET"))}
}

func (g *gatewayDatasets) auth() *commonpb.AuthInfo {
	auth := &commonpb.AuthInfo{AppId: storageAppID, Operator: "archive-mgr", RequestId: fmt.Sprintf("archive-mgr-%d", time.Now().UnixNano())}
	if g.secret != "" {
		auth.AppKey = mooxsecurity.HMACSHA256Hex(g.secret, []byte(auth.AppId))
	}
	return auth
}

// GetDataset 读取数据集的频率、保留期、DataNode 与激活状态。
func (g *gatewayDatasets) GetDataset(ctx context.Context, spaceID, datasetID string) (service.Dataset, error) {
	if g == nil || g.gateway == nil {
		return service.Dataset{}, errors.New("archive Storage gateway client is required")
	}
	rsp := &storagepb.GetDatasetRsp{}
	req := &storagepb.GetDatasetReq{AuthInfo: g.auth(), SpaceId: spaceID, DatasetId: datasetID}
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
	return service.Dataset{
		SpaceID: ds.GetSpaceId(), DatasetID: ds.GetDatasetId(), Name: ds.GetName(), DataKind: dataKind(ds.GetDataKind()), Freq: ds.GetFreq(),
		Status: ds.GetStatus(), DataNodeID: ds.GetDataNodeId(), Retention: ds.GetRetention(),
	}, nil
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
