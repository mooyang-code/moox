PRAGMA foreign_keys = ON;

-- 因子计算任务：一个源数据集和频率，产出一个结果数据集。c_name 直接用作 Storage 结果数据集的名称
CREATE TABLE IF NOT EXISTS t_factor_tasks (
    c_task_id TEXT NOT NULL PRIMARY KEY,
    c_space_id TEXT NOT NULL,
    c_name TEXT NOT NULL,
    c_source_dataset_id TEXT NOT NULL,
    c_freq TEXT NOT NULL,
    c_result_dataset_id TEXT NOT NULL,
    -- 关联的策略实例：独占、永久，写入后不能修改或清空；空串表示还没有关联
    c_bound_instance_id TEXT NOT NULL DEFAULT '',
    c_status TEXT NOT NULL DEFAULT 'pending',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_status IN ('pending', 'enabled', 'disabled')),
    UNIQUE (c_result_dataset_id),
    UNIQUE (c_space_id, c_name)
);

CREATE INDEX IF NOT EXISTS idx_t_factor_tasks_source ON t_factor_tasks (c_space_id, c_source_dataset_id, c_freq, c_status);
-- 关联实例唯一：空串（未关联）不参与
CREATE UNIQUE INDEX IF NOT EXISTS ux_t_factor_tasks_bound_instance
ON t_factor_tasks (c_bound_instance_id)
WHERE c_bound_instance_id <> '';

-- 因子分类（例如动量类、波动类）：只是元数据，用于因子库查找与按类展示
CREATE TABLE IF NOT EXISTS t_factor_categories (
    c_category_id TEXT NOT NULL PRIMARY KEY,
    c_name TEXT NOT NULL,
    c_sort_order INTEGER NOT NULL DEFAULT 0,
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT OR IGNORE INTO t_factor_categories (c_category_id, c_name, c_sort_order) VALUES ('uncategorized', '未分类', 1000);

-- 因子定义：只描述算法，与因子任务无关，无运行状态。c_spec 是表达式定义文本（YAML），
-- 因子类型、输入列、输出列与回看由编译 c_spec 派生，不落库
CREATE TABLE IF NOT EXISTS t_factor_defs (
    c_factor_id TEXT NOT NULL PRIMARY KEY,
    c_name TEXT NOT NULL,
    c_category_id TEXT NOT NULL DEFAULT 'uncategorized',
    c_spec TEXT NOT NULL,
    c_spec_hash TEXT NOT NULL,
    c_allow_partial_universe INTEGER NOT NULL DEFAULT 0,
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_allow_partial_universe IN (0, 1)),
    FOREIGN KEY (c_category_id) REFERENCES t_factor_categories (c_category_id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_t_factor_defs_category ON t_factor_defs (c_category_id);

-- 因子任务成员：定义在某个因子任务中的运行实例，身份是任务内唯一的成员键（因子 ID 加参数段），启停状态属于成员。
-- 固定成员的参数在加成员时写定、之后不变；可变成员的默认参数写在 c_params，策略逐期提交的值记在 c_last_submitted_*。
CREATE TABLE IF NOT EXISTS t_factor_task_members (
    c_task_id TEXT NOT NULL,
    c_member_key TEXT NOT NULL,
    c_factor_id TEXT NOT NULL,
    c_kind TEXT NOT NULL,
    c_status TEXT NOT NULL DEFAULT 'disabled',
    -- 固定参数，或可变成员的默认参数：加成员时的完整参数（规范 JSON），之后不变
    c_params TEXT NOT NULL DEFAULT '{}',
    -- 以下四列是可变成员的运行状态，不属于配置：目录同步、目录指纹、成员变化检测都不看它们，提交时也不更新 c_mtime。
    -- 各标的最近一次提交的参数（默认值加按标的覆盖的规范 JSON，按标的合并），最近一次被接受的目标期（Unix 秒）、请求 ID 与提交时间
    c_last_submitted_params TEXT NOT NULL DEFAULT '{}',
    c_last_submitted_period INTEGER NOT NULL DEFAULT 0,
    c_last_request_id TEXT NOT NULL DEFAULT '',
    c_last_submitted_at DATETIME,
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (c_task_id, c_member_key),
    CHECK (c_kind IN ('fixed', 'var')),
    CHECK (c_kind = 'var' OR (c_last_submitted_period = 0 AND c_last_submitted_params = '{}' AND c_last_request_id = '' AND c_last_submitted_at IS NULL)),
    CHECK (c_status IN ('enabled', 'disabled')),
    FOREIGN KEY (c_task_id) REFERENCES t_factor_tasks (c_task_id),
    FOREIGN KEY (c_factor_id) REFERENCES t_factor_defs (c_factor_id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_t_factor_task_members_task ON t_factor_task_members (c_task_id, c_status);
CREATE INDEX IF NOT EXISTS idx_t_factor_task_members_factor ON t_factor_task_members (c_factor_id);
-- 同一个任务里同一个因子最多一个可变成员
CREATE UNIQUE INDEX IF NOT EXISTS ux_t_factor_task_members_var
ON t_factor_task_members (c_task_id, c_factor_id)
WHERE c_kind = 'var';

CREATE TABLE IF NOT EXISTS t_factor_recalc_jobs (
    c_job_id TEXT NOT NULL PRIMARY KEY,
    c_request_id TEXT NOT NULL,
    c_task_id TEXT NOT NULL,
    c_members_json TEXT NOT NULL DEFAULT '[]',
    c_subjects_json TEXT NOT NULL DEFAULT '[]',
    c_members_omitted INTEGER NOT NULL DEFAULT 0 CHECK (c_members_omitted IN (0, 1)),
    c_subjects_omitted INTEGER NOT NULL DEFAULT 0 CHECK (c_subjects_omitted IN (0, 1)),
    c_start_time INTEGER NOT NULL,
    c_end_time INTEGER NOT NULL,
    c_status TEXT NOT NULL DEFAULT 'accepted',
    c_progress_time INTEGER NOT NULL DEFAULT 0,
    c_error TEXT NOT NULL DEFAULT '',
    -- 按默认参数新补的期次：补算时原行不存在、或原行的 __params 里没有这个成员，只能用默认参数算出来，
    -- 与当时的实时结果不保证一致。规范 JSON：成员键 → [[起始秒, 结束秒], ...]（闭区间，相邻期次合并）
    c_default_filled_json TEXT NOT NULL DEFAULT '{}',
    -- 执行该任务的计算引擎与租约；租约过期后任务可被重新领取
    c_engine_id TEXT NOT NULL DEFAULT '',
    c_lease_token TEXT NOT NULL DEFAULT '',
    c_lease_expires_at INTEGER NOT NULL DEFAULT 0,
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_status IN ('accepted', 'running', 'succeeded', 'failed', 'cancelled')),
    CHECK (c_end_time > c_start_time),
    UNIQUE (c_request_id)
);

CREATE INDEX IF NOT EXISTS idx_t_factor_recalc_jobs_status
ON t_factor_recalc_jobs (c_status, c_mtime);
CREATE INDEX IF NOT EXISTS idx_t_factor_recalc_jobs_pull
ON t_factor_recalc_jobs (c_status, c_lease_expires_at);

-- 最近一次被接受的引擎心跳（单行）：引擎租约只有 45 秒，离线告警按最近心跳判断；
-- 落库后管理端重启也能判定“引擎一直没回来”
CREATE TABLE IF NOT EXISTS t_factor_engine_heartbeat (
    c_id INTEGER NOT NULL PRIMARY KEY,
    c_engine_id TEXT NOT NULL,
    c_boot_id TEXT NOT NULL DEFAULT '',
    c_heartbeat_at INTEGER NOT NULL,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_id = 1)
);

CREATE TRIGGER IF NOT EXISTS trg_t_factor_tasks_mtime
AFTER UPDATE OF c_name, c_source_dataset_id, c_freq, c_result_dataset_id, c_bound_instance_id, c_status ON t_factor_tasks
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_factor_tasks SET c_mtime = CURRENT_TIMESTAMP WHERE c_task_id = OLD.c_task_id;
END;

CREATE TRIGGER IF NOT EXISTS trg_t_factor_defs_mtime
AFTER UPDATE ON t_factor_defs
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_factor_defs SET c_mtime = CURRENT_TIMESTAMP WHERE c_factor_id = OLD.c_factor_id;
END;

-- 只在配置列变化时更新 c_mtime；提交参数只写 c_last_* 运行状态列，不触发
CREATE TRIGGER IF NOT EXISTS trg_t_factor_task_members_mtime
AFTER UPDATE OF c_factor_id, c_kind, c_status, c_params ON t_factor_task_members
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_factor_task_members SET c_mtime = CURRENT_TIMESTAMP
    WHERE c_task_id = OLD.c_task_id AND c_member_key = OLD.c_member_key;
END;

CREATE TRIGGER IF NOT EXISTS trg_t_factor_recalc_jobs_mtime
AFTER UPDATE ON t_factor_recalc_jobs
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_factor_recalc_jobs SET c_mtime = CURRENT_TIMESTAMP WHERE c_job_id = OLD.c_job_id;
END;

CREATE TRIGGER IF NOT EXISTS trg_t_factor_categories_mtime
AFTER UPDATE ON t_factor_categories
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_factor_categories SET c_mtime = CURRENT_TIMESTAMP WHERE c_category_id = OLD.c_category_id;
END;
