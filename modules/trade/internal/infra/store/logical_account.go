package store

import (
	"context"
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
	// ClosedThrough 是关闭到哪个会话为止（会话的开始时刻，毫秒）：开始时刻不大于它的会话的目标一律不再接受，0 表示没关闭过。
	ClosedThrough int64
	// Environment 是组合所属环境（PAPER / TESTNET / PRODUCTION），创建时定下。
	Environment     string
	SettlementAsset string
	// OrderSettingsJSON、FundSettingsJSON 是下单设置和资金设置，空对象表示全部取默认值。
	OrderSettingsJSON string
	FundSettingsJSON  string
	// PauseReason 只用于展示；程序按 ControlMode、PauseSource（暂停来源）与 MarginReduceState（保证金率风控的减仓状态）判断，未暂停与自主下单三者均为空。
	PauseReason       string
	PauseSource       PauseSource
	MarginReduceState MarginReduceState
	// AckedMarginActionID 是最近一次恢复自动执行时已人工确认的保证金风控减仓动作号。
	AckedMarginActionID string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// ExecutionMode 由三值环境推导出执行模式（PAPER 为模拟盘，TESTNET/PRODUCTION 为实盘）。
func (r LogicalAccountRecord) ExecutionMode() string {
	switch exchange.AccountEnvironment(r.Environment) {
	case exchange.AccountEnvironmentPaper:
		return string(exchange.ExecutionModePaper)
	case exchange.AccountEnvironmentTestnet, exchange.AccountEnvironmentProduction:
		return string(exchange.ExecutionModeLive)
	default:
		return ""
	}
}

// IsManual 报告组合是否是自主下单模式（没有策略自动执行，也没有暂停状态）。
func (r LogicalAccountRecord) IsManual() bool {
	return r.ControlMode == string(logicalaccount.ControlManual)
}

// IsPaused 报告策略组合当前是否处于暂停状态；自主下单组合恒为 false。
func (r LogicalAccountRecord) IsPaused() bool {
	return !r.IsManual() && r.PauseSource != ""
}

// IsActive 报告策略组合当前是否处于自动执行中；自主下单组合恒为 false。
func (r LogicalAccountRecord) IsActive() bool {
	return !r.IsManual() && r.PauseSource == ""
}

// AutomationState 推导协议展示用的自动执行状态：自主下单返回空串，策略组合按暂停来源返回 PAUSED 或 ACTIVE。
func (r LogicalAccountRecord) AutomationState() string {
	if r.IsManual() {
		return ""
	}
	if r.PauseSource != "" {
		return string(logicalaccount.AutomationPaused)
	}
	return string(logicalaccount.AutomationActive)
}

// CanEditMembers 报告当前是否允许增删成员：自主下单组合随时允许，策略组合须先暂停。
func (r LogicalAccountRecord) CanEditMembers() bool {
	return r.IsManual() || r.IsPaused()
}

// Pause 返回组合当前的暂停状态（未暂停或自主下单为零值）。
func (r LogicalAccountRecord) Pause() Pause {
	return Pause{Source: r.PauseSource, MarginReduce: r.MarginReduceState, Reason: r.PauseReason}
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
	ControlMode         string    `gorm:"column:c_control_mode"`
	SpaceID             string    `gorm:"column:c_space_id"`
	LogicalAccountID    string    `gorm:"column:c_logical_account_id"`
	Name                string    `gorm:"column:c_name"`
	ClosedThrough       int64     `gorm:"column:c_closed_through"`
	Environment         string    `gorm:"column:c_environment"`
	SettlementAsset     string    `gorm:"column:c_settlement_asset"`
	OrderSettingsJSON   string    `gorm:"column:c_order_settings_json"`
	FundSettingsJSON    string    `gorm:"column:c_fund_settings_json"`
	PauseReason         string    `gorm:"column:c_pause_reason"`
	PauseSource         string    `gorm:"column:c_pause_source"`
	MarginReduceState   string    `gorm:"column:c_margin_reduce_state"`
	AckedMarginActionID string    `gorm:"column:c_acked_margin_action_id"`
	CreatedAt           time.Time `gorm:"column:c_ctime"`
	UpdatedAt           time.Time `gorm:"column:c_mtime"`
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
	if !record.IsManual() && record.PauseReason != "" && record.PauseSource == "" {
		record.PauseSource = PauseSourceUser
	}
	if err := logicalaccount.ValidateConfig(
		record.SpaceID, record.LogicalAccountID, record.Name,
		exchange.AccountEnvironment(record.Environment), record.SettlementAsset,
		logicalaccount.ControlMode(record.ControlMode),
		string(record.PauseSource), record.PauseReason, string(record.MarginReduceState),
	); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRecord, err)
	}
	if blank(record.OrderSettingsJSON) {
		record.OrderSettingsJSON = "{}"
	}
	if blank(record.FundSettingsJSON) {
		record.FundSettingsJSON = "{}"
	}
	err := tx.db.Exec(execInsertLogicalAccount,
		record.SpaceID, record.LogicalAccountID, record.Name,
		record.ControlMode, record.Environment, record.SettlementAsset,
		record.OrderSettingsJSON, record.FundSettingsJSON,
		record.PauseReason, string(record.PauseSource), string(record.MarginReduceState),
	).Error
	return nameWriteError(err, uniqueLogicalAccountName)
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

func (tx *Tx) SetLogicalAccountName(
	spaceID string,
	logicalAccountID string,
	name string,
) error {
	if blank(name) {
		return fmt.Errorf("%w: logical account name is required", ErrInvalidRecord)
	}
	result := tx.db.Exec(execRenameLogicalAccount, name, spaceID, logicalAccountID)
	if result.Error != nil {
		return nameWriteError(result.Error, uniqueLogicalAccountName)
	}
	return requireUpdated(result.Error, result.RowsAffected, "logical account name")
}

const (
	updateLogicalAccountOrderSettingsSQL = `UPDATE t_logical_accounts SET c_order_settings_json = ?, c_mtime = CURRENT_TIMESTAMP WHERE c_space_id = ? AND c_logical_account_id = ?`
	updateLogicalAccountFundSettingsSQL  = `UPDATE t_logical_accounts SET c_fund_settings_json = ?, c_mtime = CURRENT_TIMESTAMP WHERE c_space_id = ? AND c_logical_account_id = ?`
)

// SetLogicalAccountOrderSettings 写入下单设置（调用方已校验并转成规范 JSON）。
func (tx *Tx) SetLogicalAccountOrderSettings(spaceID, logicalAccountID, settingsJSON string) error {
	result := tx.db.Exec(updateLogicalAccountOrderSettingsSQL, settingsJSON, spaceID, logicalAccountID)
	return requireUpdated(result.Error, result.RowsAffected, "logical account order settings")
}

// SetLogicalAccountFundSettings 写入资金设置（调用方已校验并转成规范 JSON）。
func (tx *Tx) SetLogicalAccountFundSettings(spaceID, logicalAccountID, settingsJSON string) error {
	result := tx.db.Exec(updateLogicalAccountFundSettingsSQL, settingsJSON, spaceID, logicalAccountID)
	return requireUpdated(result.Error, result.RowsAffected, "logical account fund settings")
}

// ResumeLogicalAccount 恢复策略组合的自动执行，并显式确认当前最近一条保证金风控减仓动作，
// 保证同秒/同毫秒恢复后新的风控告警仍可再次触发，无需时间宽限。
func (tx *Tx) ResumeLogicalAccount(spaceID, logicalAccountID string) error {
	ackedActionID := ""
	lastMargin, err := tx.LastMarginReduceAction(spaceID, logicalAccountID)
	if err != nil {
		return err
	}
	if lastMargin != nil {
		ackedActionID = lastMargin.ActionID
	}
	result := tx.db.Exec(execResumeLogicalAccount, ackedActionID, spaceID, logicalAccountID)
	return requireUpdated(result.Error, result.RowsAffected, "logical account automation")
}

// PauseLogicalAccount 暂停策略组合，写入暂停来源、减仓状态与原因。
// 对自主下单组合不生效且直接返回成功（自主下单没有自动执行，暂停列恒为空）。
func (tx *Tx) PauseLogicalAccount(spaceID, logicalAccountID string, pause Pause) error {
	if !validPause(pause) {
		return fmt.Errorf("%w: invalid pause", ErrInvalidRecord)
	}
	result := tx.db.Exec(execPauseLogicalAccount, pause.Reason, string(pause.Source), string(pause.MarginReduce), spaceID, logicalAccountID)
	if result.Error != nil {
		return writeError(result.Error)
	}
	if result.RowsAffected == 1 {
		return nil
	}
	// 没更新到行时区分“自主下单组合（忽略暂停）”与“组合不存在”。
	current, err := tx.GetLogicalAccount(spaceID, logicalAccountID)
	if err == nil && current.IsManual() {
		return nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return fmt.Errorf("%w: missing logical account automation", ErrInvalidRecord)
}

// PauseLogicalAccountIf 只在策略组合当前的暂停来源与减仓状态仍是 expected 时把它暂停成 pause，返回是否改了；
// expected 为零值表示“仍在自动执行”。自主下单组合不匹配，直接返回 false, nil。
func (tx *Tx) PauseLogicalAccountIf(spaceID, logicalAccountID string, expected, pause Pause) (bool, error) {
	if blank(spaceID) || blank(logicalAccountID) || !validPause(pause) {
		return false, fmt.Errorf("%w: incomplete conditional pause", ErrInvalidRecord)
	}
	result := tx.db.Exec(execPauseLogicalAccountIf,
		pause.Reason, string(pause.Source), string(pause.MarginReduce),
		spaceID, logicalAccountID, string(expected.Source), string(expected.MarginReduce),
	)
	if result.Error != nil {
		return false, writeError(result.Error)
	}
	return result.RowsAffected == 1, nil
}

// LogicalAccountMarkets 返回组合账户可交易的市场：启用成员已开通市场的并集，按固定顺序排列。
// 这个集合不存储，每次由成员算出，成员变动后立刻反映在目标校验和 markets 只读字段上。
func (s *Store) LogicalAccountMarkets(ctx context.Context, spaceID, logicalAccountID string) ([]string, error) {
	return logicalAccountMarkets(s.db.WithContext(ctx), spaceID, logicalAccountID)
}

func (tx *Tx) LogicalAccountMarkets(spaceID, logicalAccountID string) ([]string, error) {
	return logicalAccountMarkets(tx.db, spaceID, logicalAccountID)
}

func logicalAccountMarkets(db *gorm.DB, spaceID, logicalAccountID string) ([]string, error) {
	var rows []string
	if err := db.Raw(queryLogicalAccountMemberMarkets, spaceID, logicalAccountID).Scan(&rows).Error; err != nil {
		return nil, err
	}
	sets := make([][]exchange.MarketType, 0, len(rows))
	for _, raw := range rows {
		markets, err := exchange.ParseMarkets(raw)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidRecord, err)
		}
		sets = append(sets, markets)
	}
	union := exchange.UnionMarkets(sets...)
	result := make([]string, len(union))
	for i, market := range union {
		result[i] = string(market)
	}
	return result, nil
}

func (tx *Tx) PutLogicalAccountMember(record LogicalAccountMemberRecord) error {
	if err := logicalaccount.ValidateMember(record.SpaceID, record.LogicalAccountID, record.TradingAccountID); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRecord, err)
	}
	account, err := tx.GetLogicalAccount(record.SpaceID, record.LogicalAccountID)
	if err != nil {
		return err
	}
	if !account.CanEditMembers() {
		return fmt.Errorf("%w: %v", ErrInvalidRecord, logicalaccount.ErrMembershipChange)
	}
	var elsewhere int64
	if err := tx.db.Raw(queryCountMemberAccountMembership, record.SpaceID, record.TradingAccountID, record.LogicalAccountID).Scan(&elsewhere).Error; err != nil {
		return err
	}
	if elsewhere > 0 {
		return fmt.Errorf("%w: %w", ErrInvalidRecord, logicalaccount.ErrMemberElsewhere)
	}
	physical, err := tx.GetTradingAccount(record.SpaceID, record.TradingAccountID)
	if err != nil {
		return err
	}
	if string(physical.EffectiveEnvironment()) != account.Environment ||
		physical.SettlementAsset != account.SettlementAsset {
		return fmt.Errorf("%w: %v", ErrInvalidRecord, logicalaccount.ErrInhomogeneous)
	}
	err = tx.db.Exec(execInsertLogicalAccountMember,
		record.SpaceID, record.LogicalAccountID, record.TradingAccountID,
		record.Enabled, record.Priority,
	).Error
	if err != nil || !record.Enabled {
		return writeError(err)
	}
	// 同步只给启用成员的外部单写组合编号：加入组合前、或停用期间在交易所下的单没有组合编号，
	// 组合的订单列表看不到、也撤不掉，却会挡住恢复。启用时把这些还没结束的单补上本组合的编号，
	// 这些单已有的成交同样补上（成交的组合编号跟随订单），要在改订单之前按订单还没有组合编号找出来。
	if err := tx.db.Exec(execAdoptMemberOpenOrderFills,
		record.LogicalAccountID, record.SpaceID, record.SpaceID, record.TradingAccountID, activeOrderStatesJSON,
	).Error; err != nil {
		return err
	}
	return tx.db.Exec(execAdoptMemberOpenOrders,
		record.LogicalAccountID, record.SpaceID, record.TradingAccountID, activeOrderStatesJSON,
	).Error
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
	if !account.CanEditMembers() {
		return fmt.Errorf("%w: %v", ErrInvalidRecord, logicalaccount.ErrMembershipChange)
	}
	result := tx.db.Exec(execDeleteLogicalAccountMember, spaceID, logicalAccountID, tradingAccountID)
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

// FindLogicalAccountMembership 返回执行账户所属的组合账户，启用和停用成员都算（一个执行账户最多属于一个组合）。
func (s *Store) FindLogicalAccountMembership(
	ctx context.Context,
	spaceID string,
	tradingAccountID string,
) (LogicalAccountRecord, LogicalAccountMemberRecord, error) {
	var member logicalAccountMemberRow
	err := s.db.WithContext(ctx).
		Where("c_space_id = ? AND c_trading_account_id = ?", spaceID, tradingAccountID).
		Take(&member).Error
	if err != nil {
		return LogicalAccountRecord{}, LogicalAccountMemberRecord{}, err
	}
	account, err := s.GetLogicalAccount(ctx, spaceID, member.LogicalAccountID)
	return account, logicalAccountMemberRecord(member), err
}

func logicalAccountRecord(row logicalAccountRow) LogicalAccountRecord {
	return LogicalAccountRecord{
		ControlMode: row.ControlMode,
		SpaceID:     row.SpaceID, LogicalAccountID: row.LogicalAccountID,
		Name: row.Name, ClosedThrough: row.ClosedThrough,
		Environment:       row.Environment,
		SettlementAsset:   row.SettlementAsset,
		OrderSettingsJSON: row.OrderSettingsJSON, FundSettingsJSON: row.FundSettingsJSON,
		PauseReason: row.PauseReason,
		PauseSource: PauseSource(row.PauseSource), MarginReduceState: MarginReduceState(row.MarginReduceState),
		AckedMarginActionID: row.AckedMarginActionID,
		CreatedAt:           row.CreatedAt, UpdatedAt: row.UpdatedAt,
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

// 以下 SQL 整条写成常量，不在调用处拼接或内联。
const (
	execInsertLogicalAccount = `INSERT INTO t_logical_accounts (
			c_space_id, c_logical_account_id, c_name,
			c_control_mode, c_environment, c_settlement_asset,
			c_order_settings_json, c_fund_settings_json,
			c_pause_reason, c_pause_source, c_margin_reduce_state
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	// 恢复自动执行：从暂停恢复时记下已确认的最近一次风控减仓动作号，清空暂停原因、来源与减仓状态；只有策略控制的组合能恢复自动执行。
	execResumeLogicalAccount = `UPDATE t_logical_accounts
		SET c_acked_margin_action_id = CASE WHEN c_pause_source <> '' THEN ? ELSE c_acked_margin_action_id END,
			c_pause_reason = '', c_pause_source = '', c_margin_reduce_state = '',
			c_mtime = CURRENT_TIMESTAMP
		WHERE c_space_id = ? AND c_logical_account_id = ? AND c_control_mode = 'STRATEGY'`
	execPauseLogicalAccount = `UPDATE t_logical_accounts
		SET c_pause_reason = ?, c_pause_source = ?, c_margin_reduce_state = ?,
			c_mtime = CURRENT_TIMESTAMP
		WHERE c_space_id = ? AND c_logical_account_id = ? AND c_control_mode = 'STRATEGY'`
	// 条件暂停：按当前的暂停来源与减仓状态比对（运行中两者都为空），只对策略组合生效。
	execPauseLogicalAccountIf = `UPDATE t_logical_accounts
		SET c_pause_reason = ?, c_pause_source = ?, c_margin_reduce_state = ?,
			c_mtime = CURRENT_TIMESTAMP
		WHERE c_space_id = ? AND c_logical_account_id = ? AND c_control_mode = 'STRATEGY'
			AND c_pause_source = ? AND c_margin_reduce_state = ?`
	queryLogicalAccountMemberMarkets = `SELECT a.c_markets
		FROM t_logical_account_members AS m
		JOIN t_trading_accounts AS a
			ON a.c_space_id = m.c_space_id
			AND a.c_trading_account_id = m.c_trading_account_id
		WHERE m.c_space_id = ? AND m.c_logical_account_id = ? AND m.c_enabled = 1`
	execRenameLogicalAccount = `UPDATE t_logical_accounts
		SET c_name = ?, c_mtime = CURRENT_TIMESTAMP
		WHERE c_space_id = ? AND c_logical_account_id = ?`
	queryCountMemberAccountMembership = `SELECT COUNT(*) FROM t_logical_account_members
		WHERE c_space_id = ? AND c_trading_account_id = ? AND c_logical_account_id <> ?`
	execInsertLogicalAccountMember = `INSERT INTO t_logical_account_members (
			c_space_id, c_logical_account_id, c_trading_account_id,
			c_enabled, c_priority
		) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(c_space_id, c_logical_account_id, c_trading_account_id)
		DO UPDATE SET
			c_enabled = excluded.c_enabled,
			c_priority = excluded.c_priority,
			c_mtime = CURRENT_TIMESTAMP`
	execDeleteLogicalAccountMember = `DELETE FROM t_logical_account_members
		WHERE c_space_id = ? AND c_logical_account_id = ?
			AND c_trading_account_id = ?`
	// 给启用成员还没结束、也没有组合编号的订单（只可能是外部单）补上组合编号；不动版本号，订单的版本化更新不写这一列。
	// 订单状态列表经 json_each(?) 传入（activeOrderStatesJSON）。
	execAdoptMemberOpenOrders = `UPDATE t_trade_orders
		SET c_logical_account_id = ?, c_mtime = CURRENT_TIMESTAMP
		WHERE c_space_id = ? AND c_trading_account_id = ? AND c_logical_account_id IS NULL
			AND c_state IN (SELECT value FROM json_each(?))`
	// 给上面将被补上组合编号的订单的已有成交补上同一个组合编号，条件与 execAdoptMemberOpenOrders 相同，须在它之前执行。
	execAdoptMemberOpenOrderFills = `UPDATE t_order_fills
		SET c_logical_account_id = ?
		WHERE c_space_id = ? AND c_order_id IN (
			SELECT c_order_id FROM t_trade_orders
			WHERE c_space_id = ? AND c_trading_account_id = ? AND c_logical_account_id IS NULL
				AND c_state IN (SELECT value FROM json_each(?))
		)`
)
