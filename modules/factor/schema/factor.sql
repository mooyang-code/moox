PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS t_factor_tasks (
    c_task_id TEXT NOT NULL PRIMARY KEY,
    c_space_id TEXT NOT NULL,
    c_source_dataset_id TEXT NOT NULL,
    c_freq TEXT NOT NULL,
    c_result_dataset_id TEXT NOT NULL,
    -- 私有任务所属的策略实例（唯一的参数写入方）；空串表示没有写入方，成员带固定参数
    c_owner_instance_id TEXT NOT NULL DEFAULT '',
    c_status TEXT NOT NULL DEFAULT 'pending',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_status IN ('pending', 'enabled', 'disabled', 'deleting')),
    UNIQUE (c_result_dataset_id)
);

CREATE INDEX IF NOT EXISTS idx_t_factor_tasks_owner ON t_factor_tasks (c_owner_instance_id);

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

-- 因子任务成员：定义在某个因子任务中的运行实例，身份是任务内唯一的别名（同一定义可以用不同别名加入多次），
-- 启停状态属于成员
CREATE TABLE IF NOT EXISTS t_factor_task_members (
    c_task_id TEXT NOT NULL,
    c_member_alias TEXT NOT NULL,
    c_factor_id TEXT NOT NULL,
    c_status TEXT NOT NULL DEFAULT 'disabled',
    -- 连续沿用参数多少期后告警
    c_carry_alert_periods INTEGER NOT NULL DEFAULT 10,
    -- 成员的固定参数（参数名 → 取值的规范 JSON，加成员时的完整参数，之后不变）；空对象表示参数由写入方逐期提交
    c_params TEXT NOT NULL DEFAULT '{}',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (c_task_id, c_member_alias),
    CHECK (c_carry_alert_periods > 0),
    CHECK (c_status IN ('enabled', 'disabled')),
    FOREIGN KEY (c_task_id) REFERENCES t_factor_tasks (c_task_id),
    FOREIGN KEY (c_factor_id) REFERENCES t_factor_defs (c_factor_id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_t_factor_task_members_task ON t_factor_task_members (c_task_id, c_status);
CREATE INDEX IF NOT EXISTS idx_t_factor_task_members_factor ON t_factor_task_members (c_factor_id);

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

-- 参数请求：写入方为成员某一期提交的完整快照（因子动态调参执行计划 2.4）。c_seq 是库内序号，冻结时按它选
-- 截止前的最后一份；请求原文只用于审计与排查，已应用的参数以冻结记录为准。保留 30 天。
CREATE TABLE IF NOT EXISTS t_factor_param_requests (
    c_seq INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_request_id TEXT NOT NULL,
    c_task_id TEXT NOT NULL,
    c_member_alias TEXT NOT NULL,
    c_period_time INTEGER NOT NULL,
    c_based_on_period INTEGER NOT NULL DEFAULT 0,
    c_snapshot_hash TEXT NOT NULL,
    c_writer TEXT NOT NULL,
    c_status TEXT NOT NULL DEFAULT 'accepted',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_status IN ('accepted', 'applied', 'superseded', 'not_applied')),
    UNIQUE (c_request_id, c_task_id, c_member_alias)
);

CREATE INDEX IF NOT EXISTS idx_t_factor_param_requests_period
ON t_factor_param_requests (c_task_id, c_member_alias, c_period_time, c_seq);
CREATE INDEX IF NOT EXISTS idx_t_factor_param_requests_ctime ON t_factor_param_requests (c_ctime);

-- 参数快照：按内容摘要去重、不可变；请求与冻结记录都引用它
CREATE TABLE IF NOT EXISTS t_factor_param_snapshots (
    c_snapshot_hash TEXT NOT NULL PRIMARY KEY,
    c_snapshot_json TEXT NOT NULL,
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 冻结记录：成员每一期最终采用的参数，是唯一事实来源。快照与来源相同的连续期次压缩成一个区间
-- [c_start_period, c_end_period]（两端都含）；只有每期都改参数的成员才每期一行。
CREATE TABLE IF NOT EXISTS t_factor_param_freezes (
    c_task_id TEXT NOT NULL,
    c_member_alias TEXT NOT NULL,
    c_start_period INTEGER NOT NULL,
    c_end_period INTEGER NOT NULL,
    c_snapshot_hash TEXT NOT NULL,
    c_source TEXT NOT NULL,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (c_task_id, c_member_alias, c_start_period),
    CHECK (c_end_period >= c_start_period),
    CHECK (c_source IN ('requested', 'carried', 'carried_offline', 'default')),
    FOREIGN KEY (c_snapshot_hash) REFERENCES t_factor_param_snapshots (c_snapshot_hash)
);

CREATE INDEX IF NOT EXISTS idx_t_factor_param_freezes_end
ON t_factor_param_freezes (c_task_id, c_member_alias, c_end_period);

-- 每个任务已冻结到的最新一期：晚于它的期次才接受参数请求
CREATE TABLE IF NOT EXISTS t_factor_param_frozen_periods (
    c_task_id TEXT NOT NULL PRIMARY KEY,
    c_frozen_through INTEGER NOT NULL,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TRIGGER IF NOT EXISTS trg_t_factor_tasks_mtime
AFTER UPDATE ON t_factor_tasks
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

CREATE TRIGGER IF NOT EXISTS trg_t_factor_task_members_mtime
AFTER UPDATE ON t_factor_task_members
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_factor_task_members SET c_mtime = CURRENT_TIMESTAMP
    WHERE c_task_id = OLD.c_task_id AND c_member_alias = OLD.c_member_alias;
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
