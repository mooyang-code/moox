package distributor

import (
	"context"

	cloudnodemodel "github.com/mooyang-code/moox/server/internal/service/cloudnode/model"
	"github.com/mooyang-code/moox/server/internal/service/collectmgr/dto"
)

// TaskDistributor 任务分配器接口
// 不同数据类型实现不同的分配策略
type TaskDistributor interface {
	// GetDataType 返回此分配器支持的数据类型
	GetDataType() string

	// GetTargetObjects 获取目标对象列表
	// - 需要按对象拆分：返回 ["BTC-USDT", "ETH-USDT", ...]
	// - 不需要拆分：返回 [] 或 nil（统一处理为一个 symbol="" 的实例）
	GetTargetObjects(ctx context.Context, rule *dto.TaskRuleDTO) ([]string, error)

	// BuildTaskParams 为指定对象构建任务参数
	// object 为空字符串时表示不按对象拆分
	BuildTaskParams(ctx context.Context, rule *dto.TaskRuleDTO, object string) (string, error)

	// GetMatchingNodes 根据分配策略获取匹配的节点列表
	GetMatchingNodes(ctx context.Context, rule *dto.TaskRuleDTO) ([]*cloudnodemodel.CloudNode, error)
}

// SymbolProvider 标的提供者接口
// 用于获取交易对列表，支持多种数据源
type SymbolProvider interface {
	// GetSymbols 获取指定数据源的所有标的
	// dataSource: binance, okx 等
	GetSymbols(ctx context.Context, dataSource string) ([]string, error)
}
