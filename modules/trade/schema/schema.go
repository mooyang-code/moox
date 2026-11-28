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

//go:embed paper.sql
var paperSQL string

//go:embed equity.sql
var equitySQL string

//go:embed target_history.sql
var targetHistorySQL string

//go:embed wallet_transfer.sql
var walletTransferSQL string

//go:embed delist.sql
var delistSQL string

// AllSQL returns the complete greenfield schema in foreign-key dependency order.
func AllSQL() string {
	return accountSQL + "\n" + instrumentSQL + "\n" + logicalAccountSQL + "\n" +
		paperSQL + "\n" + equitySQL + "\n" + targetHistorySQL + "\n" + executionSQL + "\n" +
		walletTransferSQL + "\n" + delistSQL
}
