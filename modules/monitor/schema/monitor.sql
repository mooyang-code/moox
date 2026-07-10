CREATE TABLE IF NOT EXISTS t_monitor_checks (
  c_id INTEGER PRIMARY KEY AUTOINCREMENT,
  c_space_id TEXT NOT NULL DEFAULT '',
  c_check_id TEXT NOT NULL,
  c_name TEXT NOT NULL,
  c_group_name TEXT NOT NULL DEFAULT '',
  c_kind TEXT NOT NULL,
  c_url TEXT NOT NULL DEFAULT '',
  c_method TEXT NOT NULL DEFAULT 'GET',
  c_headers TEXT NOT NULL DEFAULT '{}',
  c_body TEXT NOT NULL DEFAULT '',
  c_tcp_host TEXT NOT NULL DEFAULT '',
  c_tcp_port INTEGER NOT NULL DEFAULT 0,
  c_interval_seconds INTEGER NOT NULL DEFAULT 60,
  c_timeout_ms INTEGER NOT NULL DEFAULT 3000,
  c_expected_status TEXT NOT NULL DEFAULT '200-299',
  c_max_response_ms INTEGER NOT NULL DEFAULT 0,
  c_body_contains TEXT NOT NULL DEFAULT '',
  c_enabled INTEGER NOT NULL DEFAULT 1,
  c_source TEXT NOT NULL DEFAULT 'manual',
  c_labels TEXT NOT NULL DEFAULT '{}',
  c_description TEXT NOT NULL DEFAULT '',
  c_last_checked_at DATETIME,
  c_next_check_at DATETIME,
  c_is_deleted INTEGER NOT NULL DEFAULT 0,
  c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS uk_monitor_checks_key
  ON t_monitor_checks(c_space_id, c_check_id, c_is_deleted);
CREATE INDEX IF NOT EXISTS idx_monitor_checks_due
  ON t_monitor_checks(c_enabled, c_is_deleted, c_next_check_at);

CREATE TABLE IF NOT EXISTS t_monitor_check_results (
  c_id INTEGER PRIMARY KEY AUTOINCREMENT,
  c_result_id TEXT NOT NULL,
  c_space_id TEXT NOT NULL DEFAULT '',
  c_check_id TEXT NOT NULL,
  c_instance_id TEXT NOT NULL,
  c_success INTEGER NOT NULL DEFAULT 0,
  c_status TEXT NOT NULL,
  c_http_status INTEGER NOT NULL DEFAULT 0,
  c_connected INTEGER NOT NULL DEFAULT 0,
  c_latency_ms INTEGER NOT NULL DEFAULT 0,
  c_error_message TEXT NOT NULL DEFAULT '',
  c_body_excerpt TEXT NOT NULL DEFAULT '',
  c_checked_at DATETIME NOT NULL,
  c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS uk_monitor_results_id
  ON t_monitor_check_results(c_result_id);
CREATE INDEX IF NOT EXISTS idx_monitor_results_recent
  ON t_monitor_check_results(c_space_id, c_check_id, c_checked_at DESC);

CREATE TABLE IF NOT EXISTS t_monitor_webhooks (
  c_id INTEGER PRIMARY KEY AUTOINCREMENT,
  c_space_id TEXT NOT NULL DEFAULT '',
  c_webhook_id TEXT NOT NULL,
  c_name TEXT NOT NULL,
  c_url TEXT NOT NULL,
  c_method TEXT NOT NULL DEFAULT 'POST',
  c_headers TEXT NOT NULL DEFAULT '{}',
  c_body_template TEXT NOT NULL DEFAULT '{}',
  c_enabled INTEGER NOT NULL DEFAULT 1,
  c_is_deleted INTEGER NOT NULL DEFAULT 0,
  c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS uk_monitor_webhooks_key
  ON t_monitor_webhooks(c_space_id, c_webhook_id, c_is_deleted);

CREATE TABLE IF NOT EXISTS t_monitor_alert_rules (
  c_id INTEGER PRIMARY KEY AUTOINCREMENT,
  c_space_id TEXT NOT NULL DEFAULT '',
  c_rule_id TEXT NOT NULL,
  c_check_id TEXT NOT NULL,
  c_webhook_id TEXT NOT NULL,
  c_failure_threshold INTEGER NOT NULL DEFAULT 3,
  c_success_threshold INTEGER NOT NULL DEFAULT 2,
  c_minimum_reminder_interval_seconds INTEGER NOT NULL DEFAULT 0,
  c_send_on_resolved INTEGER NOT NULL DEFAULT 1,
  c_enabled INTEGER NOT NULL DEFAULT 1,
  c_description TEXT NOT NULL DEFAULT '',
  c_is_deleted INTEGER NOT NULL DEFAULT 0,
  c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS uk_monitor_alert_rules_key
  ON t_monitor_alert_rules(c_space_id, c_rule_id, c_is_deleted);
CREATE INDEX IF NOT EXISTS idx_monitor_alert_rules_check
  ON t_monitor_alert_rules(c_space_id, c_check_id, c_enabled, c_is_deleted);

CREATE TABLE IF NOT EXISTS t_monitor_alert_states (
  c_id INTEGER PRIMARY KEY AUTOINCREMENT,
  c_space_id TEXT NOT NULL DEFAULT '',
  c_rule_id TEXT NOT NULL,
  c_check_id TEXT NOT NULL,
  c_status TEXT NOT NULL DEFAULT 'ok',
  c_failure_count INTEGER NOT NULL DEFAULT 0,
  c_success_count INTEGER NOT NULL DEFAULT 0,
  c_owner_instance_id TEXT NOT NULL DEFAULT '',
  c_triggered_at DATETIME,
  c_resolved_at DATETIME,
  c_last_reminder_at DATETIME,
  c_dedupe_key TEXT NOT NULL DEFAULT '',
  c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS uk_monitor_alert_states_key
  ON t_monitor_alert_states(c_space_id, c_rule_id, c_check_id);

CREATE TABLE IF NOT EXISTS t_monitor_alert_events (
  c_id INTEGER PRIMARY KEY AUTOINCREMENT,
  c_event_id TEXT NOT NULL,
  c_space_id TEXT NOT NULL DEFAULT '',
  c_rule_id TEXT NOT NULL,
  c_check_id TEXT NOT NULL,
  c_event_type TEXT NOT NULL,
  c_status TEXT NOT NULL,
  c_owner_instance_id TEXT NOT NULL DEFAULT '',
  c_message TEXT NOT NULL DEFAULT '',
  c_payload TEXT NOT NULL DEFAULT '{}',
  c_created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS uk_monitor_alert_events_id
  ON t_monitor_alert_events(c_event_id);
CREATE INDEX IF NOT EXISTS idx_monitor_alert_events_recent
  ON t_monitor_alert_events(c_space_id, c_created_at DESC);

CREATE TABLE IF NOT EXISTS t_monitor_instances (
  c_id INTEGER PRIMARY KEY AUTOINCREMENT,
  c_instance_id TEXT NOT NULL,
  c_base_url TEXT NOT NULL DEFAULT '',
  c_status TEXT NOT NULL DEFAULT 'unknown',
  c_last_seen_at DATETIME,
  c_snapshot TEXT NOT NULL DEFAULT '{}',
  c_is_local INTEGER NOT NULL DEFAULT 0,
  c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS uk_monitor_instances_id
  ON t_monitor_instances(c_instance_id);

CREATE TABLE IF NOT EXISTS t_monitor_peer_snapshots (
  c_id INTEGER PRIMARY KEY AUTOINCREMENT,
  c_instance_id TEXT NOT NULL,
  c_base_url TEXT NOT NULL DEFAULT '',
  c_status TEXT NOT NULL DEFAULT 'unknown',
  c_snapshot TEXT NOT NULL DEFAULT '{}',
  c_checked_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS uk_monitor_peer_snapshots_instance
  ON t_monitor_peer_snapshots(c_instance_id);

CREATE TRIGGER IF NOT EXISTS trg_monitor_checks_mtime
AFTER UPDATE ON t_monitor_checks
FOR EACH ROW WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
  UPDATE t_monitor_checks SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

CREATE TRIGGER IF NOT EXISTS trg_monitor_webhooks_mtime
AFTER UPDATE ON t_monitor_webhooks
FOR EACH ROW WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
  UPDATE t_monitor_webhooks SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

CREATE TRIGGER IF NOT EXISTS trg_monitor_alert_rules_mtime
AFTER UPDATE ON t_monitor_alert_rules
FOR EACH ROW WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
  UPDATE t_monitor_alert_rules SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

CREATE TRIGGER IF NOT EXISTS trg_monitor_alert_states_mtime
AFTER UPDATE ON t_monitor_alert_states
FOR EACH ROW WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
  UPDATE t_monitor_alert_states SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

CREATE TRIGGER IF NOT EXISTS trg_monitor_instances_mtime
AFTER UPDATE ON t_monitor_instances
FOR EACH ROW WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
  UPDATE t_monitor_instances SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

CREATE TRIGGER IF NOT EXISTS trg_monitor_peer_snapshots_mtime
AFTER UPDATE ON t_monitor_peer_snapshots
FOR EACH ROW WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
  UPDATE t_monitor_peer_snapshots SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

-- Application metrics catalog and ingestion state. Historical samples are kept
-- in Storage; SQLite deliberately stores only bounded catalog/latest metadata.
CREATE TABLE IF NOT EXISTS t_monitor_metric_services (
  c_id INTEGER PRIMARY KEY AUTOINCREMENT,
  c_service_name TEXT NOT NULL,
  c_instance_id TEXT NOT NULL,
  c_boot_id TEXT NOT NULL DEFAULT '',
  c_node_id TEXT NOT NULL DEFAULT '',
  c_version TEXT NOT NULL DEFAULT '',
  c_last_seen_at DATETIME NOT NULL,
  c_is_stale INTEGER NOT NULL DEFAULT 0,
  c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX IF NOT EXISTS uk_monitor_metric_services_key
  ON t_monitor_metric_services(c_service_name, c_instance_id, c_boot_id);

CREATE TABLE IF NOT EXISTS t_monitor_metric_series (
  c_id INTEGER PRIMARY KEY AUTOINCREMENT,
  c_service_name TEXT NOT NULL,
  c_instance_id TEXT NOT NULL,
  c_series_id TEXT NOT NULL,
  c_metric_name TEXT NOT NULL,
  c_metric_type TEXT NOT NULL,
  c_labels_json TEXT NOT NULL DEFAULT '{}',
  c_last_seen_at DATETIME NOT NULL,
  c_is_stale INTEGER NOT NULL DEFAULT 0,
  c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX IF NOT EXISTS uk_monitor_metric_series_key
  ON t_monitor_metric_series(c_service_name, c_instance_id, c_series_id);
CREATE INDEX IF NOT EXISTS idx_monitor_metric_series_name
  ON t_monitor_metric_series(c_service_name, c_metric_name, c_is_stale);

CREATE TABLE IF NOT EXISTS t_monitor_metric_latest (
  c_id INTEGER PRIMARY KEY AUTOINCREMENT,
  c_series_id TEXT NOT NULL,
  c_service_name TEXT NOT NULL,
  c_instance_id TEXT NOT NULL,
  c_metric_name TEXT NOT NULL,
  c_metric_type TEXT NOT NULL,
  c_labels_json TEXT NOT NULL DEFAULT '{}',
  c_value REAL NOT NULL,
  c_observed_at DATETIME NOT NULL,
  c_interval_seconds INTEGER NOT NULL DEFAULT 0,
  c_message_id TEXT NOT NULL DEFAULT '',
  c_producer_node_id TEXT NOT NULL DEFAULT '',
  c_producer_version TEXT NOT NULL DEFAULT '',
  c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX IF NOT EXISTS uk_monitor_metric_latest_series ON t_monitor_metric_latest(c_series_id);
CREATE INDEX IF NOT EXISTS idx_monitor_metric_latest_name ON t_monitor_metric_latest(c_service_name, c_metric_name, c_observed_at DESC);

CREATE TABLE IF NOT EXISTS t_monitor_metric_ingest_messages (
  c_id INTEGER PRIMARY KEY AUTOINCREMENT,
  c_message_id TEXT NOT NULL,
  c_service_name TEXT NOT NULL DEFAULT '',
  c_instance_id TEXT NOT NULL DEFAULT '',
  c_occurred_at DATETIME,
  c_processed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  c_expires_at DATETIME NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS uk_monitor_metric_ingest_message ON t_monitor_metric_ingest_messages(c_message_id);
CREATE INDEX IF NOT EXISTS idx_monitor_metric_ingest_expiry ON t_monitor_metric_ingest_messages(c_expires_at);

CREATE TABLE IF NOT EXISTS t_monitor_metric_rules (
  c_id INTEGER PRIMARY KEY AUTOINCREMENT,
  c_space_id TEXT NOT NULL DEFAULT '',
  c_rule_id TEXT NOT NULL,
  c_name TEXT NOT NULL DEFAULT '',
  c_definition_json TEXT NOT NULL DEFAULT '{}',
  c_evaluation_interval_seconds INTEGER NOT NULL DEFAULT 60,
  c_enabled INTEGER NOT NULL DEFAULT 1,
  c_is_deleted INTEGER NOT NULL DEFAULT 0,
  c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX IF NOT EXISTS uk_monitor_metric_rules_key ON t_monitor_metric_rules(c_space_id, c_rule_id, c_is_deleted);

CREATE TABLE IF NOT EXISTS t_monitor_metric_rule_states (
  c_id INTEGER PRIMARY KEY AUTOINCREMENT,
  c_space_id TEXT NOT NULL DEFAULT '',
  c_rule_id TEXT NOT NULL,
  c_status TEXT NOT NULL DEFAULT 'ok',
  c_trigger_count INTEGER NOT NULL DEFAULT 0,
  c_recovery_count INTEGER NOT NULL DEFAULT 0,
  c_owner_instance_id TEXT NOT NULL DEFAULT '',
  c_last_evaluated_at DATETIME,
  c_last_triggered_at DATETIME,
  c_last_recovered_at DATETIME,
  c_notification_event TEXT NOT NULL DEFAULT '',
  c_notification_key TEXT NOT NULL DEFAULT '',
  c_notification_status TEXT NOT NULL DEFAULT '',
  c_notification_error TEXT NOT NULL DEFAULT '',
  c_notification_attempts INTEGER NOT NULL DEFAULT 0,
  c_last_notification_at DATETIME,
  c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX IF NOT EXISTS uk_monitor_metric_rule_states_key ON t_monitor_metric_rule_states(c_space_id, c_rule_id);

CREATE TABLE IF NOT EXISTS t_monitor_metric_rule_evaluations (
  c_id INTEGER PRIMARY KEY AUTOINCREMENT,
  c_space_id TEXT NOT NULL DEFAULT '',
  c_rule_id TEXT NOT NULL,
  c_evaluated_at DATETIME NOT NULL,
  c_status TEXT NOT NULL,
  c_result_json TEXT NOT NULL DEFAULT '{}',
  c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_monitor_metric_rule_evaluations_recent ON t_monitor_metric_rule_evaluations(c_space_id, c_rule_id, c_evaluated_at DESC);

CREATE TABLE IF NOT EXISTS t_monitor_metric_rule_channels (
  c_id INTEGER PRIMARY KEY AUTOINCREMENT,
  c_space_id TEXT NOT NULL DEFAULT '',
  c_rule_id TEXT NOT NULL,
  c_webhook_id TEXT NOT NULL,
  c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX IF NOT EXISTS uk_monitor_metric_rule_channels_key ON t_monitor_metric_rule_channels(c_space_id, c_rule_id, c_webhook_id);

CREATE TRIGGER IF NOT EXISTS trg_monitor_metric_services_mtime AFTER UPDATE ON t_monitor_metric_services FOR EACH ROW WHEN NEW.c_mtime = OLD.c_mtime BEGIN UPDATE t_monitor_metric_services SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id; END;
CREATE TRIGGER IF NOT EXISTS trg_monitor_metric_series_mtime AFTER UPDATE ON t_monitor_metric_series FOR EACH ROW WHEN NEW.c_mtime = OLD.c_mtime BEGIN UPDATE t_monitor_metric_series SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id; END;
CREATE TRIGGER IF NOT EXISTS trg_monitor_metric_latest_mtime AFTER UPDATE ON t_monitor_metric_latest FOR EACH ROW WHEN NEW.c_mtime = OLD.c_mtime BEGIN UPDATE t_monitor_metric_latest SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id; END;
