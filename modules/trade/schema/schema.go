// Package schema embeds the Trade execution SQLite schema.
package schema

import _ "embed"

//go:embed account.sql
var accountSQL string

//go:embed instrument.sql
var instrumentSQL string

//go:embed execution.sql
var executionSQL string

//go:embed logical_account.sql
var logicalAccountSQL string

// AllSQL returns the complete greenfield schema in foreign-key dependency order.
func AllSQL() string {
	return accountSQL + "\n" + instrumentSQL + "\n" + executionSQL + "\n" + logicalAccountSQL
}
