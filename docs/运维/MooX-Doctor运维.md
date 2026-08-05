# MooX Doctor 运维

## 使用边界

Doctor V1 是 `moox-cli` 中由用户手工触发的只读诊断能力，不是后台服务。Monitor 只保存和聚合事实，CLI 负责执行检查 DAG 和生成结论。系统只有一个 Monitor 实例，不包含 Peer、Owner、Lease、DoctorMgr、自动恢复、Trade 模拟盘或 Full Canary。

发布前运行 `bash scripts/test-doctor-e2e.sh`，复验部署清单、身份注入、Storage 零修改边界、上下文限制和故障注入用例。默认 seed 只把部署脚本实际编排的进程标为 active；Trade 和 HostAgent 保留在清单中，但在单独部署前为 disabled。

Storage 正在独立重构。V1 仍检查其 SysDeploy inventory 和已有 `/healthz`、`/readyz`、Reporter 事实，并启用固定 Check `bootstrap.storage_dataset_activation`：它使用现有 Metadata 配置和 Storage service HMAC，只读取 disabled Dataset 并调用 `CheckDatasetActivation`，不调用 `ActivateDataset`。Metadata endpoint 使用既有 `MOOX_METADATA_URL`（未设置时为本机 `http://127.0.0.1:20200`），服务身份使用 `MOOX_STORAGE_NODE_AUTH_SECRET` 计算 `storage-metadata` AppKey。所有 disabled Dataset 都 ready 才是 `HEALTHY`；ready 检查失败显示 `DEGRADED`，Metadata 不可达显示 `UNKNOWN/INCONCLUSIVE`。每次最多输出 16 条 Dataset Observation，超出部分只显示省略数量；Space/Dataset 排序固定，响应正文、地址、密钥和原始失败详情会被摘要化。更广泛的 Storage 功能水位以及穿过 Storage 的数据链路仍一律输出 `SKIPPED(storage_observability_deferred)`，不得解释为通过。

## 命令

初次部署、升级或部署清单变更后，在目标节点本机运行：

```bash
cd /home/ubuntu/moox/prod
./bin/moox-cli doctor bootstrap --format json --output doctor-bootstrap.json
```

业务或监控异常时运行：

```bash
./bin/moox-cli doctor diagnose --format json --output doctor-diagnose.json
```

也可使用 `--format text|markdown`，或用可重复的 `--check <id>` 只执行指定 Check 及其依赖。`bootstrap` 拒绝非本机 `--node`；远程节点应通过已有 SSH 运维入口登录后执行。V1 不提供 `full/get/list/cancel/rerun`。

只查看 Dataset 激活准备度时，可显式运行：

```bash
./bin/moox-cli doctor bootstrap --check bootstrap.storage_dataset_activation --format json
```

该命令只生成观测报告，不改变 Dataset 的 `status` 或 `revision`。部署初始化在 Doctor 总结为 `HEALTHY` 后，必须由独立的部署激活流程显式调用 `ActivateDataset`；普通 `doctor bootstrap` 和 `doctor diagnose` 都不承担恢复或激活职责。

退出码固定为：`0=HEALTHY`、`1=DEGRADED`、`2=UNHEALTHY`、`3=INCONCLUSIVE/调用错误`。JSON 是唯一 canonical report；text 和 Markdown 只渲染同一份报告。

## 安全和限额

- 健康直读只允许 GET 到固定 `/healthz`、`/readyz`、`/metrics`，必须使用 health HMAC。
- 单次直读超时 5 秒、响应上限 1 MiB；正文不进入报告，只记录摘要和 SHA-256。
- Context 最多 64 个组件、32 个模块健康检查、每类 128 条 Observation、100 条告警、2 MiB 响应。
- Doctor 最多执行 256 个 Check，功能指标固定白名单且最多 256 条 series。
- Storage Dataset 激活观测使用固定 3 秒 Check 超时，最多报告 16 条 Observation，并以摘要记录省略数量。
- 路径检查只能在 Manifest 声明的 release-relative writable path 创建 `.moox-doctor-probe-*`，无论成功、失败、超时或取消都会删除。
- CLI 只输出建议，不执行恢复命令，也不写业务事实。

## 恢复动作

| Action ID | 常见原因 | 只读确认 | 人工恢复 | 重新接入检查 |
| --- | --- | --- | --- | --- |
| `apply_service_deployments_seed` | 缺少 required SysDeploy 记录或发布契约 | 对比 `examples/setup/default/service-deployments.yaml` 和 Admin 中的全量部署记录 | 使用 `moox-admin-cli service-deployments import` 重新导入当前 seed | 重跑 `doctor bootstrap`，确认 `bootstrap.inventory` 通过 |
| `verify_service_identity` | `service/instance_id/node_id/boot_id` 缺失或冲突 | 带 HMAC 读取目标 `/readyz`，核对 `<service>@<node>` 与本次 boot ID | 修正部署 node ID/环境并手工重启单个服务 | 等待两个 Reporter 周期后重跑 `diagnose` |
| `repair_path_permissions` | Manifest writable path 不存在或不可写 | 使用部署用户检查目录 owner、mode 和挂载只读状态 | 创建目录并只授予部署用户所需权限 | 重跑对应 `bootstrap.path_permissions:*`，确认无探针残留 |
| `verify_eventbus_credentials` | Reporter 发布、Monitor Consumer 或 ACL 异常 | 检查 EventBus ready、consumer pending、凭据文件权限和服务 reporter 错误 | 用 Admin credential CLI 重新生成/导出凭据并手工重启受影响服务 | 等待两个周期，确认业务 health 与 Reporter coverage 同时通过 |
| `restart_service_manually` | 进程停止或 health 失败 | 检查 PID、日志和签名 `/readyz`，不先删除数据 | 使用部署目录 `./restart.sh <service>` | 先运行 `./healthcheck.sh <service>`，再重跑 Doctor |
| `inspect_health_check_input` | enabled workload 无事实、功能错误或输出停滞 | 核对白名单健康检查、输入是否前进、last success/error、backlog 和 watermark | 修复配置或上游后手工重启/触发既有业务流程 | 确认水位只向前推进，重跑 `diagnose` |
| `replay_factor_window_manually` | Factor 指定窗口缺失且输入事实完整 | 核对目标窗口、版本和已有输出，避免重复写入 | 使用现有 Factor 单次计算 CLI 手工重放明确窗口 | 查询目标结果和 factor watermark 后重跑 Doctor |
| `free_disk_space` | 7 天中位数预测进入 14/7 天水位 | 检查对应 mount、容量、增长率及可安全删除的数据类别 | 按数据保留手册清理可重建数据或扩容，不删除权威事实 | 收集至少 3 个有效间隔后确认预测恢复 |
| `run_bootstrap` | Monitor Context 不可用或事实不足 | 本机确认 Monitor/Gateway/EventBus health | 不自动降级诊断；在目标节点显式运行 `doctor bootstrap` | 修复 Context 链路后重新运行 `doctor diagnose` |

## 结论解释

- `FAIL` 是已经确认的根故障；其必需下游显示 `BLOCKED`，不会重复制造根因。
- `UNKNOWN` 表示事实不足，不能覆盖或降级一个已经确认的 `FAIL`。
- enabled workload 没有新输入时可显示 `PASS(IDLE)`；合法空目标、零权重和无需交易不是失败。
- 服务 API 正常但 Reporter 中断时，health 保持通过，Reporter/metrics delivery 单独失败或告警。
- disabled SysDeploy process 显示 `SKIPPED`，不会制造缺失告警。
- identity 或 boot ID 冲突失败关闭，不任选一条事实继续推断。

## 验证

```bash
bash scripts/test-monitor-coverage-contract.sh
bash scripts/test-release-contract.sh
(cd modules/monitor && go test -count=1 ./internal/doctor ./internal/hostmetrics ./internal/rpc)
(cd modules/cli && go test -count=1 ./internal/doctor/... ./internal/command)
```
