package observability

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	commonpb "github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/client"
)

// goneMetadata 按数据集 ID 模拟 Storage 元数据：gone 里的数据集回答“不存在”，其余存在；fail 为真时读取失败。
type goneMetadata struct {
	gone map[string]bool
	fail bool
}

func (g *goneMetadata) GetDataset(_ context.Context, req *storagepb.GetDatasetReq, _ ...client.Option) (*storagepb.GetDatasetRsp, error) {
	if g.fail {
		return nil, context.DeadlineExceeded
	}
	if g.gone[req.GetDatasetId()] {
		return &storagepb.GetDatasetRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_DATASET_NOT_FOUND, Msg: "dataset not found"}}, nil
	}
	return &storagepb.GetDatasetRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}, Dataset: &storagepb.Dataset{DatasetId: req.GetDatasetId()}}, nil
}
func (g *goneMetadata) ListDatasetSubjects(context.Context, *storagepb.ListDatasetSubjectsReq, ...client.Option) (*storagepb.ListDatasetSubjectsRsp, error) {
	return &storagepb.ListDatasetSubjectsRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}}, nil
}
func (g *goneMetadata) ListTags(context.Context, *storagepb.ListTagsReq, ...client.Option) (*storagepb.ListTagsRsp, error) {
	return &storagepb.ListTagsRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}}, nil
}

func openOverviewWithStorage(t *testing.T, metadata monmetrics.MetadataClient, seed func(*gorm.DB)) *monmetrics.QueryService {
	t.Helper()
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = manager.Close() })
	require.NoError(t, manager.ApplySchema(schema.SQL()))
	query, err := store.WithDatabase(manager, func(db *gorm.DB) *monmetrics.QueryService {
		seed(db)
		return monmetrics.NewQueryService(monmetrics.NewMetricMessageStore(db), monmetrics.NewStorageAdapter(metadata))
	})
	require.NoError(t, err)
	return query
}

// 指向已被删除的数据集的 View 留下的指标序列不再进入概览（否则一直显示“数据生产端长时间未更新”）；
// 仍存在的数据集的 View 照常显示；Storage 读取失败时按“还在”处理，不会因为 Storage 暂时不可达把 View 藏起来。
func TestOverviewHidesViewsOfDeletedDatasets(t *testing.T) {
	now := time.Date(2026, 10, 24, 2, 0, 0, 0, time.UTC)
	watermark := now.Add(-20 * time.Minute)
	gone := `{"dataset_id":"dataset_factor_gone","freq":"1m","space_id":"crypto","view_id":"view_factor_gone"}`
	alive := `{"dataset_id":"dataset_factor_alive","freq":"1m","space_id":"crypto","view_id":"view_factor_alive"}`
	seed := func(db *gorm.DB) {
		for id, labels := range map[string]string{"gone": gone, "alive": alive} {
			seedOverviewMetricForInstance(t, db, "wm-"+id, "storage-view", "storage-view@control", "moox_storage_view_output_watermark_timestamp_seconds", labels, float64(watermark.Unix()), now)
			seedOverviewMetricForInstance(t, db, "arr-"+id, "storage-view", "storage-view@control", "moox_storage_view_output_arrival_timestamp_seconds", labels, float64(watermark.Add(time.Minute).Unix()), now)
		}
	}
	datasets := func(query *monmetrics.QueryService) []string {
		got, err := (Builder{Metrics: query, Inventory: cryptoInventory, Now: func() time.Time { return now }, Policy: testRealtimePolicy()}).Build(t.Context(), "")
		require.NoError(t, err)
		var ids []string
		for _, item := range got.Datasets {
			ids = append(ids, item.DatasetID)
		}
		return ids
	}

	query := openOverviewWithStorage(t, &goneMetadata{gone: map[string]bool{"dataset_factor_gone": true}}, seed)
	require.Equal(t, []string{"view_factor_alive"}, datasets(query))

	unreachable := openOverviewWithStorage(t, &goneMetadata{fail: true}, seed)
	require.ElementsMatch(t, []string{"view_factor_alive", "view_factor_gone"}, datasets(unreachable))
}
