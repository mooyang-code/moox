package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
)

// Session 是一次启用：固化当时的 DSL 版本与绑定解析。
type Session struct {
	SessionID    string
	InstanceID   string
	DSLHash      string
	ResolvedJSON string
	// MembersJSON 是策略池固化的成员（input.PoolMember 列表的 JSON），非池为空。
	MembersJSON string
	// WarmState 是策略池预热完成时的引擎状态（engine.State 的 JSON）；WarmedThrough 是预热推进到的最后一期（bar_end），
	// nil 表示预热还没完成。非池会话两者都为空。
	WarmState     string
	WarmedThrough *time.Time
	CreatedAt     time.Time
	ClosedAt      *time.Time
}

type sessionRow struct {
	SessionID     string        `gorm:"column:c_session_id"`
	InstanceID    string        `gorm:"column:c_instance_id"`
	DSLHash       string        `gorm:"column:c_dsl_hash"`
	ResolvedJSON  string        `gorm:"column:c_resolved_json"`
	MembersJSON   string        `gorm:"column:c_members_json"`
	WarmState     string        `gorm:"column:c_warm_state"`
	WarmedThrough sql.NullInt64 `gorm:"column:c_warmed_through"`
	CreatedAt     time.Time     `gorm:"column:c_ctime"`
	ClosedAt      sql.NullTime  `gorm:"column:c_closed_at"`
}

func (r sessionRow) session() Session {
	session := Session{SessionID: r.SessionID, InstanceID: r.InstanceID, DSLHash: r.DSLHash, ResolvedJSON: r.ResolvedJSON, MembersJSON: r.MembersJSON, WarmState: r.WarmState, CreatedAt: r.CreatedAt.UTC(), ClosedAt: nullableTime(r.ClosedAt)}
	if r.WarmedThrough.Valid {
		through := fromMillis(r.WarmedThrough.Int64)
		session.WarmedThrough = &through
	}
	return session
}

// 会话的 SQL：整条写成常量，不在调用处拼接。
const (
	// sqlInsertSession 写入一条未关闭的会话快照。
	sqlInsertSession = `INSERT INTO t_strategy_sessions (c_session_id, c_instance_id, c_dsl_hash, c_resolved_json, c_members_json, c_ctime, c_closed_at) VALUES (?, ?, ?, ?, ?, ?, NULL)`
	// sqlSelectSessionByID 按会话 ID 读取会话。
	sqlSelectSessionByID = `SELECT c_session_id, c_instance_id, c_dsl_hash, c_resolved_json, c_members_json, c_warm_state, c_warmed_through, c_ctime, c_closed_at
		FROM t_strategy_sessions WHERE c_session_id = ?`
	// sqlListSessionsOfInstance 按创建时间倒序列出实例的会话。
	sqlListSessionsOfInstance = `SELECT c_session_id, c_instance_id, c_dsl_hash, c_resolved_json, c_members_json, c_warm_state, c_warmed_through, c_ctime, c_closed_at
		FROM t_strategy_sessions WHERE c_instance_id = ? ORDER BY c_ctime DESC, c_session_id`
	// sqlCloseSession 记录会话结束时间（只关闭还没关闭的会话）。
	sqlCloseSession = `UPDATE t_strategy_sessions SET c_closed_at = ? WHERE c_session_id = ? AND c_closed_at IS NULL`
	// sqlSetSessionWarmed 比较交换写入策略池预热结果：只写未关闭、有成员快照、还没有预热结果的会话。
	sqlSetSessionWarmed = `UPDATE t_strategy_sessions SET c_warm_state = ?, c_warmed_through = ? WHERE c_session_id = ? AND c_closed_at IS NULL AND c_members_json <> '' AND c_warmed_through IS NULL`
	// sqlListPendingWarmSessions 找出未关闭、挂在启用实例上、有成员快照而还没有预热结果的会话。
	sqlListPendingWarmSessions = `SELECT c_session_id, c_instance_id, c_dsl_hash, c_resolved_json, c_members_json, c_warm_state, c_warmed_through, c_ctime, c_closed_at
		FROM t_strategy_sessions
		WHERE c_closed_at IS NULL AND c_members_json <> '' AND c_warmed_through IS NULL
		AND c_session_id IN (SELECT c_session_id FROM t_strategy_instances WHERE c_enabled = 1 AND c_session_id IS NOT NULL)
		ORDER BY c_ctime`
)

// OpenSession 写入会话快照；dslYaml 用于登记 DSL 版本（已存在则忽略）。
func (s *Store) OpenSession(ctx context.Context, session Session, dslYaml string) error {
	if strings.TrimSpace(session.SessionID) == "" || strings.TrimSpace(session.InstanceID) == "" || strings.TrimSpace(session.DSLHash) == "" || strings.TrimSpace(dslYaml) == "" {
		return errors.New("会话缺少 session_id、instance_id、dsl_hash 或 DSL 文本")
	}
	now, err := requireTime(session.CreatedAt)
	if err != nil {
		return err
	}
	resolved, err := jsonObjectOrEmpty([]byte(session.ResolvedJSON))
	if err != nil {
		return err
	}
	if session.MembersJSON != "" {
		var members []json.RawMessage
		if err := json.Unmarshal([]byte(session.MembersJSON), &members); err != nil || len(members) == 0 {
			return errors.New("members_json 必须是非空的 JSON 数组")
		}
	}
	return s.transaction(ctx, func(tx *gorm.DB) error {
		if err := ensureDefinitionVersion(tx, session.DSLHash, dslYaml, now); err != nil {
			return err
		}
		return tx.Exec(sqlInsertSession, session.SessionID, session.InstanceID, session.DSLHash, resolved, session.MembersJSON, now).Error
	})
}

// GetSession 读取会话快照。
func (s *Store) GetSession(ctx context.Context, sessionID string) (Session, error) {
	var row sessionRow
	if err := s.db.WithContext(ctx).Raw(sqlSelectSessionByID, sessionID).Scan(&row).Error; err != nil {
		return Session{}, err
	}
	if row.SessionID == "" {
		return Session{}, ErrNotFound
	}
	return row.session(), nil
}

// ListSessions 按创建时间倒序列出实例的会话。
func (s *Store) ListSessions(ctx context.Context, instanceID string) ([]Session, error) {
	var rows []sessionRow
	if err := s.db.WithContext(ctx).Raw(sqlListSessionsOfInstance, instanceID).Scan(&rows).Error; err != nil {
		return nil, err
	}
	sessions := make([]Session, 0, len(rows))
	for _, row := range rows {
		sessions = append(sessions, row.session())
	}
	return sessions, nil
}

// CloseSession 记录会话结束时间（停用或启用失败）。
func (s *Store) CloseSession(ctx context.Context, sessionID string, at time.Time) error {
	now, err := requireTime(at)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Exec(sqlCloseSession, now, sessionID).Error
}

// SetSessionWarmed 写入策略池预热的结果（预热完成时的引擎状态与推进到的最后一期）：比较交换，只在会话未关闭、
// 且还没有预热结果时写入，返回是否写入（会话已被停用或已经预热过时为 false）。
func (s *Store) SetSessionWarmed(ctx context.Context, sessionID, state string, through time.Time) (bool, error) {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(state) == "" || through.IsZero() {
		return false, errors.New("预热结果缺少 session_id、状态或终点")
	}
	result := s.db.WithContext(ctx).Exec(sqlSetSessionWarmed, state, millis(through), sessionID)
	return result.RowsAffected == 1, result.Error
}

// PendingWarmSessions 返回还没有完成预热的策略池会话（未关闭、挂在启用实例上、有成员快照）：进程启动时据此重新开始预热。
func (s *Store) PendingWarmSessions(ctx context.Context) ([]Session, error) {
	var rows []sessionRow
	if err := s.db.WithContext(ctx).Raw(sqlListPendingWarmSessions).Scan(&rows).Error; err != nil {
		return nil, err
	}
	sessions := make([]Session, 0, len(rows))
	for _, row := range rows {
		sessions = append(sessions, row.session())
	}
	return sessions, nil
}
