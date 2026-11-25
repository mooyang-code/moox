-- moox storage metadata schema
--
-- 设计目标：
-- 1. Space 是业务命名空间；DataSource、Subject、Dataset 都归属 Space。
-- 2. Dataset 描述可写事实数据集，并且只绑定一个 DataSource。
-- 3. Subject 是 Space 内业务对象，不归属 DataSource；来源侧代码由抓取适配器换算。
-- 4. 每个 Dataset 恰好有一个查询索引（View），不单独登记：列等于数据集启用的列，已建索引的状态只在索引文件里。
-- 5. Dataset 的行全部存放在主服务的 Pebble 库里，元数据不记录存储位置。
-- 6. DuckDB、Bleve 索引跟随 Pebble 主存的变更日志异步派生。
-- 7. 每张表只保留主键、唯一约束、外键与查询条件用到的列，整条消息放在 c_item_json。

PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS t_schema_meta (
    c_key TEXT NOT NULL PRIMARY KEY,
    c_value TEXT NOT NULL
);

INSERT INTO t_schema_meta (c_key, c_value)
VALUES ('schema_version', '21')
ON CONFLICT(c_key) DO NOTHING;

-- ************ Space ************
CREATE TABLE IF NOT EXISTS t_spaces (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_name TEXT NOT NULL,
    c_owner TEXT NOT NULL DEFAULT '',
    c_status TEXT NOT NULL DEFAULT 'active',
    c_item_json TEXT NOT NULL DEFAULT '{}',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_status IN ('active', 'disabled')),
    UNIQUE (c_space_id),
    UNIQUE (c_name)
);

CREATE INDEX IF NOT EXISTS idx_t_spaces_status ON t_spaces (c_status);
CREATE INDEX IF NOT EXISTS idx_t_spaces_owner ON t_spaces (c_owner);

CREATE TRIGGER IF NOT EXISTS trg_t_spaces_mtime
AFTER UPDATE ON t_spaces
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_spaces SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

-- ************ 数据来源与数据对象 ************
CREATE TABLE IF NOT EXISTS t_data_sources (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_data_source_id TEXT NOT NULL,
    c_name TEXT NOT NULL,
    c_status TEXT NOT NULL DEFAULT 'active',
    c_item_json TEXT NOT NULL DEFAULT '{}',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_status IN ('active', 'disabled')),
    FOREIGN KEY (c_space_id) REFERENCES t_spaces (c_space_id) ON DELETE CASCADE ON UPDATE CASCADE,
    UNIQUE (c_space_id, c_data_source_id),
    UNIQUE (c_space_id, c_name)
);

CREATE INDEX IF NOT EXISTS idx_t_data_sources_status ON t_data_sources (c_space_id, c_status);

CREATE TRIGGER IF NOT EXISTS trg_t_data_sources_mtime
AFTER UPDATE ON t_data_sources
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_data_sources SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

CREATE TABLE IF NOT EXISTS t_subjects (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_subject_id TEXT NOT NULL,
    c_name TEXT NOT NULL DEFAULT '',
    c_status TEXT NOT NULL DEFAULT 'active',
    c_item_json TEXT NOT NULL DEFAULT '{}',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_status IN ('active', 'disabled')),
    FOREIGN KEY (c_space_id) REFERENCES t_spaces (c_space_id) ON DELETE CASCADE ON UPDATE CASCADE,
    UNIQUE (c_space_id, c_subject_id)
);

CREATE INDEX IF NOT EXISTS idx_t_subjects_status ON t_subjects (c_space_id, c_status);

CREATE TRIGGER IF NOT EXISTS trg_t_subjects_mtime
AFTER UPDATE ON t_subjects
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_subjects SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

-- 标签：一组 Subject；业务含义（市场、同步配置等）放在消息的 attributes，Storage 不解释
CREATE TABLE IF NOT EXISTS t_tags (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_tag_id TEXT NOT NULL,
    c_tag_name TEXT NOT NULL,
    c_mode TEXT NOT NULL,
    c_builtin INTEGER NOT NULL DEFAULT 0,
    c_item_json TEXT NOT NULL DEFAULT '{}',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_mode IN ('auto', 'manual')),
    CHECK (c_builtin IN (0, 1)),
    FOREIGN KEY (c_space_id) REFERENCES t_spaces (c_space_id) ON DELETE CASCADE ON UPDATE CASCADE,
    UNIQUE (c_space_id, c_tag_id),
    UNIQUE (c_space_id, c_tag_name),
    UNIQUE (c_tag_id)
);

CREATE TRIGGER IF NOT EXISTS trg_t_tags_mtime
AFTER UPDATE ON t_tags
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
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
    -- 这个成员在这个标签（即这个市场）上的交易状态，由 Collector 的交易状态检测写入：trading 正常、suspended 停牌、delisted 退市
    c_trading_status TEXT NOT NULL DEFAULT 'trading',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_status IN ('active', 'inactive')),
    CHECK (c_trading_status IN ('trading', 'suspended', 'delisted')),
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

-- ************ Dataset 与 DatasetColumn ************
CREATE TABLE IF NOT EXISTS t_datasets (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_dataset_id TEXT NOT NULL,
    c_data_source_id TEXT,
    c_name TEXT NOT NULL,
    c_data_kind TEXT NOT NULL,
    c_freq TEXT NOT NULL DEFAULT '',
    c_revision INTEGER NOT NULL DEFAULT 1 CHECK (c_revision > 0),
    c_status TEXT NOT NULL DEFAULT 'active' CHECK (c_status IN ('active', 'disabled')),
    c_item_json TEXT NOT NULL DEFAULT '{}',
    c_subject_tags_json TEXT NOT NULL DEFAULT '[]',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_data_kind IN ('record', 'time_series')),
    FOREIGN KEY (c_space_id) REFERENCES t_spaces (c_space_id) ON DELETE CASCADE ON UPDATE CASCADE,
    FOREIGN KEY (c_space_id, c_data_source_id) REFERENCES t_data_sources (c_space_id, c_data_source_id) ON DELETE RESTRICT ON UPDATE CASCADE,
    UNIQUE (c_space_id, c_dataset_id),
    UNIQUE (c_space_id, c_name)
);

CREATE INDEX IF NOT EXISTS idx_t_datasets_source ON t_datasets (c_space_id, c_data_source_id, c_status);
CREATE INDEX IF NOT EXISTS idx_t_datasets_kind ON t_datasets (c_space_id, c_data_kind, c_status);

CREATE TRIGGER IF NOT EXISTS trg_t_datasets_mtime
AFTER UPDATE ON t_datasets
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_datasets SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

-- ************ 标的修订号 ************
-- 按空间单调递增：标的、标签成员、标签属性、数据集标的范围任一变化都会递增，
-- 调用方（Strategy）用它判断缓存的标的绑定、标的详情与标签成员是否过期。
-- 触发器的 UPDATE OF 只列业务列，排除 c_mtime，避免 mtime 触发器造成多余递增。
CREATE TABLE IF NOT EXISTS t_space_revisions (
    c_space_id TEXT NOT NULL PRIMARY KEY,
    c_subjects_revision INTEGER NOT NULL DEFAULT 0
);

CREATE TRIGGER IF NOT EXISTS trg_t_subjects_rev_insert
AFTER INSERT ON t_subjects
FOR EACH ROW
BEGIN
    INSERT INTO t_space_revisions (c_space_id, c_subjects_revision)
    VALUES (NEW.c_space_id, 1)
    ON CONFLICT (c_space_id) DO UPDATE SET c_subjects_revision = c_subjects_revision + 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_t_subjects_rev_update
AFTER UPDATE OF c_status, c_item_json ON t_subjects
FOR EACH ROW
BEGIN
    INSERT INTO t_space_revisions (c_space_id, c_subjects_revision)
    VALUES (NEW.c_space_id, 1)
    ON CONFLICT (c_space_id) DO UPDATE SET c_subjects_revision = c_subjects_revision + 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_t_subjects_rev_delete
AFTER DELETE ON t_subjects
FOR EACH ROW
BEGIN
    INSERT INTO t_space_revisions (c_space_id, c_subjects_revision)
    VALUES (OLD.c_space_id, 1)
    ON CONFLICT (c_space_id) DO UPDATE SET c_subjects_revision = c_subjects_revision + 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_t_subject_tags_rev_insert
AFTER INSERT ON t_subject_tags
FOR EACH ROW
BEGIN
    INSERT INTO t_space_revisions (c_space_id, c_subjects_revision)
    VALUES (NEW.c_space_id, 1)
    ON CONFLICT (c_space_id) DO UPDATE SET c_subjects_revision = c_subjects_revision + 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_t_subject_tags_rev_update
AFTER UPDATE OF c_status, c_trading_status ON t_subject_tags
FOR EACH ROW
BEGIN
    INSERT INTO t_space_revisions (c_space_id, c_subjects_revision)
    VALUES (NEW.c_space_id, 1)
    ON CONFLICT (c_space_id) DO UPDATE SET c_subjects_revision = c_subjects_revision + 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_t_subject_tags_rev_delete
AFTER DELETE ON t_subject_tags
FOR EACH ROW
BEGIN
    INSERT INTO t_space_revisions (c_space_id, c_subjects_revision)
    VALUES (OLD.c_space_id, 1)
    ON CONFLICT (c_space_id) DO UPDATE SET c_subjects_revision = c_subjects_revision + 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_t_tags_rev_update
AFTER UPDATE OF c_item_json ON t_tags
FOR EACH ROW
BEGIN
    INSERT INTO t_space_revisions (c_space_id, c_subjects_revision)
    VALUES (NEW.c_space_id, 1)
    ON CONFLICT (c_space_id) DO UPDATE SET c_subjects_revision = c_subjects_revision + 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_t_tags_rev_delete
AFTER DELETE ON t_tags
FOR EACH ROW
BEGIN
    INSERT INTO t_space_revisions (c_space_id, c_subjects_revision)
    VALUES (OLD.c_space_id, 1)
    ON CONFLICT (c_space_id) DO UPDATE SET c_subjects_revision = c_subjects_revision + 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_t_datasets_rev_insert
AFTER INSERT ON t_datasets
FOR EACH ROW
BEGIN
    INSERT INTO t_space_revisions (c_space_id, c_subjects_revision)
    VALUES (NEW.c_space_id, 1)
    ON CONFLICT (c_space_id) DO UPDATE SET c_subjects_revision = c_subjects_revision + 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_t_datasets_rev_update
AFTER UPDATE OF c_subject_tags_json ON t_datasets
FOR EACH ROW
BEGIN
    INSERT INTO t_space_revisions (c_space_id, c_subjects_revision)
    VALUES (NEW.c_space_id, 1)
    ON CONFLICT (c_space_id) DO UPDATE SET c_subjects_revision = c_subjects_revision + 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_t_datasets_rev_delete
AFTER DELETE ON t_datasets
FOR EACH ROW
BEGIN
    INSERT INTO t_space_revisions (c_space_id, c_subjects_revision)
    VALUES (OLD.c_space_id, 1)
    ON CONFLICT (c_space_id) DO UPDATE SET c_subjects_revision = c_subjects_revision + 1;
END;

-- ************ 数据集列 ************
-- 列自描述：类型、显示名、分组都在 c_item_json 里，不是查询条件。
CREATE TABLE IF NOT EXISTS t_dataset_columns (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_dataset_id TEXT NOT NULL,
    c_column_name TEXT NOT NULL,
    c_status TEXT NOT NULL DEFAULT 'active',
    c_item_json TEXT NOT NULL DEFAULT '{}',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_status IN ('active', 'disabled')),
    FOREIGN KEY (c_space_id, c_dataset_id) REFERENCES t_datasets (c_space_id, c_dataset_id) ON DELETE CASCADE ON UPDATE CASCADE,
    UNIQUE (c_space_id, c_dataset_id, c_column_name)
);

CREATE INDEX IF NOT EXISTS idx_t_dataset_columns_dataset ON t_dataset_columns (c_space_id, c_dataset_id, c_status);

CREATE TRIGGER IF NOT EXISTS trg_t_dataset_columns_mtime
AFTER UPDATE ON t_dataset_columns
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_dataset_columns SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;
