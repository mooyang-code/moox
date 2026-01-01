package model

// 数据类型常量
const (
	DataTypeKline     = "kline"     // K线数据
	DataTypeTicker    = "ticker"    // 逐笔数据
	DataTypeOrderbook = "orderbook" // 订单簿数据
	DataTypeTrade     = "trade"     // 交易数据
	DataTypeNews      = "news"      // 新闻数据
	DataTypeList      = "list"      // 列表数据
	DataTypeSymbol    = "symbol"    // 标的数据
)

// 分配类型常量
const (
	AssignmentTypeAuto    = "auto"    // 自动分配：分配给所有支持该数据类型的节点
	AssignmentTypeFixed   = "fixed"   // 固定分配：分配给指定的节点列表
	AssignmentTypePattern = "pattern" // 通配符匹配：分配给节点ID匹配模式的节点
	AssignmentTypeTag     = "tag"     // 标签匹配：分配给标签匹配的节点
)

// 任务实例状态常量
const (
	InstanceStatusPending    = 0 // 待执行
	InstanceStatusRunning    = 1 // 执行中
	InstanceStatusSuccess    = 2 // 成功
	InstanceStatusPartFailed = 3 // 部分失败
	InstanceStatusFailed     = 4 // 失败
)

// Invalid 常量
const (
	InvalidNo  = 0 // 有效
	InvalidYes = 1 // 无效
)

// Enabled 常量
const (
	EnabledTrue  = "true"  // 启用
	EnabledFalse = "false" // 禁用
)
