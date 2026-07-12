package schema

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAccountSQLAndOrderSQL_ShouldReturnEmbeddedDDL(t *testing.T) {
	account := AccountSQL()
	order := OrderSQL()
	assert.Contains(t, account, "CREATE TABLE")
	assert.Contains(t, order, "CREATE TABLE")
	assert.Contains(t, account, "t_accounts")
	assert.Contains(t, order, "t_orders")
}

func TestAllSQL_ContainsKernelTables(t *testing.T) {
	sql := AllSQL()
	for _, want := range []string{
		"t_trade_order_aggregates",
		"t_ledger_entries",
		"t_trade_outbox",
		"t_trade_inbox",
		"t_rebalance_runs",
	} {
		assert.Contains(t, sql, want)
	}
}
