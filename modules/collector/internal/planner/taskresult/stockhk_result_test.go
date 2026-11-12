package taskresult

import (
	"context"
	"testing"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

// attributeTagMetadataFake 按标签 ID 返回完整的标签属性。
type attributeTagMetadataFake struct {
	*resultMetadataFake
	tags map[string]map[string]string
}

func (f attributeTagMetadataFake) GetTag(_ context.Context, req *storagepb.GetTagReq) (*storagepb.GetTagRsp, error) {
	attrs, ok := f.tags[req.GetTagId()]
	if !ok {
		return &storagepb.GetTagRsp{RetInfo: resultNotFound()}, nil
	}
	return &storagepb.GetTagRsp{RetInfo: resultOK(), Tag: &storagepb.Tag{SpaceId: req.GetSpaceId(), TagId: req.GetTagId(), Attributes: attrs}}, nil
}

// 用港股标签建 5m 任务的结果数据集：数据源取标签的行情源 eastmoney（成分来源 tencent 不算），品种是 equity，
// 按港股日历和交易时段对齐；不写输出字段时建的列与 A 股 5m 任务相同，是股票行的全部列。
func TestEnsureStockHKResultUsesTheTagQuoteSourceAndStockColumns(t *testing.T) {
	ensure := func(spaceID, taskID, tagID string, attrs map[string]string) (*storagepb.Dataset, []string) {
		fake := attributeTagMetadataFake{newResultMetadataFake(), map[string]map[string]string{tagID: attrs}}
		manager := NewManagerWithAPI(fake, &storagepb.AuthInfo{AppId: "collector"})
		ids, err := manager.Ensure(context.Background(), spaceID, taskID, "kline", "", Config{DataNodeID: "node-1", Frequency: "5m", SubjectTags: []string{tagID}})
		require.NoError(t, err)
		return fake.datasets[ids.DatasetID], fake.columnNames
	}
	hk, hkColumns := ensure("stockhk", "task-hk-5m", "hk_hsi", map[string]string{
		"market_type": "equity", "sync.source": "eastmoney", "sync.index_source": "tencent", "sync.index": "HSI",
	})
	require.Equal(t, "eastmoney", hk.GetDataSourceId())
	require.Equal(t, []string{"hk_hsi"}, hk.GetSubjectTags())
	require.Equal(t, "5m", hk.GetFreq())
	require.Equal(t, "equity", hk.GetAttributes()["market_type"])
	require.Equal(t, "hk_stock", hk.GetAttributes()["calendar"])
	require.Equal(t, "session", hk.GetAttributes()["alignment"])

	_, cnColumns := ensure("stockcn", "task-cn-5m", "cn_a_share", map[string]string{"market_type": "equity", "sync.source": "eastmoney"})
	require.Equal(t, cnColumns, hkColumns, "港股结果数据集的列与 A 股 5m 任务相同")
	for _, column := range []string{"open", "high", "low", "close", "volume", "amount", "amount_unit", "volume_unit", "trade_date", "provider_id", "source_id", "provider_symbol", "amount_quality"} {
		require.Contains(t, hkColumns, column)
	}
}
