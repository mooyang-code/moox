package config

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	cmap "github.com/orcaman/concurrent-map/v2"
)

var (
	// taskInstanceStore 任务实例内存存储（使用concurrent-map保证并发安全）
	taskInstanceStore cmap.ConcurrentMap[string, *CollectorTaskInstanceCache]
	// currentTasksMD5 当前任务列表的MD5值
	currentTasksMD5 string
	// storeInitOnce 保证store只初始化一次
	storeInitOnce sync.Once
)

// init 包初始化时自动初始化存储
func init() {
	InitTaskInstanceStore()
}

// CollectorTaskInstanceCache 采集任务实例缓存结构
type CollectorTaskInstanceCache struct {
	// ID 主键ID
	ID int `json:"id"`
	// TaskID 任务唯一标识
	TaskID string `json:"task_id"`
	// RuleID 规则ID（关联配置表）
	RuleID string `json:"rule_id"`
	// NodeID 执行节点ID
	NodeID string `json:"planned_exec_node"`
	// TaskParams 任务执行参数（原始JSON字符串）
	TaskParams string `json:"task_params"`
	// Invalid 任务删除标记
	Invalid int `json:"invalid"`
	// AccessUrl 访问该表的接口url
	AccessUrl string

	// === 以下为 TaskParams 解析后的结构化字段 ===
	// DataType 数据类型（如 kline, ticker, depth 等）
	DataType string `json:"data_type,omitempty"`
	// DataSource 数据源（如 binance, okx 等）
	DataSource string `json:"data_source,omitempty"`
	// InstType 产品类型: SPOT(现货), SWAP(永续合约), FUTURES(交割合约)
	InstType string `json:"inst_type,omitempty"`
	// Symbol 交易对（如 BTC-USDT）
	Symbol string `json:"symbol,omitempty"`
	// Interval 单一执行周期（心跳下发使用）
	Interval string `json:"interval,omitempty"`
	// Intervals K线周期列表（如 ["1m", "3m", "5m"]）
	Intervals []string `json:"intervals,omitempty"`
}

// taskParamsJSON TaskParams 的 JSON 解析结构
type taskParamsJSON struct {
	DataType   string   `json:"data_type"`
	DataSource string   `json:"data_source"`
	InstType   string   `json:"inst_type"`
	Symbol     string   `json:"symbol"`
	Intervals  []string `json:"intervals"`
}

// ParseTaskParams 解析 TaskParams JSON 字符串并填充结构化字段
func (c *CollectorTaskInstanceCache) ParseTaskParams() error {
	if c.TaskParams == "" {
		return nil
	}

	var params taskParamsJSON
	if err := json.Unmarshal([]byte(c.TaskParams), &params); err != nil {
		// #region agent log
		// 使用 fmt.Printf 输出到标准输出（会被日志系统捕获）
		fmt.Printf("[DEBUG_AGENT] parse_task_params_error: taskID=%s, taskParams=%s, error=%v\n",
			c.TaskID, c.TaskParams, err)
		// #endregion
		return err
	}

	c.DataType = params.DataType
	c.DataSource = params.DataSource
	c.InstType = params.InstType
	c.Symbol = params.Symbol
	c.Intervals = params.Intervals
	if len(c.Intervals) == 0 && c.Interval != "" {
		c.Intervals = []string{c.Interval}
	}
	
	// #region agent log
	fmt.Printf("[DEBUG_AGENT] parse_task_params_success: taskID=%s, symbol=%s, taskParams=%s\n",
		c.TaskID, c.Symbol, c.TaskParams)
	// #endregion
	
	return nil
}

// GetTaskInstancesByNode 根据节点ID获取任务实例列表
func GetTaskInstancesByNode(nodeID string) []*CollectorTaskInstanceCache {
	return GetTaskInstancesByNodeFromStore(nodeID)
}

// ========== 新的任务实例内存存储方法 ==========

// InitTaskInstanceStore 初始化任务实例存储
func InitTaskInstanceStore() {
	storeInitOnce.Do(func() {
		taskInstanceStore = cmap.New[*CollectorTaskInstanceCache]()
		currentTasksMD5 = "empty" // 初始值与计算逻辑保持一致
	})
}

// UpdateTaskInstances 更新任务实例到内存存储
func UpdateTaskInstances(tasks []*CollectorTaskInstanceCache) {
	// 清空现有数据
	taskInstanceStore.Clear()

	// 写入新数据
	for _, task := range tasks {
		if task != nil && task.TaskID != "" {
			// 解析任务参数
			_ = task.ParseTaskParams()
			taskInstanceStore.Set(task.TaskID, task)
		}
	}

	// 更新MD5值
	currentTasksMD5 = CalculateTasksMD5(tasks)
}

// GetTaskInstancesByNodeFromStore 从内存存储中获取指定节点的任务实例
func GetTaskInstancesByNodeFromStore(nodeID string) []*CollectorTaskInstanceCache {
	if nodeID == "" {
		return nil
	}

	var result []*CollectorTaskInstanceCache
	taskInstanceStore.IterCb(func(key string, task *CollectorTaskInstanceCache) {
		if task.NodeID == nodeID && task.Invalid == 0 {
			result = append(result, task)
		}
	})

	return result
}

// CalculateTasksMD5 计算任务列表的MD5值
func CalculateTasksMD5(tasks []*CollectorTaskInstanceCache) string {
	if len(tasks) == 0 {
		return "empty"
	}

	// 提取所有有效任务的TaskID（过滤Invalid!=0的任务）
	taskIDs := make([]string, 0, len(tasks))
	for _, task := range tasks {
		if task.Invalid == 0 {
			taskIDs = append(taskIDs, task.TaskID)
		}
	}

	// 如果过滤后没有有效任务
	if len(taskIDs) == 0 {
		return "empty"
	}

	// 排序
	sort.Strings(taskIDs)

	// 拼接
	combined := strings.Join(taskIDs, ",")

	// 计算MD5
	hash := md5.Sum([]byte(combined))
	return hex.EncodeToString(hash[:])
}

// GetCurrentTasksMD5 获取当前任务MD5值
func GetCurrentTasksMD5() string {
	return currentTasksMD5
}
