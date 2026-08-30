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

//go:embed paper_account_config.sql
var paperAccountConfigSQL string

//go:embed equity.sql
var equitySQL string

//go:embed target_receipt.sql
var targetReceiptSQL string

// AllSQL returns the complete greenfield schema in foreign-key dependency order.
func AllSQL() string {
	return accountSQL + "\n" + instrumentSQL + "\n" + logicalAccountSQL + "\n" +
		paperAccountConfigSQL + "\n" + equitySQL + "\n" + targetReceiptSQL + "\n" + executionSQL
}
