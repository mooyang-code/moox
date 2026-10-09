package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// 结果状态。
const (
	StatusOK      = "ok"
	StatusSkipped = "skipped"
)

// 由存储层或投递流程识别的跳过原因。
const (
	SkipOutOfOrder = "out_of_order"
	SkipExpired    = "expired"
)

// PublishStatus 是结果的投递状态。
type PublishStatus string

const (
	PublishNone      PublishStatus = "none"
	PublishPending   PublishStatus = "pending"
	PublishSent      PublishStatus = "sent"
	PublishCancelled PublishStatus = "cancelled"
)

var (
	// ErrResultInvalid 表示结果本身不完整。
	ErrResultInvalid = errors.New("策略结果无效")
	// ErrResultInstanceNotActive 表示实例未启用或会话不匹配。
	ErrResultInstanceNotActive = errors.New("实例未在该会话中启用")
	// ErrResultExpired 表示 ok 结果的有效期已过。
	ErrResultExpired = errors.New("策略结果已过期")
	// ErrResultOlder 表示 ok 结果早于已提交的最新周期。
	ErrResultOlder = errors.New("策略结果早于已提交的最新周期")
	// ErrResultCASConflict 表示提交时的前序记录与期望不符。
	ErrResultCASConflict = errors.New("策略结果比较交换冲突")
)

// InputRecord 是结果中保存的输入摘要（c_input_json 的结构）。
type InputRecord struct {
	ViewID   string            `json:"view_id"`
	Bar      string            `json:"bar"`
	Calendar string            `json:"calendar"`
	BarStart time.Time         `json:"bar_start"`
	EventID  string            `json:"event_id,omitempty"`
	Factors  map[string]string `json:"factors,omitempty"`
	Detail   string            `json:"detail,omitempty"`
}

// Result 是一个处理过的周期：ok 带目标与规则状态，skipped 带原因。
type Result struct {
	ResultID       string
	InstanceID     string
	SessionID      string
	BarEndTime     time.Time
	ValidUntil     time.Time
	Status         string
	SkipReason     string
	DSLHash        string
	InputJSON      json.RawMessage
	TargetsJSON    json.RawMessage
	RuleStatesJSON json.RawMessage
	SummaryJSON    json.RawMessage
	EventData      []byte
	PublishStatus  PublishStatus
	CreatedAt      time.Time
}

// ResultItem 是一条规则下一个标的的解释。
type ResultItem struct {
	RuleID       string
	InstrumentID string
	Stage        string
	Score        *float64
	Rank         *int
	Weight       *string
	Reason       string
}

type resultRow struct {
	ResultID       string        `gorm:"column:c_result_id"`
	InstanceID     string        `gorm:"column:c_instance_id"`
	SessionID      string        `gorm:"column:c_session_id"`
	BarEndTime     int64         `gorm:"column:c_bar_end_time"`
	ValidUntil     int64         `gorm:"column:c_valid_until"`
	Status         string        `gorm:"column:c_status"`
	SkipReason     string        `gorm:"column:c_skip_reason"`
	DSLHash        string        `gorm:"column:c_dsl_hash"`
	InputJSON      string        `gorm:"column:c_input_json"`
	TargetsJSON    string        `gorm:"column:c_targets_json"`
	RuleStatesJSON string        `gorm:"column:c_rule_states_json"`
	SummaryJSON    string        `gorm:"column:c_summary_json"`
	EventData      []byte        `gorm:"column:c_event_data"`
	PublishStatus  PublishStatus `gorm:"column:c_publish_status"`
	CreatedAt      time.Time     `gorm:"column:c_ctime"`
}

func (r resultRow) result() Result {
	return Result{
		ResultID: r.ResultID, InstanceID: r.InstanceID, SessionID: r.SessionID,
		BarEndTime: fromMillis(r.BarEndTime), ValidUntil: fromMillis(r.ValidUntil),
		Status: r.Status, SkipReason: r.SkipReason, DSLHash: r.DSLHash,
		InputJSON: json.RawMessage(r.InputJSON), TargetsJSON: json.RawMessage(r.TargetsJSON),
		RuleStatesJSON: json.RawMessage(r.RuleStatesJSON), SummaryJSON: json.RawMessage(r.SummaryJSON),
		EventData: append([]byte(nil), r.EventData...), PublishStatus: r.PublishStatus, CreatedAt: r.CreatedAt.UTC(),
	}
}

const resultColumns = "c_result_id, c_instance_id, c_session_id, c_bar_end_time, c_valid_until, c_status, c_skip_reason, c_dsl_hash, c_input_json, c_targets_json, c_rule_states_json, c_summary_json, c_event_data, c_publish_status, c_ctime"

// CommitRequest 是一次结果提交。
type CommitRequest struct {
	Result Result
	Items  []ResultItem
	// ExpectedLatestResultID 非 nil 时要求本实例本会话当前最新处理记录的 ID 等于它（空串表示期望没有记录）。
	ExpectedLatestResultID *string
	Now                    time.Time
}

func validateResult(result Result) error {
	if strings.TrimSpace(result.ResultID) == "" || strings.TrimSpace(result.InstanceID) == "" || strings.TrimSpace(result.SessionID) == "" || strings.TrimSpace(result.DSLHash) == "" {
		return errors.New("结果缺少 result_id、instance_id、session_id 或 dsl_hash")
	}
	if result.BarEndTime.IsZero() || result.ValidUntil.IsZero() || result.CreatedAt.IsZero() {
		return errors.New("结果缺少 bar_end_time、valid_until 或创建时间")
	}
	switch result.Status {
	case StatusOK:
		if result.SkipReason != "" {
			return errors.New("ok 结果不能带跳过原因")
		}
	case StatusSkipped:
		if strings.TrimSpace(result.SkipReason) == "" {
			return errors.New("skipped 结果必须带跳过原因")
		}
		if result.PublishStatus != PublishNone || len(result.EventData) != 0 {
			return errors.New("skipped 结果不能投递")
		}
	default:
		return fmt.Errorf("结果状态 %q 无效", result.Status)
	}
	switch result.PublishStatus {
	case PublishNone:
		if len(result.EventData) != 0 {
			return errors.New("不投递的结果不能携带事件数据")
		}
	case PublishPending:
		if len(result.EventData) == 0 {
			return errors.New("待投递的结果必须携带事件数据")
		}
	default:
		return fmt.Errorf("新结果的投递状态 %q 无效", result.PublishStatus)
	}
	var object map[string]json.RawMessage
	for name, raw := range map[string]json.RawMessage{"input_json": result.InputJSON, "rule_states_json": result.RuleStatesJSON, "summary_json": result.SummaryJSON} {
		if err := json.Unmarshal(raw, &object); err != nil || object == nil {
			return fmt.Errorf("结果的 %s 必须是 JSON 对象", name)
		}
	}
	var array []json.RawMessage
	if err := json.Unmarshal(result.TargetsJSON, &array); err != nil || array == nil {
		return errors.New("结果的 targets_json 必须是 JSON 数组")
	}
	return nil
}

func readLatest(tx *gorm.DB, instanceID, sessionID string, onlyOK bool) (resultRow, bool, error) {
	query := `SELECT ` + resultColumns + ` FROM t_strategy_results WHERE c_instance_id = ? AND c_session_id = ?`
	if onlyOK {
		query += ` AND c_status = 'ok'`
	}
	// 同一实例、同一会话内 bar_end 唯一（表约束），按 bar_end 倒序取一条即可直接走索引。
	query += ` ORDER BY c_bar_end_time DESC LIMIT 1`
	var row resultRow
	if err := tx.Raw(query, instanceID, sessionID).Scan(&row).Error; err != nil {
		return resultRow{}, false, err
	}
	return row, row.ResultID != "", nil
}

// CommitResult 在一个事务内写入结果与解释明细。ok 结果要求实例启用、会话匹配、未过期且不早于最新周期；
// skipped 结果允许过去的有效期与更早的周期（乱序记录）。同一周期重复提交返回已有记录。
func (s *Store) CommitResult(ctx context.Context, request CommitRequest) (Result, bool, error) {
	if err := validateResult(request.Result); err != nil {
		return Result{}, false, fmt.Errorf("%w：%v", ErrResultInvalid, err)
	}
	now := request.Now
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()
	result := request.Result
	var committed Result
	created := false
	var superseded int64
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		instance, err := readInstance(tx, result.InstanceID)
		if err != nil {
			return err
		}
		if instance.Enabled != 1 || instance.DeletedAt.Valid || !instance.SessionID.Valid || instance.SessionID.String != result.SessionID {
			return ErrResultInstanceNotActive
		}
		if result.Status == StatusOK && !result.ValidUntil.After(now) {
			return ErrResultExpired
		}
		latest, hasLatest, err := readLatest(tx, result.InstanceID, result.SessionID, false)
		if err != nil {
			return err
		}
		if result.Status == StatusOK && hasLatest && millis(result.BarEndTime) < latest.BarEndTime {
			return ErrResultOlder
		}
		if request.ExpectedLatestResultID != nil {
			latestID := ""
			if hasLatest {
				latestID = latest.ResultID
			}
			if latestID != *request.ExpectedLatestResultID {
				return ErrResultCASConflict
			}
		}
		var existing resultRow
		if err := tx.Raw(`SELECT `+resultColumns+` FROM t_strategy_results WHERE c_instance_id = ? AND c_session_id = ? AND c_bar_end_time = ?`, result.InstanceID, result.SessionID, millis(result.BarEndTime)).Scan(&existing).Error; err != nil {
			return err
		}
		if existing.ResultID != "" {
			committed = existing.result()
			return nil
		}
		if err := tx.Exec(`
			INSERT INTO t_strategy_results (`+resultColumns+`)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, result.ResultID, result.InstanceID, result.SessionID, millis(result.BarEndTime), millis(result.ValidUntil), result.Status, result.SkipReason, result.DSLHash,
			string(result.InputJSON), string(result.TargetsJSON), string(result.RuleStatesJSON), string(result.SummaryJSON), result.EventData, result.PublishStatus, result.CreatedAt.UTC()).Error; err != nil {
			return err
		}
		if err := insertItems(tx, result.ResultID, result.CreatedAt.UTC(), request.Items); err != nil {
			return err
		}
		if result.Status == StatusOK && result.PublishStatus == PublishPending {
			cancel := tx.Exec(`UPDATE t_strategy_results SET c_publish_status = 'cancelled' WHERE c_instance_id = ? AND c_session_id = ? AND c_bar_end_time < ? AND c_publish_status = 'pending'`, result.InstanceID, result.SessionID, millis(result.BarEndTime))
			if cancel.Error != nil {
				return cancel.Error
			}
			superseded = cancel.RowsAffected
		}
		committed = result
		created = true
		return nil
	})
	if err == nil {
		s.cancelled(CancelSuperseded, superseded)
	}
	return committed, created, err
}

func insertItems(tx *gorm.DB, resultID string, createdAt time.Time, items []ResultItem) error {
	const chunk = 200
	for start := 0; start < len(items); start += chunk {
		end := start + chunk
		if end > len(items) {
			end = len(items)
		}
		var builder strings.Builder
		builder.WriteString("INSERT INTO t_strategy_result_items (c_result_id, c_rule_id, c_instrument_id, c_stage, c_score, c_rank, c_weight, c_reason, c_ctime) VALUES ")
		args := make([]any, 0, (end-start)*9)
		for i, item := range items[start:end] {
			if strings.TrimSpace(item.RuleID) == "" || strings.TrimSpace(item.InstrumentID) == "" || strings.TrimSpace(item.Stage) == "" {
				return fmt.Errorf("%w：解释明细缺少规则、标的或阶段", ErrResultInvalid)
			}
			if i > 0 {
				builder.WriteString(", ")
			}
			builder.WriteString("(?, ?, ?, ?, ?, ?, ?, ?, ?)")
			var score, rank, weight any
			if item.Score != nil {
				score = *item.Score
			}
			if item.Rank != nil {
				rank = *item.Rank
			}
			if item.Weight != nil {
				weight = *item.Weight
			}
			args = append(args, resultID, item.RuleID, item.InstrumentID, item.Stage, score, rank, weight, item.Reason, createdAt)
		}
		if err := tx.Exec(builder.String(), args...).Error; err != nil {
			return err
		}
	}
	return nil
}

// LatestProcessed 返回本会话最近处理的周期（ok 或 skipped）。
func (s *Store) LatestProcessed(ctx context.Context, instanceID, sessionID string) (Result, bool, error) {
	row, ok, err := readLatest(s.db.WithContext(ctx), instanceID, sessionID, false)
	if err != nil || !ok {
		return Result{}, false, err
	}
	return row.result(), true, nil
}

// LatestOk 返回本会话最近的 ok 决策，规则状态只从它恢复。
func (s *Store) LatestOk(ctx context.Context, instanceID, sessionID string) (Result, bool, error) {
	row, ok, err := readLatest(s.db.WithContext(ctx), instanceID, sessionID, true)
	if err != nil || !ok {
		return Result{}, false, err
	}
	return row.result(), true, nil
}

// ResultAtBar 返回本会话某个周期的记录。
func (s *Store) ResultAtBar(ctx context.Context, instanceID, sessionID string, barEnd time.Time) (Result, bool, error) {
	var row resultRow
	if err := s.db.WithContext(ctx).Raw(`SELECT `+resultColumns+` FROM t_strategy_results WHERE c_instance_id = ? AND c_session_id = ? AND c_bar_end_time = ?`, instanceID, sessionID, millis(barEnd)).Scan(&row).Error; err != nil {
		return Result{}, false, err
	}
	if row.ResultID == "" {
		return Result{}, false, nil
	}
	return row.result(), true, nil
}

// AdjacentRecord 查找本实例对某一根 bar 的可信处理记录：同一 View、同一 bar 与日历，且含有全部所需因子的指纹；
// 不限会话，优先当前会话，其次创建时间最新。
func (s *Store) AdjacentRecord(ctx context.Context, instanceID, viewID, bar, calendar string, barEnd time.Time, factorIDs []string, preferSessionID string) (Result, InputRecord, bool, error) {
	var rows []resultRow
	if err := s.db.WithContext(ctx).Raw(`
		SELECT `+resultColumns+` FROM t_strategy_results
		WHERE c_instance_id = ? AND c_bar_end_time = ?
		ORDER BY CASE WHEN c_session_id = ? THEN 0 ELSE 1 END, c_ctime DESC, c_result_id DESC
	`, instanceID, millis(barEnd), preferSessionID).Scan(&rows).Error; err != nil {
		return Result{}, InputRecord{}, false, err
	}
	for _, row := range rows {
		var input InputRecord
		if err := json.Unmarshal([]byte(row.InputJSON), &input); err != nil {
			continue
		}
		if input.ViewID != viewID || input.Bar != bar || input.Calendar != calendar {
			continue
		}
		covered := true
		for _, factorID := range factorIDs {
			if input.Factors[factorID] == "" {
				covered = false
				break
			}
		}
		if !covered {
			continue
		}
		return row.result(), input, true, nil
	}
	return Result{}, InputRecord{}, false, nil
}

// GetResult 读取结果。
func (s *Store) GetResult(ctx context.Context, resultID string) (Result, error) {
	var row resultRow
	if err := s.db.WithContext(ctx).Raw(`SELECT `+resultColumns+` FROM t_strategy_results WHERE c_result_id = ?`, resultID).Scan(&row).Error; err != nil {
		return Result{}, err
	}
	if row.ResultID == "" {
		return Result{}, ErrNotFound
	}
	return row.result(), nil
}

// ListResults 按周期倒序分页列出实例的结果；sessionID 为空表示全部会话。
func (s *Store) ListResults(ctx context.Context, instanceID, sessionID string, offset, limit int) ([]Result, int64, error) {
	if limit <= 0 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	where := ` WHERE c_instance_id = ?`
	args := []any{instanceID}
	if strings.TrimSpace(sessionID) != "" {
		where += ` AND c_session_id = ?`
		args = append(args, sessionID)
	}
	var total int64
	if err := s.db.WithContext(ctx).Raw(`SELECT COUNT(*) FROM t_strategy_results`+where, args...).Scan(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []resultRow
	if err := s.db.WithContext(ctx).Raw(`SELECT `+resultColumns+` FROM t_strategy_results`+where+` ORDER BY c_bar_end_time DESC, c_ctime DESC, c_result_id DESC LIMIT ? OFFSET ?`, append(args, limit, offset)...).Scan(&rows).Error; err != nil {
		return nil, 0, err
	}
	results := make([]Result, 0, len(rows))
	for _, row := range rows {
		results = append(results, row.result())
	}
	return results, total, nil
}

// ListResultItems 读取一个结果的解释明细，按规则与标的排序。
func (s *Store) ListResultItems(ctx context.Context, resultID string) ([]ResultItem, error) {
	var rows []struct {
		RuleID       string          `gorm:"column:c_rule_id"`
		InstrumentID string          `gorm:"column:c_instrument_id"`
		Stage        string          `gorm:"column:c_stage"`
		Score        sql.NullFloat64 `gorm:"column:c_score"`
		Rank         sql.NullInt64   `gorm:"column:c_rank"`
		Weight       sql.NullString  `gorm:"column:c_weight"`
		Reason       string          `gorm:"column:c_reason"`
	}
	if err := s.db.WithContext(ctx).Raw(`SELECT c_rule_id, c_instrument_id, c_stage, c_score, c_rank, c_weight, c_reason FROM t_strategy_result_items WHERE c_result_id = ? ORDER BY c_rule_id, c_instrument_id`, resultID).Scan(&rows).Error; err != nil {
		return nil, err
	}
	items := make([]ResultItem, 0, len(rows))
	for _, row := range rows {
		item := ResultItem{RuleID: row.RuleID, InstrumentID: row.InstrumentID, Stage: row.Stage, Reason: row.Reason, Weight: nullableString(row.Weight)}
		if row.Score.Valid {
			score := row.Score.Float64
			item.Score = &score
		}
		if row.Rank.Valid {
			rank := int(row.Rank.Int64)
			item.Rank = &rank
		}
		items = append(items, item)
	}
	return items, nil
}

// ListPendingResults 按创建顺序列出待投递的结果。
func (s *Store) ListPendingResults(ctx context.Context, limit int) ([]Result, error) {
	if limit <= 0 {
		return []Result{}, nil
	}
	var rows []resultRow
	if err := s.db.WithContext(ctx).Raw(`SELECT `+resultColumns+` FROM t_strategy_results WHERE c_publish_status = 'pending' ORDER BY c_ctime, c_result_id LIMIT ?`, limit).Scan(&rows).Error; err != nil {
		return nil, err
	}
	results := make([]Result, 0, len(rows))
	for _, row := range rows {
		results = append(results, row.result())
	}
	return results, nil
}

// PreparePendingResult 在投递前复查：实例仍在该会话启用、未过期、且是本会话最新的 ok 决策；
// 不满足即标记 cancelled，不阻塞后续结果。
func (s *Store) PreparePendingResult(ctx context.Context, resultID string, now time.Time) (Result, bool, error) {
	if strings.TrimSpace(resultID) == "" || now.IsZero() {
		return Result{}, false, errors.New("result_id 与当前时间不能为空")
	}
	var result Result
	valid := false
	reason := ""
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		var row resultRow
		if err := tx.Raw(`SELECT `+resultColumns+` FROM t_strategy_results WHERE c_result_id = ? AND c_publish_status = 'pending'`, resultID).Scan(&row).Error; err != nil {
			return err
		}
		if row.ResultID == "" {
			return ErrNotFound
		}
		result = row.result()
		instance, err := readInstance(tx, row.InstanceID)
		if err != nil {
			return err
		}
		active := instance.Enabled == 1 && !instance.DeletedAt.Valid && instance.SessionID.Valid && instance.SessionID.String == row.SessionID
		switch {
		case !active:
			reason = CancelInactive
		case row.ValidUntil <= millis(now):
			reason = CancelExpired
		default:
			latest, hasLatest, err := readLatest(tx, row.InstanceID, row.SessionID, true)
			if err != nil {
				return err
			}
			if !hasLatest || latest.ResultID != row.ResultID {
				reason = CancelSuperseded
			}
		}
		valid = reason == ""
		if !valid {
			return tx.Exec(`UPDATE t_strategy_results SET c_publish_status = 'cancelled' WHERE c_result_id = ? AND c_publish_status = 'pending'`, resultID).Error
		}
		return nil
	})
	if err == nil && !valid {
		s.cancelled(reason, 1)
	}
	return result, valid, err
}

// TransitionPublishStatus 把待投递结果标记为已发送或已取消。
func (s *Store) TransitionPublishStatus(ctx context.Context, resultID string, from, to PublishStatus) error {
	// pending 只能变为 sent 或 cancelled；事件已经发出、而这一行在发布期间被更新的结果并发取消时，允许 cancelled 变回 sent，
	// 如实记录投递状态。
	if !(from == PublishPending && (to == PublishSent || to == PublishCancelled)) && !(from == PublishCancelled && to == PublishSent) {
		return errors.New("投递状态只能从 pending 变为 sent 或 cancelled，或在事件已发出时从 cancelled 变为 sent")
	}
	result := s.db.WithContext(ctx).Exec(`UPDATE t_strategy_results SET c_publish_status = ? WHERE c_result_id = ? AND c_publish_status = ?`, to, resultID, from)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrNotFound
	}
	if to == PublishCancelled {
		s.cancelled(CancelRejected, 1)
	}
	return nil
}

// OutboxStats 是待投递结果的统计。
type OutboxStats struct {
	PendingCount  int64
	OldestPending time.Time
}

// PendingOutboxStats 返回待投递结果的数量与最早创建时间。
func (s *Store) PendingOutboxStats(ctx context.Context) (OutboxStats, error) {
	var stats OutboxStats
	if err := s.db.WithContext(ctx).Raw(`SELECT COUNT(*) FROM t_strategy_results WHERE c_publish_status = 'pending' AND c_event_data IS NOT NULL`).Scan(&stats.PendingCount).Error; err != nil {
		return OutboxStats{}, err
	}
	if stats.PendingCount == 0 {
		return stats, nil
	}
	var oldest struct {
		CreatedAt time.Time `gorm:"column:c_ctime"`
	}
	if err := s.db.WithContext(ctx).Raw(`SELECT c_ctime FROM t_strategy_results WHERE c_publish_status = 'pending' AND c_event_data IS NOT NULL ORDER BY c_ctime, c_result_id LIMIT 1`).Scan(&oldest).Error; err != nil {
		return OutboxStats{}, err
	}
	stats.OldestPending = oldest.CreatedAt.UTC()
	return stats, nil
}
