// Package schema embeds the Control/Admin SQLite schema for explicit initialization flows.
package schema

import _ "embed"

// adminSQL 是 Control/Admin 的权威 SQLite schema.
//
//go:embed admin.sql
var adminSQL string

// AdminSQL 返回内嵌的 Control/Admin SQLite schema.
func AdminSQL() string {
	return adminSQL
}
