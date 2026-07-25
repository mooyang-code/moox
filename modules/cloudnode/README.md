# MooX CloudNode

CloudNode 管理云账户、云节点、函数包和异步 JobItem，并负责腾讯云 SCF/COS 控制面操作。

## 云账户与密钥

`t_cloud_accounts` 保留业务配置，但只保存 `credential_secret_id`。腾讯云 SecretID 和
SecretKey 由 Admin SecretMgr 加密保存。CloudNode 每次 SCF/COS 操作都通过 Service
Gateway 调用 `RevealSecret`，校验 secret 为 active、`category=cloud`、
`provider=tencent`；不缓存、不落库、不写日志。

## Job Execution Queue

提交 JobItem 时，CloudNode 按
`space_id + code_package_id + job_type` 使用 `packages/cloudjobqueue.Identity` 创建或校准
Consumer，然后写 pending 状态并发布消息。CloudNode 不订阅执行队列。

Collector SCF 使用 `cloudnode-worker` 凭据 bind 已存在的 Consumer。某个作业类型尚未创建
时只跳过该类型；所有类型都不存在时明确退出。Worker 执行后先上报终态，再 ACK 或 TERM。
可重试非末次失败只 NAK。JobItem 状态只有 `pending`、`enqueue_failed`、`success` 和
`failed`，不含 Poll、attempt、running、cancel 或服务端 ack token。

终态采用 first-terminal-wins。重复上报和未知 JobItem 都幂等成功；因此可能保留“failed
先到、success 后到”的假失败，这是个人量化系统为简洁性接受的结果。

## 发布与升级

首次 publish 创建 SCF，并注入 EventBus URL、worker username/password、Base64 CA 和
`MOOX_CODE_PACKAGE_ID`。zip 中不包含凭据。函数已存在时 publish 失败，提示使用 deploy。

deploy 先读取函数完整 Environment，再更新代码，最后仅替换
`MOOX_CODE_PACKAGE_ID` 并更新函数配置。本地 deployment 记录只在两个云端操作都成功后
更新。前端批量部署与单节点部署都走此路径。
