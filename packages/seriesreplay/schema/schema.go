// Package schema 内嵌回放服务的 SQLite 表结构。
package schema

import _ "embed"

//go:embed replay.sql
var sql string

// AllSQL 返回建表语句。
func AllSQL() string { return sql }
