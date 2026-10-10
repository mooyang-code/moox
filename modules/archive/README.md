# moox-archive

行情归档：消费 Storage 行变更，写本地 Journal 并按月物化为 Parquet，可选同步 COS。当前环境未部署。

设计文档：[归档](../../docs/模块/归档.md)

## 构建与测试

```bash
./scripts/build/build.sh archive
go test -count=1 ./modules/archive/...
```

## 配置

`config/app.yaml`（归档的空间与数据集、EventBus、COS）、`config/trpc_go.yaml`。

Archive 访问 Metadata 和 PrimaryStore 使用 `gatewayclient`：配置顶层 `gateway_client.caller: archive`、Admin 分配的 `key_id` 与 `key_file`，不再配置 Storage 主机或网关目标。模块和主机网关配置遵循部署目录布局；密钥路径相对 `archive/config/app.yaml`。目录缓存保存在 `archive.state_dir/gatewayclient/directory.json`，进程和需要远端服务的 CLI 命令各自关闭客户端。

`status`、`verify`、`compact` 和未确认的 `backfill` 可离线执行；常驻进程、确认后的 `backfill` 与 `sync-cos` 必须有签名密钥、MooX CA，以及实时目录或有效缓存。回填仍使用 Storage 的内部角色认证文件；共享客户端统一处理只读重试。

隔离部署的真实 Storage outbox 验收用例启用 `MOOX_SERIES_TAG_E2E=1` 后，还需提供 `MOOX_ARCHIVE_E2E_HOST_CONFIG`、`MOOX_ARCHIVE_E2E_OPERATOR_KEY_ID`、`MOOX_ARCHIVE_E2E_OPERATOR_KEY_FILE`；测试以 `moox-cli` 身份经主机网关写入和读取，Archive 进程仍使用自己的身份。其余数据目录、进程及节点参数见 `test/archive_e2e_test.go`。
