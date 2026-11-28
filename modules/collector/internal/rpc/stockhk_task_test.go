package rpc

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

// 用港股标签（属性与种子里的 hk_hsi 一致：行情源东方财富、成分来源腾讯）建任务：频率按标签的行情源 eastmoney
// 和品种 equity 校验，成分来源 tencent 不参与。
func TestValidateCollectionTaskTagRoutesStockHK(t *testing.T) {
	var got []string
	service := &Service{datasetSrc: tagValidationSource{tags: map[string]*storagepb.Tag{
		"hk_hsi": {TagId: "hk_hsi", Attributes: map[string]string{
			"market_type": "equity", "sync.source": "eastmoney", "sync.index_source": "tencent", "sync.index": "HSI",
		}},
	}}, checkKlineFrequency: func(marketID, marketType, providerID, frequency string) error {
		got = []string{marketID, marketType, providerID, frequency}
		return nil
	}}
	task := domain.CollectionTask{SpaceID: "stockhk", DataType: "kline", TagIDs: []string{"hk_hsi"}, CollectParams: `{"frequency":"5m"}`}
	require.NoError(t, service.validateCollectionTaskTagRoutes(context.Background(), task))
	require.Equal(t, []string{"stockhk", "equity", "eastmoney", "5m"}, got)
}
