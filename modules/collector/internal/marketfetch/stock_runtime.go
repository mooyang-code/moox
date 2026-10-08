package marketfetch

import (
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/marketstorage"
	"trpc.group/trpc-go/trpc-go/client"
)

// StorageFactory 为一个市场创建 Storage 适配器；Collector 用它在调度、失败上报等环节按市场取得写入客户端。
type StorageFactory func(marketType, writeSource string) (Storage, error)

// NewStorageFactory 返回经给定 tRPC 客户端选项访问 Storage 的工厂，Collector 传入 gatewayclient 的选项。
func NewStorageFactory(options []client.Option) StorageFactory {
	return func(marketType, writeSource string) (Storage, error) {
		return NewMarketStorageForMarket(options, marketType, writeSource)
	}
}

// NewMarketStorageForMarket 按市场选择 Storage 绑定：stockcn 使用股票绑定，其他市场按产品类型选择。
func NewMarketStorageForMarket(options []client.Option, marketType, writeSource string) (Storage, error) {
	instType := strings.TrimSpace(marketType)
	if strings.EqualFold(instType, "equity") || strings.EqualFold(instType, StockCNSpaceID) {
		instType = marketstorage.InstTypeSPOT
	}
	storage, err := marketstorage.NewBatchStorage(options, instType, writeSource)
	if err != nil {
		return nil, fmt.Errorf("create %s market storage: %w", strings.TrimSpace(marketType), err)
	}
	return storage, nil
}
