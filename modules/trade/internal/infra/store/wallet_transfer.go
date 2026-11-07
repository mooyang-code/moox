package store

import (
	"context"
	"fmt"
)

// 钱包划转的方向：普通账户的 USDT 平时放在合约钱包，现货买入前划进现货钱包，买完把多余的归集回合约钱包。
const (
	TransferToSpot    = "TO_SPOT"
	TransferToFutures = "TO_FUTURES"
)

// 划转记录的状态。PENDING 在请求交易所之前写入；COMPLETED、FAILED 是终态。
const (
	TransferStatePending   = "PENDING"
	TransferStateCompleted = "COMPLETED"
	TransferStateFailed    = "FAILED"
)

// WalletTransferRecord 是 t_trade_wallet_transfers 的一行。
type WalletTransferRecord struct {
	SpaceID          string
	TransferID       string
	TradingAccountID string
	LogicalAccountID string
	// TargetID 是触发这次划转的目标；风控等没有目标的划转为空串。
	TargetID  string
	Direction string
	Asset     string
	Amount    string
	// ClientRef 是请求交易所之前先写入的本地引用号；币安的划转接口没有客户端流水号，
	// 超时后靠它对应的记录、请求时间和金额去对账交易所的划转记录。
	ClientRef          string
	ExchangeTransferID string
	State              string
	LastError          string
	RequestedAt        int64
	CompletedAt        int64
}

const insertWalletTransferSQL = `
	INSERT INTO t_trade_wallet_transfers (
		c_space_id, c_transfer_id, c_trading_account_id, c_logical_account_id, c_target_id,
		c_direction, c_asset, c_amount, c_client_ref, c_exchange_transfer_id,
		c_state, c_last_error, c_requested_at, c_completed_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`

// InsertWalletTransfer 写入一条划转记录；本地引用号重复返回 ErrConflict。
func (tx *Tx) InsertWalletTransfer(record WalletTransferRecord) error {
	if blank(record.SpaceID) || blank(record.TransferID) || blank(record.TradingAccountID) || blank(record.LogicalAccountID) ||
		blank(record.Asset) || blank(record.Amount) || blank(record.ClientRef) || record.RequestedAt <= 0 ||
		(record.Direction != TransferToSpot && record.Direction != TransferToFutures) {
		return fmt.Errorf("%w: incomplete wallet transfer", ErrInvalidRecord)
	}
	if record.State == "" {
		record.State = TransferStatePending
	}
	return writeError(tx.db.Exec(insertWalletTransferSQL,
		record.SpaceID, record.TransferID, record.TradingAccountID, record.LogicalAccountID, record.TargetID,
		record.Direction, record.Asset, record.Amount, record.ClientRef, record.ExchangeTransferID,
		record.State, record.LastError, record.RequestedAt, record.CompletedAt,
	).Error)
}

const updateWalletTransferSQL = `
	UPDATE t_trade_wallet_transfers
	SET c_exchange_transfer_id = ?, c_state = ?, c_last_error = ?, c_completed_at = ?, c_mtime = CURRENT_TIMESTAMP
	WHERE c_space_id = ? AND c_transfer_id = ? AND c_state = ?
`

// UpdateWalletTransfer 把一条 PENDING 的划转记录推进到新状态（写入交易所流水号、完成或失败）；
// 已经是终态的记录不再改动，更新不到返回 ErrInvalidRecord。
func (tx *Tx) UpdateWalletTransfer(spaceID, transferID, exchangeTransferID, state, lastError string, completedAt int64) error {
	if blank(spaceID) || blank(transferID) ||
		(state != TransferStatePending && state != TransferStateCompleted && state != TransferStateFailed) {
		return fmt.Errorf("%w: incomplete wallet transfer update", ErrInvalidRecord)
	}
	result := tx.db.Exec(updateWalletTransferSQL, exchangeTransferID, state, lastError, completedAt, spaceID, transferID, TransferStatePending)
	return requireUpdated(result.Error, result.RowsAffected, "wallet transfer")
}

type walletTransferRow struct {
	SpaceID            string `gorm:"column:c_space_id"`
	TransferID         string `gorm:"column:c_transfer_id"`
	TradingAccountID   string `gorm:"column:c_trading_account_id"`
	LogicalAccountID   string `gorm:"column:c_logical_account_id"`
	TargetID           string `gorm:"column:c_target_id"`
	Direction          string `gorm:"column:c_direction"`
	Asset              string `gorm:"column:c_asset"`
	Amount             string `gorm:"column:c_amount"`
	ClientRef          string `gorm:"column:c_client_ref"`
	ExchangeTransferID string `gorm:"column:c_exchange_transfer_id"`
	State              string `gorm:"column:c_state"`
	LastError          string `gorm:"column:c_last_error"`
	RequestedAt        int64  `gorm:"column:c_requested_at"`
	CompletedAt        int64  `gorm:"column:c_completed_at"`
}

func (r walletTransferRow) record() WalletTransferRecord {
	return WalletTransferRecord{
		SpaceID: r.SpaceID, TransferID: r.TransferID, TradingAccountID: r.TradingAccountID,
		LogicalAccountID: r.LogicalAccountID, TargetID: r.TargetID, Direction: r.Direction, Asset: r.Asset,
		Amount: r.Amount, ClientRef: r.ClientRef, ExchangeTransferID: r.ExchangeTransferID, State: r.State,
		LastError: r.LastError, RequestedAt: r.RequestedAt, CompletedAt: r.CompletedAt,
	}
}

const listWalletTransfersForTargetSQL = `
	SELECT c_space_id, c_transfer_id, c_trading_account_id, c_logical_account_id, c_target_id,
		c_direction, c_asset, c_amount, c_client_ref, c_exchange_transfer_id,
		c_state, c_last_error, c_requested_at, c_completed_at
	FROM t_trade_wallet_transfers
	WHERE c_space_id = ? AND c_logical_account_id = ? AND c_target_id = ?
		AND c_trading_account_id = ? AND c_direction = ?
	ORDER BY c_requested_at, c_transfer_id
`

// ListWalletTransfersForTarget 返回某个目标在某个执行账户上某个方向的全部划转记录，按请求时间从早到晚。
// 第 4、6 阶段靠它从当前状态推出该做什么，重启后接着走。
func (s *Store) ListWalletTransfersForTarget(ctx context.Context, spaceID, logicalAccountID, targetID, tradingAccountID, direction string) ([]WalletTransferRecord, error) {
	var rows []walletTransferRow
	if err := s.db.WithContext(ctx).Raw(listWalletTransfersForTargetSQL, spaceID, logicalAccountID, targetID, tradingAccountID, direction).Scan(&rows).Error; err != nil {
		return nil, err
	}
	records := make([]WalletTransferRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, row.record())
	}
	return records, nil
}

const listWalletTransfersByAccountSQL = `
	SELECT c_space_id, c_transfer_id, c_trading_account_id, c_logical_account_id, c_target_id,
		c_direction, c_asset, c_amount, c_client_ref, c_exchange_transfer_id,
		c_state, c_last_error, c_requested_at, c_completed_at
	FROM t_trade_wallet_transfers
	WHERE c_space_id = ? AND c_trading_account_id = ? AND c_requested_at >= ? AND c_requested_at <= ?
	ORDER BY c_requested_at DESC, c_transfer_id DESC
`

// ListWalletTransfersByAccount 返回一个执行账户在 [from, to]（毫秒）内的划转记录，按请求时间从晚到早；资金流水用它合并划转。
func (s *Store) ListWalletTransfersByAccount(ctx context.Context, spaceID, tradingAccountID string, from, to int64) ([]WalletTransferRecord, error) {
	var rows []walletTransferRow
	if err := s.db.WithContext(ctx).Raw(listWalletTransfersByAccountSQL, spaceID, tradingAccountID, from, to).Scan(&rows).Error; err != nil {
		return nil, err
	}
	records := make([]WalletTransferRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, row.record())
	}
	return records, nil
}
