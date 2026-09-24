# 数据对象标签化与标的采集器独立 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal：** 用 `t_tags` / `t_subject_tags` 替代 `t_subject_symbols`、`t_dataset_subjects` 与 staging 机制；新增独立二进制 `moox-collector-subject` 按 cron 维护标签成员与标的公共属性；采集任务改为按标签选择标的。

**Architecture：** Storage 是唯一的数据面：新增标签定义、成员、快照应用、标的解析接口，`ListDatasetSubjects` 改为由 Dataset 的 `subject_tags` 派生。`moox-collector-subject` 轮询 `t_tags`，复用 collector 内各抓取源的 `InstrumentFetcher` 拉取标的列表并调用 `ApplyTagSnapshot`。Collector 规划器每周期调用 `ResolveSubjects(dataset.subject_tags)`，由适配器 `ToSymbol` 换算代码。实施顺序为「先新增、再迁移调用方、最后删除」，保证每个任务结束时全仓编译和测试通过。

**Tech Stack：** Go 1.25 多模块（`go.work`）、tRPC-Go、protobuf（`trpc-open create`）、SQLite（modernc）、`github.com/robfig/cron v1.2.0`（`ParseStandard`）、Vue 3 + Arco Design + Vitest + pnpm。

**设计文档：** `docs/superpowers/specs/2026-09-24-subject-tags-design.md`

---

## 前置条件

- [ ] 工作区中 `modules/monitor/...`、`modules/cli/cmd/remoterun/main.go`、`modules/cli/internal/command/setup.go` 存在用户未提交的改动，其中 `modules/monitor/internal/bootstrap/market_canary.go` 与本计划 Task 6.2 冲突。开始前请用户先提交或确认这些改动，再从干净的工作区开始执行。
- [ ] 确认本地可用：`go version`（≥ 1.25）、`trpc-open`（`make proto` 使用）、`pnpm`、`sqlite3`。

## 约定

- 所有 Go 测试命令在仓库根目录执行，形如 `go test ./modules/storage/internal/service/metadata/sqlite/ -run TestXxx -count=1`；`go.work` 负责多模块解析。
- 每个任务结束时执行 `git diff --check`，再用任务末尾给出的 commit message 提交；提交时只 `git add` 本任务涉及的路径。
- 时间统一使用 UTC，入库格式 `2006-01-02 15:04:05`（与 SQLite `CURRENT_TIMESTAMP` 一致，才能直接比较 `c_mtime > c_last_run_at`）。
- 「验证无残留」步骤使用 `rg`，预期输出为空。

## 文件结构

| 路径 | 动作 | 职责 |
| --- | --- | --- |
| `modules/storage/schema/metadata.sql` | 修改 | v12：新增 `t_tags`、`t_subject_tags`、`t_datasets.c_subject_tags_json`；收敛 `t_subjects.c_status`；删除三张旧表 |
| `modules/storage/schema/metadata_schema_version_test.go` | 修改 | v12 契约测试 |
| `modules/storage/internal/service/metadata/sqlite/store.go` | 修改 | `migrateV11ToV12` |
| `modules/storage/internal/service/metadata/sqlite/migrate_v12_test.go` | 新增 | 迁移测试 |
| `modules/storage/proto/metadata.proto` | 修改 | `Tag`、`TagMember`、`TagSnapshotItem`、`SubjectAttributes`、`Dataset.subject_tags` 及新增 RPC；精简 `DatasetSubject`；删除旧 RPC |
| `modules/storage/internal/service/metadata/tag.go` | 新增 | `TagStore` 接口、查询结构、错误定义 |
| `modules/storage/internal/service/metadata/sqlite/crud_tag.go` | 新增 | 标签定义 CRUD 与校验 |
| `modules/storage/internal/service/metadata/sqlite/crud_tag_member.go` | 新增 | 成员接口、`ApplyTagSnapshot`、`ReportTagRunFailure`、`UpdateSubjectAttributes`、`ResolveSubjects`、派生的 `ListDatasetSubjects` |
| `modules/storage/internal/service/metadata/sqlite/crud_tag_test.go`、`crud_tag_member_test.go` | 新增 | 对应测试 |
| `modules/storage/internal/service/catalog/tag_catalog.go`（含 `_test.go`） | 新增 | RPC handler |
| `modules/storage/internal/bootstrap/metadata/seed.go`、`modules/cli/internal/command/metadata_*.go` | 修改 | `tags:` 种子段（仅在不存在时创建） |
| `config/setup/metadata.yaml` | 修改 | 内置标签种子；删除 record Dataset 与 `subject_symbols` / `dataset_subjects` 种子 |
| `modules/collector/internal/sources/crypto/subject.go` | 新增 | `crypto.SubjectID(base, quote)` |
| `modules/collector/internal/sources/stockcn/common.go` | 修改 | `CanonicalSubjectID` 重命名为 `SubjectID` |
| `modules/collector/internal/subjectsync/` | 新增 | 标签维护、属性维护的运行器与配置 |
| `modules/collector/cmd/subject/main.go` | 新增 | `moox-collector-subject` 入口 |
| `modules/collector/config/subject.yaml` | 新增 | 运行配置 |
| `modules/collector/internal/marketwiring/subject_listers.go` | 新增 | 组装各源 `InstrumentFetcher` |
| `modules/collector/internal/domain/collect_params.go` | 修改 | `subject_tags` 参数；删除 `symbol_source` / `symbol_dataset_id` |
| `modules/collector/internal/marketfetch/{scheduler,reconciler,symbol_resolver,timer}.go` | 修改 | 按标签解析标的；`ToSymbol` 不再接收 `configured` |
| `modules/collector/internal/planner/taskresult/result.go` | 修改 | `Config.SubjectTags` 写入 Dataset |
| `modules/collector/internal/rpc/service.go`、`bootstrap/bootstrap.go`、`ruleseed/seed.go` | 修改 | 从参数中剥离 `subject_tags` 并写入 Dataset |
| `modules/collector/internal/jobs/symbol/`、`marketfetch/instrument_pipeline*.go`、`storagesource` 兜底逻辑 | 删除 | 旧标的采集链路 |
| `modules/monitor/internal/bootstrap/market_canary.go` | 修改 | 按内置标签检查 |
| `modules/strategy/internal/storageio/rpc.go` | 修改 | 删除全量目录兜底 |
| `modules/gateway/.../native.go`、`modules/storage/internal/accessproxy/proxy.go`、`modules/admin/internal/gateway/storage_bff.go`、`modules/admin/internal/service/sysdeploy/defaults.go`、`web/src/api/storage/http.ts` | 修改 | RPC 方法登记 |
| `scripts/build/build.sh`、`scripts/deploy/deploy-moox.sh`、`config/setup/service-deployments.yaml` | 修改 | 新二进制构建与部署 |
| `web/src/api/storage/{metadata,types}.ts` | 修改 | 标签 API |
| `web/src/views/data/subjects/`（`index.vue`、`tags-tab.vue`、`members-tab.vue`、`tag-form.ts`） | 修改 / 新增 | 两个子 tab |
| `web/src/views/collector/collection-tasks/collection-task-params.ts`、`collection-tasks.vue` | 修改 | 标签多选 |
| `web/src/views/data/datasets/components/dataset-subject-panel.vue` | 删除 | — |

---

## 阶段 1：Storage 数据模型

### Task 1.1：schema v12

**Files：**
- Modify: `modules/storage/schema/metadata.sql`
- Modify: `modules/storage/schema/metadata_schema_version_test.go`

- [ ] **Step 1：修改契约测试（先失败）**

把测试函数重命名为 `TestMetadataSchemaV12Contract`，并调整 required / forbidden 列表：

```go
required := []string{
	"VALUES ('schema_version', '12')",
	"CREATE TABLE IF NOT EXISTS t_tags (",
	"CREATE TABLE IF NOT EXISTS t_subject_tags (",
	"c_subject_tags_json TEXT NOT NULL DEFAULT '[]'",
	"CHECK (c_mode IN ('auto', 'manual'))",
	"CHECK (c_status IN ('active', 'inactive'))",
	"CREATE TRIGGER IF NOT EXISTS trg_t_tags_mtime",
	"CREATE TRIGGER IF NOT EXISTS trg_t_subject_tags_mtime",
}
forbidden := []string{
	"ALTER TABLE",
	"t_subject_symbols",
	"t_dataset_subjects",
	"t_dataset_subject_set_staging",
}
```

保留现有的其他 required 项；把旧的 `'11'` 断言改成 `'12'`。

- [ ] **Step 2：运行测试确认失败**

Run: `go test ./modules/storage/schema/ -run TestMetadataSchemaV12Contract -count=1`
Expected: FAIL，提示缺少 `schema_version', '12'` 等。

- [ ] **Step 3：修改 schema**

1. `t_schema_meta` 的种子值改为 `'12'`。
2. `t_subjects` 的 CHECK 改为 `CHECK (c_status IN ('active', 'disabled')),`。
3. 删除 `t_subject_symbols` 表、`idx_t_subject_symbols_*` 两个索引、`trg_t_subject_symbols_mtime` 触发器。
4. 在 `t_subjects` 触发器之后插入：

```sql
-- 标签：Subject 的分组；auto 标签成员由 moox-collector-subject 按 cron 同步
CREATE TABLE IF NOT EXISTS t_tags (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_tag_id TEXT NOT NULL,
    c_tag_name TEXT NOT NULL,
    c_description TEXT NOT NULL DEFAULT '',
    c_mode TEXT NOT NULL,
    c_builtin INTEGER NOT NULL DEFAULT 0,
    c_sources_json TEXT NOT NULL DEFAULT '[]',
    c_instrument_type TEXT NOT NULL DEFAULT '',
    c_cron TEXT NOT NULL DEFAULT '0 * * * *',
    c_timezone TEXT NOT NULL DEFAULT 'UTC',
    c_last_run_at DATETIME NOT NULL DEFAULT '',
    c_last_status TEXT NOT NULL DEFAULT '',
    c_last_error TEXT NOT NULL DEFAULT '',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_mode IN ('auto', 'manual')),
    CHECK (c_builtin IN (0, 1)),
    CHECK (c_last_status IN ('', 'success', 'failed')),
    FOREIGN KEY (c_space_id) REFERENCES t_spaces (c_space_id) ON DELETE CASCADE ON UPDATE CASCADE,
    UNIQUE (c_space_id, c_tag_id),
    UNIQUE (c_space_id, c_tag_name)
);

-- 回写运行状态（c_last_run_at 变化）不刷新 c_mtime，c_mtime 只反映定义变更
CREATE TRIGGER IF NOT EXISTS trg_t_tags_mtime
AFTER UPDATE ON t_tags
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime AND NEW.c_last_run_at = OLD.c_last_run_at
BEGIN
    UPDATE t_tags SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

CREATE TABLE IF NOT EXISTS t_subject_tags (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_tag_id TEXT NOT NULL,
    c_subject_id TEXT NOT NULL,
    c_status TEXT NOT NULL DEFAULT 'active',
    c_inactive_at DATETIME NOT NULL DEFAULT '',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_status IN ('active', 'inactive')),
    FOREIGN KEY (c_space_id, c_tag_id) REFERENCES t_tags (c_space_id, c_tag_id) ON DELETE CASCADE ON UPDATE CASCADE,
    FOREIGN KEY (c_space_id, c_subject_id) REFERENCES t_subjects (c_space_id, c_subject_id) ON DELETE CASCADE ON UPDATE CASCADE,
    UNIQUE (c_space_id, c_tag_id, c_subject_id)
);

CREATE INDEX IF NOT EXISTS idx_t_subject_tags_status ON t_subject_tags (c_space_id, c_tag_id, c_status);
CREATE INDEX IF NOT EXISTS idx_t_subject_tags_subject ON t_subject_tags (c_space_id, c_subject_id);

CREATE TRIGGER IF NOT EXISTS trg_t_subject_tags_mtime
AFTER UPDATE ON t_subject_tags
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_subject_tags SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;
```

`t_tags` 要放在 `t_spaces` 之后、`t_subject_tags` 之前；`t_subject_tags` 要放在 `t_subjects` 之后。

5. `t_datasets` 在 `c_attrs_json` 之后新增一列：`c_subject_tags_json TEXT NOT NULL DEFAULT '[]',`。
6. 删除 `t_dataset_subjects` 表（连同两个索引和触发器）、`t_dataset_subject_set_staging` 表（连同其注释和索引）。

- [ ] **Step 4：运行契约测试与空库载入**

Run:
```bash
go test ./modules/storage/schema/ -count=1
for f in modules/*/schema/*.sql; do rm -f /tmp/schema_check.db; sqlite3 /tmp/schema_check.db < "$f" || echo "FAIL $f"; done
git diff --check
```
Expected: 测试 PASS；循环无 `FAIL` 输出；`git diff --check` 无输出。

此时 `sqlite` 包的编译与测试会失败（旧 CRUD 仍在引用旧表），Task 1.2 修复迁移，Task 1.4 起逐步替换。**本任务不单独提交**，与 Task 1.2 一起提交。

### Task 1.2：迁移 `migrateV11ToV12`

**Files：**
- Modify: `modules/storage/internal/service/metadata/sqlite/store.go`
- Create: `modules/storage/internal/service/metadata/sqlite/migrate_v12_test.go`

- [ ] **Step 1：编写迁移测试**

v11 库由当前 v12 schema「降级」构造：这样 `t_datasets` 等其余表结构完整，迁移后 `InitSchema` 执行 schema 文件时不会因缺列而失败。

```go
package sqlite

import (
	"context"
	"testing"
)

func TestMigrateV11ToV12(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	registerActiveNode(t, ctx, store, "node")
	createTestDataset(t, ctx, store, "dataset_binance_spot_symbols", "node")
	createTestDataset(t, ctx, store, "dataset_kline", "node")

	downgrade := []string{
		`PRAGMA foreign_keys = OFF`,
		`DROP TABLE t_subject_tags`,
		`DROP TABLE t_tags`,
		`ALTER TABLE t_datasets DROP COLUMN c_subject_tags_json`,
		`DROP TABLE t_subjects`,
		`CREATE TABLE t_subjects (
			c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT, c_space_id TEXT NOT NULL, c_subject_id TEXT NOT NULL,
			c_subject_type TEXT NOT NULL, c_name TEXT NOT NULL DEFAULT '', c_market TEXT NOT NULL DEFAULT '',
			c_currency TEXT NOT NULL DEFAULT '', c_timezone TEXT NOT NULL DEFAULT '', c_status TEXT NOT NULL DEFAULT 'active',
			c_attrs_json TEXT NOT NULL DEFAULT '{}', c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			CHECK (c_status IN ('active', 'disabled', 'building', 'archived', 'deleted')),
			UNIQUE (c_space_id, c_subject_id))`,
		`INSERT INTO t_subjects (c_space_id, c_subject_id, c_subject_type, c_status) VALUES ('space', 'BTC-USDT', 'crypto_pair', 'active'), ('space', 'LUNA-USDT', 'crypto_pair', 'archived')`,
		`CREATE TABLE t_subject_symbols (c_id INTEGER PRIMARY KEY)`,
		`CREATE TABLE t_dataset_subjects (c_id INTEGER PRIMARY KEY)`,
		`CREATE TABLE t_dataset_subject_set_staging (c_id INTEGER PRIMARY KEY)`,
		`UPDATE t_schema_meta SET c_value = '11' WHERE c_key = 'schema_version'`,
		`PRAGMA foreign_keys = ON`,
	}
	for _, stmt := range downgrade {
		if _, err := store.db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	if err := store.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}

	var version string
	if err := store.db.QueryRowContext(ctx, `SELECT c_value FROM t_schema_meta WHERE c_key = 'schema_version'`).Scan(&version); err != nil || version != "12" {
		t.Fatalf("version = %q, err = %v", version, err)
	}
	for _, table := range []string{"t_subject_symbols", "t_dataset_subjects", "t_dataset_subject_set_staging"} {
		var n int
		_ = store.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&n)
		if n != 0 {
			t.Fatalf("%s still exists", table)
		}
	}
	var status string
	if err := store.db.QueryRowContext(ctx, `SELECT c_status FROM t_subjects WHERE c_subject_id = 'LUNA-USDT'`).Scan(&status); err != nil || status != "disabled" {
		t.Fatalf("LUNA status = %q, err = %v", status, err)
	}
	var tags string
	if err := store.db.QueryRowContext(ctx, `SELECT c_subject_tags_json FROM t_datasets WHERE c_dataset_id = 'dataset_kline'`).Scan(&tags); err != nil || tags != "[]" {
		t.Fatalf("subject tags = %q, err = %v", tags, err)
	}
	var records int
	_ = store.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM t_datasets WHERE c_dataset_id = 'dataset_binance_spot_symbols'`).Scan(&records)
	if records != 0 {
		t.Fatal("record dataset should be removed")
	}
}
```

- [ ] **Step 2：运行确认失败**

Run: `go test ./modules/storage/internal/service/metadata/sqlite/ -run TestMigrateV11ToV12 -count=1`
Expected: 编译失败或 FAIL（`migrateV11ToV12` 未定义 / 版本仍为 11）。

- [ ] **Step 3：实现迁移**

`store.go`：`const metadataSchemaVersion = "12"`；在 `checkSchemaVersion` 的 v10 分支后追加：

```go
	if err == nil && version == "11" {
		if migrateErr := s.migrateV11ToV12(ctx); migrateErr != nil {
			return migrateErr
		}
		version = "12"
	}
```

新增函数（放在 `migrateV10ToV11` 之后）：

```go
// migrateV11ToV12 replaces symbol mappings and dataset membership with tags.
// t_tags / t_subject_tags are created by the schema file that InitSchema runs
// right after this migration.
func (s *Store) migrateV11ToV12(ctx context.Context) error {
	tx, err := beginImmediate(ctx, s.db)
	if err != nil {
		return fmt.Errorf("migrate metadata schema v11 to v12: %w", err)
	}
	defer tx.Rollback()
	hasColumn := false
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM pragma_table_info('t_datasets') WHERE name = 'c_subject_tags_json'`).Scan(&hasColumn); err != nil {
		return fmt.Errorf("migrate metadata schema v11 to v12: %w", err)
	}
	statements := []string{
		`DROP TABLE IF EXISTS t_dataset_subject_set_staging`,
		`DROP TABLE IF EXISTS t_dataset_subjects`,
		`DROP TABLE IF EXISTS t_subject_symbols`,
		`DELETE FROM t_datasets WHERE c_dataset_id IN ('dataset_binance_spot_symbols', 'dataset_binance_swap_symbols', 'dataset_stockcn_instruments')`,
		`UPDATE t_subjects SET c_status = 'disabled' WHERE c_status NOT IN ('active', 'disabled')`,
		`CREATE TABLE t_subjects_v12 (
			c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			c_space_id TEXT NOT NULL,
			c_subject_id TEXT NOT NULL,
			c_subject_type TEXT NOT NULL,
			c_name TEXT NOT NULL DEFAULT '',
			c_market TEXT NOT NULL DEFAULT '',
			c_currency TEXT NOT NULL DEFAULT '',
			c_timezone TEXT NOT NULL DEFAULT '',
			c_status TEXT NOT NULL DEFAULT 'active',
			c_attrs_json TEXT NOT NULL DEFAULT '{}',
			c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			CHECK (c_status IN ('active', 'disabled')),
			FOREIGN KEY (c_space_id) REFERENCES t_spaces (c_space_id) ON DELETE CASCADE ON UPDATE CASCADE,
			UNIQUE (c_space_id, c_subject_id)
		)`,
		`INSERT INTO t_subjects_v12 SELECT c_id, c_space_id, c_subject_id, c_subject_type, c_name, c_market, c_currency, c_timezone, c_status, c_attrs_json, c_ctime, c_mtime FROM t_subjects`,
		`DROP TABLE t_subjects`,
		`ALTER TABLE t_subjects_v12 RENAME TO t_subjects`,
	}
	if !hasColumn {
		statements = append(statements, `ALTER TABLE t_datasets ADD COLUMN c_subject_tags_json TEXT NOT NULL DEFAULT '[]'`)
	}
	statements = append(statements, `UPDATE t_schema_meta SET c_value = '12' WHERE c_key = 'schema_version'`)
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migrate metadata schema v11 to v12: %w", err)
		}
	}
	return tx.Commit()
}
```

`t_subjects` 的索引和触发器会随旧表一起删除，由随后执行的 schema 文件（`CREATE ... IF NOT EXISTS`）重建。重建前引用 `t_subjects` 的三张子表已删除，`DROP TABLE t_subjects` 不会触发外键级联。

- [ ] **Step 4：运行迁移测试**

Run: `go test ./modules/storage/internal/service/metadata/sqlite/ -run TestMigrateV11ToV12 -count=1`
Expected: PASS

`sqlite` 包此时仍能编译（旧 CRUD 依赖的是 proto 而非表），但 `crud_subject_set_test.go`、`crud_subject_test.go` 中涉及旧表的用例会失败，Task 1.5 删除这些用例；在此之前只运行本任务指定的测试。

- [ ] **Step 5：提交**

```bash
git add modules/storage/schema modules/storage/internal/service/metadata/sqlite/store.go modules/storage/internal/service/metadata/sqlite/migrate_v12_test.go
git commit -m "feat(storage): metadata schema v12 引入标签表并删除标的映射表"
```

### Task 1.3：proto 定义

**Files：**
- Modify: `modules/storage/proto/metadata.proto`
- Regenerate: `modules/storage/proto/storagegen/*`

- [ ] **Step 1：新增消息**

在 `Subject` 消息之后加入：

```protobuf
// Tag 是 Subject 的分组；auto 标签成员由 moox-collector-subject 同步。
message Tag {
  string space_id = 1;
  // tag_id 是 lower_snake_case 的稳定标识，创建后不可修改。
  string tag_id = 2;
  // tag_name 是 Space 内唯一的显示名称。
  string tag_name = 3;
  string description = 4;
  // mode 取值 auto 或 manual。
  string mode = 5;
  // builtin 表示系统初始化写入的内置标签，禁止删除。
  bool builtin = 6;
  // sources 是提供标的列表的抓取源；manual 标签为空表示不探测。
  repeated string sources = 7;
  // instrument_type 取值 spot、swap、equity、etf、index、convertible_bond。
  string instrument_type = 8;
  // cron 是标准 5 段 cron 表达式，按 timezone 解释。
  string cron = 9;
  string timezone = 10;
  string last_run_at = 11;
  // last_status 取值 ''、success、failed。
  string last_status = 12;
  string last_error = 13;
  uint32 active_count = 14;
  uint32 inactive_count = 15;
  string created_at = 16;
  string updated_at = 17;
}

// TagMember 是标签成员；ListTagMembers 的 tag_id 为空时 tag_id、status 为空，tag_ids 列出所属标签。
message TagMember {
  string space_id = 1;
  string tag_id = 2;
  // status 取值 active 或 inactive。
  string status = 3;
  string inactive_at = 4;
  Subject subject = 5;
  repeated string tag_ids = 6;
}

// TagSnapshotItem 是同步器提交的一条标的。
message TagSnapshotItem {
  string subject_id = 1;
  string subject_type = 2;
  string name = 3;
  string market = 4;
  string currency = 5;
  string timezone = 6;
}

// SubjectAttributes 是属性维护任务提交的一条标的属性。
message SubjectAttributes {
  string subject_id = 1;
  string name = 2;
  map<string, string> attributes = 3;
}

// TagReference 是引用某标签的 Dataset。
message TagReference {
  string dataset_id = 1;
  string dataset_name = 2;
  string collector_task_id = 3;
}
```

- [ ] **Step 2：修改已有消息**

- `Dataset` 追加 `repeated string subject_tags = 21;`（注释：数据集标的范围，取值为 tag_id）。
- `Subject.status` 注释改为「active 或 disabled；disabled 仅表示人工停用」。

本任务只做新增。`SubjectSymbol`、6 个旧 RPC 及其消息、`DatasetSubject` 的字段精简都有调用方，统一推迟到 Task 5.6 删除，保证每一步都能编译。

- [ ] **Step 3：新增请求/响应与 RPC**

```protobuf
message UpsertTagReq { common.AuthInfo auth_info = 1; Tag tag = 2; }
message UpsertTagRsp { common.RetInfo ret_info = 1; Tag tag = 2; }
message GetTagReq { common.AuthInfo auth_info = 1; string space_id = 2; string tag_id = 3; }
message GetTagRsp { common.RetInfo ret_info = 1; Tag tag = 2; }
message ListTagsReq { common.AuthInfo auth_info = 1; string space_id = 2; common.Page page = 3; }
message ListTagsRsp { common.RetInfo ret_info = 1; repeated Tag tags = 2; common.PageResult page_result = 3; }
message DeleteTagReq { common.AuthInfo auth_info = 1; string space_id = 2; string tag_id = 3; }
message DeleteTagRsp { common.RetInfo ret_info = 1; repeated TagReference references = 2; }
message ListTagMembersReq {
  common.AuthInfo auth_info = 1;
  string space_id = 2;
  string tag_id = 3;
  string status = 4;
  string keyword = 5;
  common.Page page = 6;
}
message ListTagMembersRsp { common.RetInfo ret_info = 1; repeated TagMember members = 2; common.PageResult page_result = 3; }
message TagMembersReq { common.AuthInfo auth_info = 1; string space_id = 2; string tag_id = 3; repeated string subject_ids = 4; }
message TagMembersRsp { common.RetInfo ret_info = 1; uint32 affected = 2; }
message SetTagMemberStatusReq { common.AuthInfo auth_info = 1; string space_id = 2; string tag_id = 3; repeated string subject_ids = 4; string status = 5; }
message ApplyTagSnapshotReq { common.AuthInfo auth_info = 1; string space_id = 2; string tag_id = 3; string run_at = 4; repeated TagSnapshotItem items = 5; }
message ApplyTagSnapshotRsp { common.RetInfo ret_info = 1; uint32 added = 2; uint32 activated = 3; uint32 inactivated = 4; }
message ReportTagRunFailureReq { common.AuthInfo auth_info = 1; string space_id = 2; string tag_id = 3; string run_at = 4; string error = 5; }
message ReportTagRunFailureRsp { common.RetInfo ret_info = 1; }
message UpdateSubjectAttributesReq { common.AuthInfo auth_info = 1; string space_id = 2; repeated SubjectAttributes items = 3; }
message UpdateSubjectAttributesRsp { common.RetInfo ret_info = 1; uint32 updated = 2; uint32 skipped = 3; }
message ResolveSubjectsReq { common.AuthInfo auth_info = 1; string space_id = 2; repeated string tag_ids = 3; }
message ResolveSubjectsRsp { common.RetInfo ret_info = 1; repeated Subject subjects = 2; }
```

`AddTagMembers` 与 `RemoveTagMembers` 共用 `TagMembersReq/Rsp`；`SetTagMemberStatus` 返回 `TagMembersRsp`。

在 `service Metadata` 中 `ListSubjects` 之后加入：

```protobuf
  rpc UpsertTag(UpsertTagReq) returns (UpsertTagRsp);
  rpc GetTag(GetTagReq) returns (GetTagRsp);
  rpc ListTags(ListTagsReq) returns (ListTagsRsp);
  rpc DeleteTag(DeleteTagReq) returns (DeleteTagRsp);
  rpc ListTagMembers(ListTagMembersReq) returns (ListTagMembersRsp);
  rpc AddTagMembers(TagMembersReq) returns (TagMembersRsp);
  rpc RemoveTagMembers(TagMembersReq) returns (TagMembersRsp);
  rpc SetTagMemberStatus(SetTagMemberStatusReq) returns (TagMembersRsp);
  rpc ApplyTagSnapshot(ApplyTagSnapshotReq) returns (ApplyTagSnapshotRsp);
  rpc ReportTagRunFailure(ReportTagRunFailureReq) returns (ReportTagRunFailureRsp);
  rpc UpdateSubjectAttributes(UpdateSubjectAttributesReq) returns (UpdateSubjectAttributesRsp);
  rpc ResolveSubjects(ResolveSubjectsReq) returns (ResolveSubjectsRsp);
```

- [ ] **Step 4：生成并编译**

Run:
```bash
make -C modules/storage/proto
go build ./modules/storage/... ./modules/collector/... ./modules/cli/... ./modules/gateway/... ./modules/admin/...
```
Expected: 生成成功。`go build` 会因 `catalog.Service` 尚未实现新 RPC 而通过（`pb.UnimplementedMetadata` 兜底），无编译错误。

- [ ] **Step 5：提交**

```bash
git add modules/storage/proto
git commit -m "feat(storage): metadata proto 新增标签与标的解析接口"
```

### Task 1.4：TagStore 接口与标签定义 CRUD

**Files：**
- Create: `modules/storage/internal/service/metadata/tag.go`
- Modify: `modules/storage/internal/service/metadata/store.go`（`Store` 嵌入 `TagStore`）
- Modify: `modules/storage/internal/service/metadata/sqlite/write_tx.go`（新增 `QueryContext`）
- Modify: `modules/storage/internal/retinfo/metadata.go`
- Create: `modules/storage/internal/service/metadata/sqlite/crud_tag.go`
- Create: `modules/storage/internal/service/metadata/sqlite/crud_tag_test.go`

- [ ] **Step 1：接口与错误**

`tag.go`：

```go
package metadata

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

const (
	TagModeAuto   = "auto"
	TagModeManual = "manual"

	TagMemberActive   = "active"
	TagMemberInactive = "inactive"
)

var (
	ErrTagInvalid      = errors.New("invalid tag")
	ErrTagBuiltin      = errors.New("builtin tag cannot be deleted")
	ErrTagReferenced   = errors.New("tag is referenced by datasets")
	ErrTagAutoMembers  = errors.New("auto tag members are maintained by moox-collector-subject")
)

// TagReferencedError 携带引用方，catalog 据此填充 DeleteTagRsp.references。
type TagReferencedError struct {
	TagID      string
	References []*pb.TagReference
}

func (e *TagReferencedError) Error() string {
	ids := make([]string, 0, len(e.References))
	for _, ref := range e.References {
		ids = append(ids, ref.GetDatasetId())
	}
	return fmt.Sprintf("tag %s is referenced by datasets: %s", e.TagID, strings.Join(ids, ","))
}

func (e *TagReferencedError) Is(target error) bool { return target == ErrTagReferenced }

type TagMemberQuery struct {
	SpaceID string
	TagID   string
	Status  string
	Keyword string
	Page    *pb.Page
}

type TagSnapshotResult struct {
	Added       int
	Activated   int
	Inactivated int
}

type TagStore interface {
	UpsertTag(ctx context.Context, item *pb.Tag) (*pb.Tag, error)
	GetTag(ctx context.Context, spaceID, tagID string) (*pb.Tag, error)
	ListTags(ctx context.Context, spaceID string, page *pb.Page) ([]*pb.Tag, *pb.PageResult, error)
	DeleteTag(ctx context.Context, spaceID, tagID string) error
	ListTagMembers(ctx context.Context, query TagMemberQuery) ([]*pb.TagMember, *pb.PageResult, error)
	AddTagMembers(ctx context.Context, spaceID, tagID string, subjectIDs []string) (int, error)
	RemoveTagMembers(ctx context.Context, spaceID, tagID string, subjectIDs []string) (int, error)
	SetTagMemberStatus(ctx context.Context, spaceID, tagID string, subjectIDs []string, status string) (int, error)
	ApplyTagSnapshot(ctx context.Context, spaceID, tagID string, runAt time.Time, items []*pb.TagSnapshotItem) (TagSnapshotResult, error)
	ReportTagRunFailure(ctx context.Context, spaceID, tagID string, runAt time.Time, message string) error
	UpdateSubjectAttributes(ctx context.Context, spaceID string, items []*pb.SubjectAttributes) (updated int, skipped int, err error)
	ResolveSubjects(ctx context.Context, spaceID string, tagIDs []string) ([]*pb.Subject, error)
}
```

`store.go` 的 `Store` 接口加入 `TagStore`。

`retinfo/metadata.go` 在 `ErrViewIndexBuildConflict` 判断之后加入：

```go
	if errors.Is(err, metadatastore.ErrTagInvalid) || errors.Is(err, metadatastore.ErrTagBuiltin) ||
		errors.Is(err, metadatastore.ErrTagReferenced) || errors.Is(err, metadatastore.ErrTagAutoMembers) {
		return pb.ErrorCode_INVALID_PARAM
	}
```

`write_tx.go` 新增：

```go
func (tx *immediateTx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return tx.conn.QueryContext(ctx, query, args...)
}
```

- [ ] **Step 2：编写失败测试**

`crud_tag_test.go`：

```go
package sqlite

import (
	"context"
	"errors"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func autoTag(id string) *pb.Tag {
	return &pb.Tag{SpaceId: "space", TagId: id, TagName: id + " 名称", Mode: "auto", Sources: []string{"binance"}, InstrumentType: "spot"}
}

func TestUpsertTagDefaultsAndValidation(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)

	got, err := store.UpsertTag(ctx, autoTag("binance_spot"))
	if err != nil {
		t.Fatal(err)
	}
	if got.GetCron() != "0 * * * *" || got.GetTimezone() != "UTC" || got.GetBuiltin() {
		t.Fatalf("defaults not applied: %+v", got)
	}

	cases := map[string]*pb.Tag{
		"bad id":               {SpaceId: "space", TagId: "Bad-ID", TagName: "x", Mode: "manual"},
		"bad mode":             {SpaceId: "space", TagId: "x1", TagName: "x1", Mode: "sync"},
		"auto without sources": {SpaceId: "space", TagId: "x2", TagName: "x2", Mode: "auto", InstrumentType: "spot"},
		"manual half probe":    {SpaceId: "space", TagId: "x3", TagName: "x3", Mode: "manual", Sources: []string{"binance"}},
		"bad instrument type":  {SpaceId: "space", TagId: "x4", TagName: "x4", Mode: "auto", Sources: []string{"binance"}, InstrumentType: "option"},
		"bad cron":             {SpaceId: "space", TagId: "x5", TagName: "x5", Mode: "manual", Cron: "* * *"},
		"bad timezone":         {SpaceId: "space", TagId: "x6", TagName: "x6", Mode: "manual", Timezone: "Mars/Base"},
	}
	for name, item := range cases {
		if _, err := store.UpsertTag(ctx, item); !errors.Is(err, metadata.ErrTagInvalid) {
			t.Errorf("%s: err = %v, want ErrTagInvalid", name, err)
		}
	}
}

func TestUpsertTagKeepsBuiltinFlag(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	item := autoTag("binance_spot")
	item.Builtin = true
	if _, err := store.UpsertTag(ctx, item); err != nil {
		t.Fatal(err)
	}
	edit := autoTag("binance_spot")
	edit.TagName = "现货"
	got, err := store.UpsertTag(ctx, edit)
	if err != nil {
		t.Fatal(err)
	}
	if !got.GetBuiltin() || got.GetTagName() != "现货" {
		t.Fatalf("builtin flag must survive edits: %+v", got)
	}
}

func TestDeleteTagProtection(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	registerActiveNode(t, ctx, store, "node")

	builtin := autoTag("builtin_tag")
	builtin.Builtin = true
	if _, err := store.UpsertTag(ctx, builtin); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteTag(ctx, "space", "builtin_tag"); !errors.Is(err, metadata.ErrTagBuiltin) {
		t.Fatalf("err = %v, want ErrTagBuiltin", err)
	}

	if _, err := store.UpsertTag(ctx, autoTag("used_tag")); err != nil {
		t.Fatal(err)
	}
	dataset := createTestDataset(t, ctx, store, "dataset_used", "node")
	dataset.SubjectTags = []string{"used_tag"}
	if _, err := store.UpdateDataset(ctx, dataset); err != nil {
		t.Fatal(err)
	}
	err := store.DeleteTag(ctx, "space", "used_tag")
	var refErr *metadata.TagReferencedError
	if !errors.As(err, &refErr) || len(refErr.References) != 1 || refErr.References[0].GetDatasetId() != "dataset_used" {
		t.Fatalf("err = %v, want TagReferencedError(dataset_used)", err)
	}

	if _, err := store.UpsertTag(ctx, autoTag("free_tag")); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteTag(ctx, "space", "free_tag"); err != nil {
		t.Fatal(err)
	}
}
```

`registerActiveNode` 与 `createTestDataset` 为包内已有的测试辅助函数（见 `crud_subject_test.go` / `dataset_test.go`），签名以实际代码为准。

- [ ] **Step 3：运行确认失败**

Run: `go test ./modules/storage/internal/service/metadata/sqlite/ -run 'TestUpsertTag|TestDeleteTag' -count=1`
Expected: 编译失败：`store.UpsertTag undefined`、`dataset.SubjectTags` 未写入列等。

- [ ] **Step 4：实现 `crud_tag.go`**

```go
package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/robfig/cron"

	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

const (
	defaultTagCron     = "0 * * * *"
	defaultTagTimezone = "UTC"
	sqliteTimeLayout   = "2006-01-02 15:04:05"
)

var (
	tagIDPattern    = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	instrumentTypes = map[string]bool{"spot": true, "swap": true, "equity": true, "etf": true, "index": true, "convertible_bond": true}
)

func normalizeTag(item *pb.Tag) error {
	if item == nil {
		return fmt.Errorf("%w: tag is required", metadata.ErrTagInvalid)
	}
	item.SpaceId = strings.TrimSpace(item.GetSpaceId())
	item.TagId = strings.TrimSpace(item.GetTagId())
	item.TagName = strings.TrimSpace(item.GetTagName())
	item.Mode = strings.ToLower(strings.TrimSpace(item.GetMode()))
	item.InstrumentType = strings.ToLower(strings.TrimSpace(item.GetInstrumentType()))
	item.Cron = strings.TrimSpace(item.GetCron())
	item.Timezone = strings.TrimSpace(item.GetTimezone())
	sources := make([]string, 0, len(item.GetSources()))
	seen := map[string]bool{}
	for _, source := range item.GetSources() {
		source = strings.ToLower(strings.TrimSpace(source))
		if source != "" && !seen[source] {
			seen[source] = true
			sources = append(sources, source)
		}
	}
	item.Sources = sources
	if item.Cron == "" {
		item.Cron = defaultTagCron
	}
	if item.Timezone == "" {
		item.Timezone = defaultTagTimezone
	}
	switch {
	case item.SpaceId == "":
		return fmt.Errorf("%w: space_id is required", metadata.ErrTagInvalid)
	case !tagIDPattern.MatchString(item.TagId):
		return fmt.Errorf("%w: tag_id must be lower_snake_case", metadata.ErrTagInvalid)
	case item.TagName == "":
		return fmt.Errorf("%w: tag_name is required", metadata.ErrTagInvalid)
	case item.Mode != metadata.TagModeAuto && item.Mode != metadata.TagModeManual:
		return fmt.Errorf("%w: mode must be auto or manual", metadata.ErrTagInvalid)
	}
	probe := len(item.Sources) > 0 || item.InstrumentType != ""
	if item.Mode == metadata.TagModeAuto || probe {
		if len(item.Sources) == 0 || item.InstrumentType == "" {
			return fmt.Errorf("%w: sources and instrument_type must be set together", metadata.ErrTagInvalid)
		}
		if !instrumentTypes[item.InstrumentType] {
			return fmt.Errorf("%w: unsupported instrument_type %q", metadata.ErrTagInvalid, item.InstrumentType)
		}
	}
	if _, err := cron.ParseStandard(item.Cron); err != nil {
		return fmt.Errorf("%w: cron: %v", metadata.ErrTagInvalid, err)
	}
	if _, err := time.LoadLocation(item.Timezone); err != nil {
		return fmt.Errorf("%w: timezone: %v", metadata.ErrTagInvalid, err)
	}
	return nil
}

// UpsertTag 创建或更新标签；builtin 只在创建时写入，更新时保持原值。
func (s *Store) UpsertTag(ctx context.Context, item *pb.Tag) (*pb.Tag, error) {
	if err := normalizeTag(item); err != nil {
		return nil, err
	}
	sources, err := marshalJSON(item.GetSources())
	if err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO t_tags (c_space_id, c_tag_id, c_tag_name, c_description, c_mode, c_builtin, c_sources_json, c_instrument_type, c_cron, c_timezone)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_space_id, c_tag_id) DO UPDATE SET
			c_tag_name = excluded.c_tag_name,
			c_description = excluded.c_description,
			c_mode = excluded.c_mode,
			c_sources_json = excluded.c_sources_json,
			c_instrument_type = excluded.c_instrument_type,
			c_cron = excluded.c_cron,
			c_timezone = excluded.c_timezone
	`, item.GetSpaceId(), item.GetTagId(), item.GetTagName(), item.GetDescription(), item.GetMode(), boolInt(item.GetBuiltin()), sources, item.GetInstrumentType(), item.GetCron(), item.GetTimezone())
	if err != nil {
		return nil, err
	}
	return s.GetTag(ctx, item.GetSpaceId(), item.GetTagId())
}

const tagSelect = `
	SELECT t.c_space_id, t.c_tag_id, t.c_tag_name, t.c_description, t.c_mode, t.c_builtin,
	       t.c_sources_json, t.c_instrument_type, t.c_cron, t.c_timezone,
	       t.c_last_run_at, t.c_last_status, t.c_last_error, t.c_ctime, t.c_mtime,
	       (SELECT COUNT(1) FROM t_subject_tags m WHERE m.c_space_id = t.c_space_id AND m.c_tag_id = t.c_tag_id AND m.c_status = 'active'),
	       (SELECT COUNT(1) FROM t_subject_tags m WHERE m.c_space_id = t.c_space_id AND m.c_tag_id = t.c_tag_id AND m.c_status = 'inactive')
	FROM t_tags t`

func scanTag(row rowScanner) (*pb.Tag, error) {
	item := &pb.Tag{}
	var builtin int
	var sources string
	if err := row.Scan(&item.SpaceId, &item.TagId, &item.TagName, &item.Description, &item.Mode, &builtin,
		&sources, &item.InstrumentType, &item.Cron, &item.Timezone,
		&item.LastRunAt, &item.LastStatus, &item.LastError, &item.CreatedAt, &item.UpdatedAt,
		&item.ActiveCount, &item.InactiveCount); err != nil {
		return nil, err
	}
	item.Builtin = builtin == 1
	if err := json.Unmarshal([]byte(sources), &item.Sources); err != nil {
		return nil, fmt.Errorf("decode tag sources: %w", err)
	}
	return item, nil
}

func (s *Store) GetTag(ctx context.Context, spaceID, tagID string) (*pb.Tag, error) {
	return scanTag(s.queryDB(ctx).QueryRowContext(ctx, tagSelect+` WHERE t.c_space_id = ? AND t.c_tag_id = ?`, spaceID, tagID))
}

func (s *Store) ListTags(ctx context.Context, spaceID string, page *pb.Page) ([]*pb.Tag, *pb.PageResult, error) {
	pageNo, size, offset := normalizePage(page)
	db := s.queryDB(ctx)
	var total uint64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM t_tags WHERE (? = '' OR c_space_id = ?)`, spaceID, spaceID).Scan(&total); err != nil {
		return nil, nil, err
	}
	rows, err := db.QueryContext(ctx, tagSelect+` WHERE (? = '' OR t.c_space_id = ?) ORDER BY t.c_space_id, t.c_builtin DESC, t.c_tag_id LIMIT ? OFFSET ?`, spaceID, spaceID, size, offset)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	items := make([]*pb.Tag, 0)
	for rows.Next() {
		item, err := scanTag(rows)
		if err != nil {
			return nil, nil, err
		}
		items = append(items, item)
	}
	return items, &pb.PageResult{Page: pageNo, Size: size, Total: total}, rows.Err()
}

func (s *Store) DeleteTag(ctx context.Context, spaceID, tagID string) error {
	tx, err := beginImmediate(ctx, s.db)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var builtin int
	if err := tx.QueryRowContext(ctx, `SELECT c_builtin FROM t_tags WHERE c_space_id = ? AND c_tag_id = ?`, spaceID, tagID).Scan(&builtin); err != nil {
		return err
	}
	if builtin == 1 {
		return fmt.Errorf("%w: %s", metadata.ErrTagBuiltin, tagID)
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT d.c_dataset_id, d.c_name, COALESCE(json_extract(d.c_attrs_json, '$.attributes.collector_task_id'), '')
		FROM t_datasets d
		WHERE d.c_space_id = ? AND EXISTS (SELECT 1 FROM json_each(d.c_subject_tags_json) j WHERE j.value = ?)
		ORDER BY d.c_dataset_id`, spaceID, tagID)
	if err != nil {
		return err
	}
	refs := make([]*pb.TagReference, 0)
	for rows.Next() {
		ref := &pb.TagReference{}
		if err := rows.Scan(&ref.DatasetId, &ref.DatasetName, &ref.CollectorTaskId); err != nil {
			rows.Close()
			return err
		}
		refs = append(refs, ref)
	}
	rows.Close()
	if len(refs) > 0 {
		return &metadata.TagReferencedError{TagID: tagID, References: refs}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM t_tags WHERE c_space_id = ? AND c_tag_id = ?`, spaceID, tagID); err != nil {
		return err
	}
	return tx.Commit()
}

func formatSQLiteTime(t time.Time) string { return t.UTC().Format(sqliteTimeLayout) }
```

本文件的 import 只需 `context`、`encoding/json`、`fmt`、`regexp`、`strings`、`time`、`cron`、`metadata`、`pb`。`c_attrs_json` 中 Dataset 的 `attributes` 键名以 `crud_helpers.go` 中 `marshal` 的实际输出为准；若为驼峰名，同步调整 `json_extract` 路径。

- [ ] **Step 5：Dataset 写入 `c_subject_tags_json`**

`crud_dataset.go`：

- `CreateDataset` 的 INSERT 增加 `c_subject_tags_json` 列，值为 `marshalJSON(normalizeSubjectTags(item.GetSubjectTags()))`；
- `UpdateDataset` 的 UPDATE 增加 `c_subject_tags_json = ?`；
- 新增：

```go
func normalizeSubjectTags(tags []string) []string {
	out := make([]string, 0, len(tags))
	seen := map[string]bool{}
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag != "" && !seen[tag] {
			seen[tag] = true
			out = append(out, tag)
		}
	}
	sort.Strings(out)
	return out
}
```

写入前把 `item.SubjectTags = normalizeSubjectTags(item.GetSubjectTags())`，使 `c_attrs_json` 中的 proto 与列一致；并校验每个 tag 存在：

```go
func validateSubjectTagsExist(ctx context.Context, db execQueryRower, spaceID string, tags []string) error {
	for _, tag := range tags {
		var n int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM t_tags WHERE c_space_id = ? AND c_tag_id = ?`, spaceID, tag).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("%w: subject tag %s does not exist", metadata.ErrTagInvalid, tag)
		}
	}
	return nil
}
```

- [ ] **Step 6：运行测试**

Run: `go test ./modules/storage/internal/service/metadata/sqlite/ -run 'TestUpsertTag|TestDeleteTag' -count=1`
Expected: PASS

- [ ] **Step 7：提交**

```bash
git add modules/storage/internal/service/metadata modules/storage/internal/retinfo
git commit -m "feat(storage): 标签定义存储与 Dataset 标签范围"
```

### Task 1.5：成员、快照、属性与解析

**Files：**
- Create: `modules/storage/internal/service/metadata/sqlite/crud_tag_member.go`
- Create: `modules/storage/internal/service/metadata/sqlite/crud_tag_member_test.go`
- Modify: `modules/storage/internal/service/metadata/sqlite/crud_subject.go`（`ListDatasetSubjects` 移到新文件并改写）

- [ ] **Step 1：编写失败测试**

```go
package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func snapshot(ids ...string) []*pb.TagSnapshotItem {
	items := make([]*pb.TagSnapshotItem, 0, len(ids))
	for _, id := range ids {
		items = append(items, &pb.TagSnapshotItem{SubjectId: id, SubjectType: "crypto_pair", Name: id})
	}
	return items
}

func memberStatus(t *testing.T, store *Store, tagID string) map[string]string {
	t.Helper()
	members, _, err := store.ListTagMembers(context.Background(), metadata.TagMemberQuery{SpaceID: "space", TagID: tagID, Page: &pb.Page{Page: 1, Size: 100}})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, m := range members {
		out[m.GetSubject().GetSubjectId()] = m.GetStatus()
	}
	return out
}

func TestApplyTagSnapshotAuto(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	if _, err := store.UpsertTag(ctx, autoTag("binance_spot")); err != nil {
		t.Fatal(err)
	}
	run1 := time.Date(2026, 9, 25, 1, 0, 0, 0, time.UTC)
	res, err := store.ApplyTagSnapshot(ctx, "space", "binance_spot", run1, snapshot("BTC-USDT", "ETH-USDT"))
	if err != nil || res.Added != 2 {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	run2 := run1.Add(time.Hour)
	res, err = store.ApplyTagSnapshot(ctx, "space", "binance_spot", run2, snapshot("BTC-USDT"))
	if err != nil || res.Inactivated != 1 {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	if got := memberStatus(t, store, "binance_spot"); got["ETH-USDT"] != "inactive" || got["BTC-USDT"] != "active" {
		t.Fatalf("members = %v", got)
	}
	res, err = store.ApplyTagSnapshot(ctx, "space", "binance_spot", run2.Add(time.Hour), snapshot("BTC-USDT", "ETH-USDT"))
	if err != nil || res.Activated != 1 || res.Added != 0 {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	tag, _ := store.GetTag(ctx, "space", "binance_spot")
	if tag.GetLastStatus() != "success" || tag.GetActiveCount() != 2 || tag.GetLastRunAt() != "2026-09-25 03:00:00" {
		t.Fatalf("tag = %+v", tag)
	}
	if tag.GetUpdatedAt() > tag.GetLastRunAt() {
		t.Fatalf("run-state writes must not bump c_mtime: %+v", tag)
	}
}

func TestApplyTagSnapshotManualOnlyChangesStatus(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	manual := &pb.Tag{SpaceId: "space", TagId: "watch", TagName: "关注", Mode: "manual", Sources: []string{"binance"}, InstrumentType: "spot"}
	if _, err := store.UpsertTag(ctx, manual); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"BTC-USDT", "LUNA-USDT"} {
		if _, err := store.UpsertSubject(ctx, &pb.Subject{SpaceId: "space", SubjectId: id, SubjectType: "crypto_pair"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.AddTagMembers(ctx, "space", "watch", []string{"BTC-USDT", "LUNA-USDT"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyTagSnapshot(ctx, "space", "watch", time.Now(), snapshot("BTC-USDT", "ETH-USDT")); err != nil {
		t.Fatal(err)
	}
	got := memberStatus(t, store, "watch")
	if len(got) != 2 || got["LUNA-USDT"] != "inactive" || got["BTC-USDT"] != "active" {
		t.Fatalf("members = %v", got)
	}
}

func TestManualMemberAPIsRejectAutoTag(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	if _, err := store.UpsertTag(ctx, autoTag("binance_spot")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddTagMembers(ctx, "space", "binance_spot", []string{"BTC-USDT"}); !errors.Is(err, metadata.ErrTagAutoMembers) {
		t.Fatalf("AddTagMembers err = %v", err)
	}
	if _, err := store.RemoveTagMembers(ctx, "space", "binance_spot", []string{"BTC-USDT"}); !errors.Is(err, metadata.ErrTagAutoMembers) {
		t.Fatalf("RemoveTagMembers err = %v", err)
	}
	if _, err := store.SetTagMemberStatus(ctx, "space", "binance_spot", []string{"BTC-USDT"}, "active"); !errors.Is(err, metadata.ErrTagAutoMembers) {
		t.Fatalf("SetTagMemberStatus err = %v", err)
	}
}

func TestReportTagRunFailureKeepsMembers(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	_, _ = store.UpsertTag(ctx, autoTag("binance_spot"))
	_, _ = store.ApplyTagSnapshot(ctx, "space", "binance_spot", time.Now(), snapshot("BTC-USDT"))
	if err := store.ReportTagRunFailure(ctx, "space", "binance_spot", time.Now(), "timeout"); err != nil {
		t.Fatal(err)
	}
	tag, _ := store.GetTag(ctx, "space", "binance_spot")
	if tag.GetLastStatus() != "failed" || tag.GetLastError() != "timeout" || tag.GetActiveCount() != 1 {
		t.Fatalf("tag = %+v", tag)
	}
}

func TestUpdateSubjectAttributesOnlyExisting(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	_, _ = store.UpsertSubject(ctx, &pb.Subject{SpaceId: "space", SubjectId: "BTC-USDT", SubjectType: "crypto_pair"})
	updated, skipped, err := store.UpdateSubjectAttributes(ctx, "space", []*pb.SubjectAttributes{
		{SubjectId: "BTC-USDT", Name: "BTC/USDT", Attributes: map[string]string{"base": "BTC", "quote": "USDT"}},
		{SubjectId: "NOPE-USDT", Attributes: map[string]string{"base": "NOPE"}},
	})
	if err != nil || updated != 1 || skipped != 1 {
		t.Fatalf("updated=%d skipped=%d err=%v", updated, skipped, err)
	}
	got, _ := store.GetSubject(ctx, "space", "BTC-USDT")
	if got.GetName() != "BTC/USDT" || got.GetAttributes()["base"] != "BTC" {
		t.Fatalf("subject = %+v", got)
	}
	if _, err := store.GetSubject(ctx, "space", "NOPE-USDT"); err == nil {
		t.Fatal("UpdateSubjectAttributes must not create subjects")
	}
}

func TestResolveSubjectsAndDatasetSubjects(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	registerActiveNode(t, ctx, store, "node")
	_, _ = store.UpsertTag(ctx, autoTag("tag_a"))
	_, _ = store.UpsertTag(ctx, autoTag("tag_b"))
	_, _ = store.ApplyTagSnapshot(ctx, "space", "tag_a", time.Now(), snapshot("A", "B"))
	_, _ = store.ApplyTagSnapshot(ctx, "space", "tag_b", time.Now(), snapshot("B", "C"))
	_, _ = store.ApplyTagSnapshot(ctx, "space", "tag_b", time.Now(), snapshot("B"))

	subjects, err := store.ResolveSubjects(ctx, "space", []string{"tag_a", "tag_b"})
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, s := range subjects {
		ids = append(ids, s.GetSubjectId())
	}
	if len(ids) != 2 || ids[0] != "A" || ids[1] != "B" {
		t.Fatalf("resolved = %v, want [A B]", ids)
	}

	dataset := createTestDataset(t, ctx, store, "dataset_ab", "node")
	dataset.SubjectTags = []string{"tag_a", "tag_b"}
	if _, err := store.UpdateDataset(ctx, dataset); err != nil {
		t.Fatal(err)
	}
	rows, _, err := store.ListDatasetSubjects(ctx, "space", "dataset_ab", "", &pb.Page{Page: 1, Size: 100})
	if err != nil {
		t.Fatal(err)
	}
	status := map[string]string{}
	for _, row := range rows {
		status[row.GetSubjectId()] = row.GetStatus()
	}
	if len(status) != 3 || status["C"] != "inactive" || status["B"] != "active" {
		t.Fatalf("dataset subjects = %v", status)
	}
}
```

- [ ] **Step 2：运行确认失败**

Run: `go test ./modules/storage/internal/service/metadata/sqlite/ -run 'TestApplyTagSnapshot|TestManualMember|TestReportTagRun|TestUpdateSubjectAttributes|TestResolveSubjects' -count=1`
Expected: 编译失败（方法未定义）。

- [ ] **Step 3：实现 `crud_tag_member.go`**

```go
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func tagMode(ctx context.Context, db execQueryRower, spaceID, tagID string) (string, error) {
	var mode string
	err := db.QueryRowContext(ctx, `SELECT c_mode FROM t_tags WHERE c_space_id = ? AND c_tag_id = ?`, spaceID, tagID).Scan(&mode)
	return mode, err
}

func uniqueIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func (s *Store) manualMemberTx(ctx context.Context, spaceID, tagID string, fn func(tx *immediateTx, ids string) (int64, error), subjectIDs []string) (int, error) {
	tx, err := beginImmediate(ctx, s.db)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	mode, err := tagMode(ctx, tx, spaceID, tagID)
	if err != nil {
		return 0, err
	}
	if mode != metadata.TagModeManual {
		return 0, fmt.Errorf("%w: %s", metadata.ErrTagAutoMembers, tagID)
	}
	ids, err := marshalJSON(uniqueIDs(subjectIDs))
	if err != nil {
		return 0, err
	}
	affected, err := fn(tx, ids)
	if err != nil {
		return 0, err
	}
	return int(affected), tx.Commit()
}

func (s *Store) AddTagMembers(ctx context.Context, spaceID, tagID string, subjectIDs []string) (int, error) {
	return s.manualMemberTx(ctx, spaceID, tagID, func(tx *immediateTx, ids string) (int64, error) {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO t_subject_tags (c_space_id, c_tag_id, c_subject_id)
			SELECT ?, ?, s.c_subject_id FROM t_subjects s
			WHERE s.c_space_id = ? AND s.c_subject_id IN (SELECT value FROM json_each(?))
			ON CONFLICT(c_space_id, c_tag_id, c_subject_id) DO NOTHING`, spaceID, tagID, spaceID, ids)
		if err != nil {
			return 0, err
		}
		return res.RowsAffected()
	}, subjectIDs)
}

func (s *Store) RemoveTagMembers(ctx context.Context, spaceID, tagID string, subjectIDs []string) (int, error) {
	return s.manualMemberTx(ctx, spaceID, tagID, func(tx *immediateTx, ids string) (int64, error) {
		res, err := tx.ExecContext(ctx, `DELETE FROM t_subject_tags WHERE c_space_id = ? AND c_tag_id = ? AND c_subject_id IN (SELECT value FROM json_each(?))`, spaceID, tagID, ids)
		if err != nil {
			return 0, err
		}
		return res.RowsAffected()
	}, subjectIDs)
}

func (s *Store) SetTagMemberStatus(ctx context.Context, spaceID, tagID string, subjectIDs []string, status string) (int, error) {
	if status != metadata.TagMemberActive && status != metadata.TagMemberInactive {
		return 0, fmt.Errorf("%w: status must be active or inactive", metadata.ErrTagInvalid)
	}
	inactiveAt := ""
	if status == metadata.TagMemberInactive {
		inactiveAt = formatSQLiteTime(time.Now())
	}
	return s.manualMemberTx(ctx, spaceID, tagID, func(tx *immediateTx, ids string) (int64, error) {
		res, err := tx.ExecContext(ctx, `
			UPDATE t_subject_tags SET c_status = ?, c_inactive_at = ?
			WHERE c_space_id = ? AND c_tag_id = ? AND c_status <> ? AND c_subject_id IN (SELECT value FROM json_each(?))`,
			status, inactiveAt, spaceID, tagID, status, ids)
		if err != nil {
			return 0, err
		}
		return res.RowsAffected()
	}, subjectIDs)
}

// ApplyTagSnapshot 以单事务把一次完整的标的列表应用到标签成员。
func (s *Store) ApplyTagSnapshot(ctx context.Context, spaceID, tagID string, runAt time.Time, items []*pb.TagSnapshotItem) (metadata.TagSnapshotResult, error) {
	var result metadata.TagSnapshotResult
	tx, err := beginImmediate(ctx, s.db)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	mode, err := tagMode(ctx, tx, spaceID, tagID)
	if err != nil {
		return result, err
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		id := strings.TrimSpace(item.GetSubjectId())
		if id == "" {
			continue
		}
		ids = append(ids, id)
		if mode != metadata.TagModeAuto {
			continue
		}
		subject := &pb.Subject{SpaceId: spaceID, SubjectId: id, SubjectType: item.GetSubjectType(), Name: item.GetName(), Market: item.GetMarket(), Currency: item.GetCurrency(), Timezone: item.GetTimezone(), Status: "active"}
		raw, err := marshal(subject)
		if err != nil {
			return result, err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO t_subjects (c_space_id, c_subject_id, c_subject_type, c_name, c_market, c_currency, c_timezone, c_status, c_attrs_json)
			VALUES (?, ?, ?, ?, ?, ?, ?, 'active', ?)
			ON CONFLICT(c_space_id, c_subject_id) DO NOTHING`,
			spaceID, id, item.GetSubjectType(), item.GetName(), item.GetMarket(), item.GetCurrency(), item.GetTimezone(), raw); err != nil {
			return result, err
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO t_subject_tags (c_space_id, c_tag_id, c_subject_id) VALUES (?, ?, ?)
			ON CONFLICT(c_space_id, c_tag_id, c_subject_id) DO NOTHING`, spaceID, tagID, id)
		if err != nil {
			return result, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			result.Added++
		}
	}
	idsJSON, err := marshalJSON(uniqueIDs(ids))
	if err != nil {
		return result, err
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE t_subject_tags SET c_status = 'active', c_inactive_at = ''
		WHERE c_space_id = ? AND c_tag_id = ? AND c_status = 'inactive' AND c_subject_id IN (SELECT value FROM json_each(?))`,
		spaceID, tagID, idsJSON)
	if err != nil {
		return result, err
	}
	activated, _ := res.RowsAffected()
	result.Activated = int(activated)
	res, err = tx.ExecContext(ctx, `
		UPDATE t_subject_tags SET c_status = 'inactive', c_inactive_at = ?
		WHERE c_space_id = ? AND c_tag_id = ? AND c_status = 'active' AND c_subject_id NOT IN (SELECT value FROM json_each(?))`,
		formatSQLiteTime(runAt), spaceID, tagID, idsJSON)
	if err != nil {
		return result, err
	}
	inactivated, _ := res.RowsAffected()
	result.Inactivated = int(inactivated)
	if _, err := tx.ExecContext(ctx, `
		UPDATE t_tags SET c_last_run_at = ?, c_last_status = 'success', c_last_error = ''
		WHERE c_space_id = ? AND c_tag_id = ?`, formatSQLiteTime(runAt), spaceID, tagID); err != nil {
		return result, err
	}
	return result, tx.Commit()
}

func (s *Store) ReportTagRunFailure(ctx context.Context, spaceID, tagID string, runAt time.Time, message string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE t_tags SET c_last_run_at = ?, c_last_status = 'failed', c_last_error = ?
		WHERE c_space_id = ? AND c_tag_id = ?`, formatSQLiteTime(runAt), message, spaceID, tagID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) UpdateSubjectAttributes(ctx context.Context, spaceID string, items []*pb.SubjectAttributes) (int, int, error) {
	tx, err := beginImmediate(ctx, s.db)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	updated, skipped := 0, 0
	for _, item := range items {
		existing, err := getMessage(ctx, tx, `SELECT c_attrs_json FROM t_subjects WHERE c_space_id = ? AND c_subject_id = ?`, []any{spaceID, item.GetSubjectId()}, func() *pb.Subject { return &pb.Subject{} })
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				skipped++
				continue
			}
			return 0, 0, err
		}
		if name := strings.TrimSpace(item.GetName()); name != "" {
			existing.Name = name
		}
		if existing.Attributes == nil {
			existing.Attributes = map[string]string{}
		}
		for key, value := range item.GetAttributes() {
			existing.Attributes[key] = value
		}
		if err := upsertSubject(ctx, tx, existing); err != nil {
			return 0, 0, err
		}
		updated++
	}
	return updated, skipped, tx.Commit()
}

func (s *Store) ResolveSubjects(ctx context.Context, spaceID string, tagIDs []string) ([]*pb.Subject, error) {
	tags, err := marshalJSON(uniqueIDs(tagIDs))
	if err != nil {
		return nil, err
	}
	rows, err := s.queryDB(ctx).QueryContext(ctx, `
		SELECT s.c_attrs_json FROM t_subjects s
		WHERE s.c_space_id = ? AND s.c_status = 'active' AND EXISTS (
			SELECT 1 FROM t_subject_tags m
			WHERE m.c_space_id = s.c_space_id AND m.c_subject_id = s.c_subject_id
			  AND m.c_status = 'active' AND m.c_tag_id IN (SELECT value FROM json_each(?)))
		ORDER BY s.c_subject_id`, spaceID, tags)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMessages(rows, func() *pb.Subject { return &pb.Subject{} })
}

// ListDatasetSubjects 由 Dataset.subject_tags 派生全部成员；任一标签中有效即为 active。
func (s *Store) ListDatasetSubjects(ctx context.Context, spaceID, datasetID, subjectID string, page *pb.Page) ([]*pb.DatasetSubject, *pb.PageResult, error) {
	if spaceID == "" || datasetID == "" {
		return nil, nil, fmt.Errorf("space_id and dataset_id are required")
	}
	const from = `
		FROM t_datasets d
		JOIN json_each(d.c_subject_tags_json) j
		JOIN t_subject_tags m ON m.c_space_id = d.c_space_id AND m.c_tag_id = j.value
		JOIN t_subjects s ON s.c_space_id = m.c_space_id AND s.c_subject_id = m.c_subject_id
		WHERE d.c_space_id = ? AND d.c_dataset_id = ? AND (? = '' OR m.c_subject_id = ?)`
	args := []any{spaceID, datasetID, subjectID, subjectID}
	db := s.queryDB(ctx)
	var total uint64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT m.c_subject_id) `+from, args...).Scan(&total); err != nil {
		return nil, nil, err
	}
	pageNo, size, offset := normalizePage(page)
	rows, err := db.QueryContext(ctx, `
		SELECT m.c_subject_id, MAX(CASE WHEN m.c_status = 'active' AND s.c_status = 'active' THEN 1 ELSE 0 END) `+from+`
		GROUP BY m.c_subject_id ORDER BY m.c_subject_id LIMIT ? OFFSET ?`, append(args, size, offset)...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	items := make([]*pb.DatasetSubject, 0)
	for rows.Next() {
		var id string
		var active int
		if err := rows.Scan(&id, &active); err != nil {
			return nil, nil, err
		}
		status := metadata.TagMemberInactive
		if active == 1 {
			status = metadata.TagMemberActive
		}
		items = append(items, &pb.DatasetSubject{SpaceId: spaceID, DatasetId: datasetID, SubjectId: id, Status: status})
	}
	return items, &pb.PageResult{Page: pageNo, Size: size, Total: total}, rows.Err()
}
```

`ListTagMembers` 同文件实现：

- `TagID` 非空时：`FROM t_subject_tags m JOIN t_subjects s ...`，按 `Status`（空为全部）、`Keyword`（匹配 `c_subject_id` / `c_name`，小写 `instr`）过滤，按 `c_subject_id` 排序分页；每行返回 `TagMember{SpaceId, TagId, Status, InactiveAt, Subject}`；
- `TagID` 为空时：分页列出 `t_subjects`（复用 `ListSubjects` 的 keyword 条件），并对本页的 subject 批量查询所属 `c_tag_id` 填入 `TagIds`。

`UpdateSubjectAttributes` 把 `tx` 传给 `getMessage`：若 `getMessage` 的查询参数类型不接受 `*immediateTx`，改为 `tx.QueryRowContext` 读出 `c_attrs_json` 后用与 `getMessage` 相同的方式解码。

实现中用到的 `scanMessages(rows, newFn)`：若 `crud_helpers.go` 中已有等价函数（`queryPagedMessages` 的内部循环），抽出复用，不要重复实现。补充 import `database/sql`、`errors`。

同时从 `crud_subject.go` 中删除旧的 `ListDatasetSubjects` 实现（它查询已删除的 `t_dataset_subjects`），以及 `crud_subject_test.go` 中依赖旧表的用例；`crud_subject_set.go` / `crud_subject_set_test.go` 在本任务一并删除，并从 `metadata.DatasetSubjectSetWriter` 接口及其实现断言处移除（catalog 中调用 `StageDatasetSubjectSet` / `ActivateDatasetSubjectSet` 的 handler 暂时改为返回 `retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("removed"))`，Task 5.6 连同 RPC 删除）。`UpsertSubjectSymbol`、`ListSubjectSymbols`、`RegisterDataSubject`、`BindDatasetSubject` 的 sqlite 实现同样因表删除而失效，按同一方式处理：sqlite 与 cache 中删除实现与接口方法，catalog handler 暂时返回 `removed` 错误。

- [ ] **Step 4：缓存层**

`service/metadata/cache/store.go`：

- 删除 `kindSubjectSymbol`、`kindDatasetSubject` 及 `fetchSubjectSymbols`、`fetchDatasetSubjects`、`indexDataset` 中对应的索引构建；
- `ListDatasetSubjects` 改为直接委托：`return s.base.ListDatasetSubjects(ctx, spaceID, datasetID, subjectID, page)`；
- 删除 `ListSubjectSymbols`。

标签相关读取不进入快照缓存，由 catalog 直接访问 `metadata.Store`（SQLite），`t_subject_tags` 有复合索引，单次查询开销可接受。

- [ ] **Step 5：运行测试**

Run:
```bash
go test ./modules/storage/internal/service/metadata/... -count=1
go build ./modules/...
```
Expected: 全部 PASS；`go build` 若在 collector / cli 等模块中因 storage 内部包变更失败，说明有跨模块引用内部包，按报错修复（正常情况下它们只依赖 `storagegen`）。

- [ ] **Step 6：提交**

```bash
git add modules/storage
git commit -m "feat(storage): 标签成员、快照应用与标的解析，Dataset 成员改为由标签派生"
```

### Task 1.6：catalog RPC handler

**Files：**
- Create: `modules/storage/internal/service/catalog/tag_catalog.go`
- Create: `modules/storage/internal/service/catalog/tag_catalog_test.go`
- Modify: `modules/storage/internal/service/catalog/metadata_catalog.go`（`CreateDataset` / `UpdateDataset` 透传 `subject_tags`，无需额外改动时跳过）

- [ ] **Step 1：编写 handler 测试**

参照同目录现有 catalog 测试的构造方式（`NewMetadataService(store, nil, Options{...})`，store 为 `openTestStore` 等价的临时 SQLite），覆盖：

```go
func TestTagCatalogDeleteReturnsReferences(t *testing.T) {
	// 1. UpsertTag used_tag；2. CreateDataset(subject_tags=[used_tag])；
	// 3. DeleteTag -> RetInfo.Code == INVALID_PARAM 且 len(References) == 1
}

func TestTagCatalogApplySnapshotParsesRunAt(t *testing.T) {
	// ApplyTagSnapshotReq.run_at = "2026-09-25T01:00:00Z"（RFC3339）-> GetTag.last_run_at == "2026-09-25 01:00:00"
	// run_at 非法 -> INVALID_PARAM
}

func TestTagCatalogResolveSubjects(t *testing.T) {
	// 空 tag_ids -> INVALID_PARAM；正常返回 active 成员
}
```

每个用例写出完整代码，断言 `rsp.GetRetInfo().GetCode()`。

- [ ] **Step 2：实现 handler**

```go
package catalog

import (
	"context"
	"errors"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func parseRunAt(value string) (time.Time, error) {
	if value == "" {
		return time.Now().UTC(), nil
	}
	return time.Parse(time.RFC3339, value)
}

func (s *Service) UpsertTag(ctx context.Context, req *pb.UpsertTagReq) (*pb.UpsertTagRsp, error) {
	item, err := s.metadata.UpsertTag(ctx, req.GetTag())
	if err != nil {
		return &pb.UpsertTagRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.UpsertTagRsp{RetInfo: retinfo.Success("success"), Tag: item}, nil
}

func (s *Service) GetTag(ctx context.Context, req *pb.GetTagReq) (*pb.GetTagRsp, error) {
	item, err := s.metadata.GetTag(ctx, req.GetSpaceId(), req.GetTagId())
	if err != nil {
		return &pb.GetTagRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.GetTagRsp{RetInfo: retinfo.Success("success"), Tag: item}, nil
}

func (s *Service) ListTags(ctx context.Context, req *pb.ListTagsReq) (*pb.ListTagsRsp, error) {
	items, page, err := s.metadata.ListTags(ctx, req.GetSpaceId(), req.GetPage())
	if err != nil {
		return &pb.ListTagsRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.ListTagsRsp{RetInfo: retinfo.Success("success"), Tags: items, PageResult: page}, nil
}

func (s *Service) DeleteTag(ctx context.Context, req *pb.DeleteTagReq) (*pb.DeleteTagRsp, error) {
	err := s.metadata.DeleteTag(ctx, req.GetSpaceId(), req.GetTagId())
	var refErr *metadata.TagReferencedError
	if errors.As(err, &refErr) {
		return &pb.DeleteTagRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err), References: refErr.References}, nil
	}
	if err != nil {
		return &pb.DeleteTagRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.DeleteTagRsp{RetInfo: retinfo.Success("success")}, nil
}

func (s *Service) ListTagMembers(ctx context.Context, req *pb.ListTagMembersReq) (*pb.ListTagMembersRsp, error) {
	items, page, err := s.metadata.ListTagMembers(ctx, metadata.TagMemberQuery{SpaceID: req.GetSpaceId(), TagID: req.GetTagId(), Status: req.GetStatus(), Keyword: req.GetKeyword(), Page: req.GetPage()})
	if err != nil {
		return &pb.ListTagMembersRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.ListTagMembersRsp{RetInfo: retinfo.Success("success"), Members: items, PageResult: page}, nil
}

func tagMembersRsp(affected int, err error) *pb.TagMembersRsp {
	if err != nil {
		return &pb.TagMembersRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}
	}
	return &pb.TagMembersRsp{RetInfo: retinfo.Success("success"), Affected: uint32(affected)}
}

func (s *Service) AddTagMembers(ctx context.Context, req *pb.TagMembersReq) (*pb.TagMembersRsp, error) {
	return tagMembersRsp(s.metadata.AddTagMembers(ctx, req.GetSpaceId(), req.GetTagId(), req.GetSubjectIds())), nil
}

func (s *Service) RemoveTagMembers(ctx context.Context, req *pb.TagMembersReq) (*pb.TagMembersRsp, error) {
	return tagMembersRsp(s.metadata.RemoveTagMembers(ctx, req.GetSpaceId(), req.GetTagId(), req.GetSubjectIds())), nil
}

func (s *Service) SetTagMemberStatus(ctx context.Context, req *pb.SetTagMemberStatusReq) (*pb.TagMembersRsp, error) {
	return tagMembersRsp(s.metadata.SetTagMemberStatus(ctx, req.GetSpaceId(), req.GetTagId(), req.GetSubjectIds(), req.GetStatus())), nil
}

func (s *Service) ApplyTagSnapshot(ctx context.Context, req *pb.ApplyTagSnapshotReq) (*pb.ApplyTagSnapshotRsp, error) {
	runAt, err := parseRunAt(req.GetRunAt())
	if err != nil {
		return &pb.ApplyTagSnapshotRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	if len(req.GetItems()) == 0 {
		return &pb.ApplyTagSnapshotRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("snapshot items are required"))}, nil
	}
	result, err := s.metadata.ApplyTagSnapshot(ctx, req.GetSpaceId(), req.GetTagId(), runAt, req.GetItems())
	if err != nil {
		return &pb.ApplyTagSnapshotRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	s.refreshMetadataCacheAfterCommit(ctx, "apply tag snapshot")
	return &pb.ApplyTagSnapshotRsp{RetInfo: retinfo.Success("success"), Added: uint32(result.Added), Activated: uint32(result.Activated), Inactivated: uint32(result.Inactivated)}, nil
}

func (s *Service) ReportTagRunFailure(ctx context.Context, req *pb.ReportTagRunFailureReq) (*pb.ReportTagRunFailureRsp, error) {
	runAt, err := parseRunAt(req.GetRunAt())
	if err != nil {
		return &pb.ReportTagRunFailureRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	if err := s.metadata.ReportTagRunFailure(ctx, req.GetSpaceId(), req.GetTagId(), runAt, req.GetError()); err != nil {
		return &pb.ReportTagRunFailureRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.ReportTagRunFailureRsp{RetInfo: retinfo.Success("success")}, nil
}

func (s *Service) UpdateSubjectAttributes(ctx context.Context, req *pb.UpdateSubjectAttributesReq) (*pb.UpdateSubjectAttributesRsp, error) {
	updated, skipped, err := s.metadata.UpdateSubjectAttributes(ctx, req.GetSpaceId(), req.GetItems())
	if err != nil {
		return &pb.UpdateSubjectAttributesRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	s.refreshMetadataCacheAfterCommit(ctx, "update subject attributes")
	return &pb.UpdateSubjectAttributesRsp{RetInfo: retinfo.Success("success"), Updated: uint32(updated), Skipped: uint32(skipped)}, nil
}

func (s *Service) ResolveSubjects(ctx context.Context, req *pb.ResolveSubjectsReq) (*pb.ResolveSubjectsRsp, error) {
	if req.GetSpaceId() == "" || len(req.GetTagIds()) == 0 {
		return &pb.ResolveSubjectsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and tag_ids are required"))}, nil
	}
	items, err := s.metadata.ResolveSubjects(ctx, req.GetSpaceId(), req.GetTagIds())
	if err != nil {
		return &pb.ResolveSubjectsRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.ResolveSubjectsRsp{RetInfo: retinfo.Success("success"), Subjects: items}, nil
}
```

`ApplyTagSnapshot` / `UpdateSubjectAttributes` 会新增或修改 `t_subjects`，因此刷新快照缓存（`GetSubject` / `ListSubjects` 走缓存）。

- [ ] **Step 3：运行测试**

Run: `go test ./modules/storage/internal/service/catalog/ -count=1`
Expected: PASS

- [ ] **Step 4：提交**

```bash
git add modules/storage/internal/service/catalog
git commit -m "feat(storage): 标签相关 Metadata RPC"
```

### Task 1.7：RPC 方法登记

**Files：**
- Modify: `modules/storage/internal/accessproxy/proxy.go`（`MetadataName` 集合）
- Modify: `modules/gateway/internal/router/native.go`（`nativeReadOnlyMethod`）
- Modify: `modules/admin/internal/gateway/storage_bff.go`
- Modify: `modules/admin/internal/service/sysdeploy/defaults.go`（`storageMetadataGatewayMethods`）
- Modify: `config/setup/service-deployments.yaml`（storage metadata 的 `gateway_methods`，若为显式列表）
- Modify: `web/src/api/storage/http.ts`（`storageReadMethods`）

- [ ] **Step 1：登记**

- 只读方法 `GetTag`、`ListTags`、`ListTagMembers`、`ResolveSubjects` 加入 `native.go` 只读列表与 `http.ts` 读缓存集合；
- 全部 12 个新方法加入 accessproxy `MetadataName` 集合、`storage_bff.go`（值 `"storage-primary"`）、`storageMetadataGatewayMethods`。

- [ ] **Step 2：运行相关测试**

Run:
```bash
go test ./modules/storage/internal/accessproxy/ ./modules/gateway/internal/router/ ./modules/admin/internal/gateway/ ./modules/admin/internal/service/sysdeploy/ -count=1
```
Expected: PASS；若有「方法列表快照」类测试失败，更新其期望值为新增后的列表。

- [ ] **Step 3：提交**

```bash
git add modules/storage/internal/accessproxy modules/gateway modules/admin config/setup/service-deployments.yaml web/src/api/storage/http.ts
git commit -m "feat: 登记标签相关 Metadata RPC 路由"
```

### Task 1.8：内置标签种子

**Files：**
- Modify: `config/setup/metadata.yaml`
- Modify: `modules/storage/internal/bootstrap/metadata/seed.go`（含测试）
- Modify: `modules/cli/internal/command/metadata_types.go`、`metadata_implementation.go`（含测试）

两个导入入口（`moox-storage-cli import-seed` 与 `moox-cli setup init`）都读取 `metadata.yaml`，因此内置标签放在该文件的 `tags:` 段，而不是单独文件。

- [ ] **Step 1：种子内容**

`config/setup/metadata.yaml` 在 `subjects:` 段之后新增：

```yaml
tags:
  - space_id: crypto
    tag_id: binance_spot
    tag_name: 币安现货
    description: 币安现货全部标的
    mode: auto
    sources: [binance]
    instrument_type: spot
  - space_id: crypto
    tag_id: binance_swap
    tag_name: 币安永续
    description: 币安 U 本位永续全部标的
    mode: auto
    sources: [binance]
    instrument_type: swap
  - space_id: stockcn
    tag_id: cn_a_share
    tag_name: A股全部股票
    description: 沪深北交易所全部股票
    mode: auto
    sources: [sina, eastmoney]
    instrument_type: equity
    timezone: Asia/Shanghai
```

- [ ] **Step 2：编写失败测试（storage 侧）**

在 `seed_test.go` 新增：导入含 `tags:` 的种子后 `GetTag(crypto, binance_spot).Builtin == true`；随后调用 `UpsertTag` 把 `tag_name` 改成「现货」，再次导入同一种子，`tag_name` 仍为「现货」。

- [ ] **Step 3：实现（storage 侧）**

```go
type seedTag struct {
	SpaceID        string   `yaml:"space_id"`
	TagID          string   `yaml:"tag_id"`
	TagName        string   `yaml:"tag_name"`
	Description    string   `yaml:"description"`
	Mode           string   `yaml:"mode"`
	Sources        []string `yaml:"sources"`
	InstrumentType string   `yaml:"instrument_type"`
	Cron           string   `yaml:"cron"`
	Timezone       string   `yaml:"timezone"`
}

func (t seedTag) toPB() *pb.Tag {
	return &pb.Tag{SpaceId: t.SpaceID, TagId: t.TagID, TagName: t.TagName, Description: t.Description, Mode: t.Mode, Builtin: true, Sources: t.Sources, InstrumentType: t.InstrumentType, Cron: t.Cron, Timezone: t.Timezone}
}
```

`seedFile` 增加 `Tags []seedTag \`yaml:"tags"\``；`importEntities` 在 subjects 之后：

```go
	for _, item := range seed.Tags {
		if _, err := opts.Storage.GetTag(ctx, item.SpaceID, item.TagID); err == nil {
			continue
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := opts.Storage.UpsertTag(ctx, item.toPB()); err != nil {
			return fmt.Errorf("seed tag %s/%s: %w", item.SpaceID, item.TagID, err)
		}
	}
```

- [ ] **Step 4：CLI 侧**

`metadataSeed` 增加 `Tags []seedTag \`yaml:"tags"\``（结构与上面相同，放在 `metadata_types.go`）；`check("tags", item.SpaceID)` 加入空间校验循环；导入调用在 subjects 之后追加。CLI 通过 RPC 调用，因此先 `GetTag`，响应码为 `NOT_FOUND` 时再 `UpsertTag`。在 `metadataImportCall` 中增加可选字段 `Skip func(ctx context.Context) (bool, error)`，执行器在调用前判断；对 tags 设为「`GetTag` 成功即跳过」。测试：`metadata_test.go` 中 fake client 记录调用，断言已存在的标签只有 `GetTag` 而没有 `UpsertTag`。

- [ ] **Step 5：运行测试**

Run: `go test ./modules/storage/internal/bootstrap/metadata/ ./modules/cli/internal/command/ -run 'Seed|Import|Metadata' -count=1`
Expected: PASS

- [ ] **Step 6：提交**

```bash
git add config/setup/metadata.yaml modules/storage/internal/bootstrap/metadata modules/cli/internal/command
git commit -m "feat: 系统初始化写入内置标签"
```

---

## 阶段 2：抓取源适配器

### Task 2.1：标的 ID 规则

**Files：**
- Create: `modules/collector/internal/sources/crypto/subject.go`、`subject_test.go`
- Modify: `modules/collector/internal/sources/stockcn/common.go`、`common_test.go` 及全部调用方
- Modify: `modules/collector/internal/marketdata/subject.go`（删除 `CanonicalCryptoSubjectID`、`CanonicalizeCryptoInstrument`）

- [ ] **Step 1：失败测试**

```go
package crypto

import "testing"

func TestSubjectID(t *testing.T) {
	cases := []struct{ base, quote, want string; ok bool }{
		{"BTC", "USDT", "BTC-USDT", true},
		{"1000pepe", "usdt", "1000PEPE-USDT", true},
		{"", "USDT", "", false},
		{"BTC", "", "", false},
		{"BT-C", "USDT", "", false},
	}
	for _, c := range cases {
		got, err := SubjectID(c.base, c.quote)
		if (err == nil) != c.ok || got != c.want {
			t.Errorf("SubjectID(%q,%q) = %q, %v", c.base, c.quote, got, err)
		}
	}
}
```

stockcn：`common_test.go` 中 `CanonicalSubjectID` 改名为 `SubjectID`，补 `920000 -> 920000.XBSE` 用例。

- [ ] **Step 2：实现**

```go
// Package crypto 定义加密货币标的 ID 规则。
package crypto

import (
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
)

// SubjectID 由交易所返回的 baseAsset / quoteAsset 生成 BASE-QUOTE 标的 ID；现货与永续共用。
func SubjectID(base, quote string) (string, error) {
	base, quote = strings.ToUpper(strings.TrimSpace(base)), strings.ToUpper(strings.TrimSpace(quote))
	if !validAsset(base) || !validAsset(quote) {
		return "", fmt.Errorf("%w: invalid crypto asset pair %q/%q", marketdata.ErrUnsupportedSymbol, base, quote)
	}
	return base + "-" + quote, nil
}

func validAsset(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}
```

stockcn：`CanonicalSubjectID` → `SubjectID`，`CanonicalSubjectIDWithExchange` → `SubjectIDWithExchange`，用 `gopls rename` 或 `rg -l 'CanonicalSubjectID' modules/collector | xargs sed -i '' ...` 批量替换后人工检查。

`marketdata.CanonicalCryptoSubjectID` 的调用方（`marketfetch/kline_pipeline.go`、`scheduler.go`、`period_readiness.go`）：这些地方传入的已经是标的 ID，新规则下 ID 不再带 `-SPOT` / `-SWAP` 后缀，改为 `strings.ToUpper(strings.TrimSpace(id))`。

- [ ] **Step 3：运行测试**

Run: `go test ./modules/collector/internal/sources/... ./modules/collector/internal/marketdata/... ./modules/collector/internal/marketfetch/... -count=1`
Expected: PASS（测试中带 `-SPOT` 后缀的用例改为无后缀）。

- [ ] **Step 4：验证无残留并提交**

Run: `rg -n 'Canonical(Crypto)?SubjectID|CanonicalizeCryptoInstrument' modules/collector`
Expected: 无输出

```bash
git add modules/collector
git commit -m "refactor(collector): 标的 ID 规则统一为 SubjectID"
```

### Task 2.2：`ToSubjectID` 不回退、`ToSymbol` 不依赖存储映射

**Files：**
- Modify: `modules/collector/internal/sources/binance/symbol.go`、`symbol_identity.go`、`marketdata_adapter.go` 及测试
- Modify: `modules/collector/internal/marketwiring/handler.go`
- Modify: `modules/collector/internal/marketfetch/symbol_resolver.go`、`timer.go`
- Modify: `modules/collector/internal/marketdata/types.go`（合并 `ProductType`）

- [ ] **Step 1：失败测试**

`sources/binance/symbol_test.go`：

```go
func TestToSubjectIDRejectsMissingAssets(t *testing.T) {
	if _, err := ToSubjectID(&exchange.SymbolInfo{Symbol: "BTCUSDT"}); err == nil {
		t.Fatal("must not fall back to raw symbol")
	}
	got, err := ToSubjectID(&exchange.SymbolInfo{Symbol: "1000PEPEUSDT", BaseAsset: "1000PEPE", QuoteAsset: "USDT"})
	if err != nil || got != "1000PEPE-USDT" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestToSymbol(t *testing.T) {
	got, err := ToSymbol("BTC-USDT")
	if err != nil || got != "BTCUSDT" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := ToSymbol("600000.XSHG"); !errors.Is(err, marketdata.ErrUnsupportedSymbol) {
		t.Fatalf("err = %v", err)
	}
}
```

`marketdata_adapter_test.go` 新增：列表中两项生成同一 ID 时两项都被拒绝；一项缺 base 时被跳过且 `FetchInstrumentSnapshot` 仍返回其余项。

- [ ] **Step 2：实现**

`symbol_identity.go` 改为：

```go
package binance

import (
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/crypto"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/exchange"
)

// ToSymbol 把 BASE-QUOTE 标的 ID 换算为币安代码（现货与永续相同）。
func ToSymbol(subjectID string) (string, error) {
	parts := strings.Split(strings.ToUpper(strings.TrimSpace(subjectID)), "-")
	if len(parts) != 2 {
		return "", fmt.Errorf("%w: subject %s is not a BASE-QUOTE pair", marketdata.ErrUnsupportedSymbol, subjectID)
	}
	if _, err := crypto.SubjectID(parts[0], parts[1]); err != nil {
		return "", err
	}
	return parts[0] + parts[1], nil
}

// ToSubjectID 只使用结构化的 baseAsset / quoteAsset。
func ToSubjectID(symbol *exchange.SymbolInfo) (string, error) {
	if symbol == nil {
		return "", fmt.Errorf("%w: nil symbol", marketdata.ErrUnsupportedSymbol)
	}
	return crypto.SubjectID(symbol.BaseAsset, symbol.QuoteAsset)
}
```

删除 `ProviderSymbol`、`isBinanceSymbolPart`，删除 `symbol.go` 中的 `normalizedSubjectID`。`marketdata_adapter.go` 的 `FetchInstrumentSnapshot` 循环改为：

```go
	instruments := make([]marketdata.Instrument, 0, len(symbols))
	index := make(map[string]int, len(symbols))
	duplicated := map[string]bool{}
	skipped := 0
	for _, symbol := range symbols {
		subjectID, err := ToSubjectID(symbol)
		if err != nil {
			skipped++
			continue
		}
		if _, ok := index[subjectID]; ok {
			duplicated[subjectID] = true
			continue
		}
		index[subjectID] = len(instruments)
		instruments = append(instruments, marketdata.Instrument{
			SubjectID: subjectID, ProviderSymbol: symbol.Symbol, Exchange: "binance",
			Name: strings.ToUpper(symbol.BaseAsset) + "/" + strings.ToUpper(symbol.QuoteAsset), Status: symbol.Status,
			BaseAsset: symbol.BaseAsset, QuoteAsset: symbol.QuoteAsset,
		})
	}
	if len(duplicated) > 0 {
		kept := instruments[:0]
		for _, item := range instruments {
			if !duplicated[item.SubjectID] {
				kept = append(kept, item)
			}
		}
		instruments = kept
	}
	if skipped > 0 || len(duplicated) > 0 {
		log.WarnContextf(ctx, "binance symbol list skipped=%d duplicated=%d", skipped, len(duplicated))
	}
```

本任务只停止在币安适配器中给 `CanonicalSymbol`、`MinQty`、`MaxQty`、`TickSize`、`LotSize` 赋值；这些字段仍被旧 instrument 链路引用，在 Task 5.1 与该链路一起删除。

`marketwiring/handler.go`：

```go
func ResolveSymbol(provider, marketID, marketType, subjectID string) (string, error) {
	if strings.EqualFold(strings.TrimSpace(provider), "binance") {
		return binance.ToSymbol(subjectID)
	}
	return marketfetch.DefaultProviderSymbol(marketID, marketType, subjectID)
}
```

删除 `CompactSymbol`（用 `rg -n CompactSymbol modules` 确认调用方并一并改为 `ResolveSymbol`）。`marketfetch.SymbolResolver` 类型签名去掉 `configured`；`resolveProviderSymbol`、`DefaultProviderSymbol`、`marketProviderSymbolForMarket` 同步去掉 `configured`：stockhk / stockus 分支直接取 `.` 前代码，equity 分支调用 `stockProviderSymbol(subjectID)`，其余返回 `ErrUnsupportedSymbol`。

`marketdata/types.go`：删除 `ProductType` 及 `Product*` 常量，全部调用方改用 `InstrumentType` / `Instrument*`（`binance.AdapterConfig.ProductType` 改名 `InstrumentType`）。

- [ ] **Step 3：运行测试**

Run: `go test ./modules/collector/... -count=1`
Expected: PASS。`scheduler.go` / `reconciler.go` 中仍传入 `subject.ExternalSymbol` 的调用会编译失败，这一步把参数直接删掉（`ExternalSymbol` 字段在 Task 4.3 随 `DatasetSubject` 域模型一起删除）。

- [ ] **Step 4：验证无残留并提交**

Run: `rg -n 'ProductType|ProductSpot|ProductSwap|normalizedSubjectID|binance\.ProviderSymbol|CompactSymbol' modules/collector`
Expected: 无输出

```bash
git add modules/collector
git commit -m "refactor(collector): 适配器自行换算标的代码，移除存储映射依赖"
```

---

## 阶段 3：`moox-collector-subject`

### Task 3.1：运行配置

**Files：**
- Create: `modules/collector/internal/subjectsync/config.go`、`config_test.go`
- Create: `modules/collector/config/subject.yaml`

- [ ] **Step 1：失败测试**

```go
package subjectsync

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadConfigDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "subject.yaml")
	if err := os.WriteFile(path, []byte(`
storage:
  target: 127.0.0.1:11003
attributes:
  - space_id: crypto
    sources: [binance]
    cron: "0 0 * * *"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PollInterval != time.Minute || cfg.FetchTimeout != 2*time.Minute || cfg.Attributes[0].Timezone != "UTC" {
		t.Fatalf("cfg = %+v", cfg)
	}
	if cfg.HealthAddr != "127.0.0.1:11413" {
		t.Fatalf("health addr = %q", cfg.HealthAddr)
	}
}

func TestLoadConfigRejectsBadCron(t *testing.T) {
	path := filepath.Join(t.TempDir(), "subject.yaml")
	_ = os.WriteFile(path, []byte("storage: {target: x}\nattributes: [{space_id: crypto, sources: [binance], cron: 'bad'}]\n"), 0o600)
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("want error")
	}
}
```

- [ ] **Step 2：实现**

```go
package subjectsync

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/robfig/cron"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Storage      StorageConfig   `yaml:"storage"`
	PollInterval time.Duration   `yaml:"poll_interval"`
	FetchTimeout time.Duration   `yaml:"fetch_timeout"`
	HealthAddr   string          `yaml:"health_addr"`
	Attributes   []AttributeJob  `yaml:"attributes"`
}

type StorageConfig struct {
	// Target 是 service gateway 地址；为空时读取 MOOX_COLLECTOR_STORAGE_RPC_GATEWAY_TARGET。
	Target string `yaml:"target"`
}

type AttributeJob struct {
	SpaceID  string   `yaml:"space_id"`
	Sources  []string `yaml:"sources"`
	Cron     string   `yaml:"cron"`
	Timezone string   `yaml:"timezone"`
}

func LoadConfig(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.Storage.Target == "" {
		cfg.Storage.Target = strings.TrimSpace(os.Getenv("MOOX_COLLECTOR_STORAGE_RPC_GATEWAY_TARGET"))
	}
	if cfg.Storage.Target == "" {
		return Config{}, fmt.Errorf("storage.target is required")
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = time.Minute
	}
	if cfg.FetchTimeout <= 0 {
		cfg.FetchTimeout = 2 * time.Minute
	}
	if cfg.HealthAddr == "" {
		cfg.HealthAddr = "127.0.0.1:11413"
	}
	for i := range cfg.Attributes {
		job := &cfg.Attributes[i]
		if job.Timezone == "" {
			job.Timezone = "UTC"
		}
		if job.SpaceID == "" || len(job.Sources) == 0 {
			return Config{}, fmt.Errorf("attributes[%d]: space_id and sources are required", i)
		}
		if _, err := cron.ParseStandard(job.Cron); err != nil {
			return Config{}, fmt.Errorf("attributes[%d].cron: %w", i, err)
		}
		if _, err := time.LoadLocation(job.Timezone); err != nil {
			return Config{}, fmt.Errorf("attributes[%d].timezone: %w", i, err)
		}
	}
	return cfg, nil
}
```

`collector` 的 `go.mod` 把 `github.com/robfig/cron v1.2.0` 从 indirect 改为直接依赖（`go mod tidy` 自动处理）。`subject.yaml`：

```yaml
storage:
  target: ""   # 为空时读取 MOOX_COLLECTOR_STORAGE_RPC_GATEWAY_TARGET
poll_interval: 1m
fetch_timeout: 2m
health_addr: 127.0.0.1:11413
attributes:
  - space_id: stockcn
    sources: [eastmoney]
    cron: "0 8 * * *"
    timezone: Asia/Shanghai
  - space_id: crypto
    sources: [binance]
    cron: "0 0 * * *"
    timezone: UTC
```

- [ ] **Step 3：运行测试并提交**

Run: `go test ./modules/collector/internal/subjectsync/ -count=1`
Expected: PASS

```bash
git add modules/collector/internal/subjectsync modules/collector/config/subject.yaml modules/collector/go.mod modules/collector/go.sum
git commit -m "feat(collector-subject): 运行配置"
```

### Task 3.2：标的列表抓取与多源合并

**Files：**
- Create: `modules/collector/internal/subjectsync/lister.go`、`lister_test.go`
- Create: `modules/collector/internal/marketwiring/subject_listers.go`、`subject_listers_test.go`

- [ ] **Step 1：失败测试**

```go
package subjectsync

import (
	"context"
	"errors"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
)

type fakeLister struct {
	items []marketdata.Instrument
	err   error
}

func (f fakeLister) List(context.Context) ([]marketdata.Instrument, error) { return f.items, f.err }

func TestFetchSnapshotUnion(t *testing.T) {
	listers := Listers{
		{Source: "sina", InstrumentType: "equity"}:      fakeLister{items: []marketdata.Instrument{{SubjectID: "600000.XSHG", Name: "浦发银行"}}},
		{Source: "eastmoney", InstrumentType: "equity"}: fakeLister{items: []marketdata.Instrument{{SubjectID: "600000.XSHG", Name: "浦发"}, {SubjectID: "920000.XBSE"}}},
	}
	items, err := FetchSnapshot(context.Background(), listers, []string{"sina", "eastmoney"}, "equity")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].SubjectID != "600000.XSHG" || items[0].Name != "浦发银行" {
		t.Fatalf("items = %+v", items)
	}
}

func TestFetchSnapshotFailsOnAnySourceError(t *testing.T) {
	listers := Listers{
		{Source: "sina", InstrumentType: "equity"}:      fakeLister{err: errors.New("timeout")},
		{Source: "eastmoney", InstrumentType: "equity"}: fakeLister{items: []marketdata.Instrument{{SubjectID: "600000.XSHG"}}},
	}
	if _, err := FetchSnapshot(context.Background(), listers, []string{"sina", "eastmoney"}, "equity"); err == nil {
		t.Fatal("want error")
	}
}

func TestFetchSnapshotEmptyAndUnsupported(t *testing.T) {
	listers := Listers{{Source: "binance", InstrumentType: "spot"}: fakeLister{}}
	if _, err := FetchSnapshot(context.Background(), listers, []string{"binance"}, "spot"); !errors.Is(err, ErrEmptySnapshot) {
		t.Fatalf("err = %v", err)
	}
	if _, err := FetchSnapshot(context.Background(), listers, []string{"okx"}, "spot"); err == nil {
		t.Fatal("unsupported source must fail")
	}
}
```

- [ ] **Step 2：实现**

```go
package subjectsync

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
)

var ErrEmptySnapshot = errors.New("subject list is empty")

// Lister 返回某抓取源某产品类型的完整标的列表，SubjectID 已由适配器 ToSubjectID 生成。
type Lister interface {
	List(ctx context.Context) ([]marketdata.Instrument, error)
}

type ListerKey struct {
	Source         string
	InstrumentType string
}

type Listers map[ListerKey]Lister

// FetchSnapshot 按 sources 顺序拉取并按标的 ID 取并集；同一标的以排在前面的源为准。
// 任一源失败即整体失败，避免部分源失败导致成员被误置为失效。
func FetchSnapshot(ctx context.Context, listers Listers, sources []string, instrumentType string) ([]marketdata.Instrument, error) {
	merged := map[string]marketdata.Instrument{}
	for _, source := range sources {
		lister, ok := listers[ListerKey{Source: source, InstrumentType: instrumentType}]
		if !ok {
			return nil, fmt.Errorf("source %s does not support %s subject listing", source, instrumentType)
		}
		items, err := lister.List(ctx)
		if err != nil {
			return nil, fmt.Errorf("list %s %s: %w", source, instrumentType, err)
		}
		for _, item := range items {
			if _, exists := merged[item.SubjectID]; !exists && item.SubjectID != "" {
				merged[item.SubjectID] = item
			}
		}
	}
	if len(merged) == 0 {
		return nil, ErrEmptySnapshot
	}
	out := make([]marketdata.Instrument, 0, len(merged))
	for _, item := range merged {
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SubjectID < out[j].SubjectID })
	return out, nil
}

// Supported 返回 source -> instrument_types，用于登记到 t_data_sources.c_attrs_json.subject_listing。
func (l Listers) Supported() map[string][]string {
	out := map[string][]string{}
	for key := range l {
		out[key.Source] = append(out[key.Source], key.InstrumentType)
	}
	for source := range out {
		sort.Strings(out[source])
	}
	return out
}
```

`marketwiring/subject_listers.go`：把 `NewStockInstrumentPipeline` / `NewCryptoInstrumentPipeline` 中「构造 provider」的部分抽出，返回 `subjectsync.Listers`：

```go
package marketwiring

// fetcherLister 把 marketdata.InstrumentFetcher 适配为 subjectsync.Lister。
type fetcherLister struct {
	fetcher  marketdata.InstrumentFetcher
	marketID marketdata.MarketID
}

func (l fetcherLister) List(ctx context.Context) ([]marketdata.Instrument, error) {
	snapshot, err := l.fetcher.FetchInstrumentSnapshot(ctx, marketdata.InstrumentRequest{MarketID: l.marketID, SnapshotAt: time.Now().UTC()})
	if err != nil {
		return nil, err
	}
	return snapshot.Instruments, nil
}

// NewSubjectListers 返回本进程支持标的列表的全部抓取源。
func NewSubjectListers() (subjectsync.Listers, error) {
	listers := subjectsync.Listers{
		{Source: "binance", InstrumentType: "spot"}: fetcherLister{fetcher: binance.NewMarketDataAdapter(binance.AdapterConfig{InstrumentType: marketdata.InstrumentSpot}), marketID: "crypto"},
		{Source: "binance", InstrumentType: "swap"}: fetcherLister{fetcher: binance.NewMarketDataAdapter(binance.AdapterConfig{InstrumentType: marketdata.InstrumentSwap}), marketID: "crypto"},
	}
	route, err := marketfetch.LoadStockCNRoute()
	if err != nil {
		return nil, err
	}
	providerConfigs, err := marketfetch.LoadStockCNProviderRuntime(route)
	if err != nil {
		return nil, err
	}
	for _, providerID := range route.InstrumentProviders() {
		config, ok := providerConfigs[providerID]
		if !ok || !config.InstrumentEnabled {
			continue
		}
		provider, err := newStockCNProvider(providerID, config)
		if err != nil {
			return nil, err
		}
		fetcher, ok := provider.(marketdata.InstrumentFetcher)
		if !ok {
			continue
		}
		listers[subjectsync.ListerKey{Source: providerID, InstrumentType: "equity"}] = fetcherLister{fetcher: fetcher, marketID: "stockcn"}
	}
	return listers, nil
}
```

stockcn provider 的 `FetchInstrumentSnapshot` 内部生成 `SubjectID` 时须使用 `stockcn.SubjectID` / `SubjectIDWithExchange`，失败项跳过并计数（检查 `sources/stockcn/{sina,eastmoney,baidu}/provider.go`，若已有回退为原始代码的分支则删除）。`subject_listers_test.go` 断言 `NewSubjectListers().Supported()` 至少包含 `binance: [spot swap]`。

- [ ] **Step 3：运行测试并提交**

Run: `go test ./modules/collector/internal/subjectsync/ ./modules/collector/internal/marketwiring/ -count=1`
Expected: PASS

```bash
git add modules/collector/internal/subjectsync modules/collector/internal/marketwiring modules/collector/internal/sources
git commit -m "feat(collector-subject): 标的列表抓取与多源合并"
```

### Task 3.3：标签维护运行器

**Files：**
- Create: `modules/collector/internal/subjectsync/tag_runner.go`、`tag_runner_test.go`

- [ ] **Step 1：失败测试**

```go
package subjectsync

import (
	"context"
	"errors"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
)

type fakeTagStore struct {
	tags     []*pb.Tag
	applied  map[string]int
	failures map[string]string
}

func (f *fakeTagStore) ListTags(context.Context) ([]*pb.Tag, error) { return f.tags, nil }
func (f *fakeTagStore) ApplyTagSnapshot(_ context.Context, spaceID, tagID string, _ time.Time, items []*pb.TagSnapshotItem) error {
	f.applied[tagID] = len(items)
	return nil
}
func (f *fakeTagStore) ReportTagRunFailure(_ context.Context, spaceID, tagID string, _ time.Time, msg string) error {
	f.failures[tagID] = msg
	return nil
}

func TestTagDue(t *testing.T) {
	now := time.Date(2026, 9, 25, 10, 30, 0, 0, time.UTC)
	cases := []struct {
		name string
		tag  *pb.Tag
		want bool
	}{
		{"never run", &pb.Tag{Cron: "0 * * * *", Timezone: "UTC", UpdatedAt: "2026-09-25 09:00:00"}, true},
		{"cron elapsed", &pb.Tag{Cron: "0 * * * *", Timezone: "UTC", LastRunAt: "2026-09-25 09:00:00", UpdatedAt: "2026-09-01 00:00:00"}, true},
		{"cron not elapsed", &pb.Tag{Cron: "0 * * * *", Timezone: "UTC", LastRunAt: "2026-09-25 10:00:00", UpdatedAt: "2026-09-01 00:00:00"}, false},
		{"edited after run", &pb.Tag{Cron: "0 * * * *", Timezone: "UTC", LastRunAt: "2026-09-25 10:00:00", UpdatedAt: "2026-09-25 10:10:00"}, true},
		{"timezone", &pb.Tag{Cron: "0 18 * * *", Timezone: "Asia/Shanghai", LastRunAt: "2026-09-24 10:00:00", UpdatedAt: "2026-09-01 00:00:00"}, true},
	}
	for _, c := range cases {
		got, err := tagDue(c.tag, now)
		if err != nil || got != c.want {
			t.Errorf("%s: due = %v, err = %v", c.name, got, err)
		}
	}
}

func TestRunOnceAppliesAndReports(t *testing.T) {
	store := &fakeTagStore{
		tags: []*pb.Tag{
			{SpaceId: "crypto", TagId: "binance_spot", Mode: "auto", Sources: []string{"binance"}, InstrumentType: "spot", Cron: "0 * * * *", Timezone: "UTC"},
			{SpaceId: "crypto", TagId: "binance_swap", Mode: "auto", Sources: []string{"binance"}, InstrumentType: "swap", Cron: "0 * * * *", Timezone: "UTC"},
			{SpaceId: "crypto", TagId: "watch", Mode: "manual", Cron: "0 * * * *", Timezone: "UTC"},
		},
		applied: map[string]int{}, failures: map[string]string{},
	}
	listers := Listers{
		{Source: "binance", InstrumentType: "spot"}: fakeLister{items: []marketdata.Instrument{{SubjectID: "BTC-USDT"}}},
		{Source: "binance", InstrumentType: "swap"}: fakeLister{err: errors.New("451")},
	}
	runner := &TagRunner{Store: store, Listers: listers, FetchTimeout: time.Second, Now: func() time.Time { return time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC) }}
	runner.RunOnce(context.Background())
	if store.applied["binance_spot"] != 1 {
		t.Fatalf("applied = %v", store.applied)
	}
	if store.failures["binance_swap"] == "" {
		t.Fatalf("failures = %v", store.failures)
	}
	if _, ok := store.applied["watch"]; ok {
		t.Fatal("manual tag without probe must be skipped")
	}
}
```

- [ ] **Step 2：实现**

```go
package subjectsync

import (
	"context"
	"fmt"
	"time"

	"github.com/robfig/cron"
	"trpc.group/trpc-go/trpc-go/log"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

const sqliteTimeLayout = "2006-01-02 15:04:05"

type TagStore interface {
	ListTags(ctx context.Context) ([]*pb.Tag, error)
	ApplyTagSnapshot(ctx context.Context, spaceID, tagID string, runAt time.Time, items []*pb.TagSnapshotItem) error
	ReportTagRunFailure(ctx context.Context, spaceID, tagID string, runAt time.Time, message string) error
}

type TagRunner struct {
	Store        TagStore
	Listers      Listers
	FetchTimeout time.Duration
	Now          func() time.Time
	Metrics      *Metrics
}

func probing(tag *pb.Tag) bool {
	return len(tag.GetSources()) > 0 && tag.GetInstrumentType() != ""
}

// tagDue：从未运行、定义在上次运行后被修改、或上次运行后的下一个 cron 时刻已到。
func tagDue(tag *pb.Tag, now time.Time) (bool, error) {
	if tag.GetLastRunAt() == "" {
		return true, nil
	}
	lastRun, err := time.ParseInLocation(sqliteTimeLayout, tag.GetLastRunAt(), time.UTC)
	if err != nil {
		return false, fmt.Errorf("parse last_run_at: %w", err)
	}
	if tag.GetUpdatedAt() > tag.GetLastRunAt() {
		return true, nil
	}
	schedule, err := cron.ParseStandard(tag.GetCron())
	if err != nil {
		return false, err
	}
	loc, err := time.LoadLocation(tag.GetTimezone())
	if err != nil {
		return false, err
	}
	return !schedule.Next(lastRun.In(loc)).After(now), nil
}

// RunOnce 串行运行全部到期标签。
func (r *TagRunner) RunOnce(ctx context.Context) {
	tags, err := r.Store.ListTags(ctx)
	if err != nil {
		log.ErrorContextf(ctx, "list tags: %v", err)
		return
	}
	for _, tag := range tags {
		if !probing(tag) {
			continue
		}
		now := r.Now().UTC()
		due, err := tagDue(tag, now)
		if err != nil {
			log.ErrorContextf(ctx, "tag %s/%s schedule: %v", tag.GetSpaceId(), tag.GetTagId(), err)
			continue
		}
		if due {
			r.runTag(ctx, tag, now)
		}
	}
}

func (r *TagRunner) runTag(ctx context.Context, tag *pb.Tag, runAt time.Time) {
	fetchCtx, cancel := context.WithTimeout(ctx, r.FetchTimeout)
	defer cancel()
	instruments, err := FetchSnapshot(fetchCtx, r.Listers, tag.GetSources(), tag.GetInstrumentType())
	if err == nil {
		items := make([]*pb.TagSnapshotItem, 0, len(instruments))
		for _, item := range instruments {
			items = append(items, snapshotItem(tag, item))
		}
		err = r.Store.ApplyTagSnapshot(ctx, tag.GetSpaceId(), tag.GetTagId(), runAt, items)
	}
	if err != nil {
		log.WarnContextf(ctx, "tag %s/%s run failed: %v", tag.GetSpaceId(), tag.GetTagId(), err)
		r.Metrics.observeTagFailure(tag)
		if reportErr := r.Store.ReportTagRunFailure(ctx, tag.GetSpaceId(), tag.GetTagId(), runAt, err.Error()); reportErr != nil {
			log.ErrorContextf(ctx, "report tag %s/%s failure: %v", tag.GetSpaceId(), tag.GetTagId(), reportErr)
		}
		return
	}
	r.Metrics.observeTagSuccess(tag, runAt)
}
```

`snapshotItem` 按空间填写 `subject_type` / `market` / `currency` / `timezone`：

```go
func snapshotItem(tag *pb.Tag, item marketdata.Instrument) *pb.TagSnapshotItem {
	out := &pb.TagSnapshotItem{SubjectId: item.SubjectID, Name: item.Name}
	switch tag.GetSpaceId() {
	case "crypto":
		out.SubjectType, out.Market, out.Currency, out.Timezone = "crypto_pair", "CRYPTO", strings.ToUpper(item.QuoteAsset), "UTC"
	case "stockcn":
		out.SubjectType, out.Market, out.Currency, out.Timezone = "stock", item.Exchange, "CNY", "Asia/Shanghai"
	}
	return out
}
```

`subject_type` / `market` 取值以现有 `InstrumentPipeline` 写入 `t_subjects` 的值为准（在 `instrument_pipeline.go` 中搜索 `SubjectType:` 核对），保证与已有行一致。

`Metrics`（`metrics.go`）：基于 `trpc-metrics-prometheus` 或 `packages/healthz` 的 metrics handler，暴露 `moox_subject_tag_last_success_timestamp{space,tag}`、`moox_subject_tag_failures_total{space,tag}`；`Metrics` 为 nil 时方法直接返回。有效 / 失效成员数由 Storage 的 `ListTags` 统计，monitor 直接读取，不在同步器重复上报。

- [ ] **Step 3：运行测试并提交**

Run: `go test ./modules/collector/internal/subjectsync/ -count=1`
Expected: PASS

```bash
git add modules/collector/internal/subjectsync
git commit -m "feat(collector-subject): 标签维护运行器"
```

### Task 3.4：属性维护运行器

**Files：**
- Create: `modules/collector/internal/subjectsync/attr_runner.go`、`attr_runner_test.go`

- [ ] **Step 1：失败测试**

- 到期判断：`attrDue(job, lastRun, now)`，进程启动后首次按 cron 下一时刻执行（不在启动时立即执行）；
- 抽取：crypto 条目得到 `{base, quote}`，stockcn 条目得到 `{exchange}`，`name` 取 `Instrument.Name`；
- 失败隔离：某个 job 的 lister 返回错误时只记日志，其它 job 仍执行，`UpdateSubjectAttributes` 只对成功的 job 调用。

写出与 Task 3.3 同风格的完整测试代码（fake store 记录调用）。

- [ ] **Step 2：实现**

```go
type AttributeStore interface {
	UpdateSubjectAttributes(ctx context.Context, spaceID string, items []*pb.SubjectAttributes) (updated, skipped int, err error)
}

type AttributeRunner struct {
	Store        AttributeStore
	Listers      Listers
	Jobs         []AttributeJob
	FetchTimeout time.Duration
	Now          func() time.Time
	next         []time.Time
}

func attributes(spaceID string, item marketdata.Instrument) map[string]string {
	switch spaceID {
	case "crypto":
		return map[string]string{"base": strings.ToUpper(item.BaseAsset), "quote": strings.ToUpper(item.QuoteAsset)}
	case "stockcn":
		return map[string]string{"exchange": item.Exchange}
	}
	return nil
}
```

`RunOnce(ctx)`：首次调用时为每个 job 计算 `next = schedule.Next(now.In(loc))`；此后 `now >= next` 的 job 执行并重算 `next`。执行时对该 job 所属空间对应的每个 instrument type 调用 `FetchSnapshot`（crypto 用 `spot`，stockcn 用 `equity`；`AttributeJob` 增加可选 `instrument_type` 字段，缺省按上述映射），组装 `SubjectAttributes` 后调用 `UpdateSubjectAttributes`，记录 `updated` / `skipped`。上市日期、行业等需要适配器扩展 `Instrument` 字段，不在本计划范围内。

- [ ] **Step 3：运行测试并提交**

Run: `go test ./modules/collector/internal/subjectsync/ -count=1`
Expected: PASS

```bash
git add modules/collector/internal/subjectsync
git commit -m "feat(collector-subject): 标的公共属性维护"
```

### Task 3.5：Storage 客户端、数据源登记与入口

**Files：**
- Create: `modules/collector/internal/subjectsync/storage_client.go`、`storage_client_test.go`
- Create: `modules/collector/internal/subjectsync/service.go`
- Create: `modules/collector/cmd/subject/main.go`

- [ ] **Step 1：Storage 客户端**

```go
type StorageClient struct {
	client pb.MetadataClientProxy
	auth   *pb.AuthInfo
}

func NewStorageClient(target string) *StorageClient {
	options := gatewayauth.NewTRPCClientOptions(target, storageGatewayNodeID(), gatewayauth.CredentialsFromEnv())
	return &StorageClient{client: pb.NewMetadataClientProxy(options...)}
}
```

实现 `TagStore` 与 `AttributeStore`：

- `ListTags` 按 `crypto`、`stockcn`… 不限空间（`space_id` 为空）分页拉全量；
- `ApplyTagSnapshot` 的 `run_at` 用 RFC3339；
- 每个调用检查 `RetInfo.Code != SUCCESS` 时返回错误。

另实现 `RegisterSubjectListing(ctx, supported map[string][]string)`：对每个 source 找到其所属的 `t_data_sources` 行（crypto 空间 `binance`；stockcn 空间 `sina`、`eastmoney`、`baidu`，以 `config/setup/metadata.yaml` 中 `data_sources` 的 ID 为准），`GetDataSource` 后在 `attributes["subject_listing"]` 写入 JSON（如 `{"instrument_types":["spot","swap"]}`），再 `UpdateDataSource`。测试用 fake `MetadataClientProxy` 断言请求字段。

`storageGatewayNodeID()` 与 `marketstorage.collectorStorageGatewayNodeID` 相同逻辑：把后者导出为 `marketstorage.StorageGatewayNodeID()` 复用，不要复制。

- [ ] **Step 2：服务循环**

```go
type Service struct {
	Tags       *TagRunner
	Attributes *AttributeRunner
	Poll       time.Duration
}

// Run 每个轮询周期先跑标签维护、再跑属性维护，二者互不影响。
func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(s.Poll)
	defer ticker.Stop()
	for {
		s.Tags.RunOnce(ctx)
		s.Attributes.RunOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
```

属性任务执行时间较长会推迟下一轮标签维护；两者在同一 goroutine 串行执行，保证单实例内不会并发写 Storage。

- [ ] **Step 3：入口**

```go
package main

import (
	"context"
	"flag"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketwiring"
	"github.com/mooyang-code/moox/modules/collector/internal/subjectsync"
	"github.com/mooyang-code/moox/packages/healthz"
	"trpc.group/trpc-go/trpc-go/log"
)

func main() {
	configPath := flag.String("conf", "config/subject.yaml", "runtime config path")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, err := subjectsync.LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("moox-collector-subject 配置错误: %v", err)
	}
	listers, err := marketwiring.NewSubjectListers()
	if err != nil {
		log.Fatalf("moox-collector-subject 初始化抓取源失败: %v", err)
	}
	storage := subjectsync.NewStorageClient(cfg.Storage.Target)
	if err := storage.RegisterSubjectListing(ctx, listers.Supported()); err != nil {
		log.Fatalf("moox-collector-subject 登记抓取源失败: %v", err)
	}
	state := healthz.NewState("collector-subject", "", "", "")
	go func() {
		mux := healthz.StandardMux(state.Snapshot, nil)
		if err := http.ListenAndServe(cfg.HealthAddr, mux); err != nil {
			log.Errorf("health server: %v", err)
		}
	}()
	service := &subjectsync.Service{
		Tags:       &subjectsync.TagRunner{Store: storage, Listers: listers, FetchTimeout: cfg.FetchTimeout, Now: time.Now},
		Attributes: &subjectsync.AttributeRunner{Store: storage, Listers: listers, Jobs: cfg.Attributes, FetchTimeout: cfg.FetchTimeout, Now: time.Now},
		Poll:       cfg.PollInterval,
	}
	state.SetReady(true)
	log.Info("启动 moox-collector-subject")
	service.Run(ctx)
}
```

`healthz.State` 的实际 API（`Snapshot`、`SetReady`）以 `packages/healthz/healthz.go` 为准；若 `StandardMux` 的 metrics 参数需要 prometheus handler，传入 `promhttp.Handler()`。

- [ ] **Step 4：编译与测试**

Run:
```bash
go test ./modules/collector/internal/subjectsync/ -count=1
go build -o /tmp/moox-collector-subject ./modules/collector/cmd/subject
```
Expected: PASS；生成二进制。

- [ ] **Step 5：提交**

```bash
git add modules/collector/internal/subjectsync modules/collector/internal/marketstorage modules/collector/cmd/subject
git commit -m "feat(collector-subject): moox-collector-subject 入口与 Storage 客户端"
```

### Task 3.6：构建与部署

**Files：**
- Modify: `scripts/build/build.sh`
- Modify: `scripts/deploy/deploy-moox.sh`
- Modify: `config/setup/service-deployments.yaml`
- Modify: `modules/admin/internal/service/sysdeploy/defaults.go`

- [ ] **Step 1：构建**

`build.sh` 在 `build_go modules/collector ./cmd/server moox-collector 0` 之后加：

```bash
build_go modules/collector ./cmd/subject moox-collector-subject 0
```

- [ ] **Step 2：部署脚本**

参照 `start_collector` 新增 `start_collector_subject`：

```bash
start_collector_subject() {
  if [[ "${WITH_COLLECTOR}" != "1" ]]; then
    echo "collector is disabled in this deployment package" >&2
    exit 2
  fi
  gateway_service_env_for collector
  STARTUP_WAIT_SECONDS="${MOOX_COLLECTOR_SUBJECT_STARTUP_WAIT_SECONDS:-15}"
  start_service "collector-subject" "${ROOT}/collector" \
    env "${CALLER_GATEWAY_SERVICE_ENV[@]}" \
      "MOOX_GATEWAY_TARGET_NODE=${MOOX_GATEWAY_NODE_ID}" \
      "MOOX_COLLECTOR_STORAGE_RPC_GATEWAY_TARGET=${LOCAL_STORAGE_RPC_GATEWAY_TARGET}" \
      "MOOX_COLLECTOR_STORAGE_RPC_GATEWAY_NODE_ID=${LOCAL_STORAGE_GATEWAY_NODE_ID}" \
      "${ROOT}/bin/moox-collector-subject" -conf=config/subject.yaml
}
```

同时：

- `copy_required_binary "moox-collector-subject"`；
- `--no-collector` 的 rsync 排除与删除列表加入 `/bin/moox-collector-subject`；
- 服务启动序列、停止、状态查询中 `collector` 出现的位置对应加入 `collector-subject`（用 `rg -n 'start_collector|"collector"' scripts/deploy/deploy-moox.sh` 定位）；
- 打包时拷贝 `modules/collector/config/subject.yaml` 到 `${ROOT}/collector/config/`。

- [ ] **Step 3：部署登记**

`service-deployments.yaml` 在 `moox_collector` 之后：

```yaml
  - name: moox_collector_subject
    kind: collector_subject
    deployment_mode: process
    protocol: http
    host: 127.0.0.1
    port: 11413
    gateway_enabled: false
    scope: internal
    status: active
    description: 标的采集器，按 cron 维护标签成员与标的公共属性；需部署在可直连交易所的机器上，单实例
    extra_config:
      health_url: http://127.0.0.1:11413/readyz
      health_kind: readiness
      monitor_enabled: true
```

`sysdeploy/defaults.go` 同步增加一项（不注册 gateway service id）。`kind` 取值若在 sysdeploy 有枚举校验，同步加入 `collector_subject`。

- [ ] **Step 4：验证**

Run:
```bash
bash -n scripts/deploy/deploy-moox.sh && bash -n scripts/build/build.sh
go test ./modules/admin/internal/service/sysdeploy/ -count=1
./scripts/build/build.sh 2>&1 | tail -5
ls bin/moox-collector-subject
```
Expected: 语法检查通过；测试 PASS；二进制存在（`build.sh` 的实际参数与输出目录以脚本为准）。

- [ ] **Step 5：提交**

```bash
git add scripts config/setup/service-deployments.yaml modules/admin/internal/service/sysdeploy
git commit -m "build: 新增 moox-collector-subject 构建与部署"
```

---

## 阶段 4：采集侧改为按标签选择

### Task 4.1：`subject_tags` 参数

**Files：**
- Modify: `modules/collector/internal/domain/collect_params.go`、`collect_params_test.go`

- [ ] **Step 1：失败测试**

```go
func TestParseCollectParamsSubjectTags(t *testing.T) {
	params, err := ParseCollectParams(`{"provider":"binance","source_id":"spot_http","instrument_type":"spot","subject_tags":["binance_spot"],"frequency":"1m","target_dataset_id":"ds"}`, "binance", "spot", "kline")
	if err != nil {
		t.Fatal(err)
	}
	if len(params.SubjectTags) != 1 || params.SubjectTags[0] != "binance_spot" {
		t.Fatalf("tags = %v", params.SubjectTags)
	}
}

func TestParseCollectParamsRejectsSymbolDataset(t *testing.T) {
	_, err := ParseCollectParams(`{"provider":"binance","symbol_source":"dataset","symbol_dataset_id":"x","frequency":"1m","target_dataset_id":"ds"}`, "binance", "spot", "kline")
	if err == nil {
		t.Fatal("symbol_source / symbol_dataset_id must be unknown fields")
	}
}

func TestSplitSubjectTags(t *testing.T) {
	clean, tags, err := SplitSubjectTags(`{"provider":"binance","subject_tags":["b","a","a"],"frequency":"1m"}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(clean, "subject_tags") || len(tags) != 2 || tags[0] != "a" {
		t.Fatalf("clean=%s tags=%v", clean, tags)
	}
}
```

- [ ] **Step 2：实现**

- `CollectParams` 删除 `SymbolSource`、`SymbolDatasetID`，新增 `SubjectTags []string \`json:"subject_tags,omitempty"\``；
- `Normalize` 删除 `sourceKind` 推导：kline 不再有 `Source`；resample 保留 `p.Source = CollectSource{Kind: "dataset", DatasetID: p.SourceDatasetID}`（`Kind` 常量改名为 `"dataset"`，含义是「源行情 Dataset」，与标的无关）；
- `validateCollectParamsShape` 的 `standardOnly` 改为 `[]string{"frequency", "history_policy"}`；`subject_tags` 两类任务都允许；
- `Validate` 的 kline 分支删除 `source.dataset_id` 要求；instrument 分支整段删除（`InstrumentDataType` 常量在 Task 5.1 删除）；
- 新增：

```go
// SplitSubjectTags 从任务参数中取出 subject_tags：标签只保存在 Dataset 上，任务参数不保留副本。
func SplitSubjectTags(raw string) (string, []string, error) {
	var values map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return "", nil, fmt.Errorf("parse collect params: %w", err)
	}
	var tags []string
	if value, ok := values["subject_tags"]; ok {
		if err := json.Unmarshal(value, &tags); err != nil {
			return "", nil, fmt.Errorf("parse subject_tags: %w", err)
		}
		delete(values, "subject_tags")
	}
	clean, err := json.Marshal(values)
	if err != nil {
		return "", nil, err
	}
	return string(clean), normalizeTags(tags), nil
}

func normalizeTags(tags []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag != "" && !seen[tag] {
			seen[tag] = true
			out = append(out, tag)
		}
	}
	sort.Strings(out)
	return out
}
```

- [ ] **Step 3：运行测试**

Run: `go test ./modules/collector/internal/domain/ -count=1`
Expected: PASS。其他包的编译错误（引用 `SymbolDatasetID`、`Source.DatasetID`）在 Task 4.2–4.4 修复，本任务与 4.2–4.4 **合并为一次提交**。

### Task 4.2：任务创建 / 更新 / 内置任务写入 Dataset 标签

**Files：**
- Modify: `modules/collector/internal/planner/taskresult/result.go`（含测试）
- Modify: `modules/collector/internal/rpc/service.go`（含测试）
- Modify: `modules/collector/internal/bootstrap/bootstrap.go`
- Modify: `modules/collector/internal/ruleseed/seed.go`（含测试）
- Modify: `config/setup/collection-tasks.yaml`

- [ ] **Step 1：失败测试**

- `taskresult`：`Ensure(..., Config{SubjectTags: []string{"binance_spot"}})` 创建的 Dataset `subject_tags == [binance_spot]`；已存在 Dataset 且标签不同 → 调用 `UpdateDataset` 更新（fake metadata client 断言）；
- `rpc/service`：`CreateTask` 请求 `collect_params` 含 `subject_tags`，保存后的任务 `collect_params` 不含 `subject_tags`，且 fake storage 收到 `ResolveSubjects(tags)`；`ResolveSubjects` 返回空 → `CreateTask` 返回参数错误「所选标签没有有效标的」；
- kline 任务 `subject_tags` 为空 → 参数错误；resample 任务 `subject_tags` 为空 → 使用源 Dataset 的 `subject_tags`。

- [ ] **Step 2：实现**

`taskresult.Config` 增加 `SubjectTags []string`；`Ensure` 创建 Dataset 时 `Dataset.SubjectTags = cfg.SubjectTags`；Dataset 已存在时，若 `normalize(existing.SubjectTags) != normalize(cfg.SubjectTags)`，把完整的 existing 拷贝一份、替换 `SubjectTags` 后 `UpdateDataset`（`UpdateDataset` 为整条覆盖，必须带全其它字段与 `revision`）。

`rpc/service.go` 创建 / 更新任务流程：

```go
	cleanParams, subjectTags, err := domain.SplitSubjectTags(task.CollectParams)
	if err != nil {
		return invalidParam(err)
	}
	task.CollectParams = cleanParams
	if task.DataType == "kline_resample" && len(subjectTags) == 0 {
		subjectTags, err = s.sourceDatasetTags(ctx, task.SpaceID, params.SourceDatasetID)
		if err != nil {
			return invalidParam(err)
		}
	}
	if len(subjectTags) == 0 {
		return invalidParam(errors.New("请选择标的标签"))
	}
	subjects, err := s.subjects.ResolveSubjects(ctx, task.SpaceID, subjectTags)
	if err != nil {
		return invalidParam(err)
	}
	if len(subjects) == 0 {
		return invalidParam(errors.New("所选标签没有有效标的"))
	}
```

之后调用 `EnsureWithCleanup(..., taskresult.Config{..., SubjectTags: subjectTags})`。`s.subjects` 为新增依赖（`ResolveSubjects` + `GetDataset` 的最小接口），在 `bootstrap.go` 注入 `marketstorage` 的 Metadata 客户端实现。

`ruleseed/seed.go` 与 `bootstrap.go:257` 的内置任务路径同样先 `SplitSubjectTags`，把标签传入 `collectorresult.Config.SubjectTags`；内置任务入库的 `collect_params` 不含 `subject_tags`。内置任务每次启动都会重新 `Ensure`，因此标签以种子文件为准。

编辑任务时前端需要回显标签：`GetTask` / `ListTasks` 响应中 `result` 已包含 Dataset ID，前端通过 `GetDataset` 读取 `subject_tags`（Task 7.3）。

`collection-tasks.yaml`：删除三个 instrument 内置任务；kline 任务把 `symbol_source` / `symbol_dataset_id` 替换为 `subject_tags`：

| task_id | subject_tags |
| --- | --- |
| `builtin-binance-spot-kline-*` | `[binance_spot]` |
| `builtin-binance-swap-kline-*` | `[binance_swap]` |
| `builtin-stockcn-kline-*` | `[cn_a_share]` |

（用 `rg -n 'symbol_dataset_id' config/setup/collection-tasks.yaml` 逐条替换。）

### Task 4.3：规划器按标签解析标的

**Files：**
- Modify: `modules/collector/internal/marketfetch/reconciler.go`、`scheduler.go`（含测试）
- Modify: `modules/collector/internal/planner/storagesource/source.go`（含测试）
- Modify: `modules/collector/internal/domain/` 中 `DatasetSubject` 域类型

- [ ] **Step 1：失败测试**

`reconciler_test.go`：

```go
func TestReconcilerGroupsResolveByDatasetTags(t *testing.T) {
	// fake datasetSource:
	//   GetDataset(crypto, ds_kline) -> DatasetInfo{SubjectTags: [binance_spot], DataSourceID: binance}
	//   ResolveSubjects(crypto, [binance_spot]) -> [BTC-USDT, ETH-USDT]
	// task params: provider=binance, instrument_type=spot, frequency=1m, target_dataset_id=ds_kline
	// 断言 groups[0].Subjects == [BTC-USDT ETH-USDT]，ExternalSymbols == {BTC-USDT: BTCUSDT, ETH-USDT: ETHUSDT}
}

func TestReconcilerKeepsPreviousGroupsWhenResolveEmpty(t *testing.T) {
	// 第一次 ResolveSubjects 返回 [BTC-USDT]；第二次返回空
	// 断言第二次 groups 与第一次一致，并记录告警（通过注入的 alert 回调计数）
}
```

`scheduler_test.go` 对 `Tick` 路径写等价的两个用例。

- [ ] **Step 2：实现**

`datasetSource` 接口改为：

```go
type datasetSource interface {
	GetDataset(context.Context, string, string) (storagesource.DatasetInfo, error)
	ResolveSubjects(context.Context, string, []string) ([]domain.Subject, error)
}
```

`storagesource.DatasetInfo` 增加 `SubjectTags []string`；`domain.Subject` 只保留 `SubjectID`、`Name`、`Status`；删除 `domain.DatasetSubject` 中的 `ExternalSymbol` 等字段（`rg -n 'ExternalSymbol' modules/collector` 逐一清理）。

`reconciler.groups` 中 kline 分支改为：

```go
		dataset, datasetErr := r.Symbols.GetDataset(ctx, spaceID, params.Target.DatasetID)
		if datasetErr != nil {
			log.WarnContextf(ctx, "skip collection task=%s: get dataset %s: %v", task.TaskID, params.Target.DatasetID, datasetErr)
			continue
		}
		subjects, subjectErr := r.Symbols.ResolveSubjects(ctx, spaceID, dataset.SubjectTags)
		if subjectErr != nil || len(subjects) == 0 {
			if previous, ok := r.lastGroups[task.TaskID]; ok {
				log.WarnContextf(ctx, "keep previous subjects for task=%s tags=%v: resolve err=%v count=%d", task.TaskID, dataset.SubjectTags, subjectErr, len(subjects))
				r.alertEmptyResolve(task.TaskID)
				groups = append(groups, previous...)
			}
			continue
		}
		...
		for _, subject := range subjects {
			subjectID := strings.ToUpper(strings.TrimSpace(subject.SubjectID))
			external, symbolErr := resolveProviderSymbol(r.ResolveSymbol, params.Provider, marketID, params.MarketType, subjectID)
			if symbolErr != nil {
				invalidSubjects = append(invalidSubjects, subjectID)
				continue
			}
			symbolIDs = append(symbolIDs, subjectID)
			externalSymbols[subjectID] = external
		}
		// 原有的 invalidSubjects 处理保持不变
		taskGroups := make([]TaskGroup, 0, len(params.Collector.Intervals))
		for _, frequency := range params.Collector.Intervals {
			taskGroups = append(taskGroups, TaskGroup{Provider: params.Provider, MarketType: params.MarketType, MarketID: marketID, InstrumentType: instrumentType, SourceID: sourceID, SeriesTag: params.SeriesTag, DatasetID: params.Target.DatasetID, Frequency: frequency, Subjects: symbolIDs, ExternalSymbols: externalSymbols})
		}
		r.lastGroups[task.TaskID] = taskGroups
		groups = append(groups, taskGroups...)
```

`Reconciler` 增加 `lastGroups map[string][]TaskGroup`（受现有互斥保护；若 `groups` 无锁调用，新增 `sync.Mutex`）与 `alertEmptyResolve`（递增 `moox_collector_subject_resolve_empty_total{task}` 指标）。`ResolveSubjects` 已只返回有效成员，删除原先对 `subject.Status` 的判断。`scheduler.go` 930–1015 行的等价逻辑同样改写；两处重复的「标的解析 + 代码换算」抽成一个函数 `resolveTaskSubjects(ctx, source, resolver, spaceID, params, task)`，放在 `marketfetch/subjects.go`，两处调用。

`storagesource.source.go`：

- `GetDataset` 解析 `Dataset.SubjectTags`；
- 新增 `ResolveSubjects`，调用 Metadata `ResolveSubjects`；
- 删除 `ListSubjects`、`ListResampleSubjectsForTask`、代码映射合并函数、`inferResampleSymbolSource`、`metadataFailoverClient` 中只为旧接口存在的分支。

- [ ] **Step 3：resample**

`resample/planner.go`、`preparer.go` 的 subject 来源接口改为 `ResolveSubjects(target dataset tags)`；`resample/catalog.go` 约 140 行循环调用 `BindDatasetSubject` 绑定目标 Dataset 成员的逻辑整段删除（成员由标签派生）。

- [ ] **Step 4：运行测试**

Run: `go test ./modules/collector/... -count=1`
Expected: PASS

### Task 4.4：CLI 与 kline 配置

**Files：**
- Modify: `modules/cli/internal/command/collector.go`、`data_kline_config.go` 及测试

- [ ] **Step 1：修改**

- `rg -n 'symbol_dataset_id|symbol_source|SymbolDatasetID' modules/cli` 定位全部引用，替换为 `subject_tags`（CLI flag `--symbol-dataset` → `--subject-tags`，逗号分隔）；
- 测试中的期望 JSON 同步更新。

- [ ] **Step 2：运行测试并提交（Task 4.1–4.4 合并）**

Run:
```bash
go test ./modules/collector/... ./modules/cli/... -count=1
rg -n 'symbol_dataset_id|symbol_source|SymbolDatasetID|SymbolSource' modules config
```
Expected: 测试 PASS；`rg` 无输出。

```bash
git add modules/collector modules/cli config/setup/collection-tasks.yaml
git commit -m "feat(collector): 采集任务按标签选择标的，标签写入任务 Dataset"
```

---

## 阶段 5：删除旧链路

### Task 5.1：instrument 采集类型

**Files：**
- Delete: `modules/collector/internal/jobs/symbol/`
- Delete: `modules/collector/internal/marketfetch/instrument_pipeline.go`、`instrument_pipeline_test.go`
- Modify: `modules/collector/internal/jobs/registry.go`、`route.go`
- Modify: `modules/collector/internal/marketwiring/handler.go`、`stock_runtime.go`
- Modify: `modules/collector/internal/marketfetch/handler.go`（`NewInstrumentPipeline` 字段与 instrument 分支）
- Modify: `modules/collector/internal/serverless/market_data/`（SCF 标的路由）
- Modify: `modules/collector/internal/domain/`（`InstrumentDataType`）
- Modify: `modules/collector/internal/marketdata/types.go`（`Instrument` 的交易规则字段、`CanonicalSymbol`）

- [ ] **Step 1：删除并修复编译**

```bash
git rm -r modules/collector/internal/jobs/symbol modules/collector/internal/marketfetch/instrument_pipeline.go modules/collector/internal/marketfetch/instrument_pipeline_test.go
go build ./modules/collector/... 2>&1 | head -50
```

按编译错误逐一删除：`NewMarketInstrumentPipeline`、`NewCryptoInstrumentPipeline`、`NewStockInstrumentPipeline`、`InstrumentStorage`、`instrumentSnapshotShardCount`、`StockCNInstrumentDatasetID`、`InstrumentDataType` 及其在 registry、route、handler、SCF handler 中的分支。`marketdata.Instrument` 删除 `CanonicalSymbol`、`MinQty`、`MaxQty`、`TickSize`、`LotSize`。

- [ ] **Step 2：验证无残留**

Run:
```bash
rg -n 'InstrumentPipeline|instrument_pipeline|InstrumentDataType|jobs/symbol|StockCNInstrumentDatasetID|instrumentSnapshotShard' modules
go test ./modules/collector/... -count=1
```
Expected: `rg` 无输出；测试 PASS。

- [ ] **Step 3：提交**

```bash
git add -A modules/collector
git commit -m "refactor(collector): 删除 instrument 采集类型与 InstrumentPipeline"
```

### Task 5.2：record Dataset 及其配置

**Files：**
- Modify: `config/setup/metadata.yaml`（删除三个 record Dataset、其 `dataset_columns`、`subject_symbols:`、`dataset_subjects:` 段）
- Modify: `modules/cli/config/fields.yaml`
- Modify: `modules/storage/config/storage.yaml`、`modules/storage/config/storage_view/trpc_go.yaml`
- Modify: `modules/storage/internal/config/loader.go`（含测试）
- Modify: `modules/storage/cmd/server/main_test.go`、`modules/cli/internal/command/*_test.go`、`modules/cli/internal/adminclient/cloudnode_test.go`、`modules/collector/internal/ruleseed/seed_test.go` 等测试夹具

- [ ] **Step 1：删除**

Run: `rg -n 'dataset_binance_spot_symbols|dataset_binance_swap_symbols|dataset_stockcn_instruments' . --glob '!docs/**' --glob '!web/node_modules/**'`
逐文件删除这些引用（测试夹具改为使用其它 Dataset 或直接删除相关用例）。`modules/monitor` 中的引用在 Task 6.2 处理，本任务跳过。

- [ ] **Step 2：验证**

Run:
```bash
go test ./modules/storage/... ./modules/cli/... ./modules/collector/... -count=1
rg -n 'dataset_binance_spot_symbols|dataset_binance_swap_symbols|dataset_stockcn_instruments' . --glob '!docs/**' --glob '!web/node_modules/**' --glob '!modules/monitor/**'
```
Expected: PASS；`rg` 无输出。

- [ ] **Step 3：提交**

```bash
git add -A config modules/cli modules/storage modules/collector
git commit -m "refactor: 删除标的 record Dataset 及其配置"
```

### Task 5.3：strategy 兜底分支

**Files：**
- Modify: `modules/strategy/internal/storageio/rpc.go`（约 375 行）及测试

- [ ] **Step 1：失败测试**

`ListDatasetSubjects` 返回空时，strategy 的标的列表为空，不再调用 `ListSubjects` 全量目录（fake client 断言 `ListSubjects` 调用次数为 0）。

- [ ] **Step 2：删除兜底分支，运行测试并提交**

Run: `go test ./modules/strategy/... -count=1`
Expected: PASS

```bash
git add modules/strategy
git commit -m "refactor(strategy): 删除数据集成员为空时回退全量目录的兜底"
```

### Task 5.4：`ListDatasetSubjects` 调用方核对

**Files：**
- Modify（如需）：`modules/merge/internal/merge/subjects.go`、`modules/archive/internal/backfill/backfill.go`、`modules/storage/internal/service/view/build.go`、`modules/monitor/internal/metrics/storage.go`

- [ ] **Step 1：核对**

Run: `rg -n 'ListDatasetSubjects|GetSubjectRole|GetEffectiveStartTime|GetEffectiveEndTime|DatasetSubject\{' modules --glob '!**/storagegen/**'`

对每个调用确认：

- 请求同时带 `space_id` 与 `dataset_id`（新实现要求二者非空）；
- 只使用 `subject_id` 与 `status`；`status` 的比较值为 `active` / `inactive`（旧值 `disabled` / `archived` 的判断改为 `inactive`）；
- 使用 `subject_role`、`effective_*` 的逻辑删除。

- [ ] **Step 2：运行测试并提交**

Run: `go test ./modules/merge/... ./modules/archive/... ./modules/storage/... ./modules/monitor/internal/metrics/... -count=1`
Expected: PASS

```bash
git add modules/merge modules/archive modules/storage modules/monitor/internal/metrics
git commit -m "refactor: Dataset 成员调用方适配标签派生的成员状态"
```

### Task 5.5：CLI、gateway、BFF、accessproxy 中的旧 RPC

**Files：**
- Modify: `modules/cli/internal/command/metadata_implementation.go`、`metadata_types.go`、`storage_import.go` 及测试
- Modify: `modules/storage/internal/accessproxy/proxy.go`
- Modify: `modules/gateway/internal/router/native.go`
- Modify: `modules/admin/internal/gateway/storage_bff.go`
- Modify: `modules/admin/internal/service/sysdeploy/defaults.go`
- Modify: `web/src/api/storage/http.ts`
- Modify: `modules/storage/internal/bootstrap/metadata/seed.go`

- [ ] **Step 1：删除**

删除 `RegisterDataSubject`、`UpsertSubjectSymbol`、`ListSubjectSymbols`、`BindDatasetSubject`、`StageDatasetSubjectSet`、`ActivateDatasetSubjectSet` 在上述文件中的登记、调用与种子结构（`seedSubjectSymbol`、`seedDatasetSubject`、`SubjectSymbols`、`DatasetSubjects` 字段及其导入循环）。

- [ ] **Step 2：验证**

Run:
```bash
rg -n 'RegisterDataSubject|UpsertSubjectSymbol|ListSubjectSymbols|BindDatasetSubject|StageDatasetSubjectSet|ActivateDatasetSubjectSet|SubjectSymbol|seedDatasetSubject' modules web/src --glob '!**/storagegen/**' --glob '!modules/storage/proto/**' --glob '!modules/storage/internal/service/catalog/**'
go test ./modules/cli/... ./modules/gateway/... ./modules/admin/... ./modules/storage/... -count=1
```
Expected: `rg` 仅剩 `web/src/api/storage/metadata.ts` 与 `web/src/views/data/subjects/index.vue`（Task 7 处理）；测试 PASS。

- [ ] **Step 3：提交**

```bash
git add modules web/src/api/storage/http.ts
git commit -m "refactor: 删除标的映射与数据集成员旧接口的调用方"
```

### Task 5.6：proto 中的旧 RPC 与消息

**Files：**
- Modify: `modules/storage/proto/metadata.proto`
- Modify: `modules/storage/internal/service/catalog/metadata_catalog.go`

- [ ] **Step 1：删除**

- proto：删除 `SubjectSymbol` 与 6 个旧 RPC 的请求 / 响应消息和 `rpc` 声明；`DatasetSubject` 精简为 Task 1.3 中给出的 4 个字段；
- catalog：删除 6 个旧 handler（Task 1.5 中改为返回 `removed` 的那些）；
- `metadata.DatasetSubjectSetWriter` 接口若仍存在，删除。

- [ ] **Step 2：生成与全量编译**

Run:
```bash
make -C modules/storage/proto
go build ./modules/...
go vet ./modules/storage/... ./modules/collector/...
```
Expected: 无错误。

- [ ] **Step 3：验证无残留并提交**

Run: `rg -n 'SubjectSymbol|RegisterDataSubject|BindDatasetSubject|DatasetSubjectSet|subject_role|effective_start_time' modules --glob '!web/**'`
Expected: 无输出

```bash
git add -A modules/storage
git commit -m "refactor(storage): 删除标的映射与数据集成员旧 RPC"
```

---

## 阶段 6：Monitor

### Task 6.1：Monitor 读取标签状态

**Files：**
- Modify: `modules/monitor/internal/bootstrap/market_canary.go`、`market_canary_test.go`

执行前确认前置条件中用户对这两个文件的未提交改动已提交。

- [ ] **Step 1：失败测试**

```go
func TestMarketCanaryTagChecks(t *testing.T) {
	now := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		tag  *storagepb.Tag
		want []string // 期望的告警原因
	}{
		{"healthy", &storagepb.Tag{TagId: "binance_spot", Cron: "0 * * * *", Timezone: "UTC", LastRunAt: "2026-09-25 09:00:00", LastStatus: "success", ActiveCount: 400}, nil},
		{"stale", &storagepb.Tag{TagId: "binance_spot", Cron: "0 * * * *", Timezone: "UTC", LastRunAt: "2026-09-25 05:00:00", LastStatus: "success", ActiveCount: 400}, []string{"stale"}},
		{"failed", &storagepb.Tag{TagId: "binance_spot", Cron: "0 * * * *", Timezone: "UTC", LastRunAt: "2026-09-25 09:00:00", LastStatus: "failed", LastError: "451", ActiveCount: 400}, []string{"failed"}},
		{"empty", &storagepb.Tag{TagId: "binance_spot", Cron: "0 * * * *", Timezone: "UTC", LastRunAt: "2026-09-25 09:00:00", LastStatus: "success"}, []string{"no_active_members"}},
	}
	for _, c := range cases {
		got := checkTag(c.tag, now)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
```

- [ ] **Step 2：实现**

```go
// staleFactor 为最近成功运行允许落后的 cron 周期倍数。
const staleFactor = 3

func checkTag(tag *storagepb.Tag, now time.Time) []string {
	var reasons []string
	if tag.GetLastStatus() == "failed" {
		reasons = append(reasons, "failed")
	}
	if tag.GetActiveCount() == 0 {
		reasons = append(reasons, "no_active_members")
	}
	if stale(tag, now) {
		reasons = append(reasons, "stale")
	}
	return reasons
}

func stale(tag *storagepb.Tag, now time.Time) bool {
	lastRun, err := time.ParseInLocation("2006-01-02 15:04:05", tag.GetLastRunAt(), time.UTC)
	if err != nil {
		return true
	}
	schedule, err := cron.ParseStandard(tag.GetCron())
	if err != nil {
		return true
	}
	loc, err := time.LoadLocation(tag.GetTimezone())
	if err != nil {
		return true
	}
	next := schedule.Next(lastRun.In(loc))
	period := schedule.Next(next).Sub(next)
	return now.Sub(lastRun) > time.Duration(staleFactor)*period
}
```

`checkTag` 的判断顺序决定告警原因的输出顺序；测试用例每个只触发一个原因，顺序不影响结果。`market_canary` 中原来读取三个 record Dataset 的检查整体替换为：`ListTags(space)` 后过滤 `builtin == true`，逐个 `checkTag`，按现有告警上报方式发出。删除对 `dataset_binance_*_symbols`、`dataset_stockcn_instruments` 的全部引用，以及 `business_freshness.go` / `retired_checks.go` 中与之相关的项（`rg` 定位）。

- [ ] **Step 3：运行测试并提交**

Run:
```bash
go test ./modules/monitor/... -count=1
rg -n 'dataset_binance_spot_symbols|dataset_binance_swap_symbols|dataset_stockcn_instruments' modules
```
Expected: PASS；`rg` 无输出。

```bash
git add modules/monitor
git commit -m "feat(monitor): market canary 改为检查内置标签"
```

---

## 阶段 7：前端

### Task 7.1：API 层

**Files：**
- Modify: `web/src/api/storage/types.ts`、`web/src/api/storage/metadata.ts`
- Create: `web/src/api/storage/tags.test.ts`

- [ ] **Step 1：类型**

```ts
export type TagMode = "auto" | "manual";
export type TagMemberStatus = "active" | "inactive";

export interface Tag {
  space_id: string;
  tag_id: string;
  tag_name: string;
  description?: string;
  mode: TagMode;
  builtin?: boolean;
  sources?: string[];
  instrument_type?: string;
  cron?: string;
  timezone?: string;
  last_run_at?: string;
  last_status?: "" | "success" | "failed";
  last_error?: string;
  active_count?: number;
  inactive_count?: number;
}

export interface TagMember {
  space_id: string;
  tag_id?: string;
  status?: TagMemberStatus;
  inactive_at?: string;
  subject: StorageSubject;
  tag_ids?: string[];
}

export interface TagReference {
  dataset_id: string;
  dataset_name?: string;
  collector_task_id?: string;
}
```

删除 `SubjectSymbol` 相关类型；`Dataset` 类型加 `subject_tags?: string[]`。

- [ ] **Step 2：API 函数**

```ts
export const listTags = (spaceId: string, page = { page: 1, size: 200 }) =>
  callMetadata<{ tags: Tag[] }>("ListTags", { space_id: spaceId, page });
export const upsertTag = (tag: Tag) => callMetadata<{ tag: Tag }>("UpsertTag", { tag });
export const deleteTag = (spaceId: string, tagId: string) =>
  callMetadata<{ references?: TagReference[] }>("DeleteTag", { space_id: spaceId, tag_id: tagId });
export const listTagMembers = (params: { space_id: string; tag_id?: string; status?: string; keyword?: string; page: { page: number; size: number } }) =>
  callMetadata<{ members: TagMember[]; page_result: PageResult }>("ListTagMembers", params);
export const addTagMembers = (spaceId: string, tagId: string, subjectIds: string[]) =>
  callMetadata("AddTagMembers", { space_id: spaceId, tag_id: tagId, subject_ids: subjectIds });
export const removeTagMembers = (spaceId: string, tagId: string, subjectIds: string[]) =>
  callMetadata("RemoveTagMembers", { space_id: spaceId, tag_id: tagId, subject_ids: subjectIds });
export const setTagMemberStatus = (spaceId: string, tagId: string, subjectIds: string[], status: TagMemberStatus) =>
  callMetadata("SetTagMemberStatus", { space_id: spaceId, tag_id: tagId, subject_ids: subjectIds, status });
```

`callMetadata` 的泛型与错误处理方式以 `metadata.ts` 现有函数为准。删除 `listSubjectSymbols`、`upsertSubjectSymbol`、`bindDatasetSubject` 等旧函数及其方法名登记。

- [ ] **Step 3：测试**

`tags.test.ts` mock `callMetadata`，断言各函数的方法名与参数形状（参照同目录现有 `*.test.ts` 的 mock 方式）。

Run: `cd web && pnpm vitest run src/api/storage --config vitest.config.ts`
Expected: PASS

### Task 7.2：数据对象页两个子 tab

**Files：**
- Modify: `web/src/views/data/subjects/index.vue`
- Create: `web/src/views/data/subjects/tags-tab.vue`、`members-tab.vue`、`tag-form.ts`、`tag-form.test.ts`

- [ ] **Step 1：表单逻辑（先写测试）**

`tag-form.ts` 放纯函数，便于单测：

```ts
import { CronExpressionParser } from "cron-parser";

export interface TagFormState {
  tag_id: string;
  tag_name: string;
  description: string;
  mode: "auto" | "manual";
  probe: boolean;
  sources: string[];
  instrument_type: string;
  cron: string;
  timezone: string;
}

export const TAG_ID_PATTERN = /^[a-z][a-z0-9_]{0,63}$/;

export function validateTagForm(state: TagFormState, creating: boolean): string | undefined {
  if (creating && !TAG_ID_PATTERN.test(state.tag_id)) return "标签 ID 须为小写字母开头的 snake_case";
  if (!state.tag_name.trim()) return "请输入标签名称";
  const needsSource = state.mode === "auto" || state.probe;
  if (needsSource && (state.sources.length === 0 || !state.instrument_type)) return "请选择数据源与产品类型";
  if (needsSource && nextRuns(state.cron, state.timezone, 1).length === 0) return "cron 表达式无效";
  return undefined;
}

export function nextRuns(cron: string, timezone: string, count = 3): string[] {
  try {
    const it = CronExpressionParser.parse(cron, { tz: timezone });
    return Array.from({ length: count }, () => it.next().toISOString() ?? "");
  } catch {
    return [];
  }
}

export function toTagPayload(spaceId: string, state: TagFormState) {
  const probe = state.mode === "auto" || state.probe;
  return {
    space_id: spaceId,
    tag_id: state.tag_id,
    tag_name: state.tag_name.trim(),
    description: state.description.trim(),
    mode: state.mode,
    sources: probe ? state.sources : [],
    instrument_type: probe ? state.instrument_type : "",
    cron: state.cron || "0 * * * *",
    timezone: state.timezone || "UTC"
  };
}
```

依赖：`cd web && pnpm add cron-parser`（使用 v5 的 `CronExpressionParser` API；若安装的主版本不同，按其文档调整 `parse` 调用）。`tag-form.test.ts` 覆盖：新建时非法 tag_id 报错、编辑时忽略 tag_id、手工未开启探测时清空 sources、`nextRuns("0 * * * *", "UTC", 3)` 返回 3 个整点。

Run: `cd web && pnpm vitest run src/views/data/subjects --config vitest.config.ts`
Expected: 先 FAIL（文件不存在），实现后 PASS。

- [ ] **Step 2：`index.vue`**

改为容器：`a-tabs` 两个 `a-tab-pane`（「标签」、「标签成员」），通过 `activeTab` 与 `selectedTagId` 在两个子组件之间传递「成员」跳转。删除原「外部符号」抽屉及其全部状态与 API 调用。

- [ ] **Step 3：`tags-tab.vue`**

- `a-table` 列：标签名称 + 标签 ID（`builtin` 时显示「内置」`a-tag`）、描述、模式（自动 / 手工）、数据源与产品类型（未探测显示「—」）、cron 与时区、有效 / 失效成员数、最近运行（`last_status === 'failed'` 时用 `a-tooltip` 显示 `last_error`）；
- 操作列：「成员」（emit `open-members`）、「编辑」、「删除」（`builtin` 时禁用；调用 `deleteTag`，响应带 `references` 时用 `a-modal` 列出引用的数据集与采集任务 ID）；
- 新建 / 编辑 `a-modal`：字段按 `TagFormState`；`tag_id` 仅新建可编辑；手工模式显示「自动探测有效性」开关；需要数据源时显示数据源多选、产品类型下拉、cron 输入、时区选择，并在下方显示 `nextRuns` 的 3 个时间；
- 数据源与产品类型选项：`listDataSources(space)` 过滤 `attributes.subject_listing` 非空者，产品类型取其中 `instrument_types`。

- [ ] **Step 4：`members-tab.vue`**

- 顶部：标签选择（第一项「全部标的」对应 `tag_id = ""`）、状态筛选（有效 / 失效 / 全部，选中「全部标的」时隐藏）、关键字搜索；
- 表格列：对象 ID、名称、类型、市场、所属标签（`tag_ids`）、当前标签中的状态、失效时间；展开行显示 `subject.attributes` 键值；
- 选中手工标签：批量加入（`a-textarea` 粘贴对象 ID，按换行 / 逗号拆分）、勾选后「移出标签」「恢复为有效」（开启探测时在按钮旁提示「以下一次探测结果为准」）；
- 选中自动标签：隐藏编辑按钮，顶部 `a-alert`「成员由数据源自动同步」；
- 选中「全部标的」：保留原页面的新增 / 编辑数据对象入口（`upsertSubject`）。

- [ ] **Step 5：测试与构建**

Run:
```bash
cd web && pnpm vitest run --config vitest.config.ts && pnpm vue-tsc --noEmit
```
Expected: PASS；无类型错误。

### Task 7.3：采集任务表单

**Files：**
- Modify: `web/src/views/collector/collection-tasks/collection-task-params.ts`、`collection-task-params.test.ts`
- Modify: `web/src/views/collector/collection-tasks/collection-tasks.vue`
- Delete: `web/src/views/data/datasets/components/dataset-subject-panel.vue`
- Modify: `web/src/views/data/datasets/index.vue`、`web/src/views/data/browse/index.vue`

- [ ] **Step 1：参数构造（先改测试）**

`collection-task-params.test.ts`：kline 输入 `subjectTags: ["binance_spot"]` 时生成 `{provider, market_type, subject_tags: ["binance_spot"], frequency}`；`subjectTags` 为空时抛出「请选择标的标签」；instrument 数据类型用例删除。

`collection-task-params.ts`：

- `CollectionTaskInput` 删除 `symbolSource`、`symbolSourceId`，新增 `subjectTags?: string[]`；
- `buildCollectionTaskParams`：删除 instrument 分支；kline 分支：

```ts
  const subjectTags = (input.subjectTags ?? []).map((tag) => tag.trim()).filter(Boolean);
  if (subjectTags.length === 0) throw new Error("请选择标的标签");
  return { provider, market_type: market, subject_tags: subjectTags, frequency };
```

- resample 分支：`subjectTags` 非空时附加 `subject_tags`，为空时不传（后端复制源 Dataset 的标签）；
- `parseCollectionTaskInput`：不再读取 `symbol_dataset_id`；`subjectTags` 由调用方从任务结果 Dataset 读取后填入。

- [ ] **Step 2：表单**

`collection-tasks.vue`：

- 「标的来源 Dataset」选择器替换为 `a-select multiple`，选项来自 `listTags(spaceId)`，显示 `tag_name（tag_id）`；
- 打开编辑时，从任务 `result` 取 Dataset ID，调用 `getDataset` 取 `subject_tags` 回填；
- 数据类型选项删除 `instrument`。

- [ ] **Step 3：删除 Dataset 成员面板**

```bash
git rm web/src/views/data/datasets/components/dataset-subject-panel.vue
```

删除 `datasets/index.vue` 中的 import 与 `<DatasetSubjectPanel>`（225、259 行附近）；`browse/index.vue` 中若读取 `subject_role` 等旧字段则删除。

- [ ] **Step 4：测试、构建并提交（Task 7.1–7.3）**

Run:
```bash
cd web && pnpm vitest run --config vitest.config.ts && pnpm build:prod
cd .. && rg -n 'SubjectSymbol|symbol_dataset_id|symbol_source|dataset-subject-panel|DatasetSubjectPanel|listSubjectSymbols' web/src
```
Expected: 测试 PASS；构建成功；`rg` 无输出。

```bash
git add -A web
git commit -m "feat(web): 数据对象页改为标签与标签成员两个子 tab，采集任务按标签选择标的"
```

---

## 阶段 8：全量验证与上线

### Task 8.1：全量验证

- [ ] **Step 1：proto 与 Go**

Run:
```bash
make proto
make test-go
```
Expected: 全部 PASS。

- [ ] **Step 2：schema**

Run:
```bash
for f in modules/*/schema/*.sql; do rm -f /tmp/schema_check.db; sqlite3 /tmp/schema_check.db < "$f" || echo "FAIL $f"; done
git diff --check
```
Expected: 无 `FAIL`；无输出。

- [ ] **Step 3：前端**

Run: `make test-web`
Expected: PASS

- [ ] **Step 4：全仓残留检查**

Run:
```bash
rg -n 't_subject_symbols|t_dataset_subjects|dataset_subject_set_staging|SubjectSymbol|RegisterDataSubject|BindDatasetSubject|DatasetSubjectSet|symbol_dataset_id|symbol_source|InstrumentPipeline|ProductType|Canonical(Crypto)?SubjectID|dataset_binance_spot_symbols|dataset_binance_swap_symbols|dataset_stockcn_instruments' --glob '!docs/**' --glob '!web/node_modules/**' .
```
Expected: 无输出。

### Task 8.2：本地端到端

- [ ] **Step 1：启动**

按 `scripts/deploy/deploy-moox.sh` 的本地模式启动 Storage、gateway、collector，并执行 `moox-cli setup init` 导入种子；然后在可直连币安的机器上启动：

```bash
./bin/moox-collector-subject -conf=modules/collector/config/subject.yaml
```

- [ ] **Step 2：确认**

1. 1 分钟内三个内置标签的 `last_status = success`，`active_count > 0`：
   `sqlite3 <metadata.db> "SELECT c_tag_id, c_last_status, (SELECT COUNT(1) FROM t_subject_tags m WHERE m.c_tag_id = t.c_tag_id AND m.c_status = 'active') FROM t_tags t"`
2. 在页面编辑 `binance_spot` 的描述，下一轮轮询（≤ 1 分钟）内 `c_last_run_at` 更新；
3. 属性任务到点后，`GetSubject(crypto, BTC-USDT).attributes` 含 `base` / `quote`（可临时把 cron 改为 `*/2 * * * *` 验证）；
4. 内置 kline 任务 `builtin-binance-spot-kline-1m` 的 Dataset `subject_tags = [binance_spot]`，规划出的原子任务标的数与 `binance_spot` 的有效成员数一致，K 线写入成功；
5. 页面：数据对象页两个子 tab 正常；自动标签成员只读；内置标签删除按钮禁用；删除被任务引用的标签时列出引用方；采集任务表单可选择标签并回显。

### Task 8.3：上线步骤

- [ ] 上线前在生产 collector 库中列出使用 `symbol_dataset_id` 的自建任务：
  `sqlite3 <collector.db> "SELECT c_task_id, c_task_name FROM t_collection_tasks WHERE c_collect_params LIKE '%symbol_dataset_id%'"`（表名 / 列名以 collector schema 为准）。这些任务升级后参数解析失败会被跳过，需在页面重新选择标签保存。
- [ ] 先部署 Storage（v11 → v12 迁移自动执行），执行 `moox-cli setup init` 写入内置标签；再部署 `moox-collector-subject`，等待首轮同步成功；最后部署 collector、monitor、web。
- [ ] 旧的三个 record Dataset 在元数据中已由迁移删除；若 DataNode 上仍有其数据文件，用现有的数据集清理工具（`moox-cli collector task purge` 的 Dataset 删除逻辑）清理。

### Task 8.4：收尾提交

- [ ] 确认 `git status` 干净（各任务均已提交），执行 `git push`。
