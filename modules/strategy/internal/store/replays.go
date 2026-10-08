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

// Replay 是一次基于 View 的研究回放任务。
type Replay struct {
	ReplayID   string
	StrategyID *string
	DSLYaml    string
	SpaceID    string
	ViewID     string
	StartTime  time.Time
	EndTime    time.Time
	FeeBps     float64
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
	Holdings      int
	Frozen        int
	SkipReason    string
	Unfilled      int
	Liquidated    int
}

type replayRow struct {
	ReplayID     string         `gorm:"column:c_replay_id"`
	StrategyID   sql.NullString `gorm:"column:c_strategy_id"`
	DSLYaml      string         `gorm:"column:c_dsl_yaml"`
	SpaceID      string         `gorm:"column:c_space_id"`
	ViewID       string         `gorm:"column:c_view_id"`
	StartTime    int64          `gorm:"column:c_start_time"`
	EndTime      int64          `gorm:"column:c_end_time"`
	FeeBps       float64        `gorm:"column:c_fee_bps"`
	FactorsJSON  string         `gorm:"column:c_factors_json"`
	Status       string         `gorm:"column:c_status"`
	ProgressTime sql.NullInt64  `gorm:"column:c_progress_time"`
	MetricsJSON  string         `gorm:"column:c_metrics_json"`
	Error        string         `gorm:"column:c_error"`
	CreatedAt    time.Time      `gorm:"column:c_ctime"`
	UpdatedAt    time.Time      `gorm:"column:c_mtime"`
}

func (r replayRow) replay() Replay {
	replay := Replay{
		ReplayID: r.ReplayID, StrategyID: nullableString(r.StrategyID), DSLYaml: r.DSLYaml, SpaceID: r.SpaceID, ViewID: r.ViewID,
		StartTime: fromMillis(r.StartTime), EndTime: fromMillis(r.EndTime), FeeBps: r.FeeBps, Status: r.Status,
		MetricsJSON: json.RawMessage(r.MetricsJSON), Error: r.Error, CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
	}
	if r.ProgressTime.Valid {
		at := fromMillis(r.ProgressTime.Int64)
		replay.ProgressTime = &at
	}
	if r.FactorsJSON != "" {
		_ = json.Unmarshal([]byte(r.FactorsJSON), &replay.Factors)
	}
	return replay
}

const replayColumns = "c_replay_id, c_strategy_id, c_dsl_yaml, c_space_id, c_view_id, c_start_time, c_end_time, c_fee_bps, c_factors_json, c_status, c_progress_time, c_metrics_json, c_error, c_ctime, c_mtime"

// CreateReplay 登记一个 pending 的回放任务。
func (s *Store) CreateReplay(ctx context.Context, replay Replay) error {
	if strings.TrimSpace(replay.ReplayID) == "" || strings.TrimSpace(replay.SpaceID) == "" || strings.TrimSpace(replay.ViewID) == "" || strings.TrimSpace(replay.DSLYaml) == "" {
		return errors.New("回放缺少 replay_id、space_id、view_id 或 DSL 文本")
	}
	if replay.StartTime.IsZero() || replay.EndTime.IsZero() || !replay.EndTime.After(replay.StartTime) {
		return errors.New("回放区间必须满足 start < end")
	}
	if math.IsNaN(replay.FeeBps) || replay.FeeBps < 0 || replay.FeeBps > MaxReplayFeeBps {
		return fmt.Errorf("手续费必须在 0 到 %d bps 之间", MaxReplayFeeBps)
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
	return s.db.WithContext(ctx).Exec(`
		INSERT INTO t_strategy_replays (`+replayColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', NULL, '{}', '', ?, ?)
	`, replay.ReplayID, stringValue(replay.StrategyID), replay.DSLYaml, replay.SpaceID, replay.ViewID, millis(replay.StartTime), millis(replay.EndTime), replay.FeeBps, string(factorsJSON), now, now).Error
}

// GetReplay 读取回放任务。
func (s *Store) GetReplay(ctx context.Context, replayID string) (Replay, error) {
	var row replayRow
	if err := s.db.WithContext(ctx).Raw(`SELECT `+replayColumns+` FROM t_strategy_replays WHERE c_replay_id = ?`, replayID).Scan(&row).Error; err != nil {
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
	if err := s.db.WithContext(ctx).Raw(`SELECT COUNT(*) FROM t_strategy_replays WHERE c_space_id = ?`, spaceID).Scan(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []replayRow
	if err := s.db.WithContext(ctx).Raw(`SELECT `+replayColumns+` FROM t_strategy_replays WHERE c_space_id = ? ORDER BY c_ctime DESC, c_replay_id LIMIT ? OFFSET ?`, spaceID, limit, offset).Scan(&rows).Error; err != nil {
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
		if err := tx.Raw(`SELECT ` + replayColumns + ` FROM t_strategy_replays WHERE c_status = 'pending' ORDER BY c_ctime, c_replay_id LIMIT 1`).Scan(&row).Error; err != nil {
			return err
		}
		if row.ReplayID == "" {
			return nil
		}
		result := tx.Exec(`UPDATE t_strategy_replays SET c_status = 'running', c_mtime = ? WHERE c_replay_id = ? AND c_status = 'pending'`, now, row.ReplayID)
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
	return s.db.WithContext(ctx).Exec(`UPDATE t_strategy_replays SET c_progress_time = ?, c_mtime = ? WHERE c_replay_id = ? AND c_status = 'running'`, millis(progress), now, replayID).Error
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
	result := s.db.WithContext(ctx).Exec(`UPDATE t_strategy_replays SET c_status = ?, c_metrics_json = ?, c_error = ?, c_mtime = ? WHERE c_replay_id = ? AND c_status = 'running'`, status, metrics, errText, now, replayID)
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
	return s.db.WithContext(ctx).Exec(`UPDATE t_strategy_replays SET c_metrics_json = ?, c_mtime = ? WHERE c_replay_id = ? AND c_status = 'cancelled'`, string(metricsJSON), now, replayID).Error
}

// CancelReplay 取消 pending 或 running 的回放；运行中的任务由执行循环在下一期检查状态后停止。
func (s *Store) CancelReplay(ctx context.Context, replayID string, at time.Time) error {
	now, err := requireTime(at)
	if err != nil {
		return err
	}
	result := s.db.WithContext(ctx).Exec(`UPDATE t_strategy_replays SET c_status = 'cancelled', c_mtime = ? WHERE c_replay_id = ? AND c_status IN ('pending', 'running')`, now, replayID)
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
	if err := s.db.WithContext(ctx).Raw(`SELECT c_status FROM t_strategy_replays WHERE c_replay_id = ?`, replayID).Scan(&status).Error; err != nil {
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
	result := s.db.WithContext(ctx).Exec(`UPDATE t_strategy_replays SET c_status = 'failed', c_error = 'interrupted', c_mtime = ? WHERE c_status = 'running'`, now)
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
	return s.db.WithContext(ctx).Exec(`
		INSERT INTO t_strategy_replay_bars (c_replay_id, c_bar_end_time, c_status, c_targets_json, c_positions_json, c_summary_json, c_return, c_equity, c_turnover, c_fee, c_holdings, c_frozen, c_skip_reason, c_unfilled, c_liquidated)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, bar.ReplayID, millis(bar.BarEndTime), bar.Status, string(bar.TargetsJSON), string(bar.PositionsJSON), string(bar.SummaryJSON), bar.Return, bar.Equity, bar.Turnover, bar.Fee, bar.Holdings, bar.Frozen, bar.SkipReason, bar.Unfilled, bar.Liquidated).Error
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
	if err := s.db.WithContext(ctx).Raw(`SELECT COUNT(*) FROM t_strategy_replay_bars WHERE c_replay_id = ?`, replayID).Scan(&total).Error; err != nil {
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
		Holdings      int     `gorm:"column:c_holdings"`
		Frozen        int     `gorm:"column:c_frozen"`
		SkipReason    string  `gorm:"column:c_skip_reason"`
		Unfilled      int     `gorm:"column:c_unfilled"`
		Liquidated    int     `gorm:"column:c_liquidated"`
	}
	columns := "c_replay_id, c_bar_end_time, c_status, c_targets_json, c_positions_json, c_summary_json, c_return, c_equity, c_turnover, c_fee, c_holdings, c_frozen, c_skip_reason, c_unfilled, c_liquidated"
	if brief {
		columns = "c_replay_id, c_bar_end_time, c_status, '[]' AS c_targets_json, '{}' AS c_positions_json, '{}' AS c_summary_json, c_return, c_equity, c_turnover, c_fee, c_holdings, c_frozen, c_skip_reason, c_unfilled, c_liquidated"
	}
	if err := s.db.WithContext(ctx).Raw(`SELECT `+columns+` FROM t_strategy_replay_bars WHERE c_replay_id = ? ORDER BY c_bar_end_time LIMIT ? OFFSET ?`, replayID, limit, offset).Scan(&rows).Error; err != nil {
		return nil, 0, err
	}
	bars := make([]ReplayBar, 0, len(rows))
	for _, row := range rows {
		bars = append(bars, ReplayBar{
			ReplayID: row.ReplayID, BarEndTime: fromMillis(row.BarEndTime), Status: row.Status, TargetsJSON: json.RawMessage(row.TargetsJSON), PositionsJSON: json.RawMessage(row.PositionsJSON), SummaryJSON: json.RawMessage(row.SummaryJSON),
			Return: row.Return, Equity: row.Equity, Turnover: row.Turnover, Fee: row.Fee, Holdings: row.Holdings, Frozen: row.Frozen, SkipReason: row.SkipReason, Unfilled: row.Unfilled, Liquidated: row.Liquidated,
		})
	}
	return bars, total, nil
}
