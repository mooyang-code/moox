package distributor

import (
	"context"
	"encoding/json"
	"strings"

	cloudnodedao "github.com/mooyang-code/moox/server/internal/service/cloudnode/dao"
	cloudnodemodel "github.com/mooyang-code/moox/server/internal/service/cloudnode/model"
	"github.com/mooyang-code/moox/server/internal/service/collectmgr/dto"
	"github.com/mooyang-code/moox/server/internal/service/collectmgr/model"
)

// CollectParams 采集参数（从 JSON 解析）
type CollectParams struct {
	Objects   []string `json:"objects"`   // 标的列表 ["BTC-USDT", "ETH-USDT"]
	Intervals []string `json:"intervals"` // K线周期 ["1m", "5m", "1h"]
	Limit     int      `json:"limit"`     // 数据条数限制
	Depth     int      `json:"depth"`     // 订单簿深度
	Sources   []string `json:"sources"`   // 新闻来源
	Keywords  []string `json:"keywords"`  // 关键词
}

// TaskParams 任务执行参数
type TaskParams struct {
	Symbol     string   `json:"symbol,omitempty"`      // 标的
	Intervals  []string `json:"intervals,omitempty"`   // K线周期
	Limit      int      `json:"limit,omitempty"`       // 数据条数
	Depth      int      `json:"depth,omitempty"`       // 订单簿深度
	DataSource string   `json:"data_source,omitempty"` // 数据源
	Sources    []string `json:"sources,omitempty"`     // 新闻来源
	Keywords   []string `json:"keywords,omitempty"`    // 关键词
}

// BaseDistributor 基础分配器
// 提供通用能力，被具体分配器组合使用
type BaseDistributor struct {
	nodeDAO        cloudnodedao.CloudNodeDAO
	symbolProvider SymbolProvider
}

// NewBaseDistributor 创建基础分配器
func NewBaseDistributor(nodeDAO cloudnodedao.CloudNodeDAO, symbolProvider SymbolProvider) *BaseDistributor {
	return &BaseDistributor{
		nodeDAO:        nodeDAO,
		symbolProvider: symbolProvider,
	}
}

// GetMatchingNodes 通用的节点匹配逻辑（三种分配策略）
func (b *BaseDistributor) GetMatchingNodes(ctx context.Context, rule *dto.TaskRuleDTO, dataType string) ([]*cloudnodemodel.CloudNode, error) {
	switch rule.AssignmentType {
	case model.AssignmentTypeAuto:
		// 自动分配：查找所有支持该数据类型的有效节点
		return b.nodeDAO.GetNodesBySupportedCollector(ctx, dataType)

	case model.AssignmentTypeFixed:
		// 固定分配：解析 JSON 数组，查询指定节点
		var nodeIDs []string
		if err := json.Unmarshal([]byte(rule.AssignedNodes), &nodeIDs); err != nil {
			// 如果解析失败，尝试作为逗号分隔的字符串处理
			nodeIDs = strings.Split(rule.AssignedNodes, ",")
			for i, id := range nodeIDs {
				nodeIDs[i] = strings.TrimSpace(id)
			}
		}
		if len(nodeIDs) == 0 {
			return []*cloudnodemodel.CloudNode{}, nil
		}
		return b.nodeDAO.GetNodesByIDs(ctx, nodeIDs)

	case model.AssignmentTypePattern:
		// 通配符匹配：将 * 转换为 SQL LIKE 的 %
		return b.nodeDAO.GetNodesByPattern(ctx, rule.NodePattern)

	default:
		// 默认使用自动分配
		return b.nodeDAO.GetNodesBySupportedCollector(ctx, dataType)
	}
}

// ParseCollectParams 解析采集参数 JSON
func (b *BaseDistributor) ParseCollectParams(params string) (*CollectParams, error) {
	var cp CollectParams
	if params == "" || params == "{}" {
		return &cp, nil
	}
	if err := json.Unmarshal([]byte(params), &cp); err != nil {
		return nil, err
	}
	return &cp, nil
}

// GetSymbolProvider 获取标的提供者
func (b *BaseDistributor) GetSymbolProvider() SymbolProvider {
	return b.symbolProvider
}

// MergeUnique 合并两个字符串切片并去重
func MergeUnique(a, b []string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(a)+len(b))

	for _, s := range a {
		if s != "" && !seen[s] {
			seen[s] = true
			result = append(result, s)
		}
	}
	for _, s := range b {
		if s != "" && !seen[s] {
			seen[s] = true
			result = append(result, s)
		}
	}
	return result
}
