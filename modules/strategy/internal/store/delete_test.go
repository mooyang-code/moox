package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// seedInstanceRows 给一个停用、没有会话的实例在每张带实例 ID 的表里各写一条记录，并写一条它的 View 的年龄探针记录。
func seedInstanceRows(t *testing.T, repo *Store, instanceID, taskID, viewID string) {
	t.Helper()
	ctx := context.Background()
	if err := repo.CreateInstance(ctx, Instance{InstanceID: instanceID, StrategyID: "s1", SpaceID: "space", FactorTasks: map[string]string{"spot": taskID}, CreatedAt: testNow, UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	sessionID, resultID, replayID := "sess_"+instanceID, "res_"+instanceID, "rep_"+instanceID
	at := testNow.Format(time.RFC3339)
	statements := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO t_strategy_sessions (c_session_id, c_instance_id, c_dsl_hash, c_resolved_json, c_ctime, c_closed_at) VALUES (?, ?, ?, '{}', ?, ?)`,
			[]any{sessionID, instanceID, testHash, at, at}},
		{`INSERT INTO t_strategy_results (c_result_id, c_instance_id, c_session_id, c_bar_end_time, c_valid_until, c_status, c_dsl_hash, c_input_json, c_targets_json, c_rule_states_json, c_summary_json)
			VALUES (?, ?, ?, 1, 2, 'ok', ?, '{}', '[]', '{}', '{}')`, []any{resultID, instanceID, sessionID, testHash}},
		{`INSERT INTO t_strategy_result_items (c_result_id, c_rule_id, c_instrument_id, c_stage, c_ctime) VALUES (?, 'r', 'BTC-USDT', 'weighted', ?)`,
			[]any{resultID, at}},
		{`INSERT INTO t_strategy_ready_events (c_instance_id, c_session_id, c_view_id, c_bar_end_time, c_event_id, c_event_bytes) VALUES (?, ?, ?, 1, 'evt', x'00')`,
			[]any{instanceID, sessionID, viewID}},
		{`INSERT INTO t_strategy_replays (c_replay_id, c_instance_id, c_session_id, c_dsl_yaml, c_dsl_hash, c_calendar, c_space_id, c_tasks_json, c_start_time, c_end_time, c_status)
			VALUES (?, ?, ?, 'name: demo', ?, 'crypto_24x7', 'space', '{}', 1, 2, 'done')`, []any{replayID, instanceID, sessionID, testHash}},
		{`INSERT INTO t_strategy_replay_bars (c_replay_id, c_bar_end_time, c_status, c_targets_json, c_positions_json, c_summary_json) VALUES (?, 1, 'ok', '[]', '{}', '{}')`,
			[]any{replayID}},
		{`INSERT INTO t_strategy_display_settings (c_instance_id) VALUES (?)`, []any{instanceID}},
		{`INSERT INTO t_strategy_age_passed (c_space_id, c_view_id, c_index_id, c_min_age_bars, c_market, c_subject_id) VALUES ('space', ?, 'idx', 10, 'spot', 'BTC-USDT')`,
			[]any{viewID}},
	}
	for _, statement := range statements {
		if err := repo.db.Exec(statement.sql, statement.args...).Error; err != nil {
			t.Fatalf("写入种子数据失败：%v\n%s", err, statement.sql)
		}
	}
}

// tablesWithInstanceID 从库里动态找出所有带 c_instance_id 列的表：以后新增的表自动纳入删除检查。
func tablesWithInstanceID(t *testing.T, repo *Store) []string {
	t.Helper()
	var tables []string
	if err := repo.db.Raw(`SELECT m.name FROM sqlite_master m JOIN pragma_table_info(m.name) c
		WHERE m.type = 'table' AND m.name LIKE 't\_strategy\_%' ESCAPE '\' AND c.name = 'c_instance_id' ORDER BY m.name`).Scan(&tables).Error; err != nil {
		t.Fatal(err)
	}
	if len(tables) == 0 {
		t.Fatal("没有找到带 c_instance_id 的表")
	}
	return tables
}

func countRows(t *testing.T, repo *Store, query string, args ...any) int64 {
	t.Helper()
	var count int64
	if err := repo.db.Raw(query, args...).Scan(&count).Error; err != nil {
		t.Fatal(err)
	}
	return count
}

func instanceRowCounts(t *testing.T, repo *Store, tables []string, instanceID string) map[string]int64 {
	t.Helper()
	counts := make(map[string]int64, len(tables)+2)
	for _, table := range tables {
		counts[table] = countRows(t, repo, fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE c_instance_id = ?", table), instanceID)
	}
	counts["t_strategy_result_items"] = countRows(t, repo, `SELECT COUNT(*) FROM t_strategy_result_items WHERE c_result_id = ?`, "res_"+instanceID)
	counts["t_strategy_replay_bars"] = countRows(t, repo, `SELECT COUNT(*) FROM t_strategy_replay_bars WHERE c_replay_id = ?`, "rep_"+instanceID)
	return counts
}

// DeleteInstance 删掉实例在每张带 c_instance_id 的表里的记录（以及级联的明细和回放记录、按 View 存的年龄探针），
// 不动其他实例的记录。
func TestDeleteInstanceRemovesEveryInstanceRow(t *testing.T) {
	ctx := context.Background()
	repo := openTestStore(t)
	if err := repo.CreateDefinition(ctx, Definition{StrategyID: "s1", Name: "demo", DSLYaml: "name: demo", DSLHash: testHash, CreatedAt: testNow, UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	seedInstanceRows(t, repo, "inst_del", "ftask_del", "view_factor_del")
	seedInstanceRows(t, repo, "inst_keep", "ftask_keep", "view_factor_keep")
	tables := tablesWithInstanceID(t, repo)
	for table, count := range instanceRowCounts(t, repo, tables, "inst_del") {
		if count == 0 {
			t.Fatalf("表 %s 没有种子数据：新增带实例 ID 的表时，要在这里补种子数据，并在 DeleteInstance 里删除", table)
		}
	}

	if err := repo.DeleteInstance(ctx, "inst_del", "space", []string{"view_factor_del"}); err != nil {
		t.Fatal(err)
	}
	for table, count := range instanceRowCounts(t, repo, tables, "inst_del") {
		if count != 0 {
			t.Errorf("表 %s 还剩 %d 条被删实例的记录", table, count)
		}
	}
	for table, count := range instanceRowCounts(t, repo, tables, "inst_keep") {
		if count == 0 {
			t.Errorf("表 %s 里另一个实例的记录被误删", table)
		}
	}
	if count := countRows(t, repo, `SELECT COUNT(*) FROM t_strategy_age_passed WHERE c_view_id = ?`, "view_factor_del"); count != 0 {
		t.Errorf("被删实例 View 的年龄探针记录还剩 %d 条", count)
	}
	if count := countRows(t, repo, `SELECT COUNT(*) FROM t_strategy_age_passed WHERE c_view_id = ?`, "view_factor_keep"); count != 1 {
		t.Errorf("另一个实例 View 的年龄探针记录应保留，实际 %d 条", count)
	}
	if _, err := repo.GetInstance(ctx, "inst_del"); err != ErrNotFound {
		t.Fatalf("实例应已删除，得到 %v", err)
	}
	// 因子任务登记已经释放：同一个任务可以登记到新实例上。
	if err := repo.CreateInstance(ctx, Instance{InstanceID: "inst_new", StrategyID: "s1", SpaceID: "space", FactorTasks: map[string]string{"spot": "ftask_del"}, CreatedAt: testNow, UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
}

// 启用中或还挂着会话的实例什么也不删；不存在的实例返回 ErrNotFound。
func TestDeleteInstanceRejectsActiveInstance(t *testing.T) {
	ctx := context.Background()
	repo := openTestStore(t)
	seedEnabledInstance(t, repo, "inst_on", "sess_on", nil)
	err := repo.DeleteInstance(ctx, "inst_on", "space", nil)
	if err == nil || !strings.Contains(err.Error(), "不能删除") {
		t.Fatalf("启用中的实例应拒绝删除，得到 %v", err)
	}
	if _, err := repo.GetInstance(ctx, "inst_on"); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteInstance(ctx, "missing", "space", nil); err != ErrNotFound {
		t.Fatalf("不存在的实例应返回 ErrNotFound，得到 %v", err)
	}
}
