# CloudRuntime

`cloudruntime` 是 Collector SCF 的单条 JobItem 执行边界。它不拥有队列，也不轮询
CloudNode。

SCF 由 Collector 入口直接绑定 CloudNode 已创建的 JetStream Job Execution Queue。每次
投递解码后调用 `ExecuteJobItem`：

- 成功：先调用 `/api/service/cloudnode/ReportJobItemStatus`，成功后 ACK。
- 可重试且未到 `MaxDeliver`：不写终态，NAK 并延迟一秒。
- 永久失败或末次投递：先上报 failed，成功后 TERM。
- 终态上报失败：NAK，让 JetStream 重投。

队列身份由 `packages/cloudjobqueue.Identity` 统一生成。运行时必须提供
`MOOX_SPACE_ID`、`MOOX_CODE_PACKAGE_ID`、节点身份和 Service Gateway 签名凭据。
`delivery_count` 只用于判断末次投递，不进入业务状态模型。
