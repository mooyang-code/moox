# Crypto Market 短时 SCF 删除契约

`crypto_market` 是唯一行情 SCF 数据空间。短时函数不具有在线状态，因此 CloudNode 不保存节点状态；函数存活与采集结果由独立的运行与监控链路判断。

以下契约已删除且不得重新引入：`ReportHeartbeat`、`c_running_version`、`c_supported_workloads`、`c_last_heartbeat_at`、`MOOX_SCF_WATCHDOG_*`、`MOOX_SCF_CANARY_*`、`scf:heartbeat`、`external:scf_sentinel:*`。

运行正确性以 Collector 批次完成结果、重试状态、Dataset/Frequency 水位、已收盘 K 线新鲜度和出口探针为准。CLS 仅用于诊断：上报失败不改变 Storage 写入或批次完成结果。
