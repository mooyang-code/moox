package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/logicalaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

type LogicalAccountRecord struct {
	ControlMode      string
	SpaceID          string
	LogicalAccountID string
	Name             string
	// OwnerInstanceID and OwnerSessionID are the Strategy lifecycle identity.
	OwnerInstanceID string
	OwnerSessionID  string
	AuthFence       string
	OwnerGeneration int64
	ExecutionMode   string
	MarketType      string
	SettlementAsset string
	AutomationState string
	PauseReason     string
	CreatedAt       time.Time
	UpdatedAt       time.Time
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
	ControlMode      string    `gorm:"column:c_control_mode"`
	SpaceID          string    `gorm:"column:c_space_id"`
	LogicalAccountID string    `gorm:"column:c_logical_account_id"`
	Name             string    `gorm:"column:c_name"`
	OwnerClaimedAt   int64     `gorm:"column:c_owner_claimed_at"`
	OwnerInstanceID  *string   `gorm:"column:c_owner_instance_id"`
	OwnerSessionID   *string   `gorm:"column:c_owner_session_id"`
	AuthFence        string    `gorm:"column:c_auth_fence"`
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
	if record.ControlMode == "" {
		record.ControlMode = string(logicalaccount.ControlStrategy)
	}
	if record.ControlMode == string(logicalaccount.ControlManual) &&
		(record.OwnerInstanceID != "" || record.OwnerSessionID != "") {
		return fmt.Errorf("%w: manual account cannot have a strategy session", ErrInvalidRecord)
	}
	account := logicalAccountDomain(record)
	if err := account.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRecord, err)
	}
	ownerGeneration := record.OwnerGeneration
	instanceID := record.OwnerInstanceID
	var ownerInstance *string
	if !blank(instanceID) {
		ownerInstance = &instanceID
	}
	var ownerSession *string
	if !blank(record.OwnerSessionID) {
		session := record.OwnerSessionID
		ownerSession = &session
	}
	authFence := record.AuthFence
	if blank(authFence) {
		authFence = newAuthFence()
	}
	err := tx.db.Exec(`
		INSERT INTO t_logical_accounts (
			c_space_id, c_logical_account_id, c_name, c_owner_claimed_at,
			c_owner_instance_id, c_owner_session_id, c_auth_fence,
			c_control_mode, c_execution_mode, c_market_type, c_settlement_asset,
			c_automation_state, c_pause_reason
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		record.SpaceID, record.LogicalAccountID, record.Name, ownerGeneration,
		ownerInstance, ownerSession, authFence,
		record.ControlMode, record.ExecutionMode, record.MarketType, record.SettlementAsset,
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

// ClaimLogicalAccountSession atomically assigns a Strategy instance/session
// to a logical account. expectedFence is the caller's previously observed
// auth_fence and is mandatory for the V2 session path. Keeping it out of the
// public target contract makes delayed management calls harmless even when a
// new session has already claimed the account. The returned fence is the value
// to retain for the next CAS. changed is false for an identical session retry
// with the current fence, so callers retain its already-accepted target.
func (tx *Tx) ClaimLogicalAccountSession(
	spaceID, logicalAccountID, instanceID, sessionID, expectedFence string,
) (string, bool, error) {
	if blank(spaceID) || blank(logicalAccountID) || blank(instanceID) || blank(sessionID) || blank(expectedFence) {
		return "", false, fmt.Errorf("%w: session authorization identity and expected auth fence are required", ErrInvalidRecord)
	}
	account, err := tx.GetLogicalAccount(spaceID, logicalAccountID)
	if err != nil {
		return "", false, err
	}
	if account.ControlMode != "STRATEGY" {
		return "", false, fmt.Errorf("%w: logical account is not strategy controlled", ErrConflict)
	}
	if account.AuthFence != expectedFence {
		return "", false, fmt.Errorf("%w: session authorization fence changed", ErrConflict)
	}
	if account.OwnerInstanceID == instanceID && account.OwnerSessionID == sessionID {
		return account.AuthFence, false, nil
	}
	if account.OwnerInstanceID != "" || account.OwnerSessionID != "" {
		return "", false, fmt.Errorf("%w: logical account is owned by another session", ErrConflict)
	}
	fence := newAuthFence()
	result := tx.db.Exec(`
		UPDATE t_logical_accounts
		SET c_owner_instance_id = ?, c_owner_session_id = ?,
			c_owner_claimed_at = c_owner_claimed_at + 1, c_auth_fence = ?, c_mtime = CURRENT_TIMESTAMP
		WHERE c_space_id = ? AND c_logical_account_id = ?
		  AND COALESCE(c_owner_instance_id, '') = ''
		  AND c_control_mode = 'STRATEGY'
		  AND COALESCE(c_owner_session_id, '') = ''
		  AND c_auth_fence = ?
	`, instanceID, sessionID, fence, spaceID, logicalAccountID, account.AuthFence)
	if result.Error != nil {
		return "", false, writeError(result.Error)
	}
	if result.RowsAffected != 1 {
		return "", false, fmt.Errorf("%w: session authorization claim lost compare-and-set", ErrConflict)
	}
	return fence, true, nil
}

// RebindLogicalAccountSession switches an existing instance to a new session
// only when the complete old identity and fence still match. It is useful for
// explicit re-enable/rebind operations and intentionally does not revive an
// account that another instance has claimed in the meantime. changed is false
// when the requested new identity already owns the account with this fence.
func (tx *Tx) RebindLogicalAccountSession(
	spaceID, logicalAccountID, oldInstanceID, oldSessionID, expectedFence,
	newInstanceID, newSessionID string,
) (string, bool, error) {
	if blank(oldInstanceID) || blank(oldSessionID) || blank(expectedFence) || blank(newInstanceID) || blank(newSessionID) {
		return "", false, fmt.Errorf("%w: session authorization identity and expected auth fence are required", ErrInvalidRecord)
	}
	account, err := tx.GetLogicalAccount(spaceID, logicalAccountID)
	if err != nil {
		return "", false, err
	}
	if account.ControlMode != "STRATEGY" {
		return "", false, fmt.Errorf("%w: logical account is not strategy controlled", ErrConflict)
	}
	if account.AuthFence != expectedFence {
		return "", false, fmt.Errorf("%w: stale session authorization rebind", ErrConflict)
	}
	if account.OwnerInstanceID == newInstanceID && account.OwnerSessionID == newSessionID {
		return account.AuthFence, false, nil
	}
	if account.OwnerInstanceID != oldInstanceID || account.OwnerSessionID != oldSessionID ||
		account.AuthFence != expectedFence {
		return "", false, fmt.Errorf("%w: stale session authorization rebind", ErrConflict)
	}
	fence := newAuthFence()
	result := tx.db.Exec(`
		UPDATE t_logical_accounts
		SET c_owner_instance_id = ?, c_owner_session_id = ?, c_owner_claimed_at = c_owner_claimed_at + 1, c_auth_fence = ?,
			c_mtime = CURRENT_TIMESTAMP
		WHERE c_space_id = ? AND c_logical_account_id = ?
		  AND c_owner_instance_id = ? AND c_owner_session_id = ?
		  AND c_control_mode = 'STRATEGY'
		  AND c_auth_fence = ?
	`, newInstanceID, newSessionID, fence, spaceID, logicalAccountID,
		oldInstanceID, oldSessionID, account.AuthFence)
	if result.Error != nil {
		return "", false, writeError(result.Error)
	}
	if result.RowsAffected != 1 {
		return "", false, fmt.Errorf("%w: session authorization rebind lost compare-and-set", ErrConflict)
	}
	return fence, true, nil
}

// ReleaseLogicalAccountSession releases only the expected identity and fence. Releasing
// an already-idle account is idempotent; releasing an account owned by a new
// session is a conflict and never changes that new authorization.
func (tx *Tx) ReleaseLogicalAccountSession(
	spaceID, logicalAccountID, instanceID, sessionID, expectedFence string,
) error {
	if blank(instanceID) || blank(sessionID) || blank(expectedFence) {
		return fmt.Errorf("%w: session authorization identity and expected auth fence are required", ErrInvalidRecord)
	}
	account, err := tx.GetLogicalAccount(spaceID, logicalAccountID)
	if err != nil {
		return err
	}
	if account.AuthFence != expectedFence {
		return fmt.Errorf("%w: stale session authorization release", ErrConflict)
	}
	if account.OwnerInstanceID == "" && account.OwnerSessionID == "" {
		return nil
	}
	if account.OwnerInstanceID != instanceID || account.OwnerSessionID != sessionID ||
		account.AuthFence != expectedFence {
		return fmt.Errorf("%w: stale session authorization release", ErrConflict)
	}
	result := tx.db.Exec(`
		UPDATE t_logical_accounts
		SET c_owner_instance_id = NULL, c_owner_session_id = NULL,
			c_owner_claimed_at = c_owner_claimed_at + 1,
			c_auth_fence = ?, c_mtime = CURRENT_TIMESTAMP
		WHERE c_space_id = ? AND c_logical_account_id = ?
		  AND c_owner_instance_id = ? AND c_owner_session_id = ?
		  AND c_auth_fence = ?
	`, newAuthFence(), spaceID, logicalAccountID, instanceID, sessionID, account.AuthFence)
	if result.Error != nil {
		return writeError(result.Error)
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("%w: session authorization release lost compare-and-set", ErrConflict)
	}
	return nil
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
		  AND (? <> 'ACTIVE' OR c_control_mode = 'STRATEGY')
	`, state, reason, spaceID, logicalAccountID, state)
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
		SELECT DISTINCT COALESCE(NULLIF(a.c_live_environment, ''), 'PRODUCTION')
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
		physicalEnvironment := physical.Environment
		if physicalEnvironment == "" || physicalEnvironment == "PAPER" {
			physicalEnvironment = "PRODUCTION"
		}
		if environment != physicalEnvironment {
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
		ControlMode: logicalaccount.ControlMode(record.ControlMode),
		SpaceID:     record.SpaceID, ID: record.LogicalAccountID, Name: record.Name,
		ExecutionMode:   exchange.ExecutionMode(record.ExecutionMode),
		MarketType:      exchange.MarketType(record.MarketType),
		SettlementAsset: record.SettlementAsset,
		AutomationState: logicalaccount.AutomationState(record.AutomationState),
		PauseReason:     record.PauseReason,
	}
}

func (tx *Tx) requireStrategyControl(spaceID, logicalAccountID string) error {
	account, err := tx.GetLogicalAccount(spaceID, logicalAccountID)
	if err != nil {
		return err
	}
	if account.ControlMode != string(logicalaccount.ControlStrategy) {
		return fmt.Errorf("%w: logical account is not strategy controlled", ErrConflict)
	}
	return nil
}

func logicalAccountRecord(row logicalAccountRow) LogicalAccountRecord {
	var instanceID, sessionID string
	if row.OwnerInstanceID != nil {
		instanceID = *row.OwnerInstanceID
	}
	if row.OwnerSessionID != nil {
		sessionID = *row.OwnerSessionID
	}
	return LogicalAccountRecord{
		ControlMode: row.ControlMode,
		SpaceID:     row.SpaceID, LogicalAccountID: row.LogicalAccountID,
		Name: row.Name, OwnerInstanceID: instanceID,
		OwnerSessionID: sessionID, AuthFence: row.AuthFence,
		OwnerGeneration: row.OwnerClaimedAt,
		ExecutionMode:   row.ExecutionMode, MarketType: row.MarketType,
		SettlementAsset: row.SettlementAsset,
		AutomationState: row.AutomationState, PauseReason: row.PauseReason,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func newAuthFence() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return hex.EncodeToString(raw[:])
	}
	// rand.Reader failures are exceptionally rare. A non-empty fallback keeps
	// the row usable while preserving the invariant that an idle account also
	// carries a fence value.
	return fmt.Sprintf("fence-%d", time.Now().UnixNano())
}

func logicalAccountMemberRecord(row logicalAccountMemberRow) LogicalAccountMemberRecord {
	return LogicalAccountMemberRecord{
		SpaceID: row.SpaceID, LogicalAccountID: row.LogicalAccountID,
		TradingAccountID: row.TradingAccountID,
		Enabled:          row.Enabled, Priority: row.Priority,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}
