# MooX 代码审查问题修复执行计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 修复本次代码审查发现的事实源分裂、增量采集缺失、任务租约配置不一致、批量写入结果不可追踪、请求体无上限、CI 覆盖不足及发布不可复现等问题，在保持个人单用户产品定位的前提下提高系统正确性和可维护性。

**Architecture:** 以现有模块边界和 tRPC 接口为基础，不引入多租户 RBAC、分布式租约续期等超出当前使用场景的复杂机制。Trade 统一以 Kernel 聚合表为权威事实源；Collector 通过 Storage 水位线增量补数；CloudNode 用配置约束保证任务处理窗口；Storage 明确返回部分成功结果；Admin Gateway 在统一入口限制请求体。

**Tech Stack:** Go、tRPC-Go、Protocol Buffers、SQLite、NATS JetStream、React/TypeScript、pnpm、GitHub Actions

---

## 实施原则

- 本站只有一个登录用户，登录成功即视为超级管理员；不新增 `SUPER_ADMIN` 角色、权限表、RBAC 中间件或资源级授权。
- 保留现有登录、会话、签名校验和密钥保护，不能把“单用户”误解为“无认证”。
- 每个任务先补失败测试，再做最小实现，最后运行模块测试和静态检查。
- 当前主工作树已有未提交改动。执行时先创建独立 worktree，不得暂存、覆盖或提交现有用户改动。
- 每项任务单独提交，便于回滚和审查；跨模块协议变更必须同时提交生成代码、调用方和测试。
- 不顺手引入高可用、多实例协调、细粒度权限等当前没有实际需求的设计。

## Task 0：建立隔离工作区和验证基线

**Files:**
- Verify: `go.work`
- Verify: `Makefile`
- Verify: `package.json`
- Verify: `pnpm-lock.yaml`

- [ ] **Step 1：记录当前工作树状态**

Run:

```bash
git status --short --branch
git rev-parse HEAD
git rev-parse origin/main
```

Expected: 明确记录已有未提交文件；后续不得把这些文件带入计划提交。

- [ ] **Step 2：从当前 main 创建隔离 worktree**

Run:

```bash
git fetch origin
git worktree add ../moox-review-remediation -b fix/code-review-remediation origin/main
cd ../moox-review-remediation
```

Expected: 新工作区干净，分支基于最新 `origin/main`。

- [ ] **Step 3：运行基线验证**

Run:

```bash
go test -count=1 ./...
go vet ./...
CI=true pnpm install --frozen-lockfile
pnpm test
pnpm build
```

Expected: 记录现有失败项。已知 `go vet` 可能在 Monitor protobuf 锁复制处失败，该问题在 Task 7 修复；不得把既有失败误判为新回归。

## Task 1：统一 Trade 同步与查询的权威事实源

**Files:**
- Create: `modules/trade/internal/application/reconciliation/scope.go`
- Create: `modules/trade/internal/application/reconciliation/scope_test.go`
- Modify: `modules/trade/internal/bootstrap/kernel_workers.go`
- Modify: `modules/trade/internal/rpc/server.go`
- Modify: `modules/trade/internal/rpc/server_test.go`
- Modify: `modules/trade/internal/application/handlers/fill_handler.go`

- [ ] **Step 1：增加限定范围的对账测试**

覆盖以下行为：

- 指定账户、渠道和时间范围时，只拉取该范围内的成交。
- 重复执行同步不会生成重复 Fill。
- 单次同步有明确上限，不会因远端返回异常而无限运行。
- 同步完成后，`ListTrades` 能立即读取同一条 Kernel Fill。

Run:

```bash
go test -count=1 ./modules/trade/internal/application/reconciliation ./modules/trade/internal/rpc
```

Expected: 新测试先失败，证明现有 `SyncTrades` 写入旧表而 `ListTrades` 读取 Kernel 表。

- [ ] **Step 2：抽取可复用的限定范围对账入口**

从现有 `reconcileOrdersOnce` 中抽取按账户、渠道、时间范围执行的应用服务。同步结果统一通过现有 `FillHandler` 写入 Kernel，不直接写 `t_trades`。

约束：

- 明确最大页数或最大记录数。
- Context 取消后立即停止。
- 保留幂等键和现有去重语义。
- 错误中包含账户、渠道和同步范围，但不记录凭据。

- [ ] **Step 3：改造 RPC 同步入口**

让 `SyncOrders`、`SyncTrades` 调用新的 Kernel 对账入口，并统一返回同步数量与错误。修正 `ListOrders`、`ListTrades` 的过滤、排序和分页，使同步与查询针对同一组聚合表。

- [ ] **Step 4：验证 Trade 闭环**

Run:

```bash
go test -count=1 ./modules/trade/...
go vet ./modules/trade/...
```

Expected: 同步、查询和后台 worker 都使用 Kernel 路径；重复同步测试通过。

- [ ] **Step 5：提交**

```bash
git add modules/trade
git commit -m "fix(trade): unify sync and query on kernel facts"
```

## Task 2：删除 Trade 旧事实写入路径

**Files:**
- Modify: `modules/trade/internal/service/service.go`
- Modify: `modules/trade/internal/repository/models.go`
- Modify: `modules/trade/internal/repository/schema.go`
- Modify: `modules/trade/internal/repository/schema_test.go`
- Modify: `modules/trade/internal/rpc/server.go`
- Delete: 与旧 `t_orders`、`t_trades`、`t_positions` 事实写入直接相关且已无调用的实现文件

- [ ] **Step 1：增加 schema 和调用路径测试**

测试必须确认：

- 新数据库不再创建旧订单、成交、持仓事实表。
- 账户、渠道、API 凭据和交易所桥接相关表仍然存在。
- RPC 和 worker 不再引用旧事实仓储方法。

- [ ] **Step 2：删除旧写入逻辑**

删除旧 `SyncOrders`、`SyncTrades`、`ApplyFills` 及其仅服务于旧事实表的 model/repository 方法。不要删除账户配置、渠道配置、凭据管理和交易所客户端。

- [ ] **Step 3：确认没有双写或隐式回退**

Run:

```bash
rg -n 't_orders|t_trades|t_positions|ApplyFills' modules/trade
go test -count=1 ./modules/trade/...
go vet ./modules/trade/...
```

Expected: 搜索结果只允许出现在明确的迁移说明或兼容性测试中，运行时代码无旧事实路径。

- [ ] **Step 4：提交**

```bash
git add modules/trade
git commit -m "refactor(trade): remove legacy fact storage path"
```

## Task 3：为 K 线采集增加 Storage 水位线和分页补数

**Files:**
- Create: `modules/collector-stock-cn/internal/collector/kline_cursor.go`
- Create: `modules/collector-stock-cn/internal/collector/kline_cursor_test.go`
- Modify: `modules/collector-stock-cn/internal/collector/kline.go`
- Modify: `modules/collector-stock-cn/internal/config/config.go`
- Modify: `modules/collector-stock-cn/config/trpc_go.yaml`
- Modify: `modules/collector-stock-cn/internal/collector/kline_test.go`

- [ ] **Step 1：编写水位线与分页测试**

覆盖：

- Storage 已有数据时，从最新 `data_time` 后开始采集。
- 首次采集最多回看 1000 条。
- 单轮追赶最多 5000 条，每页最多 1000 条。
- 页面重叠数据依靠稳定幂等键去重。
- 空页面、重复页面、Context 取消和远端错误能正确终止。

- [ ] **Step 2：实现最新水位线查询**

通过 Storage `ReadTimeSeriesRows` 按 `data_time DESC`、`size=1` 查询当前标的和周期的最新记录。不要直接访问 Storage 数据库。

- [ ] **Step 3：实现分页补数**

新增配置：

```yaml
kline:
  initial_limit: 1000
  max_catchup_rows: 5000
  page_size: 1000
```

按水位线逐页请求数据，写入成功后再推进内存游标。禁止继续使用固定“最新 5 根”作为正常增量策略。

- [ ] **Step 4：验证 Collector**

Run:

```bash
go test -count=1 ./modules/collector-stock-cn/...
go vet ./modules/collector-stock-cn/...
```

- [ ] **Step 5：提交**

```bash
git add modules/collector-stock-cn
git commit -m "fix(collector): collect kline data from storage watermark"
```

## Task 4：修正 CloudNode 任务租约配置

**Files:**
- Modify: `modules/cloudnode/internal/config/config.go`
- Modify: `modules/cloudnode/internal/config/config_test.go`
- Modify: `modules/cloudnode/internal/app/app.go`
- Modify: `modules/cloudnode/config/trpc_go.yaml`
- Modify: `modules/cloudnode/internal/jobqueue/jetstream_queue_test.go`

- [ ] **Step 1：增加配置校验失败测试**

定义简单规则：

```text
ack_wait >= recover_after + 2 minutes
```

测试当前 `ack_wait=120000ms`、`recover_after=600000ms` 必须启动失败，并返回可操作的错误信息。

- [ ] **Step 2：实现校验并调整默认值**

把默认 `ack_wait` 调整为 `720000ms`，在创建 JetStream consumer 前调用 `Config.Validate()`。本阶段不新增 `InProgress` RPC 或心跳续租协议。

- [ ] **Step 3：增加短时重投集成测试**

用测试级短时配置验证：超过 ack wait 且未 ack 的消息会再次投递，`attempt_no` 递增为 2，成功 ack 后不再投递。

- [ ] **Step 4：验证 CloudNode**

Run:

```bash
go test -count=1 ./modules/cloudnode/...
go vet ./modules/cloudnode/...
```

- [ ] **Step 5：提交**

```bash
git add modules/cloudnode
git commit -m "fix(cloudnode): validate worker lease timing"
```

## Task 5：让 Storage 批量写入返回已提交键

**Files:**
- Modify: `modules/storage/proto/storage.proto`
- Modify: `modules/storage/proto/storage.pb.go`
- Modify: `modules/storage/proto/storage.trpc.go`
- Modify: `modules/storage/internal/rpc/data.go`
- Modify: `modules/storage/internal/rpc/data_test.go`
- Modify: `modules/storage/README.md`

- [ ] **Step 1：增加部分成功测试**

构造跨 target 批量写入：第一组提交成功，第二组失败。断言响应错误保留，同时 `written_keys` 返回第一组已经提交的幂等键。

同时覆盖全部成功和首组即失败两种情况。

- [ ] **Step 2：扩展协议**

在 `WriteTimeSeriesRowsRsp` 增加：

```proto
repeated string written_keys = 2;
```

字段号只能新增，不能复用或重排现有字段。

- [ ] **Step 3：实现逐组累计**

每个事务组成功提交后才追加该组键；后续组失败时，把已累计键与错误一起返回。对 Record 写接口检查并统一相同语义，避免失败时丢失已提交键。

- [ ] **Step 4：重新生成代码并验证**

使用仓库现有 protobuf 生成命令，禁止手改生成文件。

Run:

```bash
go test -count=1 ./modules/storage/...
go vet ./modules/storage/...
git diff --check
```

- [ ] **Step 5：提交**

```bash
git add modules/storage
git commit -m "feat(storage): report committed keys on partial writes"
```

## Task 6：统一限制 Admin Gateway 请求体

**Files:**
- Create: `modules/admin/internal/gateway/body.go`
- Create: `modules/admin/internal/gateway/body_test.go`
- Modify: `modules/admin/internal/gateway/gateway.go`
- Modify: `modules/admin/internal/gateway/request_auth.go`
- Modify: `modules/admin/internal/gateway/gateway_test.go`

- [ ] **Step 1：增加边界测试**

覆盖：

- 请求体小于或等于 4 MiB 时正常验签并转发。
- 超过 4 MiB 时返回 HTTP 413。
- 恰好 4 MiB 不误判。
- 验签和转发使用同一份字节，不发生二次读取或内容变化。

- [ ] **Step 2：实现统一读取函数**

使用 `io.LimitReader(body, maxBodyBytes+1)` 读取并判断超限，替换两处无上限 `io.ReadAll`。错误响应不得回显请求体或签名秘密。

- [ ] **Step 3：验证 Admin**

Run:

```bash
go test -count=1 ./modules/admin/...
go vet ./modules/admin/...
```

- [ ] **Step 4：提交**

```bash
git add modules/admin
git commit -m "fix(admin): bound gateway request bodies"
```

## Task 7：补齐统一验证入口和 CI

**Files:**
- Modify: `modules/monitor/internal/rpc/metrics_test.go`
- Create: `scripts/test-go-workspace.sh`
- Modify: `Makefile`
- Create: `.github/workflows/ci.yml`

- [ ] **Step 1：修复 Monitor vet 失败**

测试中不要按值复制包含 `protoimpl.MessageState` 的 protobuf 消息，改用 `proto.Clone` 或仅比较需要的字段。

Run:

```bash
go test -count=1 ./modules/monitor/...
go vet ./modules/monitor/...
```

- [ ] **Step 2：增加 Go workspace 验证脚本**

脚本从 `go.work` 读取模块列表，对每个模块执行：

```bash
go test -count=1 ./...
go vet ./...
```

要求：

- 任一模块失败立即退出非零。
- 输出当前模块路径，便于定位。
- 不依赖本机临时 `go.work.codex`。
- 不跳过 packages 或独立工具模块。

- [ ] **Step 3：增加 Makefile 入口**

至少提供：

```make
test
verify
```

其中 `verify` 串联 Go 测试、Go vet、前端测试和前端生产构建。

- [ ] **Step 4：新增 GitHub Actions**

CI 使用固定 Go/Node/pnpm 大版本，执行：

```bash
CI=true pnpm install --frozen-lockfile
make verify
```

开启依赖缓存，但不得缓存测试结果来替代实际执行。

- [ ] **Step 5：本地运行完整验证**

Run:

```bash
make verify
git diff --check
```

Expected: 所有 workspace module、前端测试和生产构建通过。

- [ ] **Step 6：提交**

```bash
git add modules/monitor scripts/test-go-workspace.sh Makefile .github/workflows/ci.yml
git commit -m "ci: verify all modules and frontend builds"
```

## Task 8：让发布流程可复现并同步实际产品文档

**Files:**
- Modify: `scripts/release.sh`
- Modify: `scripts/deploy-moox.sh`
- Modify: `web-host/Makefile`
- Modify: 发布配置清单文件
- Modify: `README.md`
- Modify: `docs/architecture.md`
- Modify: `docs/deployment.md`
- Modify: 本次审查报告或状态文档

- [ ] **Step 1：增加发布脚本静态测试**

验证：

- 不存在 `pnpm install --no-frozen-lockfile`。
- 不存在 `statik@latest`。
- Trade 配置包含在发布包清单中。
- 构建失败会立即退出，不生成看似成功的不完整产物。

- [ ] **Step 2：固定依赖安装**

前端统一使用：

```bash
CI=true pnpm install --frozen-lockfile
```

`statik` 固定为 `github.com/rakyll/statik@v0.1.7`，在脚本和 Makefile 中只保留一个可维护的版本来源或使用完全相同的固定版本。

- [ ] **Step 3：补齐 Trade 发布配置**

把 Trade 的实际运行配置加入 release artifact 和部署拷贝清单，增加发布包内容断言，避免二进制存在但配置缺失。

- [ ] **Step 4：更新文档边界**

文档必须明确：

- MooX 是个人单用户系统，唯一登录用户拥有全部管理能力。
- 不计划实现 `SUPER_ADMIN` 角色表或 RBAC；安全边界是登录、会话和请求签名。
- 模块清单包含当前实际存在的 Archive、Strategy、Factor 和共享 packages。
- Factor 状态按当前实现更新，不再描述为占位模块。
- 构建、发布、部署命令与脚本实际行为一致。
- 旧审查报告追加 `2026-07-14` 状态说明，不直接改写历史结论。

- [ ] **Step 5：验证发布与文档**

Run:

```bash
bash -n scripts/release.sh scripts/deploy-moox.sh
make verify
rg -n 'statik@latest|--no-frozen-lockfile|SUPER_ADMIN' scripts web-host README.md docs
git diff --check
```

Expected: 发布脚本不含浮动依赖；文档仅在解释“不采用 RBAC”时出现 `SUPER_ADMIN`。

- [ ] **Step 6：提交**

```bash
git add scripts/release.sh scripts/deploy-moox.sh web-host/Makefile README.md docs
git commit -m "build: make releases reproducible and refresh docs"
```

## 最终验收

- [ ] **Step 1：运行全量验证**

```bash
make verify
git diff --check
git status --short
```

Expected: 测试、vet、前端构建全部通过，工作区无未提交文件。

- [ ] **Step 2：检查关键架构约束**

```bash
rg -n 't_orders|t_trades|t_positions|ApplyFills' modules/trade
rg -n 'latest 5|limit.?5' modules/collector-stock-cn
rg -n 'io\.ReadAll' modules/admin/internal/gateway
rg -n 'statik@latest|--no-frozen-lockfile' scripts web-host
rg -n 'SUPER_ADMIN|RBAC' modules
```

Expected:

- Trade 没有旧事实写入路径。
- Collector 不再固定只取 5 根 K 线。
- Gateway 没有绕过统一上限的请求体读取。
- 发布依赖固定。
- 业务代码没有新增角色/RBAC 体系。

- [ ] **Step 3：执行模块级冒烟测试**

至少验证一次真实链路：

1. 登录 Admin。
2. 触发 Trade 同步。
3. 在 Trade 列表读取刚同步的订单和成交。
4. 触发 K 线 Collector 两次，确认第二次从水位线增量运行且无重复。
5. 构造一次 Storage 跨 target 部分失败，确认返回已提交键。
6. 提交超过 4 MiB 的 Admin 请求，确认 HTTP 413。

- [ ] **Step 4：合并与推送**

```bash
git log --oneline origin/main..HEAD
git push -u origin fix/code-review-remediation
```

发起合并前检查每个任务提交范围，确保没有带入原主工作树中的未提交文件。合并到 `main` 后再次运行 `make verify`，并用 `git status --short --branch` 确认本地与远端同步。

## 暂不实施

以下事项与当前单用户、个人部署场景不匹配，本轮明确不做：

- `SUPER_ADMIN`、普通管理员、访客等角色模型。
- 权限表、菜单权限、资源级 ACL 或 RBAC 中间件。
- CloudNode 心跳续租 RPC、多 worker 协调或分布式锁。
- 多租户数据隔离。
- 为理论扩展提前拆分新的微服务。

只有在出现第二个真实用户、多实例并发执行或明确的权限隔离需求后，再单独立项评估。
