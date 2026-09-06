package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"gorm.io/gorm"
)

type InstrumentTarget struct {
	InstrumentID     string `json:"instrument_id"`
	Quantity         string `json:"quantity"`
	TradingAccountID string `json:"trading_account_id,omitempty"`
	ExchangeSymbol   string `json:"exchange_symbol,omitempty"`
}

type BlockedTarget struct {
	InstrumentID string `json:"instrument_id"`
	Quantity     string `json:"quantity"`
	Reason       string `json:"reason"`
}

type LogicalAccountTargetRecord struct {
	SpaceID          string
	LogicalAccountID string
	TargetID         string
	RunnerID         string
	CommandSequence  uint64
	// Strategy target identity. The fields are authoritative when populated;
	// RunnerID/CommandSequence are retained for older console fixtures.
	InstanceID     string
	SessionID      string
	StrategyID     string
	BarEndTime     int64
	EffectiveAt    int64
	ValidUntil     int64
	Targets        []InstrumentTarget
	Status         string
	BlockedTargets []BlockedTarget
	LastError      string
	AcceptedAt     int64
	UpdatedAt      time.Time
}

var (
	// ErrTargetExpired is returned before any quote/equity or receipt fast path
	// can accept a target whose validity window has elapsed.
	ErrTargetExpired       = errors.New("trade store: target expired")
	ErrTargetAuthorization = errors.New("trade store: target authorization mismatch")
)

type logicalAccountTargetRow struct {
	SpaceID            string    `gorm:"column:c_space_id"`
	LogicalAccountID   string    `gorm:"column:c_logical_account_id"`
	TargetID           string    `gorm:"column:c_target_id"`
	RunnerID           string    `gorm:"column:c_runner_id"`
	CommandSequence    uint64    `gorm:"column:c_command_sequence"`
	InstanceID         string    `gorm:"column:c_instance_id"`
	SessionID          string    `gorm:"column:c_session_id"`
	StrategyID         string    `gorm:"column:c_strategy_id"`
	BarEndTime         int64     `gorm:"column:c_bar_end_time"`
	EffectiveAt        int64     `gorm:"column:c_effective_at"`
	ValidUntil         int64     `gorm:"column:c_valid_until"`
	TargetsJSON        string    `gorm:"column:c_targets_json"`
	Status             string    `gorm:"column:c_status"`
	BlockedTargetsJSON string    `gorm:"column:c_blocked_targets_json"`
	LastError          string    `gorm:"column:c_last_error"`
	AcceptedAt         int64     `gorm:"column:c_accepted_at"`
	UpdatedAt          time.Time `gorm:"column:c_mtime"`
}

func (logicalAccountTargetRow) TableName() string {
	return "t_logical_account_targets"
}

func (s *Store) AcceptLogicalAccountTarget(
	ctx context.Context,
	record LogicalAccountTargetRecord,
) (LogicalAccountTargetRecord, bool, error) {
	unlock := s.LockLogicalAccount(record.SpaceID, record.LogicalAccountID)
	defer unlock()
	var current LogicalAccountTargetRecord
	var accepted bool
	err := s.Transaction(ctx, func(tx *Tx) error {
		var err error
		current, accepted, err = tx.AcceptLogicalAccountTarget(record)
		return err
	})
	return current, accepted, err
}

func (tx *Tx) AcceptLogicalAccountTarget(
	record LogicalAccountTargetRecord,
) (LogicalAccountTargetRecord, bool, error) {
	targetsJSON, err := encodeInstrumentTargets(record.Targets)
	if err != nil {
		return LogicalAccountTargetRecord{}, false, err
	}
	blockedJSON, err := encodeBlockedTargets(record.BlockedTargets)
	if err != nil {
		return LogicalAccountTargetRecord{}, false, err
	}
	if blank(record.SpaceID) || blank(record.LogicalAccountID) ||
		blank(record.TargetID) || record.CommandSequence > math.MaxInt64 ||
		record.AcceptedAt <= 0 || !validLogicalAccountTargetStatus(record.Status) {
		return LogicalAccountTargetRecord{}, false,
			fmt.Errorf("%w: incomplete logical account target", ErrInvalidRecord)
	}
	account, err := tx.GetLogicalAccount(record.SpaceID, record.LogicalAccountID)
	if err != nil {
		return LogicalAccountTargetRecord{}, false, err
	}
	newIdentity := record.InstanceID != "" || record.SessionID != "" || record.BarEndTime != 0 || record.ValidUntil != 0
	if newIdentity {
		if blank(record.InstanceID) || blank(record.SessionID) || blank(record.StrategyID) ||
			record.BarEndTime <= 0 || record.EffectiveAt != record.BarEndTime || record.ValidUntil <= record.EffectiveAt {
			return LogicalAccountTargetRecord{}, false, fmt.Errorf("%w: incomplete target session contract", ErrInvalidRecord)
		}
		if record.CommandSequence == 0 {
			record.CommandSequence = uint64(record.BarEndTime)
		}
		if record.RunnerID == "" {
			record.RunnerID = record.InstanceID
		}
		if account.OwnerInstanceID != record.InstanceID || account.OwnerSessionID != record.SessionID {
			return LogicalAccountTargetRecord{}, false, fmt.Errorf("%w: logical account target session authorization", ErrTargetAuthorization)
		}
		now := time.Now().UTC().UnixMilli()
		if now < record.EffectiveAt || now >= record.ValidUntil {
			return LogicalAccountTargetRecord{}, false, fmt.Errorf("%w: target validity window", ErrTargetExpired)
		}
	} else {
		if blank(record.RunnerID) {
			return LogicalAccountTargetRecord{}, false, fmt.Errorf("%w: incomplete logical account target", ErrInvalidRecord)
		}
		if record.CommandSequence == 0 {
			return LogicalAccountTargetRecord{}, false, fmt.Errorf("%w: incomplete logical account target", ErrInvalidRecord)
		}
		if account.OwnerRunnerID != record.RunnerID {
			return LogicalAccountTargetRecord{}, false,
				fmt.Errorf("%w: logical account target runner ownership", ErrConflict)
		}
	}
	if account.MarketType == "SPOT" {
		for _, target := range record.Targets {
			quantity, parseErr := shared.ParseDecimal(target.Quantity)
			if parseErr != nil || quantity.Cmp(shared.Zero()) < 0 {
				return LogicalAccountTargetRecord{}, false, fmt.Errorf(
					"%w: SPOT target quantity cannot be negative",
					ErrInvalidRecord,
				)
			}
		}
	}

	var row logicalAccountTargetRow
	query := tx.db.
		Where("c_space_id = ? AND c_logical_account_id = ?",
			record.SpaceID, record.LogicalAccountID).
		Take(&row)
	switch {
	case query.Error == nil:
		current, decodeErr := logicalAccountTargetRecord(row)
		if decodeErr != nil {
			return LogicalAccountTargetRecord{}, false, decodeErr
		}
		same := current.TargetID == record.TargetID &&
			current.RunnerID == record.RunnerID &&
			((newIdentity && current.InstanceID == record.InstanceID && current.SessionID == record.SessionID && current.BarEndTime == record.BarEndTime) ||
				(!newIdentity && current.CommandSequence == record.CommandSequence)) &&
			row.TargetsJSON == targetsJSON
		if same {
			return current, false, nil
		}
		if record.TargetID == current.TargetID ||
			(newIdentity && current.InstanceID == record.InstanceID && current.SessionID == record.SessionID && record.BarEndTime <= current.BarEndTime) ||
			(!newIdentity && record.CommandSequence <= current.CommandSequence) {
			return current, false, fmt.Errorf("%w: stale or conflicting logical account target", ErrConflict)
		}
	case query.Error != nil && !errors.Is(query.Error, gorm.ErrRecordNotFound):
		return LogicalAccountTargetRecord{}, false, query.Error
	}

	result := tx.db.Exec(`
		INSERT INTO t_logical_account_targets (
			c_space_id, c_logical_account_id, c_target_id, c_runner_id,
			c_command_sequence, c_instance_id, c_session_id, c_strategy_id,
			c_bar_end_time, c_effective_at, c_valid_until,
			c_targets_json, c_status,
			c_blocked_targets_json, c_last_error, c_accepted_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_space_id, c_logical_account_id) DO UPDATE SET
			c_target_id = excluded.c_target_id,
			c_runner_id = excluded.c_runner_id,
			c_command_sequence = excluded.c_command_sequence,
			c_instance_id = excluded.c_instance_id,
			c_session_id = excluded.c_session_id,
			c_strategy_id = excluded.c_strategy_id,
			c_bar_end_time = excluded.c_bar_end_time,
			c_effective_at = excluded.c_effective_at,
			c_valid_until = excluded.c_valid_until,
			c_targets_json = excluded.c_targets_json,
			c_status = excluded.c_status,
			c_blocked_targets_json = excluded.c_blocked_targets_json,
			c_last_error = excluded.c_last_error,
			c_accepted_at = excluded.c_accepted_at,
			c_mtime = CURRENT_TIMESTAMP
		WHERE (excluded.c_instance_id <> '' AND
			excluded.c_bar_end_time > t_logical_account_targets.c_bar_end_time)
		   OR (excluded.c_instance_id = '' AND
			excluded.c_command_sequence > t_logical_account_targets.c_command_sequence)
	`,
		record.SpaceID, record.LogicalAccountID, record.TargetID, record.RunnerID,
		record.CommandSequence, record.InstanceID, record.SessionID, record.StrategyID,
		record.BarEndTime, record.EffectiveAt, record.ValidUntil,
		targetsJSON, record.Status,
		blockedJSON, record.LastError, record.AcceptedAt,
	)
	if result.Error != nil {
		return LogicalAccountTargetRecord{}, false, writeError(result.Error)
	}
	if result.RowsAffected != 1 {
		return LogicalAccountTargetRecord{}, false,
			fmt.Errorf("%w: stale logical account target", ErrConflict)
	}
	row = logicalAccountTargetRow{}
	if err := tx.db.
		Where("c_space_id = ? AND c_logical_account_id = ?",
			record.SpaceID, record.LogicalAccountID).
		Take(&row).Error; err != nil {
		return LogicalAccountTargetRecord{}, false, err
	}
	current, err := logicalAccountTargetRecord(row)
	return current, true, err
}

func (s *Store) GetLogicalAccountTarget(
	ctx context.Context,
	spaceID string,
	logicalAccountID string,
) (LogicalAccountTargetRecord, error) {
	var row logicalAccountTargetRow
	err := s.db.WithContext(ctx).
		Where("c_space_id = ? AND c_logical_account_id = ?", spaceID, logicalAccountID).
		Take(&row).Error
	if err != nil {
		return LogicalAccountTargetRecord{}, err
	}
	return logicalAccountTargetRecord(row)
}

func (tx *Tx) DeleteLogicalAccountTargetForOtherRunner(
	spaceID string,
	logicalAccountID string,
	runnerID string,
) error {
	return tx.db.Exec(`
		DELETE FROM t_logical_account_targets
		WHERE c_space_id = ? AND c_logical_account_id = ?
			AND c_runner_id <> ?
	`, spaceID, logicalAccountID, runnerID).Error
}

// DeleteLogicalAccountTarget removes the current target during an ownership
// lifecycle transition. Historical TargetReceipt rows remain immutable; only
// the live convergence target is cleared so a released runner cannot resume
// an obsolete strategy after the account is claimed again.
func (tx *Tx) DeleteLogicalAccountTarget(spaceID, logicalAccountID string) error {
	return tx.db.Exec(`
		DELETE FROM t_logical_account_targets
		WHERE c_space_id = ? AND c_logical_account_id = ?
	`, spaceID, logicalAccountID).Error
}

func (s *Store) ListLogicalAccountTargets(
	ctx context.Context,
	statuses ...string,
) ([]LogicalAccountTargetRecord, error) {
	query := s.db.WithContext(ctx).Table("t_logical_account_targets")
	if len(statuses) > 0 {
		query = query.Where("c_status IN ?", statuses)
	}
	var rows []logicalAccountTargetRow
	if err := query.
		Order("c_space_id, c_logical_account_id").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	records := make([]LogicalAccountTargetRecord, 0, len(rows))
	for _, row := range rows {
		record, err := logicalAccountTargetRecord(row)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

func (s *Store) UpdateLogicalAccountTargetState(
	ctx context.Context,
	record LogicalAccountTargetRecord,
) (bool, error) {
	if blank(record.SpaceID) || blank(record.LogicalAccountID) ||
		blank(record.TargetID) ||
		!validLogicalAccountTargetStatus(record.Status) {
		return false, fmt.Errorf("%w: invalid logical account target update", ErrInvalidRecord)
	}
	blockedJSON, err := encodeBlockedTargets(record.BlockedTargets)
	if err != nil {
		return false, err
	}
	where := `c_target_id = ? AND c_command_sequence = ?`
	args := []any{record.TargetID, record.CommandSequence}
	if record.InstanceID != "" || record.SessionID != "" {
		if record.InstanceID == "" || record.SessionID == "" || record.BarEndTime <= 0 || record.ValidUntil <= record.EffectiveAt || record.EffectiveAt != record.BarEndTime {
			return false, fmt.Errorf("%w: invalid logical account target session update", ErrInvalidRecord)
		}
		where = `c_target_id = ? AND c_instance_id = ? AND c_session_id = ? AND c_bar_end_time = ?`
		args = []any{record.TargetID, record.InstanceID, record.SessionID, record.BarEndTime}
	}
	args = append(args, record.SpaceID, record.LogicalAccountID)
	values := []any{record.Status, blockedJSON, record.LastError, record.SpaceID, record.LogicalAccountID}
	values = append(values, args[:len(args)-2]...)
	values = append(values, record.Status)
	result := s.db.WithContext(ctx).Exec(fmt.Sprintf(`
		UPDATE t_logical_account_targets
		SET c_status = ?, c_blocked_targets_json = ?, c_last_error = ?,
			c_mtime = CURRENT_TIMESTAMP
		WHERE c_space_id = ? AND c_logical_account_id = ?
			AND %s
			AND (c_status <> 'EXPIRED' OR ? = 'EXPIRED')
	`, where),
		values...,
	)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

func logicalAccountTargetRecord(
	row logicalAccountTargetRow,
) (LogicalAccountTargetRecord, error) {
	var targets []InstrumentTarget
	if err := json.Unmarshal([]byte(row.TargetsJSON), &targets); err != nil {
		return LogicalAccountTargetRecord{}, fmt.Errorf("%w: target JSON: %v", ErrInvalidRecord, err)
	}
	var blocked []BlockedTarget
	if err := json.Unmarshal([]byte(row.BlockedTargetsJSON), &blocked); err != nil {
		return LogicalAccountTargetRecord{}, fmt.Errorf("%w: blocked target JSON: %v", ErrInvalidRecord, err)
	}
	return LogicalAccountTargetRecord{
		SpaceID: row.SpaceID, LogicalAccountID: row.LogicalAccountID,
		TargetID: row.TargetID, RunnerID: row.RunnerID,
		CommandSequence: row.CommandSequence, Targets: targets,
		InstanceID: row.InstanceID, SessionID: row.SessionID, StrategyID: row.StrategyID,
		BarEndTime: row.BarEndTime, EffectiveAt: row.EffectiveAt, ValidUntil: row.ValidUntil,
		Status: row.Status, BlockedTargets: blocked,
		LastError: row.LastError, AcceptedAt: row.AcceptedAt,
		UpdatedAt: row.UpdatedAt,
	}, nil
}

func validLogicalAccountTargetStatus(status string) bool {
	switch status {
	case "PENDING", "CONVERGING", "CONVERGED", "BLOCKED", "EXPIRED":
		return true
	default:
		return false
	}
}

func encodeInstrumentTargets(targets []InstrumentTarget) (string, error) {
	canonical := make([]InstrumentTarget, len(targets))
	copy(canonical, targets)
	seen := make(map[string]struct{}, len(canonical))
	for i := range canonical {
		target := &canonical[i]
		if blank(target.InstrumentID) {
			return "", fmt.Errorf("%w: incomplete target identity", ErrInvalidRecord)
		}
		if _, duplicate := seen[target.InstrumentID]; duplicate {
			return "", fmt.Errorf("%w: duplicate target instrument %s",
				ErrInvalidRecord, target.InstrumentID)
		}
		seen[target.InstrumentID] = struct{}{}
		quantity, err := shared.ParseDecimal(target.Quantity)
		if err != nil {
			return "", fmt.Errorf("%w: target quantity", ErrInvalidRecord)
		}
		target.Quantity = quantity.String()
	}
	sort.Slice(canonical, func(i, j int) bool {
		return canonical[i].InstrumentID < canonical[j].InstrumentID
	})
	data, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("%w: encode targets: %v", ErrInvalidRecord, err)
	}
	return string(data), nil
}

func encodeBlockedTargets(targets []BlockedTarget) (string, error) {
	canonical := make([]BlockedTarget, len(targets))
	copy(canonical, targets)
	seen := make(map[string]struct{}, len(canonical))
	for i := range canonical {
		target := &canonical[i]
		if blank(target.InstrumentID) || blank(target.Reason) {
			return "", fmt.Errorf("%w: incomplete blocked target", ErrInvalidRecord)
		}
		if _, duplicate := seen[target.InstrumentID]; duplicate {
			return "", fmt.Errorf("%w: duplicate blocked target %s",
				ErrInvalidRecord, target.InstrumentID)
		}
		seen[target.InstrumentID] = struct{}{}
		quantity, err := shared.ParseDecimal(target.Quantity)
		if err != nil {
			return "", fmt.Errorf("%w: blocked target quantity", ErrInvalidRecord)
		}
		target.Quantity = quantity.String()
	}
	sort.Slice(canonical, func(i, j int) bool {
		return canonical[i].InstrumentID < canonical[j].InstrumentID
	})
	data, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("%w: encode blocked targets: %v", ErrInvalidRecord, err)
	}
	return string(data), nil
}
