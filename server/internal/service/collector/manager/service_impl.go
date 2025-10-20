package manager

// CollectorServiceImpl 采集器服务实现（空壳，仅保留基础结构）
// 实际功能已拆分到各个独立模块：
// - AsyncTask: 异步任务管理
// - CloudNode: 云节点和云账户管理
// - PackageMgr: 包管理
// - Heartbeat: 心跳管理
type CollectorServiceImpl struct {
	// 保留为空结构体，未来可能用于采集器特定的业务逻辑
}
