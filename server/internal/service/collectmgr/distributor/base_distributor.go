package distributor

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"

	cloudnodedao "github.com/mooyang-code/moox/server/internal/service/cloudnode/dao"
	cloudnodemodel "github.com/mooyang-code/moox/server/internal/service/cloudnode/model"
	"github.com/mooyang-code/moox/server/internal/service/collectmgr/dto"
	"github.com/mooyang-code/moox/server/internal/service/collectmgr/model"
)

// CollectParams 采集参数（从 JSON 解析）
type CollectParams struct {
	InstType  string   `json:"inst_type"`  // 产品类型: SPOT-现货, SWAP-永续合约, FUTURES-交割合约
	Objects   []string `json:"objects"`    // 标的列表 ["BTC-USDT", "ETH-USDT"]
	Intervals []string `json:"intervals"`  // K线周期 ["1m", "5m", "1h"]
	Depth     int      `json:"depth"`      // 订单簿深度
	Sources   []string `json:"sources"`    // 新闻来源
	Keywords  []string `json:"keywords"`   // 关键词
}

// TaskParams 任务执行参数
type TaskParams struct {
	DataType   string   `json:"data_type,omitempty"`   // 数据类型
	DataSource string   `json:"data_source,omitempty"` // 数据源
	InstType   string   `json:"inst_type,omitempty"`   // 产品类型: SPOT-现货, SWAP-永续合约, FUTURES-交割合约
	Symbol     string   `json:"symbol,omitempty"`      // 标的
	Intervals  []string `json:"intervals,omitempty"`   // K线周期
	Depth      int      `json:"depth,omitempty"`       // 订单簿深度
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
	if b.symbolProvider == nil {
		// 返回默认实现，包含硬编码的标的列表
		return &DefaultSymbolProvider{}
	}
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

// ResolveObjectPatterns 解析对象模式，支持通配符
// patterns: 模式列表，如 ["*"], ["BTC-*", "ETH-USDT"], ["*-USDT"]
// allObjects: 所有可用对象列表（从 SymbolProvider 获取）
// 返回: 匹配的对象列表（已去重）
func ResolveObjectPatterns(patterns []string, allObjects []string) []string {
	if len(patterns) == 0 {
		return []string{}
	}

	// 检查是否包含全量通配符 "*"
	for _, p := range patterns {
		if p == "*" {
			return allObjects
		}
	}

	seen := make(map[string]bool)
	result := make([]string, 0)

	for _, pattern := range patterns {
		if pattern == "" {
			continue
		}

		// 检查是否是通配符模式
		if strings.Contains(pattern, "*") {
			// 转换为正则表达式
			matched := matchWildcard(pattern, allObjects)
			for _, m := range matched {
				if !seen[m] {
					seen[m] = true
					result = append(result, m)
				}
			}
		} else {
			// 精确匹配，直接添加
			if !seen[pattern] {
				seen[pattern] = true
				result = append(result, pattern)
			}
		}
	}

	return result
}

// matchWildcard 使用通配符模式匹配字符串列表
// pattern: 通配符模式，支持 * 匹配任意字符
// candidates: 候选字符串列表
func matchWildcard(pattern string, candidates []string) []string {
	// 将通配符模式转换为正则表达式
	// * -> .* (匹配任意字符)
	// 其他字符需要转义
	regexPattern := "^" + wildcardToRegex(pattern) + "$"
	re, err := regexp.Compile(regexPattern)
	if err != nil {
		// 如果正则编译失败，返回空结果
		return []string{}
	}

	result := make([]string, 0)
	for _, s := range candidates {
		if re.MatchString(s) {
			result = append(result, s)
		}
	}
	return result
}

// wildcardToRegex 将通配符模式转换为正则表达式
func wildcardToRegex(pattern string) string {
	var result strings.Builder
	for _, c := range pattern {
		switch c {
		case '*':
			result.WriteString(".*")
		case '?':
			result.WriteString(".")
		case '.', '+', '^', '$', '[', ']', '(', ')', '{', '}', '|', '\\':
			// 转义正则特殊字符
			result.WriteRune('\\')
			result.WriteRune(c)
		default:
			result.WriteRune(c)
		}
	}
	return result.String()
}
