// Package schema embeds the Trade module SQLite schema for explicit initialization flows.
package schema

import _ "embed"

// accountSQL 是 Trade 模块账户域的 SQLite schema。
//
//go:embed account.sql
var accountSQL string

// orderSQL 保留交易通道和操作审计表；订单、成交和持仓事实由 Kernel schema 承载。
//
//go:embed order.sql
var orderSQL string

// syncSQL 是 Trade 模块定时同步游标的 SQLite schema。
//
//go:embed sync.sql
var syncSQL string

//go:embed ledger.sql
var ledgerSQL string

//go:embed execution.sql
var executionSQL string

//go:embed bus.sql
var busSQL string

//go:embed rebalance.sql
var rebalanceSQL string

// AllSQL 返回 Trade 模块全部 schema（账户域 + 交易域）。
func AllSQL() string {
	return accountSQL + "\n" + orderSQL + "\n" + syncSQL + "\n" + ledgerSQL + "\n" + executionSQL + "\n" + busSQL + "\n" + rebalanceSQL
}
