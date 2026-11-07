package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/callbackdispatcher/schema"
	"github.com/stretchr/testify/require"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "dispatcher.db"))
	require.NoError(t, err)
	require.NoError(t, s.ApplySchema(schema.AllSQL()))
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func notifyRule(id string) Rule {
	return Rule{
		RuleID: id, Name: "规则 " + id, EventName: "event.collector.instrument.delist_scheduled",
		Scope:      []ScopeFilter{{Field: "exchange", Values: []string{"binance"}}},
		ActionType: ActionNotifyURL, NotifyURL: "https://example.com/hook", Secret: "secret-" + id, Enabled: true, CreatedAt: 1, UpdatedAt: 1,
	}
}

func systemRule() Rule {
	return Rule{
		RuleID: "sys-1", Name: "标的即将下架时卖出策略持有的这个币", EventName: "event.collector.instrument.delist_scheduled",
		ActionType: ActionSystem, SystemAction: SystemActionSellDelistedHoldings, System: true, Enabled: true, CreatedAt: 1, UpdatedAt: 1,
	}
}

func execution(id, ruleID, action, dedupe string, next int64) Execution {
	return Execution{
		ExecutionID: id, RuleID: ruleID, RuleName: "规则", ActionType: action, EventID: "event-" + id,
		EventName: "event.collector.instrument.delist_scheduled", SpaceID: "mooxsys", SubjectID: "binance:SPOT:FOOUSDT",
		OccurredAt: 1, PayloadJSON: `{"delist_at":"2026-11-11T08:00:00Z"}`, Status: StatusPending, DedupeKey: dedupe,
		NextRunAt: next, CreatedAt: 1, UpdatedAt: 1,
	}
}

func TestRuleLifecycle(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	require.NoError(t, s.CreateRule(ctx, notifyRule("r1")))
	require.NoError(t, s.EnsureSystemRule(ctx, systemRule()))
	// 用户停用后，再次保证存在不能把它改回启用，也不能新建第二条。
	_, err := s.UpdateRule(ctx, "sys-1", 2, func(rule *Rule) error { rule.Enabled = false; return nil })
	require.NoError(t, err)
	other := systemRule()
	other.RuleID = "sys-2"
	require.NoError(t, s.EnsureSystemRule(ctx, other))

	rules, total, err := s.ListRules(ctx, 0, 10)
	require.NoError(t, err)
	require.EqualValues(t, 2, total)
	require.Equal(t, "sys-1", rules[0].RuleID, "系统规则排在前面")
	require.False(t, rules[0].Enabled)
	require.Equal(t, []ScopeFilter{{Field: "exchange", Values: []string{"binance"}}}, rules[1].Scope)

	enabled, err := s.ListEnabledRules(ctx, "event.collector.instrument.delist_scheduled")
	require.NoError(t, err)
	require.Len(t, enabled, 1)
	require.Equal(t, "r1", enabled[0].RuleID)

	_, err = s.GetRule(ctx, "missing")
	require.ErrorIs(t, err, ErrNotFound)
	require.ErrorIs(t, s.DeleteRule(ctx, "sys-1", 3), ErrNotFound, "系统规则不能删除")
}

func TestDeleteRuleCancelsUnfinishedExecutions(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	require.NoError(t, s.CreateRule(ctx, notifyRule("r1")))
	require.NoError(t, s.InTx(ctx, func(tx *Tx) error {
		require.NoError(t, tx.InsertExecution(execution("e1", "r1", ActionNotifyURL, "", 5)))
		done := execution("e2", "r1", ActionNotifyURL, "", 0)
		done.Status = StatusSucceeded
		return tx.InsertExecution(done)
	}))
	require.NoError(t, s.DeleteRule(ctx, "r1", 9))
	e1, err := s.GetExecution(ctx, "e1")
	require.NoError(t, err)
	require.Equal(t, StatusCanceled, e1.Status)
	require.Zero(t, e1.NextRunAt)
	e2, err := s.GetExecution(ctx, "e2")
	require.NoError(t, err)
	require.Equal(t, StatusSucceeded, e2.Status)
}

func TestReceiptMakesIntakeIdempotent(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	for i, want := range []bool{true, false} {
		require.NoError(t, s.InTx(ctx, func(tx *Tx) error {
			claimed, err := tx.ClaimReceipt("event-1", "r1", 10)
			require.Equal(t, want, claimed, i)
			return err
		}))
	}
	require.NoError(t, s.InTx(ctx, func(tx *Tx) error {
		claimed, err := tx.ClaimReceipt("event-1", "r2", 10)
		require.True(t, claimed, "同一事件对另一条规则是独立的")
		return err
	}))
}

func TestIntakeTransactionRollsBack(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	err := s.InTx(ctx, func(tx *Tx) error {
		_, err := tx.ClaimReceipt("event-1", "r1", 10)
		require.NoError(t, err)
		return ErrConflict
	})
	require.ErrorIs(t, err, ErrConflict)
	require.NoError(t, s.InTx(ctx, func(tx *Tx) error {
		claimed, err := tx.ClaimReceipt("event-1", "r1", 10)
		require.True(t, claimed, "回滚后回执不留痕，重投时才会重新处理")
		return err
	}))
}

func TestDedupeMergeFindsOnlyUnfinished(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	require.NoError(t, s.InTx(ctx, func(tx *Tx) error {
		return tx.InsertExecution(execution("e1", "sys-1", ActionSystem, "sys-1|binance:SPOT:FOOUSDT", 5))
	}))
	require.NoError(t, s.InTx(ctx, func(tx *Tx) error {
		found, ok, err := tx.FindUnfinishedByDedupe("sys-1|binance:SPOT:FOOUSDT")
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, "e1", found.ExecutionID)
		return tx.ReplaceExecutionEvent(found.ExecutionID, "event-new", `{"delist_at":"2026-11-12T08:00:00Z"}`, 77, 20)
	}))
	e1, err := s.GetExecution(ctx, "e1")
	require.NoError(t, err)
	require.Contains(t, e1.PayloadJSON, "2026-11-12")
	require.EqualValues(t, 20, e1.NextRunAt)
	require.Equal(t, "event-new", e1.EventID)

	_, err = s.CancelExecution(ctx, "e1", 30)
	require.NoError(t, err)
	require.NoError(t, s.InTx(ctx, func(tx *Tx) error {
		_, ok, err := tx.FindUnfinishedByDedupe("sys-1|binance:SPOT:FOOUSDT")
		require.False(t, ok, "已取消的记录不再参与合并")
		return err
	}))
}

func TestClaimDueSkipsSystemActionsWithoutHandler(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	require.NoError(t, s.CreateRule(ctx, notifyRule("r1")))
	require.NoError(t, s.EnsureSystemRule(ctx, systemRule()))
	require.NoError(t, s.InTx(ctx, func(tx *Tx) error {
		require.NoError(t, tx.InsertExecution(execution("n1", "r1", ActionNotifyURL, "", 5)))
		require.NoError(t, tx.InsertExecution(execution("n2", "r1", ActionNotifyURL, "", 500)))
		return tx.InsertExecution(execution("s1", "sys-1", ActionSystem, "k", 5))
	}))

	claimed, err := s.ClaimDue(ctx, 100, 10, nil)
	require.NoError(t, err)
	require.Len(t, claimed, 1, "没有处理器的系统动作不取，没到期的通知也不取")
	require.Equal(t, "n1", claimed[0].ExecutionID)
	require.Equal(t, StatusRunning, claimed[0].Status)

	again, err := s.ClaimDue(ctx, 100, 10, nil)
	require.NoError(t, err)
	require.Empty(t, again, "已经取走的不会再取")

	withHandler, err := s.ClaimDue(ctx, 100, 10, []string{SystemActionSellDelistedHoldings})
	require.NoError(t, err)
	require.Len(t, withHandler, 1)
	require.Equal(t, "s1", withHandler[0].ExecutionID)
}

func TestSaveResultOnlyWhileRunning(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	require.NoError(t, s.CreateRule(ctx, notifyRule("r1")))
	require.NoError(t, s.InTx(ctx, func(tx *Tx) error { return tx.InsertExecution(execution("n1", "r1", ActionNotifyURL, "", 5)) }))
	claimed, err := s.ClaimDue(ctx, 100, 10, nil)
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	// 处理期间被用户取消：处理结果不能把它改回去。
	_, err = s.CancelExecution(ctx, "n1", 101)
	require.NoError(t, err)
	applied, err := s.SaveResult(ctx, "n1", claimed[0].ClaimedAt, Result{Status: StatusSucceeded, Attempts: 1, FinishedAt: 102, Attempt: &Attempt{Attempt: 1, At: 102, HTTPStatus: 200}}, 102)
	require.NoError(t, err)
	require.False(t, applied)
	got, err := s.GetExecution(ctx, "n1")
	require.NoError(t, err)
	require.Equal(t, StatusCanceled, got.Status)
	attempts, err := s.ListAttempts(ctx, "n1")
	require.NoError(t, err)
	require.Empty(t, attempts)
}

// 卡住后被放回又被重新领取的记录：旧线程迟到的结果带着旧凭证，写不进来。
func TestSaveResultRejectsStaleClaim(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	require.NoError(t, s.CreateRule(ctx, notifyRule("r1")))
	require.NoError(t, s.InTx(ctx, func(tx *Tx) error { return tx.InsertExecution(execution("n1", "r1", ActionNotifyURL, "", 5)) }))
	first, err := s.ClaimDue(ctx, 100, 10, nil)
	require.NoError(t, err)
	require.Len(t, first, 1)
	recovered, err := s.RecoverStaleRunning(ctx, 200, 200)
	require.NoError(t, err)
	require.EqualValues(t, 1, recovered)
	second, err := s.ClaimDue(ctx, 200, 10, nil)
	require.NoError(t, err)
	require.Len(t, second, 1)
	require.NotEqual(t, first[0].ClaimedAt, second[0].ClaimedAt)

	oldResult := Result{Status: StatusSucceeded, Attempts: 1, FinishedAt: 201}
	applied, err := s.SaveResult(ctx, "n1", first[0].ClaimedAt, oldResult, 201)
	require.NoError(t, err)
	require.False(t, applied, "旧线程的结果不能覆盖新领取的")
	got, err := s.GetExecution(ctx, "n1")
	require.NoError(t, err)
	require.Equal(t, StatusRunning, got.Status)

	applied, err = s.SaveResult(ctx, "n1", second[0].ClaimedAt, oldResult, 202)
	require.NoError(t, err)
	require.True(t, applied)
}

// 一条规则积压很多时，按规则限额在 SQL 里做：别的规则照常被领到，已占满名额的规则一条也不领。
func TestClaimDueLimitedPerRule(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	require.NoError(t, s.CreateRule(ctx, notifyRule("a")))
	require.NoError(t, s.CreateRule(ctx, notifyRule("b")))
	require.NoError(t, s.InTx(ctx, func(tx *Tx) error {
		for i := 0; i < 6; i++ {
			if err := tx.InsertExecution(execution("a"+string(rune('0'+i)), "a", ActionNotifyURL, "", int64(1+i))); err != nil {
				return err
			}
		}
		for i := 0; i < 3; i++ {
			if err := tx.InsertExecution(execution("b"+string(rune('0'+i)), "b", ActionNotifyURL, "", int64(50+i))); err != nil {
				return err
			}
		}
		return nil
	}))
	ids := func(items []Execution) []string {
		var out []string
		for _, item := range items {
			out = append(out, item.ExecutionID)
		}
		return out
	}

	// a 的记录更早到期且更多，但每条规则最多 2 条，名额留给 b。
	claimed, err := s.ClaimDueLimited(ctx, 100, 4, nil, 2, map[string]int{})
	require.NoError(t, err)
	require.Equal(t, []string{"a0", "a1", "b0", "b1"}, ids(claimed))

	// a 已经占满名额（剩余 0），b 还剩 1 个名额。
	claimed, err = s.ClaimDueLimited(ctx, 100, 10, nil, 2, map[string]int{"a": 0, "b": 1})
	require.NoError(t, err)
	require.Equal(t, []string{"b2"}, ids(claimed))

	// a 剩 1 个名额：只领一条。
	claimed, err = s.ClaimDueLimited(ctx, 100, 10, nil, 2, map[string]int{"a": 1})
	require.NoError(t, err)
	require.Equal(t, []string{"a2"}, ids(claimed))
}

func TestFailureRetryAcknowledgeAndRecovery(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	require.NoError(t, s.CreateRule(ctx, notifyRule("r1")))
	require.NoError(t, s.InTx(ctx, func(tx *Tx) error { return tx.InsertExecution(execution("n1", "r1", ActionNotifyURL, "", 5)) }))
	claimed, err := s.ClaimDue(ctx, 100, 10, nil)
	require.NoError(t, err)

	applied, err := s.SaveResult(ctx, "n1", claimed[0].ClaimedAt, Result{Status: StatusFailed, Attempts: 7, Failures: 7, FirstFailedAt: 50, LastError: "状态码 500", DurationMS: 12, FinishedAt: 120,
		Attempt: &Attempt{Attempt: 7, At: 120, HTTPStatus: 500, DurationMS: 12, ResponseSnippet: "oops"}}, 120)
	require.NoError(t, err)
	require.True(t, applied)

	failures, err := s.ListUnacknowledgedFailures(ctx)
	require.NoError(t, err)
	require.Len(t, failures, 1)
	require.NoError(t, s.AcknowledgeExecution(ctx, "n1", 121))
	failures, err = s.ListUnacknowledgedFailures(ctx)
	require.NoError(t, err)
	require.Empty(t, failures)
	require.ErrorIs(t, s.AcknowledgeExecution(ctx, "missing", 121), ErrNotFound)

	retried, err := s.RetryExecution(ctx, "n1", 130)
	require.NoError(t, err)
	require.Equal(t, StatusPending, retried.Status)
	require.EqualValues(t, 130, retried.NextRunAt)
	require.False(t, retried.Acknowledged)
	require.Zero(t, retried.FirstFailedAt)
	require.Zero(t, retried.Failures)
	require.Equal(t, 7, retried.Attempts, "累计尝试次数保留，尝试记录的编号继续往后")
	_, err = s.RetryExecution(ctx, "n1", 131)
	require.ErrorIs(t, err, ErrConflict, "只有失败的记录能重新执行")

	// 进程重启：执行中的记录放回待处理。
	_, err = s.ClaimDue(ctx, 200, 10, nil)
	require.NoError(t, err)
	recovered, err := s.RecoverRunning(ctx, 300)
	require.NoError(t, err)
	require.EqualValues(t, 1, recovered)
	got, err := s.GetExecution(ctx, "n1")
	require.NoError(t, err)
	require.Equal(t, StatusPending, got.Status)
	require.EqualValues(t, 300, got.NextRunAt)
}

func TestRetryRefusesWhenSameInstrumentHasUnfinishedExecution(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	require.NoError(t, s.InTx(ctx, func(tx *Tx) error {
		failed := execution("old", "sys-1", ActionSystem, "k", 0)
		failed.Status = StatusFailed
		require.NoError(t, tx.InsertExecution(failed))
		return tx.InsertExecution(execution("new", "sys-1", ActionSystem, "k", 5))
	}))
	_, err := s.RetryExecution(ctx, "old", 10)
	require.ErrorIs(t, err, ErrConflict)
}

// 系统动作记录取消时同一条 UPDATE 记下“善后未完成”；做完之前清理旧记录不删它。
func TestCancelMarksCleanupPendingAndPurgeKeepsIt(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	require.NoError(t, s.CreateRule(ctx, systemRule()))
	require.NoError(t, s.CreateRule(ctx, notifyRule("r1")))
	require.NoError(t, s.InTx(ctx, func(tx *Tx) error {
		if err := tx.InsertExecution(execution("sys", "sys-1", ActionSystem, "sys-1|x", 5)); err != nil {
			return err
		}
		return tx.InsertExecution(execution("note", "r1", ActionNotifyURL, "", 5))
	}))
	_, err := s.CancelExecution(ctx, "sys", 100)
	require.NoError(t, err)
	_, err = s.CancelExecution(ctx, "note", 100)
	require.NoError(t, err)

	got, err := s.GetExecution(ctx, "sys")
	require.NoError(t, err)
	require.True(t, got.CleanupPending)
	got, err = s.GetExecution(ctx, "note")
	require.NoError(t, err)
	require.False(t, got.CleanupPending, "通知类没有善后")
	pending, err := s.ListPendingCleanups(ctx)
	require.NoError(t, err)
	require.Len(t, pending, 1)

	require.NoError(t, s.PurgeBefore(ctx, 1000))
	_, err = s.GetExecution(ctx, "sys")
	require.NoError(t, err, "善后没做成的不清理")
	_, err = s.GetExecution(ctx, "note")
	require.ErrorIs(t, err, ErrNotFound)

	require.NoError(t, s.FinishCleanup(ctx, "sys", 101))
	require.NoError(t, s.PurgeBefore(ctx, 1000))
	_, err = s.GetExecution(ctx, "sys")
	require.ErrorIs(t, err, ErrNotFound, "善后做完后照常清理")
}

func TestListFiltersAndLastExecutionAndPurge(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	require.NoError(t, s.CreateRule(ctx, notifyRule("r1")))
	require.NoError(t, s.InTx(ctx, func(tx *Tx) error {
		first := execution("a", "r1", ActionNotifyURL, "", 0)
		first.Status, first.CreatedAt = StatusSucceeded, 10
		second := execution("b", "r1", ActionNotifyURL, "", 0)
		second.Status, second.CreatedAt = StatusFailed, 20
		pending := execution("c", "r1", ActionNotifyURL, "", 5)
		pending.CreatedAt = 30
		for _, e := range []Execution{first, second, pending} {
			if err := tx.InsertExecution(e); err != nil {
				return err
			}
		}
		_, err := tx.ClaimReceipt("old-event", "r1", 5)
		return err
	}))
	list, total, err := s.ListExecutions(ctx, ExecutionFilter{RuleID: "r1"}, 0, 10)
	require.NoError(t, err)
	require.EqualValues(t, 3, total)
	require.Equal(t, "c", list[0].ExecutionID, "按创建时间倒序")
	failedOnly, total, err := s.ListExecutions(ctx, ExecutionFilter{Status: StatusFailed}, 0, 10)
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Equal(t, "b", failedOnly[0].ExecutionID)

	last, err := s.LastExecutions(ctx, []string{"r1", "none"})
	require.NoError(t, err)
	require.Equal(t, LastExecution{Status: StatusPending, At: 30}, last["r1"])
	require.NotContains(t, last, "none")

	require.NoError(t, s.PurgeBefore(ctx, 25))
	_, total, err = s.ListExecutions(ctx, ExecutionFilter{}, 0, 10)
	require.NoError(t, err)
	require.EqualValues(t, 1, total, "只清理已结束且足够旧的记录")
	require.NoError(t, s.InTx(ctx, func(tx *Tx) error {
		claimed, err := tx.ClaimReceipt("old-event", "r1", 99)
		require.True(t, claimed, "旧回执已清理")
		return err
	}))
}

func TestDelistActionsUpsert(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	require.NoError(t, s.InTx(ctx, func(tx *Tx) error {
		return tx.InsertExecution(execution("s1", "sys-1", ActionSystem, "k", 5))
	}))
	action := DelistAction{ExecutionID: "s1", Round: 1, Kind: DelistKindFlatten, SpaceID: "crypto", LogicalAccountID: "la-1", Exchange: "binance", Status: DelistPending, UpdatedAt: 1}
	require.NoError(t, s.UpsertDelistAction(ctx, action))
	action.Status, action.Attempts, action.OperatorActionID = DelistSucceeded, 2, "op-1"
	require.NoError(t, s.UpsertDelistAction(ctx, action))
	actions, err := s.ListDelistActions(ctx, "s1")
	require.NoError(t, err)
	require.Equal(t, []DelistAction{action}, actions)
}
