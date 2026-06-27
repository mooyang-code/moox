# 软删除字段重构：c_invalid → c_is_deleted

## 背景

原软删除约定：`c_invalid INTEGER NOT NULL DEFAULT 0`（0=有效，1=删除），Go 字段 `Invalid int`。
问题：`int` 零值与「未传」不可区分，前端过滤传 0 时后台难以判断是「显式要有效」还是「不过滤」。

## 新约定

| 项 | 旧 | 新 |
| --- | --- | --- |
| 列名 | `c_invalid` | `c_is_deleted` |
| 类型 | `INTEGER` | `TEXT` |
| 默认值 | `0` | `'false'` |
| 有效 | `0` | `'false'` |
| 已删除 | `1` | `'true'` |
| Go 字段 | `Invalid int` | `IsDeleted string` |
| JSON 字段 | `invalid` / `Invalid` | `is_deleted` / `IsDeleted` |
| 常量 | `model.InvalidNo=0 / InvalidYes=1`（分散两处） | `common.IsDeletedFalse="false" / IsDeletedTrue="true"`（收口到 `modules/admin/internal/common`） |

语义翻转：原 `c_invalid = 0`（有效）→ `c_is_deleted = 'false'`（有效）；原 `c_invalid = 1`（删除）→ `c_is_deleted = 'true'`（删除）。

查询约定：过滤「有效」记录统一用 `c_is_deleted != 'true'`（参数化 `c_is_deleted != ?` + `common.IsDeletedTrue`），不再用 `= 'false'`；删除操作置 `c_is_deleted = 'true'`。显式按精确值过滤（如列表查已删除）仍用 `= ?`。

proto 层 `TaskInstanceFilter.is_deleted` 为 `string`：空串=不过滤，`"false"`=只看有效，`"true"`=只看已删除（取代旧的 `optional int32`）。

## 影响范围

- **DB schema**：`modules/admin/schema/admin.sql`（10 张表）、`modules/trade/schema/{account,order}.sql`
- **admin Go**：model / dao / 业务层 / 常量（auth、space、cloudnode、collectmgr）
- **admin proto**：`collect_service.proto` 的 `invalid` 字段（6 处）→ `is_deleted string`，已重生成 `admingen`
- **collector**：`pkg/config/task_instance.go` 缓存结构 + 心跳日志
- **cli**：`internal/adminclient/cloudnode.go` 的 `CloudAccount` + 防火墙命令过滤
- **前端**：`web/src/views/collector/task-instances/task-instances.vue`
- **文档/配置**：`docs/采集任务管理.md`、`modules/admin/alert.md`、`modules/trade/DESIGN.md`、`modules/cli/config/collector.yaml`

## 开发库迁移（开发库可直接重建）

SQLite 不支持直接 `ALTER COLUMN` 改类型/改名，开发库可重建：

1. 停服务（moox-admin / moox-storage / moox-web-host）。
2. 删除本地 SQLite 库文件（admin / trade 各自的 `*.db` / `data/*.db`），或手动 `DROP TABLE` 涉及表。
3. 重启服务，由 GORM AutoMigrate + 启动建表（`schema.AllSQL()` 等）按新 schema 重建。
4. 重新导入种子数据（如 `collector.yaml` 中的数据类型/字段配置——已同步改为 `c_is_deleted: 'false'`）。

> 生产库若有数据需保留，需走「建新表 → 拷贝并翻转值（`c_invalid=0`→`'false'`，`c_invalid=1`→`'true'`）→ 删旧表 → 改名」的标准 SQLite 表重建流程，并同步重建含 `c_is_deleted` 的唯一/普通索引。

## 验证

- 全模块 `go build ./...` 通过（admin / collector / cli / trade / storage / factor）。
- 改动模块 `go test ./...` 通过（软删除相关：keepalive_account_test、impl_task_planner_e2e_test、space service_test、executor_test 等）。
- 既有 `TestOnlyExpectedSQLSchemaFilesExist` 失败与本重构无关（`modules/trade/` 为未跟踪 WIP，schema 清单测试未纳入）。
