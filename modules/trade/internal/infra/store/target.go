package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"gorm.io/gorm"
)

type InstrumentTarget struct {
	InstrumentID string `json:"instrument_id"`
	// Quantity 在 Hold 为 true 时为空：保持标的不换算数量。
	Quantity string `json:"quantity,omitempty"`
	// Hold 为 true 表示保持该标的当前持仓数量不动（标的所在市场本期不可用或只缺上一根，策略暂时无法决策），执行器对它既不加仓也不减仓。
	Hold bool `json:"hold,omitempty"`
}

type BlockedTarget struct {
	InstrumentID string `json:"instrument_id"`
	Quantity     string `json:"quantity"`
	Reason       string `json:"reason"`
	// Deferred 为真表示这一项不是被拦下，而是被单币每小时上限顺延到下一个整点。
	Deferred bool `json:"deferred,omitempty"`
}

type LogicalAccountTargetRecord struct {
	SpaceID          string
	LogicalAccountID string
	TargetID         string
	RunnerID         string
	CommandSequence  uint64
	// Strategy target identity; RunnerID mirrors InstanceID and CommandSequence
	// mirrors BarEndTime.
	InstanceID string
	SessionID  string
	// SessionStartedAt 是目标所属会话的开始时刻（毫秒），比较会话先后用。
	SessionStartedAt int64
	StrategyID       string
	BarEndTime       int64
	ValidUntil       int64
	Targets          []InstrumentTarget
	Status           string
	BlockedTargets   []BlockedTarget
	LastError        string
	AcceptedAt       int64
	UpdatedAt        time.Time
}

var (
	// ErrTargetExpired is returned before any quote/equity or receipt fast path
	// can accept a target whose validity window has elapsed.
	ErrTargetExpired = errors.New("trade store: target expired")
	// ErrTargetAuthorization 是组合不是策略控制的（人工控制），不接受策略目标。
	ErrTargetAuthorization = errors.New("trade store: target authorization mismatch")
	// ErrTargetSuperseded 是目标所属的会话已关闭（开始时刻不大于组合的 c_closed_through），
	// 或组合上已是开始时刻更晚的会话的目标：旧会话的目标，按被取代结束。
	ErrTargetSuperseded = fmt.Errorf("%w: target session closed or superseded", ErrConflict)
	// ErrTargetSessionConflict 是目标与组合上当前目标的会话开始时刻相同、会话却不同：不应出现，按授权冲突告警。
	ErrTargetSessionConflict = fmt.Errorf("%w: target sessions share one start time", ErrConflict)
)

// TargetSession 是目标所属的策略会话：实例、会话与会话的开始时刻（毫秒）。
type TargetSession struct {
	InstanceID string
	SessionID  string
	StartedAt  int64
}

// CurrentTarget 是组合当前目标行的身份与有效期，不带目标内容：下单前的会话检查只读这几列。
type CurrentTarget struct {
	TargetID string
	TargetSession
	BarEndTime int64
	ValidUntil int64
}

// CheckClosedTargetSession 判断目标所属的会话是不是已经关闭：开始时刻不大于组合的 c_closed_through 时返回 ErrTargetSuperseded。
func CheckClosedTargetSession(account LogicalAccountRecord, startedAt int64) error {
	if startedAt <= account.ClosedThrough {
		return fmt.Errorf("%w: 会话开始于 %d，组合已关闭到 %d", ErrTargetSuperseded, startedAt, account.ClosedThrough)
	}
	return nil
}

// CompareTargetSession 按会话开始时刻比较新到的目标与组合上的当前目标（批 3 的 3.6 第 4 步）：
// 开始时刻更晚是新会话，放行（覆盖当前目标行）；相等且是同一个会话，放行，由调用方再按 bar_end_time 比较；
// 更早返回 ErrTargetSuperseded；相等但会话不同返回 ErrTargetSessionConflict。
func CompareTargetSession(current, incoming TargetSession) error {
	switch {
	case incoming.StartedAt > current.StartedAt:
		return nil
	case incoming.StartedAt < current.StartedAt:
		return fmt.Errorf("%w: 会话开始于 %d，组合上已是开始于 %d 的会话的目标", ErrTargetSuperseded, incoming.StartedAt, current.StartedAt)
	case incoming.InstanceID == current.InstanceID && incoming.SessionID == current.SessionID:
		return nil
	default:
		return fmt.Errorf("%w: 会话 %s/%s 与当前目标的会话 %s/%s 开始时刻都是 %d",
			ErrTargetSessionConflict, incoming.InstanceID, incoming.SessionID, current.InstanceID, current.SessionID, incoming.StartedAt)
	}
}

// checkTargetSession 是接收目标时的会话先后判断（3.6 第 3、4 步）：会话已关闭、或比组合上的当前目标更早，都不接受。
// 组合上没有目标行时只看是否已关闭。同一会话的 bar_end_time 先后不在这里比。
func (tx *Tx) checkTargetSession(account LogicalAccountRecord, incoming TargetSession) error {
	if err := CheckClosedTargetSession(account, incoming.StartedAt); err != nil {
		return err
	}
	current, err := tx.GetCurrentTarget(account.SpaceID, account.LogicalAccountID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return CompareTargetSession(current.TargetSession, incoming)
}

type logicalAccountTargetRow struct {
	SpaceID            string    `gorm:"column:c_space_id"`
	LogicalAccountID   string    `gorm:"column:c_logical_account_id"`
	TargetID           string    `gorm:"column:c_target_id"`
	RunnerID           string    `gorm:"column:c_runner_id"`
	CommandSequence    uint64    `gorm:"column:c_command_sequence"`
	InstanceID         string    `gorm:"column:c_instance_id"`
	SessionID          string    `gorm:"column:c_session_id"`
	SessionStartedAt   int64     `gorm:"column:c_session_started_at"`
	StrategyID         string    `gorm:"column:c_strategy_id"`
	BarEndTime         int64     `gorm:"column:c_bar_end_time"`
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
	if account.ControlMode != "STRATEGY" {
		return LogicalAccountTargetRecord{}, false, ErrTargetAuthorization
	}
	if blank(record.InstanceID) || blank(record.SessionID) || blank(record.StrategyID) || record.SessionStartedAt <= 0 ||
		record.BarEndTime <= 0 || record.ValidUntil <= record.BarEndTime {
		return LogicalAccountTargetRecord{}, false, fmt.Errorf("%w: incomplete target session contract", ErrInvalidRecord)
	}
	if record.CommandSequence == 0 {
		record.CommandSequence = uint64(record.BarEndTime)
	}
	if record.RunnerID == "" {
		record.RunnerID = record.InstanceID
	}
	// 会话先后（含已关闭）在过期之前判断，与接收入口的顺序一致。
	if err := tx.checkTargetSession(account, record.session()); err != nil {
		return LogicalAccountTargetRecord{}, false, err
	}
	if time.Now().UTC().UnixMilli() >= record.ValidUntil {
		return LogicalAccountTargetRecord{}, false, fmt.Errorf("%w: target validity window", ErrTargetExpired)
	}
	tradable, marketsErr := tx.LogicalAccountMarkets(record.SpaceID, record.LogicalAccountID)
	if marketsErr != nil {
		return LogicalAccountTargetRecord{}, false, marketsErr
	}
	tradableMarkets := make([]exchange.MarketType, len(tradable))
	for i, market := range tradable {
		tradableMarkets[i] = exchange.MarketType(market)
	}
	for _, target := range record.Targets {
		targetMarket := exchange.ResolveInstrumentMarket(target.InstrumentID, tradableMarkets)
		if targetMarket == "" {
			return LogicalAccountTargetRecord{}, false, fmt.Errorf("%w: 标的 %s 缺少市场后缀 -SPOT 或 -SWAP", ErrInvalidRecord, target.InstrumentID)
		}
		if target.Hold || exchange.RuleFor(targetMarket).CanShort {
			continue
		}
		quantity, parseErr := shared.ParseDecimal(target.Quantity)
		if parseErr != nil || quantity.Cmp(shared.Zero()) < 0 {
			return LogicalAccountTargetRecord{}, false, fmt.Errorf(
				"%w: %s 不能做空，目标数量不能为负",
				ErrInvalidRecord,
				target.InstrumentID,
			)
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
		sameSession := current.InstanceID == record.InstanceID && current.SessionID == record.SessionID
		same := current.TargetID == record.TargetID &&
			current.RunnerID == record.RunnerID && sameSession &&
			current.SessionStartedAt == record.SessionStartedAt && current.BarEndTime == record.BarEndTime &&
			row.TargetsJSON == targetsJSON
		if same {
			return current, false, nil
		}
		// 走到这里时会话先后已经判断过：要么是开始时刻更晚的新会话（直接覆盖），要么是同一会话（只接受更晚的一根）。
		if record.TargetID == current.TargetID || (sameSession && record.BarEndTime <= current.BarEndTime) {
			return current, false, fmt.Errorf("%w: stale or conflicting logical account target", ErrConflict)
		}
	case query.Error != nil && !errors.Is(query.Error, gorm.ErrRecordNotFound):
		return LogicalAccountTargetRecord{}, false, query.Error
	}

	result := tx.db.Exec(execInsertLogicalAccountTarget,
		record.SpaceID, record.LogicalAccountID, record.TargetID, record.RunnerID,
		record.CommandSequence, record.InstanceID, record.SessionID, record.SessionStartedAt, record.StrategyID,
		record.BarEndTime, record.ValidUntil,
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

// session 返回目标所属的会话。
func (r LogicalAccountTargetRecord) session() TargetSession {
	return TargetSession{InstanceID: r.InstanceID, SessionID: r.SessionID, StartedAt: r.SessionStartedAt}
}

// currentTargetRow 是 CurrentTarget 的查询结果。
type currentTargetRow struct {
	TargetID         string `gorm:"column:c_target_id"`
	InstanceID       string `gorm:"column:c_instance_id"`
	SessionID        string `gorm:"column:c_session_id"`
	SessionStartedAt int64  `gorm:"column:c_session_started_at"`
	BarEndTime       int64  `gorm:"column:c_bar_end_time"`
	ValidUntil       int64  `gorm:"column:c_valid_until"`
}

func (r currentTargetRow) current() CurrentTarget {
	return CurrentTarget{
		TargetID:      r.TargetID,
		TargetSession: TargetSession{InstanceID: r.InstanceID, SessionID: r.SessionID, StartedAt: r.SessionStartedAt},
		BarEndTime:    r.BarEndTime, ValidUntil: r.ValidUntil,
	}
}

// GetCurrentTarget 读组合当前目标行的身份与有效期（不读目标内容）；没有目标行时返回 gorm.ErrRecordNotFound。
func (s *Store) GetCurrentTarget(ctx context.Context, spaceID, logicalAccountID string) (CurrentTarget, error) {
	var rows []currentTargetRow
	if err := s.db.WithContext(ctx).Raw(queryCurrentTarget, spaceID, logicalAccountID).Scan(&rows).Error; err != nil {
		return CurrentTarget{}, err
	}
	if len(rows) == 0 {
		return CurrentTarget{}, gorm.ErrRecordNotFound
	}
	return rows[0].current(), nil
}

// GetCurrentTarget 是事务内的 Store.GetCurrentTarget。
func (tx *Tx) GetCurrentTarget(spaceID, logicalAccountID string) (CurrentTarget, error) {
	var rows []currentTargetRow
	if err := tx.db.Raw(queryCurrentTarget, spaceID, logicalAccountID).Scan(&rows).Error; err != nil {
		return CurrentTarget{}, err
	}
	if len(rows) == 0 {
		return CurrentTarget{}, gorm.ErrRecordNotFound
	}
	return rows[0].current(), nil
}

// CloseTargetSession 关闭到 startedAt 为止的会话（批 3 的 3.6）：组合的 c_closed_through 取它与 startedAt 中较大的一个，
// 不会回退；当前目标行属于开始时刻不大于它的会话、并且到 now 还没过期时，把有效期改成 now 与 bar_end_time 加 1 毫秒中较大的一个
// （只会缩短，目标行不删，之后由执行器的过期路径撤单并置为过期）。幂等。返回关闭之后的 c_closed_through，
// 以及目标行的有效期是否被改。调用方持有组合锁。
func (tx *Tx) CloseTargetSession(spaceID, logicalAccountID string, startedAt, now int64) (int64, bool, error) {
	if blank(spaceID) || blank(logicalAccountID) || startedAt <= 0 || now <= 0 {
		return 0, false, fmt.Errorf("%w: incomplete target session close", ErrInvalidRecord)
	}
	account, err := tx.GetLogicalAccount(spaceID, logicalAccountID)
	if err != nil {
		return 0, false, err
	}
	closed := max(account.ClosedThrough, startedAt)
	if closed > account.ClosedThrough {
		if err := tx.db.Exec(execAdvanceClosedThrough, closed, spaceID, logicalAccountID, closed).Error; err != nil {
			return 0, false, writeError(err)
		}
	}
	result := tx.db.Exec(execShortenClosedTargetValidity, now, spaceID, logicalAccountID, closed, now)
	if result.Error != nil {
		return 0, false, writeError(result.Error)
	}
	return closed, result.RowsAffected > 0, nil
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
	query := updateTargetBySequenceSQL
	args := []any{record.TargetID, record.CommandSequence}
	if record.InstanceID != "" || record.SessionID != "" {
		if record.InstanceID == "" || record.SessionID == "" || record.BarEndTime <= 0 || record.ValidUntil <= record.BarEndTime {
			return false, fmt.Errorf("%w: invalid logical account target session update", ErrInvalidRecord)
		}
		query = updateTargetBySessionSQL
		args = []any{record.TargetID, record.InstanceID, record.SessionID, record.BarEndTime}
	}
	args = append(args, record.SpaceID, record.LogicalAccountID)
	values := []any{record.Status, blockedJSON, record.LastError, record.SpaceID, record.LogicalAccountID}
	values = append(values, args[:len(args)-2]...)
	values = append(values, record.Status)
	result := s.db.WithContext(ctx).Exec(query, values...)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

func logicalAccountTargetRecord(
	row logicalAccountTargetRow,
) (LogicalAccountTargetRecord, error) {
	targets, err := decodeCurrentTargetsCached(row.SpaceID+"\x00"+row.LogicalAccountID, row.TargetsJSON)
	if err != nil {
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
		InstanceID: row.InstanceID, SessionID: row.SessionID, SessionStartedAt: row.SessionStartedAt, StrategyID: row.StrategyID,
		BarEndTime: row.BarEndTime, ValidUntil: row.ValidUntil,
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
		if target.Hold {
			// 保持标的不带数量。
			if !blank(target.Quantity) {
				return "", fmt.Errorf("%w: hold target must not carry a quantity", ErrInvalidRecord)
			}
			target.Quantity = ""
			continue
		}
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

// targetDecodeCache 按组合账户记住最近一次解码的目标：下单前每笔子单都要读一次目标，
// 约 500 个标的的校验（逐个解析数量再规范化）比查询本身贵得多。只在原文完全相同时复用，
// 每个组合账户只留一份，不会随目标数量增长。
var targetDecodeCache sync.Map

type decodedTargets struct {
	raw     string
	targets []InstrumentTarget
}

func decodeCurrentTargetsCached(key, raw string) ([]InstrumentTarget, error) {
	if value, ok := targetDecodeCache.Load(key); ok {
		if entry := value.(decodedTargets); entry.raw == raw {
			return append(make([]InstrumentTarget, 0, len(entry.targets)), entry.targets...), nil
		}
	}
	targets, err := decodeCurrentTargets(raw)
	if err != nil {
		return nil, err
	}
	targetDecodeCache.Store(key, decodedTargets{raw: raw, targets: append(make([]InstrumentTarget, 0, len(targets)), targets...)})
	return targets, nil
}

// Current executable targets are not receipts: unknown fields must never
// silently disappear when a worker reads them.
func decodeCurrentTargets(raw string) ([]InstrumentTarget, error) {
	d := json.NewDecoder(strings.NewReader(raw))
	tok, err := d.Token()
	if err != nil || tok != json.Delim('[') {
		return nil, fmt.Errorf("%w: target must be an array", ErrInvalidRecord)
	}
	targets := []InstrumentTarget{}
	for d.More() {
		tok, err = d.Token()
		if err != nil || tok != json.Delim('{') {
			return nil, fmt.Errorf("%w: target must be an object", ErrInvalidRecord)
		}
		fields := map[string]string{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return nil, err
			}
			name, ok := key.(string)
			if !ok {
				return nil, ErrInvalidRecord
			}
			if _, duplicate := fields[name]; duplicate {
				return nil, fmt.Errorf("%w: duplicate target field", ErrInvalidRecord)
			}
			switch name {
			case "instrument_id", "quantity", "hold":
			default:
				return nil, fmt.Errorf("%w: unknown target field %s", ErrInvalidRecord, name)
			}
			if name == "hold" {
				var hold bool
				if err := d.Decode(&hold); err != nil {
					return nil, fmt.Errorf("%w: invalid target field %s", ErrInvalidRecord, name)
				}
				fields[name] = strconv.FormatBool(hold)
				continue
			}
			var value string
			if err := d.Decode(&value); err != nil || blank(value) {
				return nil, fmt.Errorf("%w: invalid target field %s", ErrInvalidRecord, name)
			}
			fields[name] = value
		}
		if _, err := d.Token(); err != nil {
			return nil, err
		}
		targets = append(targets, InstrumentTarget{InstrumentID: fields["instrument_id"], Quantity: fields["quantity"], Hold: fields["hold"] == "true"})
	}
	if _, err := d.Token(); err != nil {
		return nil, err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, fmt.Errorf("%w: trailing target JSON", ErrInvalidRecord)
	}
	if _, err := encodeInstrumentTargets(targets); err != nil {
		return nil, err
	}
	return targets, nil
}

// 逻辑账户目标状态更新的两种定位方式：按指令序号，或按实例、会话与收盘时刻。
const (
	updateTargetBySequenceSQL = updateTargetPrefixSQL + `c_target_id = ? AND c_command_sequence = ?` + updateTargetSuffixSQL
	updateTargetBySessionSQL  = updateTargetPrefixSQL + `c_target_id = ? AND c_instance_id = ? AND c_session_id = ? AND c_bar_end_time = ?` + updateTargetSuffixSQL

	updateTargetPrefixSQL = `
	UPDATE t_logical_account_targets
	SET c_status = ?, c_blocked_targets_json = ?, c_last_error = ?,
		c_mtime = CURRENT_TIMESTAMP
	WHERE c_space_id = ? AND c_logical_account_id = ?
		AND `
	updateTargetSuffixSQL = `
		AND (c_status <> 'EXPIRED' OR ? = 'EXPIRED')
`
	// 覆盖当前目标行只有两种情况：开始时刻更晚的新会话，或同一会话更晚的一根（与 Go 侧的判断一致，这里再守一次）。
	execInsertLogicalAccountTarget = `INSERT INTO t_logical_account_targets (
			c_space_id, c_logical_account_id, c_target_id, c_runner_id,
			c_command_sequence, c_instance_id, c_session_id, c_session_started_at, c_strategy_id,
			c_bar_end_time, c_valid_until,
			c_targets_json, c_status,
			c_blocked_targets_json, c_last_error, c_accepted_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_space_id, c_logical_account_id) DO UPDATE SET
			c_target_id = excluded.c_target_id,
			c_runner_id = excluded.c_runner_id,
			c_command_sequence = excluded.c_command_sequence,
			c_instance_id = excluded.c_instance_id,
			c_session_id = excluded.c_session_id,
			c_session_started_at = excluded.c_session_started_at,
			c_strategy_id = excluded.c_strategy_id,
			c_bar_end_time = excluded.c_bar_end_time,
			c_valid_until = excluded.c_valid_until,
			c_targets_json = excluded.c_targets_json,
			c_status = excluded.c_status,
			c_blocked_targets_json = excluded.c_blocked_targets_json,
			c_last_error = excluded.c_last_error,
			c_accepted_at = excluded.c_accepted_at,
			c_mtime = CURRENT_TIMESTAMP
		WHERE excluded.c_session_started_at > t_logical_account_targets.c_session_started_at
		   OR (t_logical_account_targets.c_instance_id = excluded.c_instance_id
		       AND t_logical_account_targets.c_session_id = excluded.c_session_id
		       AND excluded.c_bar_end_time > t_logical_account_targets.c_bar_end_time)`
	queryCurrentTarget = `SELECT c_target_id, c_instance_id, c_session_id, c_session_started_at, c_bar_end_time, c_valid_until
		FROM t_logical_account_targets WHERE c_space_id = ? AND c_logical_account_id = ?`
	// 推进组合的“关闭到”时刻，只增不减。
	execAdvanceClosedThrough = `UPDATE t_logical_accounts
		SET c_closed_through = ?, c_mtime = CURRENT_TIMESTAMP
		WHERE c_space_id = ? AND c_logical_account_id = ? AND c_closed_through < ?`
	// 关闭会话时缩短目标行的有效期：只改属于已关闭会话、还没过期、确实会缩短的那一行。
	// 新的有效期取“现在”与 bar_end_time 加 1 毫秒中较大的一个，保持 c_valid_until 晚于 c_bar_end_time。
	execShortenClosedTargetValidity = `UPDATE t_logical_account_targets
		SET c_valid_until = MAX(?, c_bar_end_time + 1), c_mtime = CURRENT_TIMESTAMP
		WHERE c_space_id = ? AND c_logical_account_id = ?
			AND c_session_started_at <= ? AND c_status <> 'EXPIRED'
			AND c_valid_until > MAX(?, c_bar_end_time + 1)`
)
