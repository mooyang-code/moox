# MooX Skill K 线 RPC 查询实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让外部用户通过 MooX Skill 的自然语言入口调用 `moox-cli data kline get`，使用专用只读 HMAC 经原生 tRPC Gateway 获取 Binance 现货 1m K 线，并在正式环境完成真实 BTC-USDT 查询验收。

**Architecture:** CLI 从严格 `0600` YAML 读取 RPC 目标、专用 Gateway 凭据、派生 Storage AppKey 及 `data-type/exchange/interval` 数据目录，使用 PrimaryStore proxy 和 `gatewayauth.NewTRPCClientOptions` 调用 `ReadTimeSeriesRows`。Admin 将该方法拆成独立 ACL，部署包生成 `moox-skill` 派生密钥；Skill 打包通过 `setup export-skill-config` 在 staging 中生成最小凭据配置。

**Tech Stack:** Go、Cobra、tRPC-Go、protobuf、YAML v3、Bash、MooX Node Service Gateway、SSH setup transport。

---

## 文件结构

- `modules/admin/internal/service/sysdeploy/defaults.go`：Gateway 路由拆分。
- `config/setup/service-deployments.yaml`：正式 seed 与内置路由保持一致。
- `scripts/deploy/deploy-moox.sh`：生成、注册和安装 `moox-skill` 派生密钥。
- `modules/cli/internal/command/data_kline.go`：K 线命令、RPC 请求和输出。
- `modules/cli/internal/command/data_kline_config.go`：严格配置与数据目录解析。
- `modules/cli/internal/command/setup_skill_config.go`：导出最小 Skill 配置。
- `scripts/build/package-skill.sh`：staging、原子归档和权限控制。
- `skills/moox/references/data-query.md`：自然语言调用契约。

### Task 1: Gateway 只读路由和专用凭据

**Files:**
- Modify: `modules/admin/internal/service/sysdeploy/defaults.go`
- Modify: `modules/admin/internal/service/sysdeploy/defaults_test.go`
- Modify: `config/setup/service-deployments.yaml`
- Modify: `modules/admin/cmd/cli/service_deployments_test.go`
- Modify: `scripts/deploy/deploy-moox.sh`
- Modify: `scripts/test/contract/test-deploy-moox-gateway.sh`
- Modify: `scripts/test/contract/test-deploy-moox-control-profile.sh`

- [ ] **Step 1: 写失败的路由测试**

断言普通 PrimaryStore route 不含 `ReadTimeSeriesRows` 和 `moox-skill`；专用 route 的方法精确为 `ReadTimeSeriesRows`，Caller 为现有调用方加 `moox-skill`。

```go
require.NotContains(t, general.GatewayMethods, "ReadTimeSeriesRows")
require.NotContains(t, general.GatewayCallers, "moox-skill")
require.Equal(t, []string{"ReadTimeSeriesRows"}, readOnly.GatewayMethods)
require.Contains(t, readOnly.GatewayCallers, "moox-skill")
```

- [ ] **Step 2: 运行红灯**

Run: `(cd modules/admin && go test -count=1 ./internal/service/sysdeploy ./cmd/cli)`

Expected: FAIL，指出缺少独立 route 或 seed/default 不一致。

- [ ] **Step 3: 实现不重叠 route**

从 `storagePrimaryGatewayMethods` 删除 `ReadTimeSeriesRows`，在 `storagePrimaryExtraConfig()` 增加同一 `service_path=trpc.moox.storage.PrimaryStore`、`port=20102` 的专用 route；同步 YAML seed。不得修改 `gatewayproxy` 核心。

- [ ] **Step 4: 写失败的部署凭据契约测试**

断言 `gateway-moox-skill.key` 存在且为 `0600`，registry 包含唯一 `key_id/caller=moox-skill`，派生 key 不等于共享 root secret。

- [ ] **Step 5: 生成、注册并安装凭据**

把 `moox-skill` 加入派生 caller 列表、Gateway registry 和安装列表。升级路径必须更新 registry，不能保留缺少新 identity 的旧 registry。

- [ ] **Step 6: 运行绿灯**

Run: `(cd modules/admin && go test -count=1 ./internal/service/sysdeploy ./cmd/cli) && bash scripts/test/contract/test-deploy-moox-gateway.sh && bash scripts/test/contract/test-deploy-moox-control-profile.sh`

Expected: PASS。

### Task 2: 严格数据目录和 `data kline get`

**Files:**
- Create: `modules/cli/internal/command/data_kline_config.go`
- Create: `modules/cli/internal/command/data_kline_config_test.go`
- Create: `modules/cli/internal/command/data_kline.go`
- Create: `modules/cli/internal/command/data_kline_test.go`

- [ ] **Step 1: 写失败的配置测试**

覆盖显式路径、`MOOX_SKILL_CONFIG`、默认路径、未知字段、version、symlink、非普通文件和非 `0600` 权限。配置模型为：

```go
type dataAccessConfig struct {
    Version int `yaml:"version"`
    Gateway dataGatewayConfig `yaml:"gateway"`
    Storage dataStorageAuthConfig `yaml:"storage"`
    DataTypes map[string]dataTypeConfig `yaml:"data_types"`
}
```

- [ ] **Step 2: 运行配置红灯**

Run: `(cd modules/cli && go test -count=1 ./internal/command -run TestDataAccessConfig)`

Expected: FAIL，因为加载函数不存在。

- [ ] **Step 3: 实现严格配置**

使用 `yaml.Decoder.KnownFields(true)`；优先级为 `--config`、`MOOX_SKILL_CONFIG`、Skill 默认路径。目标必须是非 symlink 的 `0600` 普通文件；version=1；精确查找规范化的 `data-type/exchange/interval`，不拼接 Dataset ID。

- [ ] **Step 4: 写失败的命令测试**

使用窄接口注入 fake：

```go
type timeSeriesReader interface {
    ReadTimeSeriesRows(context.Context, *pb.ReadTimeSeriesRowsReq, ...client.Option) (*pb.ReadTimeSeriesRowsRsp, error)
}
```

断言 `--data-type/--symbol` 必填、exchange 默认、limit `1..1000`、RFC3339、start<=end、派生 AuthInfo、精确 selector、`venue:binance`、DESC 和 page size。

- [ ] **Step 5: 运行命令红灯**

Run: `(cd modules/cli && go test -count=1 ./internal/command -run TestDataKline)`

Expected: FAIL，因为命令不存在。

- [ ] **Step 6: 实现 RPC 命令**

```go
options := gatewayauth.NewTRPCClientOptions(cfg.Gateway.Target, cfg.Gateway.TargetNode, gatewayauth.Credentials{
    KeyID: cfg.Gateway.KeyID, Caller: cfg.Gateway.Caller, Secret: cfg.Gateway.Secret,
})
reader := pb.NewPrimaryStoreClientProxy(options...)
```

请求使用配置中的 Storage `app_id/app_key`。成功 protobuf JSON 写 `cmd.OutOrStdout()` 或原子 `0600` 文件；业务错误返回非零，空 rows 返回成功。

- [ ] **Step 7: 运行 CLI 绿灯**

Run: `(cd modules/cli && go test -count=1 ./internal/command -run 'Test(DataAccessConfig|DataKline)')`

Expected: PASS。

### Task 3: `setup export-skill-config`

**Files:**
- Create: `modules/cli/internal/command/setup_skill_config.go`
- Create: `modules/cli/internal/command/setup_skill_config_test.go`
- Modify: `modules/cli/internal/command/setup.go`
- Modify: `modules/cli/internal/command/setup_test.go`

- [ ] **Step 1: 写失败的导出测试**

为 `setupDeps` 增加 `exportSkillConfig` 注入点。覆盖 flags/help、Space 精确匹配、target/node 缺失、远端 key/root secret 缺失、stdout 不泄密、目标 symlink、输出 `0600`、原子失败和 `VerifyUnchanged()`。

- [ ] **Step 2: 运行红灯**

Run: `(cd modules/cli && go test -count=1 ./internal/command -run 'TestSetup(Help|ExportSkillConfig)')`

Expected: FAIL，因为子命令不存在。

- [ ] **Step 3: 实现安全导出**

从 `SCFFetcher.Spaces` 读取 `StorageRPCGatewayTarget/StorageGatewayNodeID`，通过 setup SSH 读取固定路径 `gateway-moox-skill.key` 和 `storage-internal-auth.env`。只在内存中派生：

```go
storageAppKey := security.HMACSHA256Hex(primaryRootSecret, []byte("moox-skill"))
```

使用临时文件、sync、`chmod 0600`、rename 写配置；stdout 只报告 status/output。

- [ ] **Step 4: 运行 setup 绿灯**

Run: `(cd modules/cli && go test -count=1 ./internal/command ./internal/setup/...)`

Expected: PASS。

### Task 4: Skill staging 打包和自然语言入口

**Files:**
- Modify: `scripts/build/package-skill.sh`
- Create: `scripts/build/package-skill_test.sh`
- Modify: `Makefile`
- Modify: `skills/moox/SKILL.md`
- Create: `skills/moox/references/data-query.md`
- Create: `skills/moox/scripts/test/contract/test-data-query-contract.sh`

- [ ] **Step 1: 写失败的打包契约**

fake CLI 生成 `0600` marker 配置；断言参数、archive/config 权限、清单、不含 `moox.toml`/root secret/SSH/腾讯云 marker，失败不覆盖旧包并清理 staging。

- [ ] **Step 2: 运行红灯**

Run: `bash scripts/build/package-skill_test.sh`

Expected: FAIL，因为当前脚本直接 tar 且 archive 为 `0644`。

- [ ] **Step 3: 实现 staging 和原子打包**

支持 `CONFIG/MOOX_CLI/OUT` 环境覆盖；默认使用根 `moox.toml`、`bin/moox-cli`、`dist/moox-skill.tar.gz`。流程为 mktemp、trap、复制 Skill、调用导出、校验 `0600`、临时 tar、chmod `0600`、原子 mv。

- [ ] **Step 4: 写文档契约并实现 Skill 文档**

`data-query.md` 说明 data type 必填、exchange 默认、symbol 原样、目录精确匹配、limit/time flags、CLI 查找顺序、结果总结和禁止输出凭据；`SKILL.md` 链接它。

- [ ] **Step 5: 运行脚本绿灯**

Run: `bash scripts/build/package-skill_test.sh && bash skills/moox/scripts/test/contract/test-data-query-contract.sh && bash -n scripts/build/package-skill.sh scripts/build/package-skill_test.sh skills/moox/scripts/test/contract/test-data-query-contract.sh`

Expected: PASS。

### Task 5: 模块 E2E、本地验证和构建

**Files:**
- Create: `modules/cli/test/kline_rpc_e2e_test.go`
- Modify: implementation files only when tests expose contract gaps

- [ ] **Step 1: 增加模块 E2E**

在 `modules/cli/test` 启动真实或可控 Native Gateway 链路，注册只允许 `moox-skill/ReadTimeSeriesRows` 的 route，证明 CLI 经 Gateway HMAC、ACL 和 Storage AuthInfo 返回行；同一凭据调用写方法必须失败。

- [ ] **Step 2: 运行 E2E**

Run: `(cd modules/cli && go test -count=1 ./test -run TestKlineRPC)`

Expected: PASS。

- [ ] **Step 3: 运行完整本地证明集**

Run: `(cd modules/cli && go test -count=1 ./internal/command ./internal/setup/... ./test) && (cd modules/admin && go test -count=1 ./internal/service/sysdeploy ./cmd/cli) && (cd modules/gateway && go test -count=1 ./internal/router/...) && bash scripts/build/package-skill_test.sh && bash skills/moox/scripts/test/contract/test-data-query-contract.sh && ./scripts/test/contract/test-go-workspace.sh && git diff --check`

Expected: 全部 PASS。

- [ ] **Step 4: 构建发布制品**

Run: `make build && make release-binaries`

Expected: CLI、Admin、Gateway 和 Linux 发布制品成功生成。

### Task 6: 全新 `codeCR` 和修复闭环

- [ ] **Step 1: 启动新的 `codeCR` Agent**

审查相对 `f327465f` 的全部实现，重点检查只读权限、secret 泄露、原子写、RPC、registry 升级、测试与部署假设；结论附文件、符号或行号。

- [ ] **Step 2: 主 Agent 独立核验并修复**

每个 finding 标记“已修改”或“不适用”，并给出调用链或测试依据。

- [ ] **Step 3: 重跑 Task 5 全部证明**

Expected: 修复后全部 PASS，不复用审查前结果。

### Task 7: 提交推送、正式发布和真实 E2E

- [ ] **Step 1: 提交并推送**

Run: `git add modules/admin modules/cli config/setup/service-deployments.yaml scripts/deploy/deploy-moox.sh scripts/build/package-skill.sh scripts/build/package-skill_test.sh scripts/test/contract Makefile skills/moox docs/superpowers && git commit -m 'feat(skill): 增加 K 线 RPC 查询能力' && git push`

Expected: `git rev-parse HEAD` 与 `git rev-parse origin/feature/mooyang` 一致。

- [ ] **Step 2: 核验正式主机并发布**

Run: `./bin/moox-cli setup validate --file ./moox.toml && ./bin/moox-cli setup hosts --file ./moox.toml && ./bin/moox-cli setup deploy-control --file ./moox.toml`

依据 `crypto.storage_gateway_node_id` 与脱敏 host 输出，若 Storage Gateway 不在 control，执行 `./bin/moox-cli setup deploy-storage --file ./moox.toml --host "$MOOX_STORAGE_HOST"`。禁止 reset 数据。

- [ ] **Step 3: 用正式配置打包**

Run: `make package-skill && stat -f '%Lp %N' dist/moox-skill.tar.gz && tar -tzf dist/moox-skill.tar.gz`

Expected: archive 为 `600`，包含 `moox/config/data-access.yaml`，不含 `moox.toml`。

- [ ] **Step 4: 从打包产物执行真实查询**

```bash
./bin/moox-cli data kline get \
  --config "$MOOX_SKILL_UNPACKED/moox/config/data-access.yaml" \
  --data-type crypto --exchange binance \
  --symbol BTC-USDT --interval 1m --limit 5
```

Expected: exit 0、`ret_info.code=SUCCESS`、rows 非空；所有行属于 `crypto/dataset_binance_spot_kline_1m/BTC-USDT/1m` 和 `venue:binance`，时间 DESC。

- [ ] **Step 5: 完成审计**

确认本地测试、模块 E2E、codeCR、构建、提交推送、正式部署、Skill 归档和真实查询均有当前证据，并检查正式 readiness 与新鲜 BTC-USDT 数据。只有全部成立才宣告完成。
