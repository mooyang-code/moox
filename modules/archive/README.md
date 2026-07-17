# moox-archive

`moox-archive` 消费 Storage 行变更事件，把事实写入本地 Journal，并物化为按月分区的 Parquet 文件；启用 COS 时还会同步长期副本。Parquet 默认永久保留，COS 副本不会自动删除本地文件，部署者仍需规划本地容量。

## 定时任务

- `trpc.moox.archive.materialize.timer`：每 10 分钟物化 Journal、清理 7 天去重回执，启动时立即执行，单次超时 120 秒。
- `trpc.moox.archive.cos_sync.timer`：每小时同步 COS，启动时立即执行，单次超时 300 秒；COS 未启用时安全空跑。

两个 Handler 都 Clone tRPC Context、同步返回错误并跳过同进程重入。Timer 失败不会终止事件消费；退出时会停止消费并在有界超时内最后 flush 一次。默认 `DefaultScheduler` 只提供单进程调度，多副本部署必须指定单一 owner 或接入分布式 scheduler。

## 运行

```bash
../../scripts/build.sh archive
./bin/moox-archive -config=config/app.yaml -conf=config/trpc_go.yaml
```

数据保留和磁盘边界见[数据保留与磁盘空间](../../docs/运维/数据保留与磁盘空间.md)。
