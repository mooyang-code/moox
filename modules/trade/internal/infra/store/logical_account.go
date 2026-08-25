package store

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/logicalaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

type LogicalAccountRecord struct {
	SpaceID          string
	LogicalAccountID string
	Name             string
	OwnerRunnerID    string
	ExecutionMode    string
	MarketType       string
	SettlementAsset  string
	AutomationState  string
	PauseReason      string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type LogicalAccountMemberRecord struct {
	SpaceID          string
	LogicalAccountID string
	TradingAccountID string
	Enabled          bool
	Priority         int
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type logicalAccountRow struct {
	SpaceID          string    `gorm:"column:c_space_id"`
	LogicalAccountID string    `gorm:"column:c_logical_account_id"`
	Name             string    `gorm:"column:c_name"`
	OwnerRunnerID    *string   `gorm:"column:c_owner_runner_id"`
	ExecutionMode    string    `gorm:"column:c_execution_mode"`
	MarketType       string    `gorm:"column:c_market_type"`
	SettlementAsset  string    `gorm:"column:c_settlement_asset"`
	AutomationState  string    `gorm:"column:c_automation_state"`
	PauseReason      string    `gorm:"column:c_pause_reason"`
	CreatedAt        time.Time `gorm:"column:c_ctime"`
	UpdatedAt        time.Time `gorm:"column:c_mtime"`
}

func (logicalAccountRow) TableName() string {
	return "t_logical_accounts"
}

type logicalAccountMemberRow struct {
	SpaceID          string    `gorm:"column:c_space_id"`
	LogicalAccountID string    `gorm:"column:c_logical_account_id"`
	TradingAccountID string    `gorm:"column:c_trading_account_id"`
	Enabled          bool      `gorm:"column:c_enabled"`
	Priority         int       `gorm:"column:c_priority"`
	CreatedAt        time.Time `gorm:"column:c_ctime"`
	UpdatedAt        time.Time `gorm:"column:c_mtime"`
}

func (logicalAccountMemberRow) TableName() string {
	return "t_logical_account_members"
}

func (tx *Tx) CreateLogicalAccount(record LogicalAccountRecord) error {
	account := logicalAccountDomain(record)
	if err := account.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRecord, err)
	}
	var owner *string
	if !blank(record.OwnerRunnerID) {
		value := record.OwnerRunnerID
		owner = &value
	}
	err := tx.db.Exec(`
		INSERT INTO t_logical_accounts (
			c_space_id, c_logical_account_id, c_name, c_owner_runner_id,
			c_execution_mode, c_market_type, c_settlement_asset,
			c_automation_state, c_pause_reason
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		record.SpaceID, record.LogicalAccountID, record.Name, owner,
		record.ExecutionMode, record.MarketType, record.SettlementAsset,
		record.AutomationState, record.PauseReason,
	).Error
	return writeError(err)
}

func (s *Store) GetLogicalAccount(
	ctx context.Context,
	spaceID string,
	logicalAccountID string,
) (LogicalAccountRecord, error) {
	var row logicalAccountRow
	err := s.db.WithContext(ctx).
		Where("c_space_id = ? AND c_logical_account_id = ?", spaceID, logicalAccountID).
		Take(&row).Error
	if err != nil {
		return LogicalAccountRecord{}, err
	}
	return logicalAccountRecord(row), nil
}

func (tx *Tx) GetLogicalAccount(
	spaceID string,
	logicalAccountID string,
) (LogicalAccountRecord, error) {
	var row logicalAccountRow
	err := tx.db.
		Where("c_space_id = ? AND c_logical_account_id = ?", spaceID, logicalAccountID).
		Take(&row).Error
	if err != nil {
		return LogicalAccountRecord{}, err
	}
	return logicalAccountRecord(row), nil
}

func (s *Store) ListLogicalAccounts(
	ctx context.Context,
	spaceID string,
) ([]LogicalAccountRecord, error) {
	var rows []logicalAccountRow
	if err := s.db.WithContext(ctx).
		Where("c_space_id = ?", spaceID).
		Order("c_name, c_logical_account_id").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	records := make([]LogicalAccountRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, logicalAccountRecord(row))
	}
	return records, nil
}

func (tx *Tx) SetLogicalAccountOwner(
	spaceID string,
	logicalAccountID string,
	runnerID string,
) error {
	var owner any
	if !blank(runnerID) {
		owner = runnerID
	}
	result := tx.db.Exec(`
		UPDATE t_logical_accounts
		SET c_owner_runner_id = ?, c_mtime = CURRENT_TIMESTAMP
		WHERE c_space_id = ? AND c_logical_account_id = ?
	`, owner, spaceID, logicalAccountID)
	return requireUpdated(result.Error, result.RowsAffected, "logical account owner")
}

func (tx *Tx) SetLogicalAccountName(
	spaceID string,
	logicalAccountID string,
	name string,
) error {
	if blank(name) {
		return fmt.Errorf("%w: logical account name is required", ErrInvalidRecord)
	}
	result := tx.db.Exec(`
		UPDATE t_logical_accounts
		SET c_name = ?, c_mtime = CURRENT_TIMESTAMP
		WHERE c_space_id = ? AND c_logical_account_id = ?
	`, name, spaceID, logicalAccountID)
	return requireUpdated(result.Error, result.RowsAffected, "logical account name")
}

func (tx *Tx) SetLogicalAccountAutomation(
	spaceID string,
	logicalAccountID string,
	state string,
	reason string,
) error {
	if state != string(logicalaccount.AutomationActive) &&
		state != string(logicalaccount.AutomationPaused) {
		return fmt.Errorf("%w: unsupported automation state", ErrInvalidRecord)
	}
	if (state == string(logicalaccount.AutomationActive) && !blank(reason)) ||
		(state == string(logicalaccount.AutomationPaused) && blank(reason)) {
		return fmt.Errorf("%w: automation state and reason disagree", ErrInvalidRecord)
	}
	result := tx.db.Exec(`
		UPDATE t_logical_accounts
		SET c_automation_state = ?, c_pause_reason = ?,
			c_mtime = CURRENT_TIMESTAMP
		WHERE c_space_id = ? AND c_logical_account_id = ?
	`, state, reason, spaceID, logicalAccountID)
	return requireUpdated(result.Error, result.RowsAffected, "logical account automation")
}

func (tx *Tx) PutLogicalAccountMember(record LogicalAccountMemberRecord) error {
	member := logicalaccount.Member{
		SpaceID: record.SpaceID, LogicalAccountID: record.LogicalAccountID,
		TradingAccountID: record.TradingAccountID,
		Enabled:          record.Enabled, Priority: record.Priority,
	}
	if err := member.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRecord, err)
	}
	account, err := tx.GetLogicalAccount(record.SpaceID, record.LogicalAccountID)
	if err != nil {
		return err
	}
	if account.AutomationState != string(logicalaccount.AutomationPaused) {
		return fmt.Errorf("%w: %v", ErrInvalidRecord, logicalaccount.ErrMembershipChange)
	}
	physical, err := tx.GetTradingAccount(record.SpaceID, record.TradingAccountID)
	if err != nil {
		return err
	}
	if physical.ExecutionMode != account.ExecutionMode ||
		physical.MarketType != account.MarketType ||
		physical.SettlementAsset != account.SettlementAsset {
		return fmt.Errorf("%w: %v", ErrInvalidRecord, logicalaccount.ErrInhomogeneous)
	}
	var environments []string
	if err := tx.db.Raw(`
		SELECT DISTINCT a.c_environment
		FROM t_logical_account_members AS m
		JOIN t_trading_accounts AS a
			ON a.c_space_id = m.c_space_id
			AND a.c_trading_account_id = m.c_trading_account_id
		WHERE m.c_space_id = ? AND m.c_logical_account_id = ?
			AND m.c_trading_account_id <> ?
	`,
		record.SpaceID, record.LogicalAccountID, record.TradingAccountID,
	).Scan(&environments).Error; err != nil {
		return err
	}
	for _, environment := range environments {
		if environment != physical.Environment {
			return fmt.Errorf("%w: %v", ErrInvalidRecord, logicalaccount.ErrInhomogeneous)
		}
	}
	err = tx.db.Exec(`
		INSERT INTO t_logical_account_members (
			c_space_id, c_logical_account_id, c_trading_account_id,
			c_enabled, c_priority
		) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(c_space_id, c_logical_account_id, c_trading_account_id)
		DO UPDATE SET
			c_enabled = excluded.c_enabled,
			c_priority = excluded.c_priority,
			c_mtime = CURRENT_TIMESTAMP
	`,
		record.SpaceID, record.LogicalAccountID, record.TradingAccountID,
		record.Enabled, record.Priority,
	).Error
	return writeError(err)
}

func (s *Store) ListLogicalAccountMembers(
	ctx context.Context,
	spaceID string,
	logicalAccountID string,
	includeDisabled bool,
) ([]LogicalAccountMemberRecord, error) {
	query := s.db.WithContext(ctx).
		Where("c_space_id = ? AND c_logical_account_id = ?", spaceID, logicalAccountID)
	if !includeDisabled {
		query = query.Where("c_enabled = 1")
	}
	var rows []logicalAccountMemberRow
	if err := query.Order("c_priority, c_trading_account_id").Find(&rows).Error; err != nil {
		return nil, err
	}
	records := make([]LogicalAccountMemberRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, logicalAccountMemberRecord(row))
	}
	return records, nil
}

func (tx *Tx) DeleteLogicalAccountMember(
	spaceID string,
	logicalAccountID string,
	tradingAccountID string,
) error {
	account, err := tx.GetLogicalAccount(spaceID, logicalAccountID)
	if err != nil {
		return err
	}
	if account.AutomationState != string(logicalaccount.AutomationPaused) {
		return fmt.Errorf("%w: %v", ErrInvalidRecord, logicalaccount.ErrMembershipChange)
	}
	result := tx.db.Exec(`
		DELETE FROM t_logical_account_members
		WHERE c_space_id = ? AND c_logical_account_id = ?
			AND c_trading_account_id = ?
	`, spaceID, logicalAccountID, tradingAccountID)
	return requireUpdated(result.Error, result.RowsAffected, "logical account member")
}

func (s *Store) FindLogicalAccountByTradingAccount(
	ctx context.Context,
	spaceID string,
	tradingAccountID string,
) (LogicalAccountRecord, LogicalAccountMemberRecord, error) {
	var member logicalAccountMemberRow
	err := s.db.WithContext(ctx).
		Where("c_space_id = ? AND c_trading_account_id = ? AND c_enabled = 1",
			spaceID, tradingAccountID).
		Take(&member).Error
	if err != nil {
		return LogicalAccountRecord{}, LogicalAccountMemberRecord{}, err
	}
	account, err := s.GetLogicalAccount(ctx, spaceID, member.LogicalAccountID)
	return account, logicalAccountMemberRecord(member), err
}

func logicalAccountDomain(record LogicalAccountRecord) logicalaccount.Account {
	return logicalaccount.Account{
		SpaceID: record.SpaceID, ID: record.LogicalAccountID, Name: record.Name,
		OwnerRunnerID:   record.OwnerRunnerID,
		ExecutionMode:   exchange.ExecutionMode(record.ExecutionMode),
		MarketType:      exchange.MarketType(record.MarketType),
		SettlementAsset: record.SettlementAsset,
		AutomationState: logicalaccount.AutomationState(record.AutomationState),
		PauseReason:     record.PauseReason,
	}
}

func logicalAccountRecord(row logicalAccountRow) LogicalAccountRecord {
	var owner string
	if row.OwnerRunnerID != nil {
		owner = *row.OwnerRunnerID
	}
	return LogicalAccountRecord{
		SpaceID: row.SpaceID, LogicalAccountID: row.LogicalAccountID,
		Name: row.Name, OwnerRunnerID: owner,
		ExecutionMode: row.ExecutionMode, MarketType: row.MarketType,
		SettlementAsset: row.SettlementAsset,
		AutomationState: row.AutomationState, PauseReason: row.PauseReason,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func logicalAccountMemberRecord(row logicalAccountMemberRow) LogicalAccountMemberRecord {
	return LogicalAccountMemberRecord{
		SpaceID: row.SpaceID, LogicalAccountID: row.LogicalAccountID,
		TradingAccountID: row.TradingAccountID,
		Enabled:          row.Enabled, Priority: row.Priority,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}
