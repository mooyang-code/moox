# 字段管理工作台验证记录

日期：2026-07-15

## 验证范围

- 字段组两级模型、字段查询、聚合计数、批量更新和受限删除。
- Metadata protobuf、SQLite、缓存刷新、Storage service 和 Admin gateway 转发契约。
- 量化初始元数据 YAML、CLI dry-run 和空库导入。
- 字段管理工作台桌面端、窄屏、右侧编辑抽屉和 Space 切换保护。

## 后端验证

```bash
GOWORK=/tmp/moox-field-work.Cg662u/go.work go test -count=1 ./...
```

在 `modules/storage` 执行，全部通过。覆盖字段查询、LIKE 字面搜索、两级字段组、事务批量更新、删除约束、Create/Update 语义、Space header 一致性、空库量化 seed 导入等。

```bash
GOWORK=/tmp/moox-field-work.Cg662u/go.work go test -count=1 ./internal/gateway ./internal/service/sysdeploy
```

在 `modules/admin` 执行，全部通过。验证 `storage_metadata` 部署的 `20200` 端口、`trpc.moox.storage.Metadata` 服务路径，以及字段 RPC 的最终转发 URL。

```bash
GOWORK=/tmp/moox-field-work.Cg662u/go.work go test -count=1 ./internal/command
```

在 `modules/cli` 执行，全部通过。覆盖字段组先于字段导入、兼容默认组，以及重复组、缺失父组和第三级分组的预校验。

## 初始元数据

```bash
GOWORK=/tmp/moox-field-work.Cg662u/go.work go run ./cmd/moox-cli metadata import \
  --file ../../examples/metadata-quant-initial.seed.yaml --dry-run
```

结果为 `dry_run`，计划导入 312 项：7 个空间、8 个数据源、31 个数据集、25 个字段组、96 个字段、15 个因子、122 个数据集列和 8 个视图。

## 前端验证

```bash
pnpm exec eslint src/api/storage/http.ts src/api/storage/metadata.ts \
  src/views/data/fields/index.vue src/views/data/fields/components/*.vue \
  src/views/data/fields/field-workbench.ts \
  src/views/data/fields/field-workbench.test.ts \
  tests/field-management-workbench.e2e.spec.ts
pnpm exec vitest run
pnpm run build:prod
```

ESLint 通过；Vitest 11 个文件、28 个测试通过；生产构建通过。构建仅保留项目已有的 Browserslist、Sass legacy API、Lottie `eval` 和大 chunk 警告。

```bash
CI=true pnpm exec playwright test \
  tests/field-management-workbench.e2e.spec.ts --project=chromium
```

覆盖桌面治理流程、390px 窄屏抽屉和存在未保存编辑时的 Space 切换确认，并生成桌面和移动端截图。

## 独立审查

独立审查首先发现 Space 上下文、陈旧请求回填、Create/Update upsert、缓存刷新错误语义、更新时间、业务错误提示、seed 层级校验、窄屏和网关证明等问题。逐项修复后又补充了原子 Create/Update 和数据库级两层字段组触发器；最终复审结论为“无 P1/P2”。

## 已知边界

CLI 通过多个 RPC 导入整份 YAML，现有 Metadata API 不提供跨资源分布式事务。导入前会完整解析和验证本地关系，但运行期间的网络或服务故障仍可能造成部分导入；初始化应在空库执行，重试时使用 `--if-not-exists`。
