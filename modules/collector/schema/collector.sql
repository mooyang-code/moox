PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS t_collector_tasks (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL DEFAULT '',
    c_task_id TEXT NOT NULL,
    c_task_name TEXT NOT NULL,
    c_description TEXT NOT NULL DEFAULT '',
    c_data_type TEXT NOT NULL DEFAULT '',
    c_definition_hash TEXT NOT NULL DEFAULT '',
    c_series_hash TEXT NOT NULL DEFAULT '',
    c_collect_params TEXT NOT NULL DEFAULT '{}',
    c_enabled INTEGER NOT NULL DEFAULT 1,
    c_creator TEXT NOT NULL DEFAULT '',
    c_prepare_state TEXT NOT NULL DEFAULT 'ready',
    c_last_error TEXT NOT NULL DEFAULT '',
    c_result_dataset_id TEXT NOT NULL DEFAULT '',
    c_result_view_id TEXT NOT NULL DEFAULT '',
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_collector_tasks_space_task ON t_collector_tasks (c_space_id, c_task_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_collector_tasks_space_name ON t_collector_tasks (c_space_id, c_task_name);
CREATE UNIQUE INDEX IF NOT EXISTS idx_collector_tasks_definition
ON t_collector_tasks (c_space_id, c_definition_hash)
WHERE c_definition_hash <> '';
CREATE INDEX IF NOT EXISTS idx_collector_tasks_space_result_view
ON t_collector_tasks (c_space_id, c_result_view_id)
WHERE c_result_view_id <> '';
CREATE INDEX IF NOT EXISTS idx_collector_tasks_space ON t_collector_tasks (c_space_id);

CREATE INDEX IF NOT EXISTS idx_collector_tasks_type ON t_collector_tasks (c_data_type);

CREATE INDEX IF NOT EXISTS idx_collector_tasks_enabled ON t_collector_tasks (c_enabled);

CREATE INDEX IF NOT EXISTS idx_collector_tasks_prepare ON t_collector_tasks (c_data_type, c_prepare_state, c_enabled);

CREATE TABLE IF NOT EXISTS t_collector_task_tags (
    c_space_id TEXT NOT NULL,
    c_task_id TEXT NOT NULL,
    c_tag_id TEXT NOT NULL,
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (c_space_id, c_task_id, c_tag_id),
    FOREIGN KEY (c_space_id, c_task_id) REFERENCES t_collector_tasks (c_space_id, c_task_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_collector_task_tags_tag ON t_collector_task_tags (c_space_id, c_tag_id);

CREATE TABLE IF NOT EXISTS t_collector_task_series (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_task_id TEXT NOT NULL,
    c_series_index INTEGER NOT NULL,
    c_series_key TEXT NOT NULL,
    c_subject_id TEXT NOT NULL,
    c_provider TEXT NOT NULL DEFAULT '',
    c_source_id TEXT NOT NULL DEFAULT '',
    c_market_type TEXT NOT NULL DEFAULT '',
    c_provider_symbol TEXT NOT NULL DEFAULT '',
    c_series_tag TEXT NOT NULL DEFAULT '',
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (c_space_id, c_task_id, c_series_key),
    UNIQUE (c_space_id, c_task_id, c_series_index),
    FOREIGN KEY (c_space_id, c_task_id) REFERENCES t_collector_tasks (c_space_id, c_task_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_collector_task_series_subject ON t_collector_task_series (c_space_id, c_subject_id);

CREATE TRIGGER IF NOT EXISTS update_collector_tasks_mtime
AFTER UPDATE ON t_collector_tasks
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_collector_tasks SET c_mtime = STRFTIME('%Y-%m-%d %H:%M:%f', 'now') WHERE rowid = NEW.rowid;
END;

-- SCF 定时采集的采集计划：每个空间同一时间最多一份未生效的计划，最近一份已生效的计划即“已生效状态”。
CREATE TABLE IF NOT EXISTS t_collector_fetch_plans (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_status TEXT NOT NULL DEFAULT 'pending',
    c_plan TEXT NOT NULL,
    c_progress TEXT NOT NULL DEFAULT '{}',
    c_last_error TEXT NOT NULL DEFAULT '',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_status IN ('pending', 'applying', 'applied'))
);

CREATE UNIQUE INDEX IF NOT EXISTS uk_t_collector_fetch_plans_open
ON t_collector_fetch_plans (c_space_id)
WHERE c_status <> 'applied';
CREATE INDEX IF NOT EXISTS idx_t_collector_fetch_plans_space ON t_collector_fetch_plans (c_space_id, c_id);

CREATE TRIGGER IF NOT EXISTS trg_t_collector_fetch_plans_mtime
AFTER UPDATE ON t_collector_fetch_plans
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_collector_fetch_plans SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

-- 空间的采集暂停开关：暂停期间 Collector 不对账、不推进补采，并已停用该空间的 SCF 定时器；重启后保持。
CREATE TABLE IF NOT EXISTS t_collector_fetch_plan_pauses (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL UNIQUE,
    c_paused INTEGER NOT NULL DEFAULT 0 CHECK (c_paused IN (0, 1)),
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TRIGGER IF NOT EXISTS trg_t_collector_fetch_plan_pauses_mtime
AFTER UPDATE ON t_collector_fetch_plan_pauses
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_collector_fetch_plan_pauses SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

-- K 线补采作业：一个作业拆成若干（数据集、标的、时间段）分块。
-- c_dataset_id、c_frequency、c_subject_id 为空表示全部；c_kind 取 gap（按缺口）、force（整段重拉）、history（确认历史缺口）。
CREATE TABLE IF NOT EXISTS t_collector_kline_backfill_jobs (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_job_id TEXT NOT NULL,
    c_space_id TEXT NOT NULL,
    c_source TEXT NOT NULL DEFAULT 'manual',
    c_kind TEXT NOT NULL DEFAULT 'gap',
    c_dataset_id TEXT NOT NULL DEFAULT '',
    c_frequency TEXT NOT NULL DEFAULT '',
    c_subject_id TEXT NOT NULL DEFAULT '',
    c_from DATETIME NOT NULL,
    c_to DATETIME NOT NULL,
    c_advance_watermark INTEGER NOT NULL DEFAULT 0,
    c_status TEXT NOT NULL DEFAULT 'pending',
    c_last_error TEXT NOT NULL DEFAULT '',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_source IN ('manual', 'auto')),
    CHECK (c_kind IN ('gap', 'force', 'history')),
    CHECK (c_status IN ('pending', 'running', 'succeeded', 'failed', 'canceled')),
    UNIQUE (c_job_id)
);

CREATE INDEX IF NOT EXISTS idx_t_collector_kline_backfill_jobs_space ON t_collector_kline_backfill_jobs (c_space_id, c_status);
CREATE INDEX IF NOT EXISTS idx_t_collector_kline_backfill_jobs_source
ON t_collector_kline_backfill_jobs (c_space_id, c_source, c_ctime);
CREATE INDEX IF NOT EXISTS idx_t_collector_kline_backfill_jobs_retention
ON t_collector_kline_backfill_jobs (c_space_id, c_mtime);

CREATE TRIGGER IF NOT EXISTS trg_t_collector_kline_backfill_jobs_mtime
AFTER UPDATE ON t_collector_kline_backfill_jobs
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_collector_kline_backfill_jobs SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

-- c_missing 是上一次执行后复查仍缺的 K 线（规范化区间串）：连续两次一致才确认是行情源没有。
CREATE TABLE IF NOT EXISTS t_collector_kline_backfill_chunks (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_job_id TEXT NOT NULL,
    c_dataset_id TEXT NOT NULL,
    c_frequency TEXT NOT NULL,
    c_subject_id TEXT NOT NULL,
    c_from DATETIME NOT NULL,
    c_to DATETIME NOT NULL,
    c_chunk_id TEXT NOT NULL,
    c_status TEXT NOT NULL DEFAULT 'pending',
    c_attempts INTEGER NOT NULL DEFAULT 0,
    c_rows INTEGER NOT NULL DEFAULT 0,
    c_missing TEXT NOT NULL DEFAULT '',
    c_started_at DATETIME,
    c_latest_end_at DATETIME,
    c_last_error TEXT NOT NULL DEFAULT '',
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_status IN ('pending', 'running', 'succeeded', 'failed', 'canceled')),
    UNIQUE (c_job_id, c_dataset_id, c_frequency, c_subject_id, c_from)
);

CREATE INDEX IF NOT EXISTS idx_t_collector_kline_backfill_chunks_job ON t_collector_kline_backfill_chunks (c_job_id, c_status);
CREATE INDEX IF NOT EXISTS idx_t_collector_kline_backfill_chunks_status ON t_collector_kline_backfill_chunks (c_status, c_mtime);

CREATE TRIGGER IF NOT EXISTS trg_t_collector_kline_backfill_chunks_mtime
AFTER UPDATE ON t_collector_kline_backfill_chunks
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_collector_kline_backfill_chunks SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

-- 已确认“行情源没有”的 K 线：补采成功后复查仍缺且结果一致的连续区间，检查与补采都扣除。
CREATE TABLE IF NOT EXISTS t_collector_source_absences (
    c_space_id TEXT NOT NULL,
    c_dataset_id TEXT NOT NULL,
    c_frequency TEXT NOT NULL,
    c_subject_id TEXT NOT NULL,
    c_from DATETIME NOT NULL,
    c_to DATETIME NOT NULL,
    c_bars INTEGER NOT NULL DEFAULT 0,
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (c_space_id, c_dataset_id, c_frequency, c_subject_id, c_from)
);

-- 每个数据集最近一次历史缺口扫描（早于最早生效点的一段历史）的结果；c_gaps 是缺口分组 JSON，供确认补采。
CREATE TABLE IF NOT EXISTS t_collector_history_scans (
    c_space_id TEXT NOT NULL,
    c_dataset_id TEXT NOT NULL,
    c_frequency TEXT NOT NULL,
    c_scanned_at DATETIME NOT NULL,
    c_from DATETIME,
    c_to DATETIME,
    c_gap_bars INTEGER NOT NULL DEFAULT 0,
    c_gap_subjects INTEGER NOT NULL DEFAULT 0,
    c_gaps TEXT NOT NULL DEFAULT '[]',
    c_last_error TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (c_space_id, c_dataset_id, c_frequency)
);

-- 自动补采的扫描水位：已扫描到哪个时间点。
CREATE TABLE IF NOT EXISTS t_collector_backfill_watermarks (
    c_space_id TEXT NOT NULL,
    c_dataset_id TEXT NOT NULL,
    c_frequency TEXT NOT NULL,
    c_scanned_until DATETIME NOT NULL,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (c_space_id, c_dataset_id, c_frequency)
);

-- 每个数据集的采集方式：stream（WS 推送）或 pull（定时拉取）；executor 是拉取与补采的执行端（scf 云函数或 local 本地执行端）。
-- c_force 为 1 表示最近一次切换用了 --force：交接计划不等失联的推送或本地执行端确认，计划生效后清零。
CREATE TABLE IF NOT EXISTS t_collector_ingest_modes (
    c_space_id TEXT NOT NULL,
    c_dataset_id TEXT NOT NULL,
    c_mode TEXT NOT NULL,
    c_executor TEXT NOT NULL,
    c_force INTEGER NOT NULL DEFAULT 0,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (c_space_id, c_dataset_id),
    CHECK (c_mode IN ('stream', 'pull')),
    CHECK (c_executor IN ('scf', 'local')),
    CHECK (c_force IN (0, 1))
);

-- collector-stream 实例的心跳与已应用的计划：c_applied 是已应用的计划记录 ID 与阶段，c_status 是连接状态；
-- 同一空间只部署一个实例，一空间一行，不设租约与写权限到期。
CREATE TABLE IF NOT EXISTS t_collector_stream_workers (
    c_space_id TEXT NOT NULL,
    c_instance_id TEXT NOT NULL,
    c_applied TEXT NOT NULL DEFAULT '{}',
    c_status TEXT NOT NULL DEFAULT '{}',
    c_heartbeat_at DATETIME NOT NULL,
    PRIMARY KEY (c_space_id)
);

-- 采集名单排除：叠加在标签成员之上，只作用于采集计划（标签成员与标的属性照常同步）；
-- c_market_type 为空表示该空间所有市场，c_expires_at 为空表示不过期，到期后自动失效。
CREATE TABLE IF NOT EXISTS t_collector_subject_exclusions (
    c_space_id TEXT NOT NULL,
    c_subject_id TEXT NOT NULL,
    c_market_type TEXT NOT NULL DEFAULT '',
    c_reason TEXT NOT NULL DEFAULT '',
    c_caller TEXT NOT NULL DEFAULT '',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_expires_at DATETIME,
    PRIMARY KEY (c_space_id, c_subject_id, c_market_type)
);

-- 合成目标数据集的标的快照（按生效点追加）：一个目标桶落在哪份快照内，用周期提交写入；
-- 生效点之前的桶和快照之外的标的用普通写入。c_effective_from 是 Unix 秒，等于 Storage 登记的生效点。
CREATE TABLE IF NOT EXISTS t_collector_resample_snapshots (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_task_id TEXT NOT NULL,
    c_effective_from INTEGER NOT NULL,
    c_series_hash TEXT NOT NULL,
    c_subjects TEXT NOT NULL DEFAULT '[]',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (c_space_id, c_task_id, c_effective_from)
);

-- 历史合成作业：范围 [c_start, c_end) 按目标桶推进，c_next_bucket 是游标，进程重启后从游标继续。
-- c_origin 取 manual（CLI、控制台）或 source_backfill（源数据补采完成后触发）；c_not_before 之前不开始。
CREATE TABLE IF NOT EXISTS t_collector_resample_jobs (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_job_id TEXT NOT NULL,
    c_space_id TEXT NOT NULL,
    c_task_id TEXT NOT NULL,
    c_request_id TEXT NOT NULL,
    c_origin TEXT NOT NULL DEFAULT 'manual',
    c_subjects TEXT NOT NULL DEFAULT '[]',
    c_start DATETIME NOT NULL,
    c_end DATETIME NOT NULL,
    c_next_bucket DATETIME NOT NULL,
    c_not_before DATETIME NOT NULL,
    c_status TEXT NOT NULL DEFAULT 'queued',
    c_total_buckets INTEGER NOT NULL DEFAULT 0,
    c_done_buckets INTEGER NOT NULL DEFAULT 0,
    c_written INTEGER NOT NULL DEFAULT 0,
    c_skipped INTEGER NOT NULL DEFAULT 0,
    c_skipped_samples TEXT NOT NULL DEFAULT '[]',
    c_last_error TEXT NOT NULL DEFAULT '',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_origin IN ('manual', 'source_backfill')),
    CHECK (c_status IN ('queued', 'running', 'syncing', 'succeeded', 'failed', 'cancelled')),
    UNIQUE (c_job_id),
    UNIQUE (c_space_id, c_task_id, c_request_id)
);

CREATE INDEX IF NOT EXISTS idx_t_collector_resample_jobs_task ON t_collector_resample_jobs (c_space_id, c_task_id, c_status);
CREATE INDEX IF NOT EXISTS idx_t_collector_resample_jobs_status ON t_collector_resample_jobs (c_status, c_id);

CREATE TRIGGER IF NOT EXISTS trg_t_collector_resample_jobs_mtime
AFTER UPDATE ON t_collector_resample_jobs
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_collector_resample_jobs SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

-- 补写通知：补采分块读回确认成功（以及合成在周期结束后的补写）时，与状态更新在同一事务里记录。
-- 发布协程按（空间、数据集、频率、来源、作业）合并成批，c_batch_id 为空表示还没有成批。
CREATE TABLE IF NOT EXISTS t_collector_backfill_notices (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_dataset_id TEXT NOT NULL,
    c_frequency TEXT NOT NULL,
    c_subject_id TEXT NOT NULL,
    c_start DATETIME NOT NULL,
    c_end DATETIME NOT NULL,
    c_origin TEXT NOT NULL,
    c_job_id TEXT NOT NULL DEFAULT '',
    c_batch_id TEXT NOT NULL DEFAULT '',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_t_collector_backfill_notices_batch ON t_collector_backfill_notices (c_batch_id, c_id);

-- Collector 本地事件发件箱：事件成批后与通知的封存在同一事务里写入，发布协程推送到 JetStream，
-- 以 c_event_id 作为 Msg-Id 去重；发布成功才标记 c_published。
CREATE TABLE IF NOT EXISTS t_collector_event_outbox (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_event_id TEXT NOT NULL,
    c_space_id TEXT NOT NULL,
    c_event_name TEXT NOT NULL,
    c_subject_token TEXT NOT NULL,
    c_payload BLOB NOT NULL,
    c_published INTEGER NOT NULL DEFAULT 0,
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (c_event_id)
);

CREATE INDEX IF NOT EXISTS idx_t_collector_event_outbox_pending ON t_collector_event_outbox (c_published, c_id);

-- 已删除的旧结构：逐标的合成实例、实例写入目标、周期就绪（合成改为按数据集 × 桶批量执行，目标周期由 Storage 跟踪）。
-- 保留这些语句让已有的库在升级后清掉旧表；新库里它们什么都不做。
DROP TRIGGER IF EXISTS update_collector_instances_mtime;
DROP TABLE IF EXISTS t_period_readiness_items;
DROP TABLE IF EXISTS t_period_readiness;
DROP TABLE IF EXISTS t_collector_instance_write_targets;
DROP TABLE IF EXISTS t_collector_task_instances;
