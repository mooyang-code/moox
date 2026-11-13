// Package systemspace 只放系统范围标签这一个常量，不带任何依赖：
// 只需要这个常量的包（例如 Admin 的密钥模型）引用它，不必把整个 events 包及其协议依赖编进来。
package systemspace

// ID 是“不属于任何市场”的范围标签：系统事件（主机快照、服务指标、下架等）的信封空间，
// 以及 Monitor 里与市场无关的检查、告警和 Admin 的系统密钥都归在它下面。它不是 Storage 里的空间。
const ID = "mooxsys"
