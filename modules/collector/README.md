# moox-collector

采集控制面、SCF 运行时与 `collector-stream`：采集任务、采集计划对账、Timer 自主拉取、WS 收盘推送、本地补采、标的同步和 K 线重采样。

设计文档：[采集](../../docs/模块/采集.md)

## 构建与测试

```bash
./scripts/build/build.sh collector
TARGET_GOOS=linux TARGET_GOARCH=amd64 ./scripts/build/build.sh collector-scf
SCF_SPACE_ID=crypto ./scripts/build/build-collector-scf-package.sh
go test -count=1 ./modules/collector/...
```

## 配置

`config/trpc_go.yaml`、`config/app.yaml`、`config/markets/`；schema 在 `schema/collector.sql`。行情源参考见 [docs/行情接口目录.md](docs/行情接口目录.md)。

常驻进程和标的同步共用 `config/app.yaml` 中的 `gateway_client` 身份：caller 固定为 `collector`，KeyID 由 Admin 分配，签名文件默认位于 `../../secrets/caller-collector.key`；本机网关地址、主机 ID 和私有 CA 从同级 hostgateway 配置读取。标的同步任务统一在 `app.yaml` 的 `subject_sync` 中配置，由 Collector 定时器执行。Storage 请求中的角色认证仍由行情绑定配置解析，独立于网关签名。

进程内 Metadata/Primary、FunctionInstance、Admin 发布租约与出口代理调用使用共享 `gatewayclient`，空间标识经元数据传递，写调用只发送一次。SCF 使用 `scf_access` / `scf_access_routes` 派生的外部 Access 地址与签名身份，运行参数由对账器写入环境变量。`storage.gateway_target` 已删除。Collector 程序均不需要 CGO，可在本机编译或交叉编译；SCF 目标限 Linux amd64。

CollectMgr 仅在 loopback 11402 提供 tRPC。Monitor 清单、CLI 任务管理和控制台均经主机网关调用；旧 11418 HTTP 监听已删除。

运行配置提交后的网络或解码错误按结果未知处理，保留发布围栏。当前实时 SCF 根据环境中的计划自行响应 Timer，不再向 Collector 领取批次，也不发送旧 Completion 事件；失败缺口由检查与补采修复。
