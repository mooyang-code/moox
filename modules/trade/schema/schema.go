// Package schema embeds the Trade module SQLite schema for explicit initialization flows.
// Trade 模块统一承载账户域（account.sql）与交易域（order.sql）两套表。
package schema

import _ "embed"

// accountSQL 是 Trade 模块账户域的 SQLite schema。
//
//go:embed account.sql
var accountSQL string

// orderSQL 是 Trade 模块交易域的 SQLite schema。
//
//go:embed order.sql
var orderSQL string

// syncSQL 是 Trade 模块定时同步游标的 SQLite schema。
//
//go:embed sync.sql
var syncSQL string

// AccountSQL 返回账户域（账户/余额/流水/凭证）的 SQLite schema。
func AccountSQL() string {
	return accountSQL
}

// OrderSQL 返回交易域（通道/订单/成交/持仓/操作）的 SQLite schema。
func OrderSQL() string {
	return orderSQL
}

// AllSQL 返回 Trade 模块全部 schema（账户域 + 交易域）。
func AllSQL() string {
	return accountSQL + "\n" + orderSQL + "\n" + syncSQL
}
