package cloudnode

// 云函数调用类型常量（供 impl_node_service.InvokeFunction 与
// api/cloud_function_invoke 等构造 provider 调用使用）。
const (
	InvokeTypeSync  = "RequestResponse" // 同步调用
	InvokeTypeAsync = "Event"           // 异步调用
)
