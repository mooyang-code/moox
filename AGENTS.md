这是一个全新项目，请遵循以下原则：

**项目原则**
- **无需兼容历史**：不考虑任何旧版本的数据结构或代码逻辑
- **完全重构权限**：可自由删除、修改或重写任何已有内容
- **合理性优先**：所有方案和实现以设计合理、逻辑清晰为最高准则

**代码清理原则**
- **主动清理过时代码**：在涉及相关文件时，顺手清理死代码、冗余逻辑、无用注释等
- **协议无需向后兼容**：Protobuf / Thrift 等协议定义中，所有 `reserved` 字段声明（包括 `reserved` 编号与 `reserved` 字段名）均可直接删除，例如：
  ```protobuf
  // 以下内容在新项目中应全部删除
  message TaskInstance {
    reserved 12;
    reserved "planned_exec_node";
    ...
  }
  ```
- **清理范围包括但不限于**：
  - 协议文件中的 `reserved` 声明
  - 被注释掉的废弃逻辑块
  - 仅为兼容旧版本而存在的条件分支、适配层、版本判断逻辑
  - 已无调用方的孤立函数、方法、类、常量

**数据库 Schema 格式规范**
- **适用范围**：所有 `modules/*/schema/*.sql` 文件；SQLite 方言和具体排版以 `modules/storage/schema/metadata.sql` 为基准
- **关键字与缩进**：SQL 关键字大写，使用 4 个空格缩进，不使用 Tab，不保留行尾空白，文件以单个换行结尾
- **表定义**：`CREATE TABLE` 的左括号留在声明行；每个字段和表级约束独占一行；字段在前，`CHECK`、`FOREIGN KEY`、`UNIQUE`、`PRIMARY KEY` 等表级约束在后；除最后一项外均以逗号结尾
- **括号与逗号**：对象名或约束关键字与左括号之间保留一个空格，例如 `t_table (c_id)`、`PRIMARY KEY (c_id)`、`REFERENCES t_table (c_id)`；逗号后保留一个空格
- **索引**：短索引保持单行；长索引在索引名后换行，`ON` 和可选的 `WHERE` 顶格书写；同一张表的连续索引可相邻书写
- **触发器**：触发器名、`BEFORE`/`AFTER`、`FOR EACH ROW`、`WHEN`、`BEGIN`、`END` 分行书写；触发器体使用 4 个空格缩进，只保留原逻辑实际需要的子句
- **语句间距**：表定义与其首个索引之间空一行；索引组与下一张表或触发器之间空一行；不要使用多个连续空行
- **注释**：使用 `--` 注释说明业务含义或表分区，避免无意义注释和依赖尾随空格的视觉对齐
- **变更边界**：纯格式调整不得修改 SQL token、约束、默认值、索引条件、触发器行为或其他 DDL 语义
- **提交前校验**：所有 schema 文件必须能逐个载入空 SQLite 数据库，并执行 `git diff --check`；纯格式调整还需确认格式化前后 SQL token 等价

```sql
CREATE TABLE IF NOT EXISTS t_examples (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_name TEXT NOT NULL,
    c_status TEXT NOT NULL DEFAULT 'active',
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_status IN ('active', 'disabled')),
    UNIQUE (c_name)
);

CREATE INDEX IF NOT EXISTS idx_t_examples_status ON t_examples (c_status);

CREATE TRIGGER IF NOT EXISTS trg_t_examples_mtime
AFTER UPDATE ON t_examples
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_examples SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;
```

**会话结束规范**
- 每次会话结束时，将本次会话中**所有被修改、新增或删除**的文件提交至 Git
- commit message 须简要描述本次会话的改动内容
- 提交后执行 `git push`，同步至远程仓库
