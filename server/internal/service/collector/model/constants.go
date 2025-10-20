package model

const (
	// NodeStatus 节点状态
	NodeStatusOffline = 0 // 离线
	NodeStatusOnline  = 1 // 在线

	// EnabledStatus 启用状态（字符串类型）
	EnabledTrue  = "true"  // 启用
	EnabledFalse = "false" // 禁用

	// InvalidStatus 删除标记
	InvalidNo  = 0 // 未删除
	InvalidYes = 1 // 已删除

	// NodeType 节点类型
	NodeTypeSCF    = "scf"    // 云函数
	NodeTypeServer = "server" // 服务器

	// TaskType 任务类型
	TaskTypeObjectList  = "object_list"  // 对象列表采集
	TaskTypeDataCollect = "data_collect" // 数据采集

	// AssignmentType 分配类型
	AssignmentTypeAuto    = "auto"    // 自动分配
	AssignmentTypeFixed   = "fixed"   // 固定节点
	AssignmentTypePattern = "pattern" // 通配符匹配

	// LoadBalanceStrategy 负载均衡策略
	LoadBalanceRoundRobin = "round_robin" // 轮询
	LoadBalanceLeastLoad  = "least_load"  // 最小负载
	LoadBalanceRandom     = "random"      // 随机

	// TaskInstanceStatus 任务实例状态
	TaskInstanceStatusPending  = 0 // 待执行
	TaskInstanceStatusRunning  = 1 // 执行中
	TaskInstanceStatusSuccess  = 2 // 成功
	TaskInstanceStatusFailed   = 3 // 失败
	TaskInstanceStatusTimeout  = 4 // 超时
	TaskInstanceStatusCanceled = 5 // 已取消
	TaskInstanceStatusStopped  = 6 // 已停止
)
