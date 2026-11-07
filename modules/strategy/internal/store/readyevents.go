package store

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"
)

// ReadyEvent 是绑定多个因子任务的实例收到的一个 View 的就绪事件（原始 ViewDataReady 按字节保存）。
type ReadyEvent struct {
	InstanceID string
	SessionID  string
	ViewID     string
	BarEnd     time.Time
	EventID    string
	Payload    []byte
	ArrivedAt  time.Time
}

// PutReadyEvent 记录一个 View 的就绪事件；(实例, 会话, bar_end, View) 重复时保留先到的那条，返回是否新写入。
func (s *Store) PutReadyEvent(ctx context.Context, event ReadyEvent) (bool, error) {
	if strings.TrimSpace(event.InstanceID) == "" || strings.TrimSpace(event.SessionID) == "" || strings.TrimSpace(event.ViewID) == "" || len(event.Payload) == 0 {
		return false, errors.New("就绪事件缺少实例、会话、View 或内容")
	}
	arrived, err := requireTime(event.ArrivedAt)
	if err != nil {
		return false, err
	}
	result := s.db.WithContext(ctx).Exec(`
		INSERT OR IGNORE INTO t_strategy_ready_events (c_instance_id, c_session_id, c_view_id, c_bar_end_time, c_event_id, c_event_bytes, c_ctime)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, event.InstanceID, event.SessionID, event.ViewID, millis(event.BarEnd), event.EventID, event.Payload, arrived)
	return result.RowsAffected == 1, result.Error
}

type readyEventRow struct {
	ViewID  string    `gorm:"column:c_view_id"`
	BarEnd  int64     `gorm:"column:c_bar_end_time"`
	EventID string    `gorm:"column:c_event_id"`
	Payload []byte    `gorm:"column:c_event_bytes"`
	Arrived time.Time `gorm:"column:c_ctime"`
}

// ReadyEventsAt 返回本会话某个周期已到达的就绪事件（按 View 排序）。
func (s *Store) ReadyEventsAt(ctx context.Context, instanceID, sessionID string, barEnd time.Time) ([]ReadyEvent, error) {
	var rows []readyEventRow
	if err := s.db.WithContext(ctx).Raw(`
		SELECT c_view_id, c_bar_end_time, c_event_id, c_event_bytes, c_ctime FROM t_strategy_ready_events
		WHERE c_instance_id = ? AND c_session_id = ? AND c_bar_end_time = ? ORDER BY c_view_id
	`, instanceID, sessionID, millis(barEnd)).Scan(&rows).Error; err != nil {
		return nil, err
	}
	events := make([]ReadyEvent, 0, len(rows))
	for _, row := range rows {
		events = append(events, ReadyEvent{InstanceID: instanceID, SessionID: sessionID, ViewID: row.ViewID, BarEnd: fromMillis(row.BarEnd), EventID: row.EventID, Payload: row.Payload, ArrivedAt: row.Arrived.UTC()})
	}
	return events, nil
}

// UnprocessedReadyPeriods 返回本会话晚于 after（已处理到的那一期，零值表示没有）、早于 before、有到达记录但还没有结果的周期（升序），
// 以及每个周期已到达的 View。
func (s *Store) UnprocessedReadyPeriods(ctx context.Context, instanceID, sessionID string, after, before time.Time) (map[time.Time][]string, []time.Time, error) {
	var rows []struct {
		ViewID string `gorm:"column:c_view_id"`
		BarEnd int64  `gorm:"column:c_bar_end_time"`
	}
	if err := s.db.WithContext(ctx).Raw(`
		SELECT e.c_view_id, e.c_bar_end_time FROM t_strategy_ready_events e
		WHERE e.c_instance_id = ? AND e.c_session_id = ? AND e.c_bar_end_time > ? AND e.c_bar_end_time < ?
		AND NOT EXISTS (
			SELECT 1 FROM t_strategy_results r
			WHERE r.c_instance_id = e.c_instance_id AND r.c_session_id = e.c_session_id AND r.c_bar_end_time = e.c_bar_end_time
		)
		ORDER BY e.c_bar_end_time, e.c_view_id
	`, instanceID, sessionID, afterMillis(after), millis(before)).Scan(&rows).Error; err != nil {
		return nil, nil, err
	}
	views := map[time.Time][]string{}
	var periods []time.Time
	for _, row := range rows {
		at := fromMillis(row.BarEnd)
		if _, seen := views[at]; !seen {
			periods = append(periods, at)
		}
		views[at] = append(views[at], row.ViewID)
	}
	return views, periods, nil
}

// LatestReadyByView 返回本会话给定的每个 View 已到达的最晚一期；没有记录的 View 不出现在结果里。
// 每个 View 各查一次“按 bar_end 倒序取一行”：沿主键（实例, 会话, bar_end, View）从最新一期往回找，命中即停，不扫整个会话的记录。
func (s *Store) LatestReadyByView(ctx context.Context, instanceID, sessionID string, viewIDs []string) (map[string]time.Time, error) {
	latest := make(map[string]time.Time, len(viewIDs))
	for _, viewID := range viewIDs {
		var barEnds []int64
		if err := s.db.WithContext(ctx).Raw(`
			SELECT c_bar_end_time FROM t_strategy_ready_events
			WHERE c_instance_id = ? AND c_session_id = ? AND c_view_id = ?
			ORDER BY c_bar_end_time DESC LIMIT 1
		`, instanceID, sessionID, viewID).Scan(&barEnds).Error; err != nil {
			return nil, err
		}
		if len(barEnds) > 0 {
			latest[viewID] = fromMillis(barEnds[0])
		}
	}
	return latest, nil
}

// DeleteReadyEventsBefore 删除到达时间早于 cutoff 的就绪事件记录（分批，不在单连接上长时间阻塞）。
func (s *Store) DeleteReadyEventsBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	var total int64
	for {
		result := s.db.WithContext(ctx).Exec(`
			DELETE FROM t_strategy_ready_events
			WHERE rowid IN (SELECT rowid FROM t_strategy_ready_events WHERE c_ctime < ? LIMIT ?)
		`, cutoff.UTC(), retentionBatch)
		if result.Error != nil {
			return total, result.Error
		}
		total += result.RowsAffected
		if result.RowsAffected < retentionBatch {
			return total, nil
		}
	}
}

// afterMillis 是“晚于 after”查询的下界；零值时间不限下界。
func afterMillis(after time.Time) int64 {
	if after.IsZero() {
		return math.MinInt64
	}
	return millis(after)
}
