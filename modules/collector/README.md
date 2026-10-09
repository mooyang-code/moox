# moox-collector

采集控制面与 SCF 运行时：采集任务、周期批次规划、Timer 领取与对账、失败重试、标的同步和 K 线重采样。

设计文档：[采集](../../docs/模块/采集.md)

## 构建与测试

```bash
./scripts/build/build.sh collector
./scripts/build/build.sh collector-subject
./scripts/build/build.sh collector-scf
./scripts/build/build-collector-scf-package.sh
go test -count=1 ./modules/collector/...
```

## 配置

`config/trpc_go.yaml`、`config/app.yaml`、`config/subject.yaml`、`config/markets/`；schema 在 `schema/collector.sql`。行情源参考见 [docs/行情接口目录.md](docs/行情接口目录.md)。

常驻进程和标的同步共用 `config/app.yaml` 中的 `gateway_client` 身份：caller 固定为 `collector`，KeyID 由 Admin 分配，签名文件默认位于 `../../secrets/caller-collector.key`；本机网关地址、主机 ID 和私有 CA 从同级 hostgateway 配置读取。`subject.yaml` 只保存同步任务与轮询策略。Storage 请求中的角色认证仍由行情绑定配置解析，独立于网关签名。

进程内 Metadata/Primary 调用使用共享客户端；`storage.gateway_target` 与 `gateway_node_id` 暂供 SCF 外部调用使用，随主计划 E2 迁移。DNS 和 SysDeploy 分别随 E4、D2c 迁移。Collector 四个程序均不需要 CGO，在本机编译或交叉编译。

CollectMgr 仅在 loopback 11402 提供 tRPC，MarketFetchRuntime 仅在 loopback 11422 提供 tRPC。Monitor 清单、CLI 任务管理和控制台均经主机网关调用；旧 11418 HTTP 监听已删除。
