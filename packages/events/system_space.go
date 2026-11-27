package events

// SystemSpaceID 是“不属于任何市场”的范围标签：系统事件（主机快照、服务指标、下架等）的信封空间，
// 以及 Monitor 里与市场无关的检查、告警和 Admin 的系统密钥都归在它下面。它不是 Storage 里的空间。
const SystemSpaceID = "mooxsys"
