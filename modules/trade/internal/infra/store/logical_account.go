package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/logicalaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"gorm.io/gorm"
)

type LogicalAccountRecord struct {
	ControlMode      string
	SpaceID          string
	LogicalAccountID string
	Name             string
	OwnerRunnerID    string
	// OwnerInstanceID and OwnerSessionID are the authoritative Strategy
	// lifecycle identity. OwnerRunnerID is retained for old RPC clients only.
	OwnerInstanceID string
	OwnerSessionID  string
	AuthFence       string
	OwnerGeneration int64
	// OwnerClaimedAt is retained as a source-compatibility alias for older
	// callers. It now carries the monotonic lifecycle generation, not time.
	OwnerClaimedAt  int64
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
	OwnerRunnerID    *string   `gorm:"column:c_owner_runner_id"`
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
	var owner *string
	if !blank(record.OwnerRunnerID) {
		value := record.OwnerRunnerID
		owner = &value
	}
	ownerGeneration := record.OwnerGeneration
	if ownerGeneration == 0 {
		ownerGeneration = record.OwnerClaimedAt
	}
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
			c_space_id, c_logical_account_id, c_name, c_owner_runner_id, c_owner_claimed_at,
			c_owner_instance_id, c_owner_session_id, c_auth_fence,
			c_control_mode, c_execution_mode, c_market_type, c_settlement_asset,
			c_automation_state, c_pause_reason
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		record.SpaceID, record.LogicalAccountID, record.Name, owner, ownerGeneration,
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

// HasLogicalAccountOwnerRebind reports whether a durable rebind key has
// already been applied. It lets retry callers return without touching a newer
// target that may have been accepted after the first successful rebind.
func (s *Store) HasLogicalAccountOwnerRebind(
	ctx context.Context,
	spaceID string,
	logicalAccountID string,
	rebindKey string,
) (bool, error) {
	if s == nil || s.db == nil {
		return false, fmt.Errorf("trade database is not open")
	}
	var count int64
	if err := s.db.WithContext(ctx).Table("t_logical_account_owner_rebinds").
		Where("c_space_id = ? AND c_logical_account_id = ? AND c_rebind_key = ?", spaceID, logicalAccountID, rebindKey).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
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
	return tx.SetLogicalAccountOwnerGeneration(spaceID, logicalAccountID, runnerID)
}

// SetLogicalAccountOwnerAt is retained for source compatibility. Ownership
// fencing uses a monotonic generation; wall-clock timestamps are deliberately
// ignored so cross-process clock skew cannot reject valid targets.
func (tx *Tx) SetLogicalAccountOwnerAt(
	spaceID string,
	logicalAccountID string,
	runnerID string,
	claimedAt time.Time,
) error {
	return tx.SetLogicalAccountOwnerGeneration(spaceID, logicalAccountID, runnerID)
}

// SetLogicalAccountOwnerGeneration advances the logical-account lifecycle and
// associates the new generation with the current owner (or with no owner on
// release). The update is serialized by the caller's logical-account lock.
func (tx *Tx) SetLogicalAccountOwnerGeneration(
	spaceID string,
	logicalAccountID string,
	runnerID string,
) error {
	var owner any
	var instance any
	if !blank(runnerID) {
		owner = runnerID
		instance = runnerID
	}
	fence := newAuthFence()
	result := tx.db.Exec(`
		UPDATE t_logical_accounts
		SET c_owner_runner_id = ?, c_owner_instance_id = ?, c_owner_session_id = NULL,
			c_owner_claimed_at = c_owner_claimed_at + 1, c_auth_fence = ?, c_mtime = CURRENT_TIMESTAMP
		WHERE c_space_id = ? AND c_logical_account_id = ? AND c_control_mode = 'STRATEGY'
	`, owner, instance, fence, spaceID, logicalAccountID)
	return requireUpdated(result.Error, result.RowsAffected, "logical account owner")
}

// TryClaimLogicalAccountOwner performs the ownership transition as a
// database-level compare-and-set. The in-process mutex is only an optimization;
// this predicate is the fence that remains correct when two Trade processes
// share the same SQLite database or a replicated control plane.
func (tx *Tx) TryClaimLogicalAccountOwner(spaceID, logicalAccountID, runnerID string) error {
	if blank(runnerID) {
		return fmt.Errorf("%w: owner runner is required", ErrInvalidRecord)
	}
	result := tx.db.Exec(`
		UPDATE t_logical_accounts
		SET c_owner_runner_id = ?, c_owner_instance_id = ?, c_owner_session_id = NULL,
			c_owner_claimed_at = c_owner_claimed_at + 1, c_auth_fence = ?,
			c_mtime = CURRENT_TIMESTAMP
		WHERE c_space_id = ? AND c_logical_account_id = ?
		  AND (c_owner_runner_id IS NULL OR c_owner_runner_id = '')
		  AND c_control_mode = 'STRATEGY'
	`, runnerID, runnerID, newAuthFence(), spaceID, logicalAccountID)
	if result.Error != nil {
		return writeError(result.Error)
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("%w: logical account owner claim lost compare-and-set", ErrConflict)
	}
	return nil
}

// RebindLogicalAccountOwner starts a fresh lifecycle for the current owner
// without dropping ownership or changing the automation state. It is used
// when a V1 archived runner is replaced by a V2 runner with the same identity:
// delayed targets from the archived lifecycle must be fenced and the live
// target must be cleared before the new runner emits its first result.
func (tx *Tx) RebindLogicalAccountOwner(spaceID, logicalAccountID, runnerID, rebindKey string) (bool, error) {
	if err := tx.requireStrategyControl(spaceID, logicalAccountID); err != nil {
		return false, err
	}
	if blank(runnerID) || blank(rebindKey) {
		return false, fmt.Errorf("%w: owner runner and rebind key are required", ErrInvalidRecord)
	}
	var existing struct {
		RunnerID string `gorm:"column:c_runner_id"`
	}
	lookup := tx.db.Table("t_logical_account_owner_rebinds").
		Select("c_runner_id").
		Where("c_space_id = ? AND c_logical_account_id = ? AND c_rebind_key = ?", spaceID, logicalAccountID, rebindKey).
		Take(&existing)
	if lookup.Error == nil {
		if existing.RunnerID != runnerID {
			return false, fmt.Errorf("%w: rebind key belongs to another runner", ErrConflict)
		}
		return false, nil
	}
	if !errors.Is(lookup.Error, gorm.ErrRecordNotFound) {
		return false, writeError(lookup.Error)
	}
	insert := tx.db.Exec(`
		INSERT INTO t_logical_account_owner_rebinds (
			c_space_id, c_logical_account_id, c_rebind_key, c_runner_id
		) VALUES (?, ?, ?, ?)
		ON CONFLICT (c_space_id, c_logical_account_id, c_rebind_key) DO NOTHING
	`, spaceID, logicalAccountID, rebindKey, runnerID)
	if insert.Error != nil {
		return false, writeError(insert.Error)
	}
	if insert.RowsAffected == 0 {
		var conflicted struct {
			RunnerID string `gorm:"column:c_runner_id"`
		}
		if err := tx.db.Table("t_logical_account_owner_rebinds").
			Select("c_runner_id").
			Where("c_space_id = ? AND c_logical_account_id = ? AND c_rebind_key = ?", spaceID, logicalAccountID, rebindKey).
			Take(&conflicted).Error; err != nil {
			return false, writeError(err)
		}
		if conflicted.RunnerID != runnerID {
			return false, fmt.Errorf("%w: rebind key belongs to another runner", ErrConflict)
		}
		return false, nil
	}
	result := tx.db.Exec(`
		UPDATE t_logical_accounts
		SET c_owner_runner_id = ?, c_owner_instance_id = ?, c_owner_session_id = NULL,
			c_owner_claimed_at = c_owner_claimed_at + 1, c_auth_fence = ?,
			c_mtime = CURRENT_TIMESTAMP
		WHERE c_space_id = ? AND c_logical_account_id = ?
		  AND (c_owner_runner_id IS NULL OR c_owner_runner_id = ?)
		  AND c_control_mode = 'STRATEGY'
	`, runnerID, runnerID, newAuthFence(), spaceID, logicalAccountID, runnerID)
	if result.Error != nil {
		return false, writeError(result.Error)
	}
	if result.RowsAffected != 1 {
		return false, fmt.Errorf("%w: logical account owner rebind lost compare-and-set", ErrConflict)
	}
	return true, nil
}

// ReleaseLogicalAccountOwner clears ownership only when the expected runner
// still owns the account, preventing a delayed release from clobbering a new
// claimant in another process.
func (tx *Tx) ReleaseLogicalAccountOwner(spaceID, logicalAccountID, runnerID string) error {
	if blank(runnerID) {
		return fmt.Errorf("%w: owner runner is required", ErrInvalidRecord)
	}
	result := tx.db.Exec(`
		UPDATE t_logical_accounts
		SET c_owner_runner_id = NULL, c_owner_instance_id = NULL, c_owner_session_id = NULL,
			c_owner_claimed_at = c_owner_claimed_at + 1, c_auth_fence = ?,
			c_mtime = CURRENT_TIMESTAMP
		WHERE c_space_id = ? AND c_logical_account_id = ?
		  AND c_owner_runner_id = ?
	`, newAuthFence(), spaceID, logicalAccountID, runnerID)
	if result.Error != nil {
		return writeError(result.Error)
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("%w: logical account owner release lost compare-and-set", ErrConflict)
	}
	return nil
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
			c_auth_fence = ?, c_mtime = CURRENT_TIMESTAMP
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
		SET c_owner_instance_id = ?, c_owner_session_id = ?, c_auth_fence = ?,
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
		SET c_automation_state = ?, c_pause_reason = CASE
			WHEN c_pause_reason = ? AND ? = 'PAUSED' THEN c_pause_reason
			ELSE ? END,
			c_mtime = CURRENT_TIMESTAMP
		WHERE c_space_id = ? AND c_logical_account_id = ?
		  AND (? <> 'ACTIVE' OR c_control_mode = 'STRATEGY')
	`, state, TargetPinMigrationPauseReason, state, reason, spaceID, logicalAccountID, state)
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
		OwnerRunnerID:   record.OwnerRunnerID,
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
	var owner string
	if row.OwnerRunnerID != nil {
		owner = *row.OwnerRunnerID
	}
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
		Name: row.Name, OwnerRunnerID: owner, OwnerInstanceID: instanceID,
		OwnerSessionID: sessionID, AuthFence: row.AuthFence,
		OwnerGeneration: row.OwnerClaimedAt, OwnerClaimedAt: row.OwnerClaimedAt,
		ExecutionMode: row.ExecutionMode, MarketType: row.MarketType,
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
