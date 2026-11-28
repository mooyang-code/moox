// Package schema 嵌入 Archive 管理端与 worker 暂存库的 SQLite schema。
package schema

import _ "embed"

//go:embed archive.sql
var archiveSQL string

//go:embed staging.sql
var stagingSQL string

// AllSQL 返回 Archive 管理端的完整 schema。
func AllSQL() string {
	return archiveSQL
}

// StagingSQL 返回 Archive worker 暂存库的固定状态表 schema（task_state、partition_state）。
func StagingSQL() string {
	return stagingSQL
}
