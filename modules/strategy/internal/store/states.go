package store

import (
	"context"
	"encoding/json"
	"time"

	"gorm.io/gorm"
)

// State 是实例的规则状态：每个实例一行，是引擎下一期求值的起点。
type State struct {
	InstanceID string
	// SessionID 与 DSLHash 是状态当前所属的会话与 DSL 版本。
	SessionID string
	DSLHash   string
	// BarEndTime 是状态推进到的那一期（bar_end）。
	BarEndTime time.Time
	// StateJSON 是 engine.State 的 JSON 对象。
	StateJSON json.RawMessage
	UpdatedAt time.Time
}

type stateRow struct {
	InstanceID string    `gorm:"column:c_instance_id"`
	SessionID  string    `gorm:"column:c_session_id"`
	DSLHash    string    `gorm:"column:c_dsl_hash"`
	BarEndTime int64     `gorm:"column:c_bar_end_time"`
	StateJSON  string    `gorm:"column:c_state_json"`
	UpdatedAt  time.Time `gorm:"column:c_mtime"`
}

// 规则状态的 SQL：整条写成常量，不在调用处拼接。
const (
	// sqlSelectState 读取实例的规则状态。
	sqlSelectState = `SELECT c_instance_id, c_session_id, c_dsl_hash, c_bar_end_time, c_state_json, c_mtime FROM t_strategy_states WHERE c_instance_id = ?`
	// sqlUpsertState 写入或覆盖实例的规则状态。
	sqlUpsertState = `
	INSERT INTO t_strategy_states (c_instance_id, c_session_id, c_dsl_hash, c_bar_end_time, c_state_json, c_mtime)
	VALUES (?, ?, ?, ?, ?, ?)
	ON CONFLICT (c_instance_id) DO UPDATE SET c_session_id = excluded.c_session_id, c_dsl_hash = excluded.c_dsl_hash,
		c_bar_end_time = excluded.c_bar_end_time, c_state_json = excluded.c_state_json, c_mtime = excluded.c_mtime`
	// sqlRebindState 把实例的规则状态改挂到新会话（状态与它推进到的那一期不变）。
	sqlRebindState = `UPDATE t_strategy_states SET c_session_id = ?, c_mtime = ? WHERE c_instance_id = ?`
	// sqlDeleteState 删除实例的规则状态。
	sqlDeleteState = `DELETE FROM t_strategy_states WHERE c_instance_id = ?`
	// sqlDeleteStateOfSession 删除实例挂在指定会话上的规则状态（状态无法解析时随那一期的记录同事务清掉）。
	sqlDeleteStateOfSession = `DELETE FROM t_strategy_states WHERE c_instance_id = ? AND c_session_id = ?`
)

func readState(tx *gorm.DB, instanceID string) (stateRow, bool, error) {
	var row stateRow
	if err := tx.Raw(sqlSelectState, instanceID).Scan(&row).Error; err != nil {
		return stateRow{}, false, err
	}
	return row, row.InstanceID != "", nil
}

// upsertState 在调用方的事务里写入或覆盖实例的规则状态。
func upsertState(tx *gorm.DB, state State, now time.Time) error {
	return tx.Exec(sqlUpsertState, state.InstanceID, state.SessionID, state.DSLHash, millis(state.BarEndTime), string(state.StateJSON), now.UTC()).Error
}

// carryState 在打开新会话的事务里决定实例已有的规则状态是否沿用：状态所属会话固化的 DSL 版本、绑定解析与策略池成员
// 和新会话的逐字节一致时，把状态改挂到新会话（内容与它推进到的那一期不变）；任何一项不同，或原会话已读不到，就删除，
// 新会话从头开始。没有状态时什么也不做。
func carryState(tx *gorm.DB, instanceID, sessionID, dslHash, resolvedJSON, membersJSON string, now time.Time) error {
	state, found, err := readState(tx, instanceID)
	if err != nil || !found {
		return err
	}
	var previous sessionRow
	if err := tx.Raw(sqlSelectSessionByID, state.SessionID).Scan(&previous).Error; err != nil {
		return err
	}
	same := previous.SessionID != "" && state.DSLHash == dslHash && previous.DSLHash == dslHash &&
		previous.ResolvedJSON == resolvedJSON && previous.MembersJSON == membersJSON
	if !same {
		return tx.Exec(sqlDeleteState, instanceID).Error
	}
	return tx.Exec(sqlRebindState, sessionID, now.UTC(), instanceID).Error
}

// GetState 返回实例的规则状态；还没有状态时 found 为 false。
func (s *Store) GetState(ctx context.Context, instanceID string) (State, bool, error) {
	row, found, err := readState(s.db.WithContext(ctx), instanceID)
	if err != nil || !found {
		return State{}, false, err
	}
	return State{
		InstanceID: row.InstanceID, SessionID: row.SessionID, DSLHash: row.DSLHash, BarEndTime: fromMillis(row.BarEndTime),
		StateJSON: json.RawMessage(row.StateJSON), UpdatedAt: row.UpdatedAt.UTC(),
	}, true, nil
}
