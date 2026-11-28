// Package schema 内嵌 Storage 元数据的权威 SQLite schema。
package schema

import _ "embed"

//go:embed metadata.sql
var metadataSQL string

// AllSQL 返回内嵌的 Storage 元数据 SQLite schema。
func AllSQL() string {
	return metadataSQL
}
