package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPageNormalizeAllowsSyncPageSize(t *testing.T) {
	got := (Page{PageNo: 1, PageSize: 500}).Normalize()
	assert.Equal(t, 500, got.PageSize)
	assert.Equal(t, 1, got.PageNo)
	assert.Equal(t, 0, got.Offset())
}

func TestPageNormalize_InvalidValues_ShouldUseDefaults(t *testing.T) {
	got := (Page{PageNo: 0, PageSize: 0}).Normalize()
	assert.Equal(t, Page{PageNo: 1, PageSize: 20}, got)
	assert.Equal(t, 0, got.Offset())

	got = (Page{PageNo: -1, PageSize: 1001}).Normalize()
	assert.Equal(t, Page{PageNo: 1, PageSize: 20}, got)
}

func TestPageOffset_WithSecondPage_ShouldCalculateSQLOffset(t *testing.T) {
	assert.Equal(t, 40, (Page{PageNo: 3, PageSize: 20}).Offset())
}

func TestTableNames_ShouldMatchSchemaTables(t *testing.T) {
	assert.Equal(t, AccountTableName, (Account{}).TableName())
	assert.Equal(t, AccountBalanceTableName, (Balance{}).TableName())
	assert.Equal(t, AccountFundFlowTableName, (FundFlow{}).TableName())
	assert.Equal(t, AccountAPIKeyTableName, (APIKey{}).TableName())
	assert.Equal(t, TradeChannelTableName, (TradeChannel{}).TableName())
	assert.Equal(t, OrderTableName, (Order{}).TableName())
	assert.Equal(t, TradeTableName, (Trade{}).TableName())
	assert.Equal(t, PositionTableName, (Position{}).TableName())
	assert.Equal(t, OrderOperationTableName, (OrderOperation{}).TableName())
	assert.Equal(t, SyncCursorTableName, (SyncCursor{}).TableName())
}
