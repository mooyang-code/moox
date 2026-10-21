package seriesreplay

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "github.com/glebarez/go-sqlite"
	"github.com/mooyang-code/moox/packages/seriesreplay/schema"
)

// Store 是回放服务自己的 SQLite：任务状态与周期记录。多个进程可以同时打开（CLI 暂停、取消、查询，运行进程推进）。
type Store struct{ db *sql.DB }

// OpenStore 打开（必要时创建）回放库。path 为 ":memory:" 时是内存库（测试用）。
func OpenStore(path string) (*Store, error) {
	dsn := path
	if path != ":memory:" {
		dsn = "file:" + path + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if path == ":memory:" {
		db.SetMaxOpenConns(1)
	}
	if _, err := db.Exec(schema.AllSQL()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("初始化回放库：%w", err)
	}
	return &Store{db: db}, nil
}

// Close 关闭回放库。
func (s *Store) Close() error { return s.db.Close() }

// Run 是一个回放任务的状态。
type Run struct {
	ReplayID      string
	Spec          Spec
	Binding       StrategyBinding
	Status        string
	Error         string
	PeriodsTotal  int
	PeriodsDone   int
	CurrentPeriod int64
}

// ErrNotFound 表示回放任务或周期记录不存在。
var ErrNotFound = errors.New("回放记录不存在")

const ts = time.RFC3339

// CreateRun 创建任务；已存在时返回已有任务（created 为 false，规格以先创建的为准）。
func (s *Store) CreateRun(spec Spec, total int) (Run, bool, error) {
	raw, err := json.Marshal(spec)
	if err != nil {
		return Run{}, false, err
	}
	res, err := s.db.Exec(`INSERT INTO t_replay_runs (c_replay_id, c_spec_json, c_periods_total) VALUES (?, ?, ?) ON CONFLICT (c_replay_id) DO NOTHING`,
		spec.ReplayID, string(raw), total)
	if err != nil {
		return Run{}, false, err
	}
	n, _ := res.RowsAffected()
	run, err := s.GetRun(spec.ReplayID)
	return run, n > 0, err
}

// GetRun 读取任务。
func (s *Store) GetRun(id string) (Run, error) {
	var run Run
	var spec, binding string
	err := s.db.QueryRow(`SELECT c_replay_id, c_spec_json, c_binding_json, c_status, c_error, c_periods_total, c_periods_done, c_current_period FROM t_replay_runs WHERE c_replay_id = ?`, id).
		Scan(&run.ReplayID, &spec, &binding, &run.Status, &run.Error, &run.PeriodsTotal, &run.PeriodsDone, &run.CurrentPeriod)
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, ErrNotFound
	}
	if err != nil {
		return Run{}, err
	}
	if err := json.Unmarshal([]byte(spec), &run.Spec); err != nil {
		return Run{}, err
	}
	if err := json.Unmarshal([]byte(binding), &run.Binding); err != nil {
		return Run{}, err
	}
	return run, nil
}

// ListRuns 返回全部任务（按创建时间倒序）。
func (s *Store) ListRuns() ([]Run, error) {
	rows, err := s.db.Query(`SELECT c_replay_id FROM t_replay_runs ORDER BY c_ctime DESC, c_replay_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	runs := make([]Run, 0, len(ids))
	for _, id := range ids {
		run, err := s.GetRun(id)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, nil
}

// SetBinding 固定任务绑定的策略版本与因子指纹。
func (s *Store) SetBinding(id string, binding StrategyBinding, total int) error {
	raw, err := json.Marshal(binding)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE t_replay_runs SET c_binding_json = ?, c_periods_total = ?, c_mtime = ? WHERE c_replay_id = ?`, string(raw), total, time.Now().UTC().Format(ts), id)
	return err
}

// SetStatus 更新任务状态；从取消状态不能再改回运行。pause 只能把运行中改成暂停，resume 只能把暂停改回运行。
func (s *Store) SetStatus(id, status, errText string) error {
	_, err := s.db.Exec(`UPDATE t_replay_runs SET c_status = ?, c_error = ?, c_mtime = ? WHERE c_replay_id = ?`, status, errText, time.Now().UTC().Format(ts), id)
	return err
}

// Transition 在当前状态属于 from 之一时把状态改成 to，返回是否改动（控制命令用：暂停、继续、取消）。
func (s *Store) Transition(id, to string, from ...string) (bool, error) {
	if len(from) == 0 {
		return false, errors.New("from 不能为空")
	}
	marks, args := "", []any{to, time.Now().UTC().Format(ts), id}
	for i, f := range from {
		if i > 0 {
			marks += ", "
		}
		marks += "?"
		args = append(args, f)
	}
	res, err := s.db.Exec(`UPDATE t_replay_runs SET c_status = ?, c_mtime = ? WHERE c_replay_id = ? AND c_status IN (`+marks+`)`, args...)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// SetProgress 更新进度。
func (s *Store) SetProgress(id string, done int, current int64) error {
	_, err := s.db.Exec(`UPDATE t_replay_runs SET c_periods_done = ?, c_current_period = ?, c_mtime = ? WHERE c_replay_id = ?`, done, current, time.Now().UTC().Format(ts), id)
	return err
}

// Record 是一期的周期记录。
type Record struct {
	ReplayID      string
	Period        int64
	Decision      StrategyDecision
	State         json.RawMessage
	Advance       TradeAdvanceRequest
	TradeAcked    bool
	FactorWritten bool
	Complete      bool
	FactorMillis  int64
	StrategyMs    int64
	TradeMillis   int64
}

// SaveRecord 保存周期记录（决策、下一期策略状态与待发送的命令一起落库）；已经存在时保持原记录不变（同一期重复执行返回同一条记录）。
func (s *Store) SaveRecord(r Record) error {
	decision, err := json.Marshal(r.Decision)
	if err != nil {
		return err
	}
	advance, err := json.Marshal(r.Advance)
	if err != nil {
		return err
	}
	state := r.State
	if len(state) == 0 {
		state = json.RawMessage("{}")
	}
	_, err = s.db.Exec(`INSERT INTO t_replay_period_records (c_replay_id, c_period, c_decision_json, c_state_json, c_advance_json, c_factor_ms, c_strategy_ms)
VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT (c_replay_id, c_period) DO NOTHING`, r.ReplayID, r.Period, string(decision), string(state), string(advance), r.FactorMillis, r.StrategyMs)
	return err
}

// GetRecord 读取周期记录。
func (s *Store) GetRecord(id string, period int64) (Record, error) {
	var r Record
	var decision, state, advance string
	var acked, written, complete int
	err := s.db.QueryRow(`SELECT c_replay_id, c_period, c_decision_json, c_state_json, c_advance_json, c_trade_acked, c_factor_written, c_complete, c_factor_ms, c_strategy_ms, c_trade_ms
FROM t_replay_period_records WHERE c_replay_id = ? AND c_period = ?`, id, period).
		Scan(&r.ReplayID, &r.Period, &decision, &state, &advance, &acked, &written, &complete, &r.FactorMillis, &r.StrategyMs, &r.TradeMillis)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, err
	}
	if err := json.Unmarshal([]byte(decision), &r.Decision); err != nil {
		return Record{}, err
	}
	if err := json.Unmarshal([]byte(advance), &r.Advance); err != nil {
		return Record{}, err
	}
	r.State, r.TradeAcked, r.FactorWritten, r.Complete = json.RawMessage(state), acked == 1, written == 1, complete == 1
	return r, nil
}

// MarkTradeAcked 标记 Trade 已确认这一期。
func (s *Store) MarkTradeAcked(id string, period, millis int64) error {
	_, err := s.db.Exec(`UPDATE t_replay_period_records SET c_trade_acked = 1, c_trade_ms = ?, c_mtime = ? WHERE c_replay_id = ? AND c_period = ?`, millis, time.Now().UTC().Format(ts), id, period)
	return err
}

// MarkFactorWritten 标记因子结果已写回。
func (s *Store) MarkFactorWritten(id string, period int64) error {
	_, err := s.db.Exec(`UPDATE t_replay_period_records SET c_factor_written = 1, c_mtime = ? WHERE c_replay_id = ? AND c_period = ?`, time.Now().UTC().Format(ts), id, period)
	return err
}

// MarkComplete 标记这一期的全部阶段已完成。
func (s *Store) MarkComplete(id string, period int64) error {
	_, err := s.db.Exec(`UPDATE t_replay_period_records SET c_complete = 1, c_mtime = ? WHERE c_replay_id = ? AND c_period = ?`, time.Now().UTC().Format(ts), id, period)
	return err
}

// DeleteRun 删除任务及其周期记录。
func (s *Store) DeleteRun(id string) error {
	_, err := s.db.Exec(`DELETE FROM t_replay_runs WHERE c_replay_id = ?`, id)
	return err
}

// CompletedPeriods 返回已完成的周期数。
func (s *Store) CompletedPeriods(id string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM t_replay_period_records WHERE c_replay_id = ? AND c_complete = 1`, id).Scan(&n)
	return n, err
}
