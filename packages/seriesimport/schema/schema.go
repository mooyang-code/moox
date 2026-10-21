// Package schema 内嵌导入任务的 SQLite 表结构。
package schema

import _ "embed"

//go:embed importer.sql
var sql string

// AllSQL 返回建表语句。
func AllSQL() string { return sql }
