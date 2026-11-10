// Package storetest 提供只给测试用的账户状态搭建函数。
package storetest

import "github.com/mooyang-code/moox/modules/trade/internal/infra/store"

// AccountState 是测试里要搭出来的账户同步状态。
type AccountState struct {
	Ready              bool
	LeverageSettings   store.LeverageSettings
	Snapshot           store.TradingAccountSnapshot
	SnapshotSourceTime int64
	LastSyncAt         int64
	LastError          string
}

// SeedAccount 用生产写入函数把账户置成给定状态：杠杆、快照与同步时间、就绪标记与错误。
// 就绪时 LastReadyAt 取 LastSyncAt；未就绪时 LastReadyAt 保持原值。
func SeedAccount(tx *store.Tx, spaceID, accountID string, state AccountState) error {
	if err := tx.SetTradingAccountLeverage(spaceID, accountID, state.LeverageSettings); err != nil {
		return err
	}
	if err := tx.UpdateTradingAccountFacts(spaceID, accountID, nil, state.Snapshot, state.SnapshotSourceTime, state.LastSyncAt); err != nil {
		return err
	}
	return tx.UpdateTradingAccountReadiness(spaceID, accountID, state.Ready, state.LastSyncAt, state.LastError)
}
