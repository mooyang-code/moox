package store

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/schema"
)

// 挂会话与启用都对会话做比较交换：会话必须属于该实例、未关闭，并与实例当前引用的定义版本和 View 一致。
// 打开会话之后实例被改绑、定义被修改，或会话已被关闭，都在联系 Trade 之前拒绝。
func TestSetInstanceEnabledComparesSessionWithInstance(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	if err := repo.CreateDefinition(ctx, Definition{StrategyID: "s1", Name: "demo", DSLYaml: "name: demo", DSLHash: testHash, CreatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"i1", "i2"} {
		if err := repo.CreateInstance(ctx, Instance{InstanceID: id, StrategyID: "s1", SpaceID: "space", ViewID: "view_a", CreatedAt: testNow}); err != nil {
			t.Fatal(err)
		}
	}
	open := func(sessionID, instanceID, hash, view string) string {
		t.Helper()
		if err := repo.OpenSession(ctx, Session{SessionID: sessionID, InstanceID: instanceID, DSLHash: hash, ResolvedJSON: fmt.Sprintf(`{"view_id":%q}`, view), CreatedAt: testNow}, "name: demo"); err != nil {
			t.Fatal(err)
		}
		return sessionID
	}
	attach := func(sessionID string) error {
		return repo.SetInstanceEnabled(ctx, "i1", false, &sessionID, nil, testNow)
	}

	// 打开会话之后实例被改绑到另一个 View。
	stale := open("s-view", "i1", testHash, "view_a")
	if err := repo.UpdateInstance(ctx, Instance{InstanceID: "i1", StrategyID: "s1", SpaceID: "space", ViewID: "view_b", UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	if err := attach(stale); err == nil || !strings.Contains(err.Error(), "被修改") {
		t.Fatalf("改绑后不应挂上旧会话：%v", err)
	}
	// 会话属于另一个实例。
	foreign := open("s-foreign", "i2", testHash, "view_b")
	if err := attach(foreign); err == nil || !strings.Contains(err.Error(), "不属于该实例") {
		t.Fatalf("不应挂上其他实例的会话：%v", err)
	}
	// 打开会话之后定义被修改。
	before := open("s-def", "i1", testHash, "view_b")
	if err := repo.UpdateDefinition(ctx, Definition{StrategyID: "s1", Name: "demo", DSLYaml: "name: demo2", DSLHash: "sha256:def2", UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	if err := attach(before); err == nil || !strings.Contains(err.Error(), "被修改") {
		t.Fatalf("定义修改后不应挂上旧版本的会话：%v", err)
	}
	// 一致的会话可以挂上；挂上之后被关闭（例如对账释放）则不能启用。
	current := open("s-ok", "i1", "sha256:def2", "view_b")
	if err := attach(current); err != nil {
		t.Fatal(err)
	}
	if err := repo.CloseSession(ctx, current, testNow); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetInstanceEnabled(ctx, "i1", true, &current, nil, testNow); err == nil || !strings.Contains(err.Error(), "已关闭") {
		t.Fatalf("已关闭的会话不应能启用：%v", err)
	}
	if instance, err := repo.GetInstance(ctx, "i1"); err != nil || instance.Enabled {
		t.Fatalf("实例应保持停用：%+v err=%v", instance, err)
	}
}

// 按个数清理只在已结束的回放里排名次：最新的几个还在排队时，已结束的仍保留 keep 个。
func TestDeleteReplaysBeyondRanksOnlyFinished(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if err := repo.CreateReplay(ctx, Replay{ReplayID: fmt.Sprintf("p%d", i), DSLYaml: "name: demo", DSLHash: testHash, ViewGeneration: "idx", SpaceID: "space", ViewID: "view", StartTime: testNow, EndTime: testNow.Add(time.Hour), CreatedAt: testNow.Add(time.Duration(i) * time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	// 最早的 3 个结束，最新的 2 个仍在排队。
	for i := 0; i < 3; i++ {
		claimed, found, err := repo.ClaimNextReplay(ctx, testNow)
		if err != nil || !found {
			t.Fatalf("认领失败：found=%v err=%v", found, err)
		}
		if err := repo.FinishReplay(ctx, claimed.ReplayID, ReplayDone, []byte(`{}`), "", testNow); err != nil {
			t.Fatal(err)
		}
	}
	deleted, err := repo.DeleteReplaysBeyond(ctx, 2)
	if err != nil || deleted != 1 {
		t.Fatalf("应只删除最早的一个已结束回放：deleted=%d err=%v", deleted, err)
	}
	if _, err := repo.GetReplay(ctx, "p0"); err == nil {
		t.Fatal("p0 应被删除")
	}
	for _, id := range []string{"p1", "p2", "p3", "p4"} {
		if _, err := repo.GetReplay(ctx, id); err != nil {
			t.Fatalf("%s 应保留：%v", id, err)
		}
	}
}

// 每个空间同时排队与运行的回放有上限；其他空间不受影响，结束一个后可以再发起。
func TestCreateReplayLimitsActivePerSpace(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	replay := func(id, space string) Replay {
		return Replay{ReplayID: id, DSLYaml: "name: demo", DSLHash: testHash, ViewGeneration: "idx", SpaceID: space, ViewID: "view", StartTime: testNow, EndTime: testNow.Add(time.Hour), CreatedAt: testNow}
	}
	for i := 0; i < MaxActiveReplays; i++ {
		if err := repo.CreateReplay(ctx, replay(fmt.Sprintf("a%d", i), "a")); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.CreateReplay(ctx, replay("a-over", "a")); err == nil || !strings.Contains(err.Error(), "排队或运行中") {
		t.Fatalf("超过上限应拒绝：%v", err)
	}
	if err := repo.CreateReplay(ctx, replay("b0", "b")); err != nil {
		t.Fatalf("其他空间不受影响：%v", err)
	}
	if err := repo.CancelReplay(ctx, "a0", testNow); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateReplay(ctx, replay("a-next", "a")); err != nil {
		t.Fatalf("取消一个后应可再发起：%v", err)
	}
}

// 列表不读取 DSL 全文，但带版本哈希；详情带 DSL 与提交时校验区间所用的活动索引。
func TestReplayListOmitsDSLButKeepsHash(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	if err := repo.CreateReplay(ctx, Replay{ReplayID: "p1", DSLYaml: "name: demo", DSLHash: testHash, ViewGeneration: "idx_a@b1", SpaceID: "space", ViewID: "view", StartTime: testNow, EndTime: testNow.Add(time.Hour), CreatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	listed, _, err := repo.ListReplays(ctx, "space", 0, 10)
	if err != nil || len(listed) != 1 || listed[0].DSLYaml != "" || listed[0].DSLHash != testHash {
		t.Fatalf("列表不应带 DSL 全文、应带哈希：%+v err=%v", listed, err)
	}
	full, err := repo.GetReplay(ctx, "p1")
	if err != nil || full.DSLYaml != "name: demo" || full.DSLHash != testHash || full.ViewGeneration != "idx_a@b1" {
		t.Fatalf("详情不符：%+v err=%v", full, err)
	}
}

// 启动校验只比较规范化后的建表语句：按 AGENTS.md 调整排版（缩进、括号与逗号两侧的空格、关键字大小写、注释）
// 的库照常通过，语义变化（默认值不同）被拒绝；建表在一个事务里，中途失败不留下残缺的 schema。
func TestApplySchemaToleratesFormattingAndIsAtomic(t *testing.T) {
	formatted := strings.NewReplacer("    ", "\t", ", ", ",", "PRIMARY KEY (", "primary key(", "CREATE TABLE IF NOT EXISTS", "-- 排版不同\ncreate table if not exists").Replace(schema.AllSQL())
	if formatted == schema.AllSQL() {
		t.Fatal("测试未能改写排版")
	}
	repo, err := Open(filepath.Join(t.TempDir(), "formatted.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.ApplySchema(formatted); err != nil {
		t.Fatalf("纯排版差异不应被拒绝：%v", err)
	}
	changed, err := Open(filepath.Join(t.TempDir(), "changed.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = changed.Close() })
	semantic := strings.Replace(schema.AllSQL(), "c_error TEXT NOT NULL DEFAULT ''", "c_error TEXT NOT NULL DEFAULT 'x'", 1)
	if semantic == schema.AllSQL() {
		t.Fatal("测试未能改写默认值")
	}
	if err := changed.ApplySchema(semantic); err == nil || !strings.Contains(err.Error(), "t_strategy_replays") {
		t.Fatalf("默认值不同应被拒绝：%v", err)
	}

	broken, err := Open(filepath.Join(t.TempDir(), "broken.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = broken.Close() })
	if err := broken.ApplySchema(schema.AllSQL() + "\nCREATE TABLE t_strategy_broken (;\n"); err == nil {
		t.Fatal("有语法错误的 schema 应失败")
	}
	var tables int64
	if err := broken.db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name LIKE 't_strategy_%'`).Scan(&tables).Error; err != nil || tables != 0 {
		t.Fatalf("建表失败不应留下残缺的 schema：tables=%d err=%v", tables, err)
	}
	if err := broken.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatalf("失败后应能重新建表：%v", err)
	}
}

// 取消与超时只替换错误链里的英文原文，保留前面的中文上下文（例如停用已落库但 Trade 释放未确认）。
func TestFriendlyMessageKeepsContext(t *testing.T) {
	message, replaced := FriendlyMessage(fmt.Errorf("已停用但 Trade 释放未确认，稍后自动重试：%w", context.DeadlineExceeded))
	if !replaced || message != "已停用但 Trade 释放未确认，稍后自动重试：请求超时" {
		t.Fatalf("应保留上下文：%q replaced=%v", message, replaced)
	}
	if message, replaced := FriendlyMessage(context.Canceled); !replaced || message != "请求已取消" {
		t.Fatalf("单独的取消应替换为中文：%q", message)
	}
}
