PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS t_merge_arrivals (
    c_dataset_id TEXT NOT NULL,
    c_snapshot_id TEXT NOT NULL,
    c_subject_id TEXT NOT NULL,
    c_frequency TEXT NOT NULL,
    c_period_time DATETIME NOT NULL,
    c_series_tag TEXT NOT NULL,
    c_source_dataset_id TEXT NOT NULL,
    c_fields_json TEXT NOT NULL,
    c_complete INTEGER NOT NULL DEFAULT 0,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (c_dataset_id, c_snapshot_id, c_subject_id, c_frequency, c_period_time, c_series_tag, c_source_dataset_id)
);

CREATE TABLE IF NOT EXISTS t_merge_commits (
    c_commit_id TEXT NOT NULL PRIMARY KEY,
    c_dataset_id TEXT NOT NULL,
    c_snapshot_id TEXT NOT NULL,
    c_subject_id TEXT NOT NULL,
    c_frequency TEXT NOT NULL,
    c_period_time DATETIME NOT NULL,
    c_series_tag TEXT NOT NULL,
    c_status TEXT NOT NULL,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (c_dataset_id, c_snapshot_id, c_subject_id, c_frequency, c_period_time, c_series_tag),
    CHECK (c_status IN ('committed'))
);
