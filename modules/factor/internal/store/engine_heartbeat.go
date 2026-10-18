package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// EngineHeartbeat 是最近一次被接受的引擎心跳。
type EngineHeartbeat struct {
	EngineID string
	BootID   string
	At       time.Time
}

// SaveEngineHeartbeat 覆盖保存最近一次心跳（单行表）。
func (s *Store) SaveEngineHeartbeat(ctx context.Context, heartbeat EngineHeartbeat) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("factor database is not open")
	}
	if strings.TrimSpace(heartbeat.EngineID) == "" || heartbeat.At.IsZero() {
		return fmt.Errorf("engine heartbeat requires an engine id and a time")
	}
	return s.db.WithContext(ctx).Exec(`
		INSERT INTO t_factor_engine_heartbeat (c_id, c_engine_id, c_boot_id, c_heartbeat_at) VALUES (1, ?, ?, ?)
		ON CONFLICT (c_id) DO UPDATE SET c_engine_id = excluded.c_engine_id, c_boot_id = excluded.c_boot_id,
			c_heartbeat_at = excluded.c_heartbeat_at, c_mtime = CURRENT_TIMESTAMP`,
		heartbeat.EngineID, heartbeat.BootID, heartbeat.At.UTC().UnixMilli()).Error
}

// LastEngineHeartbeat 返回最近一次心跳；ok 为 false 表示从未收到。
func (s *Store) LastEngineHeartbeat(ctx context.Context) (EngineHeartbeat, bool, error) {
	if s == nil || s.db == nil {
		return EngineHeartbeat{}, false, fmt.Errorf("factor database is not open")
	}
	var rows []struct {
		EngineID    string `gorm:"column:c_engine_id"`
		BootID      string `gorm:"column:c_boot_id"`
		HeartbeatAt int64  `gorm:"column:c_heartbeat_at"`
	}
	if err := s.db.WithContext(ctx).Raw(`SELECT c_engine_id, c_boot_id, c_heartbeat_at FROM t_factor_engine_heartbeat WHERE c_id = 1`).Scan(&rows).Error; err != nil {
		return EngineHeartbeat{}, false, err
	}
	if len(rows) == 0 {
		return EngineHeartbeat{}, false, nil
	}
	return EngineHeartbeat{EngineID: rows[0].EngineID, BootID: rows[0].BootID, At: time.UnixMilli(rows[0].HeartbeatAt).UTC()}, true, nil
}
