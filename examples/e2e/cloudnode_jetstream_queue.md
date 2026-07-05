# CloudNode JetStream JobItem Queue E2E

本例验证 CloudNode 新的异步执行模型：

```text
SCF runtime -> /api/service/cloudnode/PollJobItems
CloudNode -> MOOX_CLOUDNODE_EXEC pull consumer
SCF runtime -> /api/service/cloudnode/ReportJobItemStatus
CloudNode -> MOOX_CLOUDNODE_PROJECTION reported event
Projector -> SQLite t_cloud_job_items / t_cloud_job_item_attempts
```

SCF 不直接连接 NATS，也不直接写 CloudNode SQLite。

## 本地检查

```bash
go test ./modules/cloudnode/...
go test ./modules/collector/internal/reporter ./modules/collector/internal/serverless ./modules/collector/internal/taskrunner
go test ./packages/cloudruntime
```

## 部署检查

发布后在目标机器检查：

```bash
/home/ubuntu/moox/prod/status.sh
ss -ltnp | grep 4223
curl -sS --max-time 10 http://106.53.107.122:11000/api/admin/health
curl -sS -I --max-time 10 http://106.53.107.122:9527/ | head
```

`4223` 是 CloudNode 私有嵌入式 NATS JetStream 端口。storage 如使用 NATS，应继续使用自己的 `moox.storage.*` subject，不与 CloudNode 混用。

CloudNode SQLite 应保持单连接写入：

```bash
grep -n "max_open_conns: 1" /home/ubuntu/moox/prod/cloudnode/config/app.yaml
grep -n "max_idle_conns: 1" /home/ubuntu/moox/prod/cloudnode/config/app.yaml
```

服务代码也会在启动时强制 `MaxOpenConns=1`，即使配置被误改为更高值，也不应重新引入同进程 SQLite 写锁竞争。

## 重建 CloudNode 执行状态

新项目可在确认不需要保留当前云节点队列状态时重建：

```bash
/home/ubuntu/moox/prod/stop.sh cloudnode
rm -rf /home/ubuntu/moox/prod/data/cloudnode/moox_cloudnode.db
rm -rf /home/ubuntu/moox/prod/data/cloudnode/nats
/home/ubuntu/moox/prod/start.sh cloudnode
```

这会清空 CloudNode 队列和管理台 JobItem 投影，不影响 storage 中已经写入的行情数据。

## 管理台验证

- `/#/collector/cloudnodes`：云节点心跳继续更新，不应出现高频 `SQLite database is locked`。
- `/#/collector/tasks`：JobItem 可见 `pending/running/success/failed/canceled/enqueue_failed` 状态。
- K 线采集链路：collector 生成 JobItem 后，SCF poll 执行并上报，最新 K 线进入 storage 视图。

## 日志关键词

CloudNode：

```text
cloudnode JetStream 已启用
heartbeat enqueue failed
submit job items failed
poll job items failed
report job item status failed
```

Collector SCF：

```text
PollJobItems
ReportJobItemStatus
received cancel directive
```
