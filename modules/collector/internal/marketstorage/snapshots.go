package marketstorage

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/storageio"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/seriesset"
)

// ErrStaleEffectiveFrom 表示生效周期 P 已不在未来（或其后已有周期），且该 P 尚未登记，可重新选择 P。
var ErrStaleEffectiveFrom = errors.New("series snapshot effective_from is stale")

// SnapshotRegistration 是登记成功后 Storage 返回的 hash 与总数。
type SnapshotRegistration struct {
	Hash              string
	Count             uint32
	AlreadyRegistered bool
}

// SnapshotRegistrar 把数据集的完整标的列表登记到 Storage，并声明从周期 P 起生效。
type SnapshotRegistrar struct {
	access storageio.Primary
	auth   *storagepb.AuthInfo
}

// NewGatewaySnapshotRegistrar 通过进程网关创建登记客户端。
func NewGatewaySnapshotRegistrar(gateway gatewayclient.Invoker, instType string) (*SnapshotRegistrar, error) {
	if gateway == nil {
		return nil, fmt.Errorf("collector gateway client is required")
	}
	auth, err := ResolveStorageAuthInfo(instType)
	if err != nil {
		return nil, err
	}
	return &SnapshotRegistrar{access: storageio.NewGatewayClient(gateway), auth: auth}, nil
}

// RegisterSeriesSnapshot 登记快照。结果不明（超时、断连）时返回原始错误，调用方重放同一请求即可（Storage 幂等）；
// 只有 Storage 明确拒绝“P 已过期”时才返回 ErrStaleEffectiveFrom。
func (r *SnapshotRegistrar) RegisterSeriesSnapshot(ctx context.Context, spaceID, datasetID, frequency string, series []seriesset.Series, effectiveFrom int64) (SnapshotRegistration, error) {
	if r == nil || r.access == nil {
		return SnapshotRegistration{}, errors.New("register series snapshot: Storage client is required")
	}
	entries := make([]*storagepb.SeriesSnapshotEntry, len(series))
	for i, item := range series {
		entries[i] = &storagepb.SeriesSnapshotEntry{SubjectId: item.SubjectID, SeriesTag: item.SeriesTag}
	}
	response, err := r.access.RegisterSeriesSnapshot(ctx, &storagepb.PrimaryRegisterSeriesSnapshotReq{
		AuthInfo: r.auth, SpaceId: spaceID, DatasetId: datasetID, Frequency: frequency, Series: entries, EffectiveFrom: effectiveFrom,
	})
	if err != nil {
		return SnapshotRegistration{}, fmt.Errorf("register series snapshot: %w", err)
	}
	if response == nil || response.GetRetInfo() == nil {
		return SnapshotRegistration{}, errors.New("register series snapshot: empty response")
	}
	ret := response.GetRetInfo()
	if ret.GetCode() == storagepb.ErrorCode_CONFLICT && strings.Contains(ret.GetMsg(), "effective_from_stale") {
		return SnapshotRegistration{}, fmt.Errorf("%w: %s", ErrStaleEffectiveFrom, ret.GetMsg())
	}
	if err := ensureStorageOK("register series snapshot", ret); err != nil {
		return SnapshotRegistration{}, err
	}
	return SnapshotRegistration{Hash: response.GetSeriesHash(), Count: response.GetExpectedCount(), AlreadyRegistered: response.GetAlreadyRegistered()}, nil
}
