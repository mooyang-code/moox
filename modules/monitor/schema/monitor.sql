-- 完整部署快照与系统探测定义在同一事务内更新。
CREATE TABLE IF NOT EXISTS t_monitor_topology (
    c_id INTEGER NOT NULL PRIMARY KEY,
    c_snapshot_json TEXT NOT NULL,
    CHECK (c_id = 1)
);

-- 每项检查一行：最近一次结果与告警状态。检查由评估按本轮事实生成，本轮不再产出且没有冻结的检查当轮删除；
-- 告警阈值与级别由检查 ID 在代码里推出，不入库（关注项的级别由生产者给出，记在 c_severity）。c_ctime 是检查第一次出现的时间。时间均为 UTC。
CREATE TABLE IF NOT EXISTS t_monitor_checks (
    c_space_id TEXT NOT NULL,
    c_check_id TEXT NOT NULL,
    c_name TEXT NOT NULL,
    c_severity TEXT NOT NULL,
    c_status TEXT NOT NULL,
    c_reason TEXT NOT NULL DEFAULT '',
    c_raw_error TEXT NOT NULL DEFAULT '',
    c_checked_at DATETIME NOT NULL,
    c_failure_reason TEXT NOT NULL DEFAULT '',
    c_failure_raw_error TEXT NOT NULL DEFAULT '',
    c_failed_at DATETIME,
    c_failure_count INTEGER NOT NULL DEFAULT 0,
    c_success_count INTEGER NOT NULL DEFAULT 0,
    c_alert_status TEXT NOT NULL DEFAULT 'ok',
    c_triggered_at DATETIME,
    c_notified_at DATETIME,
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_severity IN ('critical', 'warning')),
    CHECK (c_status IN ('ok', 'degraded', 'down')),
    CHECK (c_alert_status IN ('ok', 'firing')),
    PRIMARY KEY (c_space_id, c_check_id)
);

CREATE INDEX IF NOT EXISTS idx_t_monitor_checks_alert ON t_monitor_checks (c_alert_status);

CREATE TRIGGER IF NOT EXISTS trg_t_monitor_checks_mtime
AFTER UPDATE ON t_monitor_checks
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_monitor_checks SET c_mtime = CURRENT_TIMESTAMP WHERE c_space_id = OLD.c_space_id AND c_check_id = OLD.c_check_id;
END;

-- 每台登记主机最近一次上报：以 Admin 登记的主机 ID 为键，重装 HostAgent 沿用同一行。
CREATE TABLE IF NOT EXISTS t_monitor_host_agents (
    c_host_id TEXT NOT NULL PRIMARY KEY,
    c_hostname TEXT NOT NULL,
    c_boot_id TEXT NOT NULL,
    c_last_seen_at DATETIME NOT NULL,
    c_last_event_id TEXT NOT NULL,
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 主机快照历史：每台主机每分钟一行（同一分钟内后到的覆盖先到的），c_snapshot 是 HostSnapshot 的 protobuf 编码。
-- 按采样时间保留 7 天，由数据清理定时任务删除。时间均为 UTC。
CREATE TABLE IF NOT EXISTS t_monitor_host_samples (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_host_id TEXT NOT NULL,
    c_observed_at DATETIME NOT NULL,
    c_snapshot BLOB NOT NULL,
    UNIQUE (c_host_id, c_observed_at)
);

CREATE INDEX IF NOT EXISTS idx_monitor_host_samples_observed ON t_monitor_host_samples (c_observed_at);

-- 告警推送渠道（企业微信、飞书、钉钉群机器人或通用 Webhook），告警发给全部渠道；初始渠道由 moox-cli setup init 在没有任何渠道时写入一次，之后在监控页增删
-- c_secret 选填：飞书的签名校验密钥、钉钉的加签密钥或通用 Webhook 的签名密钥，只写不读
CREATE TABLE IF NOT EXISTS t_monitor_notification_channels (
    c_id INTEGER PRIMARY KEY AUTOINCREMENT,
    c_channel_id TEXT NOT NULL,
    c_channel_type TEXT NOT NULL,
    c_webhook_url TEXT NOT NULL,
    c_secret TEXT NOT NULL DEFAULT '',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_channel_type IN ('wecom', 'feishu', 'dingtalk', 'webhook')),
    CHECK (c_webhook_url <> ''),
    UNIQUE (c_channel_id),
    UNIQUE (c_webhook_url)
);

-- 告警事件：触发、提醒、恢复与推送失败。标题与级别在事件里，检查删除后告警经过与静默汇总仍能显示。按 c_created_at 保留。
CREATE TABLE IF NOT EXISTS t_monitor_alert_events (
    c_id INTEGER PRIMARY KEY AUTOINCREMENT,
    c_event_id TEXT NOT NULL,
    c_space_id TEXT NOT NULL,
    c_check_id TEXT NOT NULL,
    c_title TEXT NOT NULL,
    c_severity TEXT NOT NULL,
    c_event_type TEXT NOT NULL,
    c_message TEXT NOT NULL DEFAULT '',
    c_payload TEXT NOT NULL DEFAULT '{}',
    c_created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_event_type IN ('triggered', 'reminder', 'resolved', 'send_failed')),
    UNIQUE (c_event_id)
);

CREATE INDEX IF NOT EXISTS idx_t_monitor_alert_events_recent ON t_monitor_alert_events (c_space_id, c_created_at DESC);
CREATE INDEX IF NOT EXISTS idx_t_monitor_alert_events_created_at ON t_monitor_alert_events (c_created_at);
CREATE INDEX IF NOT EXISTS idx_t_monitor_alert_events_check
ON t_monitor_alert_events (c_space_id, c_check_id, c_created_at DESC);

-- Application metrics catalog and ingestion state. Historical samples are kept
-- in Storage; SQLite deliberately stores only bounded catalog/latest metadata.
-- 每个进程启动（c_boot_id）一行：c_ctime 是这次启动第一次上报的时间，反复重启按它数；
-- 就绪（ready、not_ready，空表示上报方没有就绪判断）与模块级生命周期取最近一次上报。
CREATE TABLE IF NOT EXISTS t_monitor_metric_services (
    c_id INTEGER PRIMARY KEY AUTOINCREMENT,
    c_service_name TEXT NOT NULL,
    c_instance_id TEXT NOT NULL,
    c_boot_id TEXT NOT NULL DEFAULT '',
    c_node_id TEXT NOT NULL DEFAULT '',
    c_version TEXT NOT NULL DEFAULT '',
    c_last_seen_at DATETIME NOT NULL,
    c_readiness TEXT NOT NULL DEFAULT '',
    c_not_ready_reason TEXT NOT NULL DEFAULT '',
    c_lifecycle_json TEXT NOT NULL DEFAULT '',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_readiness IN ('', 'ready', 'not_ready'))
);

CREATE UNIQUE INDEX IF NOT EXISTS uk_monitor_metric_services_key
ON t_monitor_metric_services (c_service_name, c_instance_id, c_boot_id);

-- 指标序列：目录与最新值在同一行。c_last_seen_at 是最近一次上报这条序列的时间（24 小时没上报的序列由清理定时器删除）；
-- 最新值只前进到更新的观测，水位类指标不往小里退，所以 c_observed_at 可能早于 c_last_seen_at。
CREATE TABLE IF NOT EXISTS t_monitor_metric_series (
    c_id INTEGER PRIMARY KEY AUTOINCREMENT,
    c_series_id TEXT NOT NULL,
    c_service_name TEXT NOT NULL,
    c_instance_id TEXT NOT NULL,
    c_metric_name TEXT NOT NULL,
    c_metric_type TEXT NOT NULL,
    c_labels_json TEXT NOT NULL DEFAULT '{}',
    c_last_seen_at DATETIME NOT NULL,
    c_value REAL NOT NULL,
    c_observed_at DATETIME NOT NULL,
    c_interval_seconds INTEGER NOT NULL DEFAULT 0,
    c_message_id TEXT NOT NULL DEFAULT '',
    c_producer_node_id TEXT NOT NULL DEFAULT '',
    c_producer_version TEXT NOT NULL DEFAULT '',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS uk_monitor_metric_series_key ON t_monitor_metric_series (c_series_id);
CREATE INDEX IF NOT EXISTS idx_monitor_metric_series_name
ON t_monitor_metric_series (c_service_name, c_metric_name, c_observed_at DESC);
CREATE INDEX IF NOT EXISTS idx_monitor_metric_series_last_seen ON t_monitor_metric_series (c_last_seen_at);

CREATE TABLE IF NOT EXISTS t_monitor_metric_ingest_messages (
    c_id INTEGER PRIMARY KEY AUTOINCREMENT,
    c_message_id TEXT NOT NULL,
    c_service_name TEXT NOT NULL DEFAULT '',
    c_instance_id TEXT NOT NULL DEFAULT '',
    c_occurred_at DATETIME,
    c_processed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_expires_at DATETIME NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS uk_monitor_metric_ingest_message ON t_monitor_metric_ingest_messages (c_message_id);
CREATE INDEX IF NOT EXISTS idx_monitor_metric_ingest_expiry ON t_monitor_metric_ingest_messages (c_expires_at);

-- 数据链路逐根到达记录：生产者的输出水位每前进一次记一行（新的最新一根；一批写完的更早几根不单独记），
-- 与指标入库在同一事务里写入；按空间、周期与开盘时间查询，按到达时间清理（保留 7 天）。时间均为 UTC。
CREATE TABLE IF NOT EXISTS t_monitor_bar_arrivals (
    c_id INTEGER PRIMARY KEY AUTOINCREMENT,
    c_producer TEXT NOT NULL,
    c_space_id TEXT NOT NULL,
    c_dataset_id TEXT NOT NULL,
    c_primary_dataset_id TEXT NOT NULL DEFAULT '',
    c_freq TEXT NOT NULL,
    c_bar_at DATETIME NOT NULL,
    c_previous_bar_at DATETIME,
    c_arrived_at DATETIME NOT NULL,
    c_reported_at DATETIME NOT NULL,
    c_service_name TEXT NOT NULL DEFAULT '',
    c_instance_id TEXT NOT NULL DEFAULT '',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS uk_monitor_bar_arrival
ON t_monitor_bar_arrivals (c_producer, c_space_id, c_dataset_id, c_freq, c_bar_at);
CREATE INDEX IF NOT EXISTS idx_monitor_bar_arrival_space_bar ON t_monitor_bar_arrivals (c_space_id, c_freq, c_bar_at);
CREATE INDEX IF NOT EXISTS idx_monitor_bar_arrival_arrived ON t_monitor_bar_arrivals (c_arrived_at);

CREATE TRIGGER IF NOT EXISTS trg_monitor_metric_services_mtime
AFTER UPDATE ON t_monitor_metric_services
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_monitor_metric_services SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

CREATE TRIGGER IF NOT EXISTS trg_monitor_metric_series_mtime
AFTER UPDATE ON t_monitor_metric_series
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_monitor_metric_series SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

CREATE TABLE IF NOT EXISTS t_monitor_component_health (
    c_host_id TEXT NOT NULL,
    c_component_id TEXT NOT NULL,
    c_status TEXT NOT NULL,
    c_since_at DATETIME NOT NULL,
    c_observed_at DATETIME NOT NULL,
    PRIMARY KEY (c_host_id, c_component_id),
    CHECK (c_status IN ('healthy', 'degraded', 'down', 'unknown', 'disabled', 'unchecked'))
);

-- 最后一份有效的采集任务清单：采集服务不可用或 Monitor 重启后继续用它检查 K 线新鲜度
CREATE TABLE IF NOT EXISTS t_monitor_task_inventory (
    c_id INTEGER NOT NULL PRIMARY KEY,
    c_snapshot TEXT NOT NULL,
    c_fetched_at DATETIME NOT NULL,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_id = 1)
);

-- 活跃对象第一次出现在采集对象清单里的时间：K 线新鲜度据此给新上线对象一段等待首根 K 线的宽限期。
-- c_baseline=1 表示数据集首次被观察时就已存在的对象（部署前的存量），不给宽限。
CREATE TABLE IF NOT EXISTS t_monitor_subject_first_seen (
    c_space_id TEXT NOT NULL,
    c_dataset_id TEXT NOT NULL,
    c_subject_id TEXT NOT NULL,
    c_first_seen_at DATETIME NOT NULL,
    c_baseline INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (c_space_id, c_dataset_id, c_subject_id),
    CHECK (c_baseline IN (0, 1))
);

-- 各服务上报的重要日志：Error 及以上级别，以及代码里显式标出的关键事件。按日志时间保留 30 天，并有行数上限。
-- c_record_key 是“实例 | 启动号 | 序号”，重复投递靠它去重；必须带启动号，进程重启后序号从头开始。
CREATE TABLE IF NOT EXISTS t_monitor_log_records (
    c_id INTEGER PRIMARY KEY AUTOINCREMENT,
    c_record_key TEXT NOT NULL,
    c_host_id TEXT NOT NULL DEFAULT '',
    c_component TEXT NOT NULL,
    c_instance_id TEXT NOT NULL DEFAULT '',
    c_level TEXT NOT NULL,
    c_event TEXT NOT NULL DEFAULT '',
    c_message TEXT NOT NULL DEFAULT '',
    c_caller TEXT NOT NULL DEFAULT '',
    c_fields TEXT NOT NULL DEFAULT '{}',
    c_merged_count INTEGER NOT NULL DEFAULT 0,
    c_logged_at DATETIME NOT NULL,
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (c_record_key)
);

CREATE INDEX IF NOT EXISTS idx_t_monitor_log_records_logged_at ON t_monitor_log_records (c_logged_at);
CREATE INDEX IF NOT EXISTS idx_t_monitor_log_records_component
ON t_monitor_log_records (c_component, c_logged_at);
