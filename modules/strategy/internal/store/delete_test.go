package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// seedInstanceRows 给一个停用、没有会话的实例在每张带实例 ID 的表里各写一条记录，并写一条它的 View 的年龄探针记录。
func seedInstanceRows(t *testing.T, repo *Store, instanceID, taskID, datasetID string) {
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
		{`INSERT INTO t_strategy_sessions (c_session_id, c_instance_id, c_dsl_hash, c_resolved_json, c_started_at, c_ctime, c_closed_at)
			VALUES (?, ?, ?, '{}', (SELECT COALESCE(MAX(c_started_at), 0) + 1 FROM t_strategy_sessions), ?, ?)`,
			[]any{sessionID, instanceID, testHash, at, at}},
		{`INSERT INTO t_strategy_results (c_result_id, c_instance_id, c_session_id, c_bar_end_time, c_valid_until, c_status, c_input_json, c_targets_json, c_summary_json)
			VALUES (?, ?, ?, 1, 2, 'ok', '{}', '[]', '{}')`, []any{resultID, instanceID, sessionID}},
		{`INSERT INTO t_strategy_result_items (c_result_id, c_rule_id, c_instrument_id, c_stage, c_ctime) VALUES (?, 'r', 'BTC-USDT', 'weighted', ?)`,
			[]any{resultID, at}},
		{`INSERT INTO t_strategy_states (c_instance_id, c_session_id, c_bar_end_time, c_state_json, c_mtime) VALUES (?, ?, 1, '{}', ?)`,
			[]any{instanceID, sessionID, at}},
		{`INSERT INTO t_strategy_ready_events (c_instance_id, c_session_id, c_dataset_id, c_bar_end_time, c_event_id, c_event_bytes) VALUES (?, ?, ?, 1, 'evt', x'00')`,
			[]any{instanceID, sessionID, datasetID}},
		{`INSERT INTO t_strategy_replays (c_replay_id, c_instance_id, c_session_id, c_dsl_yaml, c_dsl_hash, c_calendar, c_space_id, c_tasks_json, c_start_time, c_end_time, c_status)
			VALUES (?, ?, ?, 'name: demo', ?, 'crypto_24x7', 'space', '{}', 1, 2, 'done')`, []any{replayID, instanceID, sessionID, testHash}},
		{`INSERT INTO t_strategy_replay_bars (c_replay_id, c_bar_end_time, c_status, c_targets_json, c_positions_json, c_summary_json) VALUES (?, 1, 'ok', '[]', '{}', '{}')`,
			[]any{replayID}},
		{`INSERT INTO t_strategy_display_settings (c_instance_id) VALUES (?)`, []any{instanceID}},
		{`INSERT INTO t_strategy_age_passed (c_space_id, c_dataset_id, c_index_id, c_min_age_bars, c_market, c_subject_id) VALUES ('space', ?, 'idx', 10, 'spot', 'BTC-USDT')`,
			[]any{datasetID}},
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
	seedInstanceRows(t, repo, "inst_del", "ftask_del", "dataset_factor_del")
	seedInstanceRows(t, repo, "inst_keep", "ftask_keep", "dataset_factor_keep")
	tables := tablesWithInstanceID(t, repo)
	for table, count := range instanceRowCounts(t, repo, tables, "inst_del") {
		if count == 0 {
			t.Fatalf("表 %s 没有种子数据：新增带实例 ID 的表时，要在这里补种子数据，并在 DeleteInstance 里删除", table)
		}
	}

	if err := repo.DeleteInstance(ctx, "inst_del", "space", []string{"dataset_factor_del"}); err != nil {
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
	if count := countRows(t, repo, `SELECT COUNT(*) FROM t_strategy_age_passed WHERE c_dataset_id = ?`, "dataset_factor_del"); count != 0 {
		t.Errorf("被删实例 View 的年龄探针记录还剩 %d 条", count)
	}
	if count := countRows(t, repo, `SELECT COUNT(*) FROM t_strategy_age_passed WHERE c_dataset_id = ?`, "dataset_factor_keep"); count != 1 {
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

// 每张策略表都要分好类：要么是不属于任何实例的全局表，要么在删除实例时被清理（带 c_instance_id 的表、随父表级联的明细表、
// 按 View 清理的年龄探针表）。新增的表没有分类时测试失败，提醒在 DeleteInstance 里处理或登记为全局表。
func TestEveryStrategyTableIsClassifiedForInstanceDeletion(t *testing.T) {
	repo := openTestStore(t)
	global := map[string]bool{"t_strategy_defs": true, "t_strategy_def_versions": true}
	// 没有 c_instance_id、由 DeleteInstance 另外清理的表：结果明细随结果、回放明细随回放、年龄探针按 View。
	cleanedOtherwise := map[string]bool{"t_strategy_result_items": true, "t_strategy_replay_bars": true, "t_strategy_age_passed": true}
	withInstance := map[string]bool{}
	for _, table := range tablesWithInstanceID(t, repo) {
		withInstance[table] = true
	}
	var tables []string
	if err := repo.db.Raw(`SELECT name FROM sqlite_master WHERE type = 'table' AND name LIKE 't\_strategy\_%' ESCAPE '\' ORDER BY name`).Scan(&tables).Error; err != nil {
		t.Fatal(err)
	}
	if len(tables) == 0 {
		t.Fatal("没有找到策略表")
	}
	for _, table := range tables {
		if !global[table] && !cleanedOtherwise[table] && !withInstance[table] {
			t.Errorf("表 %s 没有分类：带实例数据的要在 DeleteInstance 里删除并补进删除检查，全局表登记进 global", table)
		}
		if global[table] && withInstance[table] {
			t.Errorf("表 %s 带 c_instance_id，不能登记为全局表", table)
		}
	}
	allDeletes := append(append([]string{}, instanceBatchedDeletes...), instanceFinalDeletes...)
	statements := strings.Join(allDeletes, "\n") + "\n" + sqlDeleteViewsAgePassedBatch
	for table := range withInstance {
		if table != "t_strategy_instances" && !strings.Contains(statements, "DELETE FROM "+table) {
			t.Errorf("表 %s 带 c_instance_id，但 DeleteInstance 没有删除它", table)
		}
	}
	for table := range cleanedOtherwise {
		if !strings.Contains(statements, "DELETE FROM "+table) {
			t.Errorf("表 %s 应由 DeleteInstance 清理", table)
		}
	}
}

// DeleteInstance 分多批删除大明细子表（ready_events、result_items、results、replay_bars、age_passed），
// 并在明细清完后用短事务删除实例主行；期间不动其他实例的记录。
func TestDeleteInstanceDeletesDetailRowsInBatches(t *testing.T) {
	oldBatch := deleteInstanceBatch
	deleteInstanceBatch = 2
	t.Cleanup(func() { deleteInstanceBatch = oldBatch })

	ctx := context.Background()
	repo := openTestStore(t)
	if err := repo.CreateDefinition(ctx, Definition{StrategyID: "s1", Name: "demo", DSLYaml: "name: demo", DSLHash: testHash, CreatedAt: testNow, UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	seedInstanceRows(t, repo, "inst_batch", "ftask_batch", "dataset_factor_batch")
	seedInstanceRows(t, repo, "inst_keep", "ftask_keep", "dataset_factor_keep")

	at := testNow.Format(time.RFC3339)
	for i := 2; i <= 5; i++ {
		resID := fmt.Sprintf("res_inst_batch_%d", i)
		if err := repo.db.Exec(`INSERT INTO t_strategy_results (c_result_id, c_instance_id, c_session_id, c_bar_end_time, c_valid_until, c_status, c_input_json, c_targets_json, c_summary_json)
			VALUES (?, 'inst_batch', 'sess_inst_batch', ?, ?, 'ok', '{}', '[]', '{}')`, resID, i, i+1).Error; err != nil {
			t.Fatal(err)
		}
		for j := 0; j < 2; j++ {
			if err := repo.db.Exec(`INSERT INTO t_strategy_result_items (c_result_id, c_rule_id, c_instrument_id, c_stage, c_ctime)
				VALUES (?, 'r', ?, 'weighted', ?)`, resID, fmt.Sprintf("SYM-%d-%d", i, j), at).Error; err != nil {
				t.Fatal(err)
			}
		}
		if err := repo.db.Exec(`INSERT INTO t_strategy_ready_events (c_instance_id, c_session_id, c_dataset_id, c_bar_end_time, c_event_id, c_event_bytes)
			VALUES ('inst_batch', 'sess_inst_batch', 'dataset_factor_batch', ?, 'evt', x'00')`, i).Error; err != nil {
			t.Fatal(err)
		}
		if err := repo.db.Exec(`INSERT INTO t_strategy_replay_bars (c_replay_id, c_bar_end_time, c_status, c_targets_json, c_positions_json, c_summary_json)
			VALUES ('rep_inst_batch', ?, 'ok', '[]', '{}', '{}')`, i).Error; err != nil {
			t.Fatal(err)
		}
		if err := repo.db.Exec(`INSERT INTO t_strategy_age_passed (c_space_id, c_dataset_id, c_index_id, c_min_age_bars, c_market, c_subject_id)
			VALUES ('space', 'dataset_factor_batch', 'idx', 10, 'spot', ?)`, fmt.Sprintf("SYM-%d", i)).Error; err != nil {
			t.Fatal(err)
		}
	}

	if err := repo.DeleteInstance(ctx, "inst_batch", "space", []string{"dataset_factor_batch"}); err != nil {
		t.Fatal(err)
	}
	tables := tablesWithInstanceID(t, repo)
	for table, count := range instanceRowCounts(t, repo, tables, "inst_batch") {
		if count != 0 {
			t.Errorf("分批删除后表 %s 还剩 %d 条记录", table, count)
		}
	}
	if count := countRows(t, repo, `SELECT COUNT(*) FROM t_strategy_result_items WHERE c_result_id LIKE 'res_inst_batch%'`); count != 0 {
		t.Errorf("分批删除后 t_strategy_result_items 还剩 %d 条记录", count)
	}
	if count := countRows(t, repo, `SELECT COUNT(*) FROM t_strategy_age_passed WHERE c_dataset_id = 'dataset_factor_batch'`); count != 0 {
		t.Errorf("分批删除后 t_strategy_age_passed 还剩 %d 条记录", count)
	}
	for table, count := range instanceRowCounts(t, repo, tables, "inst_keep") {
		if count == 0 {
			t.Errorf("表 %s 里另一个实例的记录被误删", table)
		}
	}
}
