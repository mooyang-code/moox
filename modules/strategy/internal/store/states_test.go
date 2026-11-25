package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

const stateResolved = `{"market":{"select":"spot","prefer":"spot"},"tasks":{"spot":{"dataset_id":"dataset_factor_a","factor_task_id":"ftask_a"}}}`

// 规则状态与结果在同一个事务里写入：每个实例只有一行，新结果覆盖它；同一周期重复提交不动它；没有求值的记录不带状态。
func TestCommitResultWritesStateInSameTransaction(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	seedEnabledInstance(t, repo, "i1", "session-1", nil)
	if _, found, err := repo.GetState(ctx, "i1"); err != nil || found {
		t.Fatalf("还没有求值时没有状态行：found=%v err=%v", found, err)
	}
	first := okResult("r1", "i1", "session-1", 1, PublishNone)
	if _, created, err := repo.CommitResult(ctx, CommitRequest{Result: first, State: json.RawMessage(`{"rules":{"r":{"held":["BTC-USDT"]}}}`), Now: testNow}); err != nil || !created {
		t.Fatalf("提交失败：%v %v", created, err)
	}
	state, found, err := repo.GetState(ctx, "i1")
	if err != nil || !found || state.SessionID != "session-1" || !state.BarEndTime.Equal(first.BarEndTime) || string(state.StateJSON) != `{"rules":{"r":{"held":["BTC-USDT"]}}}` {
		t.Fatalf("状态行不符：%+v found=%v err=%v", state, found, err)
	}
	// 同一周期重复提交返回已有记录，状态不被第二次的内容覆盖。
	again := okResult("r1-dup", "i1", "session-1", 1, PublishNone)
	if _, created, err := repo.CommitResult(ctx, CommitRequest{Result: again, State: json.RawMessage(`{"rules":{}}`), Now: testNow}); err != nil || created {
		t.Fatalf("重复提交应返回已有记录：%v %v", created, err)
	}
	// 没有求值的跳过记录不带状态，状态与它推进到的那一期都不动。
	if _, created, err := repo.CommitResult(ctx, CommitRequest{Result: skippedResult("r2", "i1", "session-1", 2, "factor_skipped"), Now: testNow}); err != nil || !created {
		t.Fatalf("提交失败：%v %v", created, err)
	}
	state, _, _ = repo.GetState(ctx, "i1")
	if !state.BarEndTime.Equal(first.BarEndTime) || string(state.StateJSON) != `{"rules":{"r":{"held":["BTC-USDT"]}}}` {
		t.Fatalf("状态不应变化：%+v", state)
	}
	// 只推进状态的跳过记录（追赶、过期）带状态，覆盖原来的一行。
	if _, created, err := repo.CommitResult(ctx, CommitRequest{Result: skippedResult("r3", "i1", "session-1", 3, SkipCatchup), State: json.RawMessage(`{"rules":{"r":{"held":["ETH-USDT"]}}}`), Now: testNow}); err != nil || !created {
		t.Fatalf("提交失败：%v %v", created, err)
	}
	state, _, _ = repo.GetState(ctx, "i1")
	if !state.BarEndTime.Equal(testNow.Add(3*time.Hour)) || string(state.StateJSON) != `{"rules":{"r":{"held":["ETH-USDT"]}}}` {
		t.Fatalf("状态应推进到第 3 期：%+v", state)
	}
	// 状态必须是 JSON 对象。
	if _, _, err := repo.CommitResult(ctx, CommitRequest{Result: okResult("r4", "i1", "session-1", 4, PublishNone), State: json.RawMessage(`[]`), Now: testNow}); !errors.Is(err, ErrResultInvalid) {
		t.Fatalf("状态不是对象应被拒绝：%v", err)
	}
}

// 提交在事务中途失败（这里是解释明细不完整）时结果与状态都不留：不会出现“状态推进了、结果没写”的半截。
func TestCommitResultFailureLeavesNeitherResultNorState(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	seedEnabledInstance(t, repo, "i1", "session-1", nil)
	mustCommitState := func(result Result, state string) {
		t.Helper()
		if _, created, err := repo.CommitResult(ctx, CommitRequest{Result: result, State: json.RawMessage(state), Now: testNow}); err != nil || !created {
			t.Fatalf("提交失败：%v %v", created, err)
		}
	}
	mustCommitState(okResult("r1", "i1", "session-1", 1, PublishNone), `{"rules":{"r":{"held":["A"]}}}`)
	bad := []ResultItem{{RuleID: "", InstrumentID: "B", Stage: "weighted"}}
	if _, _, err := repo.CommitResult(ctx, CommitRequest{Result: okResult("r2", "i1", "session-1", 2, PublishNone), Items: bad, State: json.RawMessage(`{"rules":{"r":{"held":["B"]}}}`), Now: testNow}); err == nil {
		t.Fatal("解释明细不完整应提交失败")
	}
	if _, found, err := repo.ResultAtBar(ctx, "i1", "session-1", testNow.Add(2*time.Hour)); err != nil || found {
		t.Fatalf("失败的提交不应留下结果：found=%v err=%v", found, err)
	}
	state, _, _ := repo.GetState(ctx, "i1")
	if !state.BarEndTime.Equal(testNow.Add(time.Hour)) || string(state.StateJSON) != `{"rules":{"r":{"held":["A"]}}}` {
		t.Fatalf("失败的提交不应推进状态：%+v", state)
	}
	// 比较交换冲突同样整体回滚。
	wrong := "someone-else"
	if _, _, err := repo.CommitResult(ctx, CommitRequest{Result: okResult("r2", "i1", "session-1", 2, PublishNone), State: json.RawMessage(`{"rules":{"r":{"held":["B"]}}}`), ExpectedLatestResultID: &wrong, Now: testNow}); !errors.Is(err, ErrResultCASConflict) {
		t.Fatalf("期望比较交换冲突：%v", err)
	}
	if state, _, _ := repo.GetState(ctx, "i1"); string(state.StateJSON) != `{"rules":{"r":{"held":["A"]}}}` {
		t.Fatalf("冲突的提交不应推进状态：%+v", state)
	}
}

// reopen 停用实例（还启用着时），DSL 版本不同时先把定义改成这个版本，再用给定的快照启用到新会话（沿用与否在启用的事务里决定）。
func reopen(t *testing.T, repo *Store, previous, next, dslHash, resolved, members string) {
	t.Helper()
	ctx := context.Background()
	instance, err := repo.GetInstance(ctx, "i1")
	if err != nil {
		t.Fatal(err)
	}
	if instance.Enabled {
		if err := repo.DisableInstance(ctx, "i1", previous, testNow); err != nil {
			t.Fatal(err)
		}
	}
	if definition, err := repo.GetDefinition(ctx, instance.StrategyID); err != nil {
		t.Fatal(err)
	} else if definition.DSLHash != dslHash {
		if err := repo.UpdateDefinition(ctx, Definition{StrategyID: instance.StrategyID, Name: definition.Name, DSLYaml: "name: " + next, DSLHash: dslHash, UpdatedAt: testNow}); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.EnableInstance(ctx, Session{SessionID: next, InstanceID: "i1", DSLHash: dslHash, ResolvedJSON: resolved, MembersJSON: members, CreatedAt: testNow}, "name: "+next); err != nil {
		t.Fatal(err)
	}
}

// 重新启用产生新会话：DSL 版本、绑定解析与成员都没变时状态改挂到新会话沿用；任何一项变了就删除，从头开始。
func TestOpenSessionCarriesStateOnlyWhenSnapshotUnchanged(t *testing.T) {
	const stateJSON = `{"rules":{"r":{"entries":{"A":{"price":10}},"cooldown":{"B":7}}}}`
	otherHash := "sha256:" + "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	cases := []struct {
		name                   string
		hash, resolved, member string
		carried                bool
	}{
		{name: "快照没变", hash: testHash, resolved: stateResolved, carried: true},
		{name: "DSL 变了", hash: otherHash, resolved: stateResolved},
		{name: "绑定解析变了", hash: testHash, resolved: `{"market":{"select":"spot","prefer":"spot"},"tasks":{"spot":{"dataset_id":"dataset_factor_a","factor_task_id":"ftask_a"}},"factors":{"f":"h2|fixed"}}`},
		{name: "成员变了", hash: testHash, resolved: stateResolved, member: poolMembersJSON},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := openTestStore(t)
			ctx := context.Background()
			seedEnabledInstance(t, repo, "i1", "session-1", nil)
			first := okResult("r1", "i1", "session-1", 5, PublishNone)
			if _, _, err := repo.CommitResult(ctx, CommitRequest{Result: first, State: json.RawMessage(stateJSON), Now: testNow}); err != nil {
				t.Fatal(err)
			}
			reopen(t, repo, "session-1", "session-2", tc.hash, tc.resolved, tc.member)
			state, found, err := repo.GetState(ctx, "i1")
			if err != nil {
				t.Fatal(err)
			}
			if !tc.carried {
				if found {
					t.Fatalf("快照变了应清空状态：%+v", state)
				}
				return
			}
			if !found || state.SessionID != "session-2" || !state.BarEndTime.Equal(first.BarEndTime) || string(state.StateJSON) != stateJSON {
				t.Fatalf("快照没变应原样沿用到新会话：%+v found=%v", state, found)
			}
			// 再停用、再启用（快照仍然没变）：继续沿用；之后快照变了才清空。
			reopen(t, repo, "session-2", "session-3", testHash, stateResolved, "")
			if state, found, _ := repo.GetState(ctx, "i1"); !found || state.SessionID != "session-3" || string(state.StateJSON) != stateJSON {
				t.Fatalf("连续重新启用应继续沿用：%+v found=%v", state, found)
			}
			reopen(t, repo, "session-3", "session-4", otherHash, stateResolved, "")
			if _, found, _ := repo.GetState(ctx, "i1"); found {
				t.Fatal("之后 DSL 变了应清空状态")
			}
		})
	}
}

// 状态无法解析时，清状态与那一期的记录在同一个事务里：记录写入失败时状态也不动；不能同时写入和清空。
func TestCommitResultClearsStateInSameTransaction(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	seedEnabledInstance(t, repo, "i1", "session-1", nil)
	if _, _, err := repo.CommitResult(ctx, CommitRequest{Result: okResult("r1", "i1", "session-1", 1, PublishNone), State: json.RawMessage(`{"rules":{}}`), Now: testNow}); err != nil {
		t.Fatal(err)
	}
	wrong := "someone-else"
	if _, _, err := repo.CommitResult(ctx, CommitRequest{Result: skippedResult("r2", "i1", "session-1", 2, "config_error"), ClearState: true, ExpectedLatestResultID: &wrong, Now: testNow}); !errors.Is(err, ErrResultCASConflict) {
		t.Fatalf("期望比较交换冲突：%v", err)
	}
	if _, found, _ := repo.GetState(ctx, "i1"); !found {
		t.Fatal("记录没写成时状态不应被清掉")
	}
	if _, _, err := repo.CommitResult(ctx, CommitRequest{Result: skippedResult("r2", "i1", "session-1", 2, "config_error"), ClearState: true, State: json.RawMessage(`{}`), Now: testNow}); !errors.Is(err, ErrResultInvalid) {
		t.Fatalf("同时写入和清空应被拒绝：%v", err)
	}
	if _, created, err := repo.CommitResult(ctx, CommitRequest{Result: skippedResult("r2", "i1", "session-1", 2, "config_error"), ClearState: true, Now: testNow}); err != nil || !created {
		t.Fatalf("提交失败：%v %v", created, err)
	}
	if _, found, _ := repo.GetState(ctx, "i1"); found {
		t.Fatal("状态应随记录一起清掉")
	}
}

// 修改实例（换组合账户或策略定义）在同一个事务里清空规则状态：状态是按原来的账户和定义积累的理论持仓，不能带到新的账户或定义上。
func TestUpdateInstanceClearsState(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	seedEnabledInstance(t, repo, "i1", "session-1", nil)
	if _, _, err := repo.CommitResult(ctx, CommitRequest{Result: okResult("r1", "i1", "session-1", 1, PublishNone), State: json.RawMessage(`{"rules":{"r":{"held":["BTC-USDT"]}}}`), Now: testNow}); err != nil {
		t.Fatal(err)
	}
	previous := "session-1"
	if err := repo.DisableInstance(ctx, "i1", previous, testNow); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := repo.GetState(ctx, "i1"); !found {
		t.Fatal("停用本身不清状态")
	}
	account := "acct-new"
	if err := repo.UpdateInstance(ctx, Instance{InstanceID: "i1", StrategyID: "s1", SpaceID: "space", FactorTasks: map[string]string{"spot": "ftask_a"}, LogicalAccountID: &account, UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := repo.GetState(ctx, "i1"); found {
		t.Fatal("改了账户应清空规则状态")
	}
	// 之后用原来的快照重新启用也没有状态可沿用。
	reopen(t, repo, "", "session-2", testHash, stateResolved, "")
	if _, found, _ := repo.GetState(ctx, "i1"); found {
		t.Fatal("清空之后重新启用不应再有状态")
	}
}
