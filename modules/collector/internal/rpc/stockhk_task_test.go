package rpc

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

// 用港股标签（属性与种子里的 hk_hsi 一致：行情源东方财富、成分来源腾讯）建 5m 任务：任务能力表按标签的行情源 eastmoney
// 和品种 equity 匹配，成分来源 tencent 不参与；能力表里没有的频率在创建时就拒绝。
func TestValidateCollectionTaskTagRoutesStockHK(t *testing.T) {
	service := &Service{datasetSrc: tagValidationSource{tags: map[string]*storagepb.Tag{
		"hk_hsi": {TagId: "hk_hsi", Attributes: map[string]string{
			"market_type": "equity", "sync.source": "eastmoney", "sync.index_source": "tencent", "sync.index": "HSI",
		}},
	}}}
	task := domain.CollectionTask{SpaceID: "stockhk", DataType: "kline", TagIDs: []string{"hk_hsi"}, CollectParams: `{"target_dataset_id":"bars","frequency":"5m"}`}
	require.NoError(t, service.validateCollectionTaskTagRoutes(context.Background(), task))

	task.CollectParams = `{"target_dataset_id":"bars","frequency":"15m"}`
	require.ErrorContains(t, service.validateCollectionTaskTagRoutes(context.Background(), task), "unsupported")
}
