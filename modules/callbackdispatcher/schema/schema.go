// Package schema 内嵌事件中心的 SQLite schema。
package schema

import _ "embed"

//go:embed callbackdispatcher.sql
var callbackDispatcherSQL string

// AllSQL 返回事件中心的完整 schema。
func AllSQL() string {
	return callbackDispatcherSQL
}
