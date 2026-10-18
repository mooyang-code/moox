package store

import (
	"context"
	"database/sql"
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
	CreatedAt    time.Time
	ClosedAt     *time.Time
}

type sessionRow struct {
	SessionID    string       `gorm:"column:c_session_id"`
	InstanceID   string       `gorm:"column:c_instance_id"`
	DSLHash      string       `gorm:"column:c_dsl_hash"`
	ResolvedJSON string       `gorm:"column:c_resolved_json"`
	CreatedAt    time.Time    `gorm:"column:c_ctime"`
	ClosedAt     sql.NullTime `gorm:"column:c_closed_at"`
}

func (r sessionRow) session() Session {
	return Session{SessionID: r.SessionID, InstanceID: r.InstanceID, DSLHash: r.DSLHash, ResolvedJSON: r.ResolvedJSON, CreatedAt: r.CreatedAt.UTC(), ClosedAt: nullableTime(r.ClosedAt)}
}

const sessionColumns = "c_session_id, c_instance_id, c_dsl_hash, c_resolved_json, c_ctime, c_closed_at"

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
	return s.transaction(ctx, func(tx *gorm.DB) error {
		if err := ensureDefinitionVersion(tx, session.DSLHash, dslYaml, now); err != nil {
			return err
		}
		return tx.Exec(`INSERT INTO t_strategy_sessions (c_session_id, c_instance_id, c_dsl_hash, c_resolved_json, c_ctime, c_closed_at) VALUES (?, ?, ?, ?, ?, NULL)`, session.SessionID, session.InstanceID, session.DSLHash, resolved, now).Error
	})
}

// GetSession 读取会话快照。
func (s *Store) GetSession(ctx context.Context, sessionID string) (Session, error) {
	var row sessionRow
	if err := s.db.WithContext(ctx).Raw(`SELECT `+sessionColumns+` FROM t_strategy_sessions WHERE c_session_id = ?`, sessionID).Scan(&row).Error; err != nil {
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
	if err := s.db.WithContext(ctx).Raw(`SELECT `+sessionColumns+` FROM t_strategy_sessions WHERE c_instance_id = ? ORDER BY c_ctime DESC, c_session_id`, instanceID).Scan(&rows).Error; err != nil {
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
	return s.db.WithContext(ctx).Exec(`UPDATE t_strategy_sessions SET c_closed_at = ? WHERE c_session_id = ? AND c_closed_at IS NULL`, now, sessionID).Error
}
