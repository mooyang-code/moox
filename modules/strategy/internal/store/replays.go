package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"gorm.io/gorm"
)

// 回放状态。
const (
	ReplayPending   = "pending"
	ReplayRunning   = "running"
	ReplayDone      = "done"
	ReplayFailed    = "failed"
	ReplayCancelled = "cancelled"
)

// ReplayTask 是回放在一个市场读取的因子任务及其结果 View。
type ReplayTask struct {
	FactorTaskID string `json:"factor_task_id"`
	// ViewID 是任务的结果 View；ViewGeneration 是提交时校验区间所用的活动索引代次：同一代索引只追加、不删除行，
	// 执行时代次没变就沿用提交时的区间。
	ViewID         string `json:"view_id"`
	ViewGeneration string `json:"view_generation"`
}

// Replay 是一次基于 View 的研究回放任务。
type Replay struct {
	ReplayID   string
	StrategyID *string
	// InstanceID 与 SessionID 记录从实例发起的回放来自哪个实例、回放的是哪个会话固化的 DSL。
	InstanceID *string
	SessionID  *string
	DSLYaml    string
	// DSLHash 是被回放 DSL 的内容哈希（列表不带 DSL 全文时据此展示版本）。Calendar 是回放所用的日历
	// （A 股日线的区间按上海日期解释，页面据此显示日期）。
	DSLHash  string
	Calendar string
	SpaceID  string
	// Tasks 是各任务键（市场 spot、swap、equity，统一数据集上的任务是 multi；附加来源是 来源名:市场）读取的因子任务与结果 View。
	Tasks     map[string]ReplayTask
	StartTime time.Time
	EndTime   time.Time
	// SpotFeeBps 是现货与股票的单边手续费，SwapFeeBps 是永续的（基点）。
	SpotFeeBps float64
	SwapFeeBps float64
	// FundingColumn 非空时永续持仓按 View 里的这一列（本根累计的资金费率）结算资金费；空串表示不计。
	FundingColumn string
	// Factors 是发起回放时固化的因子定义指纹；执行时与当时的定义比对，排队期间变化即拒绝执行。
	Factors      map[string]string
	Status       string
	ProgressTime *time.Time
	MetricsJSON  json.RawMessage
	Error        string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// MaxReplayFeeBps 是回放单边手续费的上限（bps）。
const MaxReplayFeeBps = 1000

// MaxActiveReplays 是每个空间同时排队与运行的回放上限：回放由一个执行循环逐个运行，排队过多只会让后发起的
// 任务长时间等待，并让按个数清理失去意义。
const MaxActiveReplays = 20

// ReplayBar 是回放中一个周期的记录。
type ReplayBar struct {
	ReplayID      string
	BarEndTime    time.Time
	Status        string
	TargetsJSON   json.RawMessage
	PositionsJSON json.RawMessage
	SummaryJSON   json.RawMessage
	Return        float64
	Equity        float64
	Turnover      float64
	Fee           float64
	// SpotValue 是期末现货类持仓的市值之和，SwapValue 是永续持仓的净名义价值（多头为正、空头为负）。
	SpotValue  float64
	SwapValue  float64
	Holdings   int
	Frozen     int
	SkipReason string
	Unfilled   int
	Liquidated int
}

type replayRow struct {
	ReplayID      string         `gorm:"column:c_replay_id"`
	StrategyID    sql.NullString `gorm:"column:c_strategy_id"`
	InstanceID    sql.NullString `gorm:"column:c_instance_id"`
	SessionID     sql.NullString `gorm:"column:c_session_id"`
	DSLYaml       string         `gorm:"column:c_dsl_yaml"`
	DSLHash       string         `gorm:"column:c_dsl_hash"`
	Calendar      string         `gorm:"column:c_calendar"`
	SpaceID       string         `gorm:"column:c_space_id"`
	TasksJSON     string         `gorm:"column:c_tasks_json"`
	StartTime     int64          `gorm:"column:c_start_time"`
	EndTime       int64          `gorm:"column:c_end_time"`
	SpotFeeBps    float64        `gorm:"column:c_spot_fee_bps"`
	SwapFeeBps    float64        `gorm:"column:c_swap_fee_bps"`
	FundingColumn string         `gorm:"column:c_funding_column"`
	FactorsJSON   string         `gorm:"column:c_factors_json"`
	Status        string         `gorm:"column:c_status"`
	ProgressTime  sql.NullInt64  `gorm:"column:c_progress_time"`
	MetricsJSON   string         `gorm:"column:c_metrics_json"`
	Error         string         `gorm:"column:c_error"`
	CreatedAt     time.Time      `gorm:"column:c_ctime"`
	UpdatedAt     time.Time      `gorm:"column:c_mtime"`
}

func (r replayRow) replay() Replay {
	replay := Replay{
		ReplayID: r.ReplayID, StrategyID: nullableString(r.StrategyID), InstanceID: nullableString(r.InstanceID), SessionID: nullableString(r.SessionID), DSLYaml: r.DSLYaml, DSLHash: r.DSLHash, Calendar: r.Calendar, SpaceID: r.SpaceID,
		StartTime: fromMillis(r.StartTime), EndTime: fromMillis(r.EndTime), SpotFeeBps: r.SpotFeeBps, SwapFeeBps: r.SwapFeeBps, FundingColumn: r.FundingColumn, Status: r.Status,
		MetricsJSON: json.RawMessage(r.MetricsJSON), Error: r.Error, CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
	}
	if r.ProgressTime.Valid {
		at := fromMillis(r.ProgressTime.Int64)
		replay.ProgressTime = &at
	}
	if r.TasksJSON != "" {
		_ = json.Unmarshal([]byte(r.TasksJSON), &replay.Tasks)
	}
	if r.FactorsJSON != "" {
		_ = json.Unmarshal([]byte(r.FactorsJSON), &replay.Factors)
	}
	return replay
}

// 回放的 SQL：整条写成常量，不在调用处拼接；读回放的语句列清单与 replayRow 一一对应。
const (
	// sqlInsertReplay 写入一条排队中的回放。
	sqlInsertReplay = `
	INSERT INTO t_strategy_replays (c_replay_id, c_strategy_id, c_instance_id, c_session_id, c_dsl_yaml, c_dsl_hash, c_calendar, c_space_id, c_tasks_json, c_start_time, c_end_time, c_spot_fee_bps, c_swap_fee_bps, c_funding_column, c_factors_json, c_status, c_progress_time, c_metrics_json, c_error, c_ctime, c_mtime)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', NULL, '{}', '', ?, ?)`
	// sqlCountActiveReplays 统计空间内排队与运行中的回放。
	sqlCountActiveReplays = `SELECT COUNT(*) FROM t_strategy_replays WHERE c_space_id = ? AND c_status IN ('pending', 'running')`
	// sqlSelectReplayByID 按回放 ID 读取回放。
	sqlSelectReplayByID = `SELECT c_replay_id, c_strategy_id, c_instance_id, c_session_id, c_dsl_yaml, c_dsl_hash, c_calendar, c_space_id, c_tasks_json, c_start_time, c_end_time, c_spot_fee_bps, c_swap_fee_bps, c_funding_column, c_factors_json, c_status, c_progress_time, c_metrics_json, c_error, c_ctime, c_mtime FROM t_strategy_replays WHERE c_replay_id = ?`
	// sqlCountReplaysOfSpace 统计空间内的全部回放。
	sqlCountReplaysOfSpace = `SELECT COUNT(*) FROM t_strategy_replays WHERE c_space_id = ?`
	// sqlListReplaysOfSpace 按创建时间倒序分页列出空间内的回放，不读取 DSL 全文（c_dsl_yaml 返回空串）。
	sqlListReplaysOfSpace = `SELECT c_replay_id, c_strategy_id, c_instance_id, c_session_id, '' AS c_dsl_yaml, c_dsl_hash, c_calendar, c_space_id, c_tasks_json, c_start_time, c_end_time, c_spot_fee_bps, c_swap_fee_bps, c_funding_column, c_factors_json, c_status, c_progress_time, c_metrics_json, c_error, c_ctime, c_mtime FROM t_strategy_replays WHERE c_space_id = ? ORDER BY c_ctime DESC, c_replay_id LIMIT ? OFFSET ?`
	// sqlSelectNextPendingReplay 读取最早的一条排队中的回放。
	sqlSelectNextPendingReplay = `SELECT c_replay_id, c_strategy_id, c_instance_id, c_session_id, c_dsl_yaml, c_dsl_hash, c_calendar, c_space_id, c_tasks_json, c_start_time, c_end_time, c_spot_fee_bps, c_swap_fee_bps, c_funding_column, c_factors_json, c_status, c_progress_time, c_metrics_json, c_error, c_ctime, c_mtime FROM t_strategy_replays WHERE c_status = 'pending' ORDER BY c_ctime, c_replay_id LIMIT 1`
	// sqlClaimReplay 比较交换把排队中的回放改为运行中。
	sqlClaimReplay = `UPDATE t_strategy_replays SET c_status = 'running', c_mtime = ? WHERE c_replay_id = ? AND c_status = 'pending'`
	// sqlUpdateReplayProgress 推进运行中回放的进度。
	sqlUpdateReplayProgress = `UPDATE t_strategy_replays SET c_progress_time = ?, c_mtime = ? WHERE c_replay_id = ? AND c_status = 'running'`
	// sqlFinishReplay 把运行中的回放置为终态并写入指标与错误。
	sqlFinishReplay = `UPDATE t_strategy_replays SET c_status = ?, c_metrics_json = ?, c_error = ?, c_mtime = ? WHERE c_replay_id = ? AND c_status = 'running'`
	// sqlRecordCancelledReplayMetrics 给已取消的回放写入部分指标。
	sqlRecordCancelledReplayMetrics = `UPDATE t_strategy_replays SET c_metrics_json = ?, c_mtime = ? WHERE c_replay_id = ? AND c_status = 'cancelled'`
	// sqlCancelReplay 取消排队或运行中的回放。
	sqlCancelReplay = `UPDATE t_strategy_replays SET c_status = 'cancelled', c_mtime = ? WHERE c_replay_id = ? AND c_status IN ('pending', 'running')`
	// sqlSelectReplayStatus 只读取回放状态。
	sqlSelectReplayStatus = `SELECT c_status FROM t_strategy_replays WHERE c_replay_id = ?`
	// sqlMarkRunningReplaysInterrupted 把遗留的运行中回放标记为 failed(interrupted)。
	sqlMarkRunningReplaysInterrupted = `UPDATE t_strategy_replays SET c_status = 'failed', c_error = 'interrupted', c_mtime = ? WHERE c_status = 'running'`
	// sqlInsertReplayBar 写入一个周期的回放记录。
	sqlInsertReplayBar = `
		INSERT INTO t_strategy_replay_bars (c_replay_id, c_bar_end_time, c_status, c_targets_json, c_positions_json, c_summary_json, c_return, c_equity, c_turnover, c_fee, c_spot_value, c_swap_value, c_holdings, c_frozen, c_skip_reason, c_unfilled, c_liquidated)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	// sqlCountReplayBars 统计回放的周期记录数。
	sqlCountReplayBars = `SELECT COUNT(*) FROM t_strategy_replay_bars WHERE c_replay_id = ?`
	// sqlListReplayBars 按周期顺序分页读取回放记录（含目标、持仓与摘要 JSON）。
	sqlListReplayBars = `SELECT c_replay_id, c_bar_end_time, c_status, c_targets_json, c_positions_json, c_summary_json, c_return, c_equity, c_turnover, c_fee, c_spot_value, c_swap_value, c_holdings, c_frozen, c_skip_reason, c_unfilled, c_liquidated FROM t_strategy_replay_bars WHERE c_replay_id = ? ORDER BY c_bar_end_time LIMIT ? OFFSET ?`
	// sqlListReplayBarsBrief 与 sqlListReplayBars 相同，只是目标、持仓与摘要 JSON 返回空值。
	sqlListReplayBarsBrief = `SELECT c_replay_id, c_bar_end_time, c_status, '[]' AS c_targets_json, '{}' AS c_positions_json, '{}' AS c_summary_json, c_return, c_equity, c_turnover, c_fee, c_spot_value, c_swap_value, c_holdings, c_frozen, c_skip_reason, c_unfilled, c_liquidated FROM t_strategy_replay_bars WHERE c_replay_id = ? ORDER BY c_bar_end_time LIMIT ? OFFSET ?`
)

// CreateReplay 登记一个 pending 的回放任务。
func (s *Store) CreateReplay(ctx context.Context, replay Replay) error {
	if strings.TrimSpace(replay.ReplayID) == "" || strings.TrimSpace(replay.SpaceID) == "" || strings.TrimSpace(replay.DSLYaml) == "" || strings.TrimSpace(replay.DSLHash) == "" || strings.TrimSpace(replay.Calendar) == "" {
		return errors.New("回放缺少 replay_id、space_id、DSL 文本、版本哈希或日历")
	}
	taskIDs := make(map[string]string, len(replay.Tasks))
	for key, task := range replay.Tasks {
		taskIDs[key] = task.FactorTaskID
	}
	if err := ValidateTaskKeys(taskIDs); err != nil {
		return fmt.Errorf("回放的因子任务：%w", err)
	}
	for key, task := range replay.Tasks {
		if strings.TrimSpace(task.ViewID) == "" || strings.TrimSpace(task.ViewGeneration) == "" {
			return fmt.Errorf("回放的 %s 任务缺少 view_id 或 View 索引代次", key)
		}
	}
	if replay.StartTime.IsZero() || replay.EndTime.IsZero() || !replay.EndTime.After(replay.StartTime) {
		return errors.New("回放区间必须满足 start < end")
	}
	for name, fee := range map[string]float64{"现货": replay.SpotFeeBps, "永续": replay.SwapFeeBps} {
		if math.IsNaN(fee) || fee < 0 || fee > MaxReplayFeeBps {
			return fmt.Errorf("%s手续费必须在 0 到 %d bps 之间", name, MaxReplayFeeBps)
		}
	}
	now, err := requireTime(replay.CreatedAt)
	if err != nil {
		return err
	}
	factors := replay.Factors
	if factors == nil {
		factors = map[string]string{}
	}
	factorsJSON, err := json.Marshal(factors)
	if err != nil {
		return err
	}
	tasksJSON, err := json.Marshal(replay.Tasks)
	if err != nil {
		return err
	}
	return s.transaction(ctx, func(tx *gorm.DB) error {
		var active int64
		if err := tx.Raw(sqlCountActiveReplays, replay.SpaceID).Scan(&active).Error; err != nil {
			return err
		}
		if active >= MaxActiveReplays {
			return fmt.Errorf("空间 %s 已有 %d 个排队或运行中的回放，请等它们完成或取消后再发起", replay.SpaceID, active)
		}
		return tx.Exec(sqlInsertReplay, replay.ReplayID, stringValue(replay.StrategyID), stringValue(replay.InstanceID), stringValue(replay.SessionID), replay.DSLYaml, replay.DSLHash, replay.Calendar, replay.SpaceID, string(tasksJSON), millis(replay.StartTime), millis(replay.EndTime), replay.SpotFeeBps, replay.SwapFeeBps, replay.FundingColumn, string(factorsJSON), now, now).Error
	})
}

// CountActiveReplays 返回空间内排队与运行中的回放数。
func (s *Store) CountActiveReplays(ctx context.Context, spaceID string) (int64, error) {
	var active int64
	err := s.db.WithContext(ctx).Raw(sqlCountActiveReplays, spaceID).Scan(&active).Error
	return active, err
}

// GetReplay 读取回放任务。
func (s *Store) GetReplay(ctx context.Context, replayID string) (Replay, error) {
	var row replayRow
	if err := s.db.WithContext(ctx).Raw(sqlSelectReplayByID, replayID).Scan(&row).Error; err != nil {
		return Replay{}, err
	}
	if row.ReplayID == "" {
		return Replay{}, ErrNotFound
	}
	return row.replay(), nil
}

// ListReplays 按创建时间倒序分页列出空间内的回放。
func (s *Store) ListReplays(ctx context.Context, spaceID string, offset, limit int) ([]Replay, int64, error) {
	if limit <= 0 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	var total int64
	if err := s.db.WithContext(ctx).Raw(sqlCountReplaysOfSpace, spaceID).Scan(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []replayRow
	// 列表每 5 秒轮询一次：不读 DSL 全文（每行最多 64 KiB），版本用 c_dsl_hash 展示。
	if err := s.db.WithContext(ctx).Raw(sqlListReplaysOfSpace, spaceID, limit, offset).Scan(&rows).Error; err != nil {
		return nil, 0, err
	}
	replays := make([]Replay, 0, len(rows))
	for _, row := range rows {
		replays = append(replays, row.replay())
	}
	return replays, total, nil
}

// ClaimNextReplay 把最早的 pending 回放改为 running 并返回；没有则返回 false。
func (s *Store) ClaimNextReplay(ctx context.Context, at time.Time) (Replay, bool, error) {
	now, err := requireTime(at)
	if err != nil {
		return Replay{}, false, err
	}
	var claimed Replay
	found := false
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		var row replayRow
		if err := tx.Raw(sqlSelectNextPendingReplay).Scan(&row).Error; err != nil {
			return err
		}
		if row.ReplayID == "" {
			return nil
		}
		result := tx.Exec(sqlClaimReplay, now, row.ReplayID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return nil
		}
		row.Status = ReplayRunning
		row.UpdatedAt = now
		claimed = row.replay()
		found = true
		return nil
	})
	return claimed, found, err
}

// UpdateReplayProgress 推进回放进度。
func (s *Store) UpdateReplayProgress(ctx context.Context, replayID string, progress, at time.Time) error {
	now, err := requireTime(at)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Exec(sqlUpdateReplayProgress, millis(progress), now, replayID).Error
}

// FinishReplay 把运行中的回放置为终态（done | failed）。
func (s *Store) FinishReplay(ctx context.Context, replayID, status string, metricsJSON json.RawMessage, errText string, at time.Time) error {
	now, err := requireTime(at)
	if err != nil {
		return err
	}
	if status != ReplayDone && status != ReplayFailed {
		return fmt.Errorf("回放终态 %q 无效", status)
	}
	metrics := "{}"
	if len(metricsJSON) > 0 {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(metricsJSON, &object); err != nil || object == nil {
			return errors.New("回放指标必须是 JSON 对象")
		}
		metrics = string(metricsJSON)
	}
	result := s.db.WithContext(ctx).Exec(sqlFinishReplay, status, metrics, errText, now, replayID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrNotFound
	}
	return nil
}

// RecordCancelledReplayMetrics 为已取消的回放写入截至取消时的部分指标。
func (s *Store) RecordCancelledReplayMetrics(ctx context.Context, replayID string, metricsJSON json.RawMessage, at time.Time) error {
	now, err := requireTime(at)
	if err != nil {
		return err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(metricsJSON, &object); err != nil || object == nil {
		return errors.New("回放指标必须是 JSON 对象")
	}
	return s.db.WithContext(ctx).Exec(sqlRecordCancelledReplayMetrics, string(metricsJSON), now, replayID).Error
}

// CancelReplay 取消 pending 或 running 的回放；运行中的任务由执行循环在下一期检查状态后停止。
func (s *Store) CancelReplay(ctx context.Context, replayID string, at time.Time) error {
	now, err := requireTime(at)
	if err != nil {
		return err
	}
	result := s.db.WithContext(ctx).Exec(sqlCancelReplay, now, replayID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrNotFound
	}
	return nil
}

// ReplayStatus 只读取状态，供执行循环检查取消。
func (s *Store) ReplayStatus(ctx context.Context, replayID string) (string, error) {
	var status string
	if err := s.db.WithContext(ctx).Raw(sqlSelectReplayStatus, replayID).Scan(&status).Error; err != nil {
		return "", err
	}
	if status == "" {
		return "", ErrNotFound
	}
	return status, nil
}

// MarkRunningReplaysInterrupted 在进程启动时把遗留的 running 任务标记为 failed(interrupted)，已写入的 bars 保留。
func (s *Store) MarkRunningReplaysInterrupted(ctx context.Context, at time.Time) (int64, error) {
	now, err := requireTime(at)
	if err != nil {
		return 0, err
	}
	result := s.db.WithContext(ctx).Exec(sqlMarkRunningReplaysInterrupted, now)
	return result.RowsAffected, result.Error
}

// AppendReplayBar 写入一个周期的回放记录。
func (s *Store) AppendReplayBar(ctx context.Context, bar ReplayBar) error {
	if strings.TrimSpace(bar.ReplayID) == "" || bar.BarEndTime.IsZero() || strings.TrimSpace(bar.Status) == "" {
		return errors.New("回放周期缺少 replay_id、bar_end_time 或状态")
	}
	for name, raw := range map[string]json.RawMessage{"targets_json": bar.TargetsJSON, "positions_json": bar.PositionsJSON, "summary_json": bar.SummaryJSON} {
		if !json.Valid(raw) {
			return fmt.Errorf("回放周期的 %s 不是有效 JSON", name)
		}
	}
	return s.db.WithContext(ctx).Exec(sqlInsertReplayBar, bar.ReplayID, millis(bar.BarEndTime), bar.Status, string(bar.TargetsJSON), string(bar.PositionsJSON), string(bar.SummaryJSON), bar.Return, bar.Equity, bar.Turnover, bar.Fee, bar.SpotValue, bar.SwapValue, bar.Holdings, bar.Frozen, bar.SkipReason, bar.Unfilled, bar.Liquidated).Error
}

// ListReplayBars 按周期顺序分页读取回放记录。brief 为 true 时不读目标、持仓与摘要 JSON（返回空值），
// 用于绘制权益曲线与概览，单页可以取得很大而不受报文大小限制。
func (s *Store) ListReplayBars(ctx context.Context, replayID string, offset, limit int, brief bool) ([]ReplayBar, int64, error) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	var total int64
	if err := s.db.WithContext(ctx).Raw(sqlCountReplayBars, replayID).Scan(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []struct {
		ReplayID      string  `gorm:"column:c_replay_id"`
		BarEndTime    int64   `gorm:"column:c_bar_end_time"`
		Status        string  `gorm:"column:c_status"`
		TargetsJSON   string  `gorm:"column:c_targets_json"`
		PositionsJSON string  `gorm:"column:c_positions_json"`
		SummaryJSON   string  `gorm:"column:c_summary_json"`
		Return        float64 `gorm:"column:c_return"`
		Equity        float64 `gorm:"column:c_equity"`
		Turnover      float64 `gorm:"column:c_turnover"`
		Fee           float64 `gorm:"column:c_fee"`
		SpotValue     float64 `gorm:"column:c_spot_value"`
		SwapValue     float64 `gorm:"column:c_swap_value"`
		Holdings      int     `gorm:"column:c_holdings"`
		Frozen        int     `gorm:"column:c_frozen"`
		SkipReason    string  `gorm:"column:c_skip_reason"`
		Unfilled      int     `gorm:"column:c_unfilled"`
		Liquidated    int     `gorm:"column:c_liquidated"`
	}
	query := sqlListReplayBars
	if brief {
		query = sqlListReplayBarsBrief
	}
	if err := s.db.WithContext(ctx).Raw(query, replayID, limit, offset).Scan(&rows).Error; err != nil {
		return nil, 0, err
	}
	bars := make([]ReplayBar, 0, len(rows))
	for _, row := range rows {
		bars = append(bars, ReplayBar{
			ReplayID: row.ReplayID, BarEndTime: fromMillis(row.BarEndTime), Status: row.Status, TargetsJSON: json.RawMessage(row.TargetsJSON), PositionsJSON: json.RawMessage(row.PositionsJSON), SummaryJSON: json.RawMessage(row.SummaryJSON),
			Return: row.Return, Equity: row.Equity, Turnover: row.Turnover, Fee: row.Fee, SpotValue: row.SpotValue, SwapValue: row.SwapValue, Holdings: row.Holdings, Frozen: row.Frozen, SkipReason: row.SkipReason, Unfilled: row.Unfilled, Liquidated: row.Liquidated,
		})
	}
	return bars, total, nil
}
