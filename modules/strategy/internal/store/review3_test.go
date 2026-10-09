package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/strategy/schema"
	"gorm.io/gorm"
)

// 启动校验比较规范化后的建表语句：列名相同但约束不同（例如旧版的 c_health CHECK 不含 session_unverified）的库
// 必须被拒绝，否则要到运行期写入新状态时才失败；纯格式差异（注释、空白）不影响判断。
func TestOpenRejectsChangedConstraints(t *testing.T) {
	path := filepath.Join(t.TempDir(), "strategy.sqlite")
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	old := strings.Replace(schema.AllSQL(), "CHECK (c_health IN ('ok', 'degraded', 'session_unverified'))", "CHECK (c_health IN ('ok', 'degraded'))", 1)
	if old == schema.AllSQL() {
		t.Fatal("测试未能改写约束")
	}
	if err := db.Exec(old).Error; err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	_ = sqlDB.Close()
	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "t_strategy_instances") {
		t.Fatalf("约束变化的旧库应被拒绝：%v", err)
	}
	if normalizeSchemaSQL("CREATE TABLE t (\n    a INT, -- 注释\n    b TEXT\n)") != normalizeSchemaSQL("CREATE TABLE t (a INT,\n b TEXT)") {
		t.Fatal("纯格式差异不应影响比较")
	}
}

// 健康标记只作用于仍以该会话启用的实例；对账自动停用时在同一事务里标记 degraded。
func TestInstanceHealthIsScopedToCurrentSession(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	seedEnabledInstance(t, repo, "i1", "session-1", nil)
	health := func() string {
		instance, err := repo.GetInstance(ctx, "i1")
		if err != nil {
			t.Fatal(err)
		}
		return instance.Health
	}
	if err := repo.MarkInstanceDegraded(ctx, "i1", "session-old", testNow); err != nil || health() != HealthOK {
		t.Fatalf("其他会话的结论不应标记实例：%s err=%v", health(), err)
	}
	session := "session-1"
	if err := repo.DisableInstance(ctx, "i1", &session, nil, HealthDegraded, testNow); err != nil || health() != HealthDegraded {
		t.Fatalf("自动停用应同时标记 degraded：%s err=%v", health(), err)
	}
	if err := repo.RecoverInstanceHealth(ctx, "i1", "session-1", testNow); err != nil || health() != HealthDegraded {
		t.Fatalf("已停用实例的 degraded 不能被并发提交的 ok 周期抹掉：%s err=%v", health(), err)
	}
	if err := repo.DisableInstance(ctx, "i1", nil, nil, "broken", testNow); err == nil {
		t.Fatal("非法的健康状态应被拒绝")
	}
}

// 写实例时在事务里确认定义存在且未删除：改绑或新建不能引用一个刚被删除的定义。
func TestInstanceWritesRequireLiveDefinition(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	for _, id := range []string{"s1", "s2"} {
		if err := repo.CreateDefinition(ctx, Definition{StrategyID: id, Name: id, DSLYaml: "name: " + id, DSLHash: testHash + id, CreatedAt: testNow, UpdatedAt: testNow}); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.CreateInstance(ctx, Instance{InstanceID: "i1", StrategyID: "s1", SpaceID: "space", ViewID: "view_a", CreatedAt: testNow, UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SoftDeleteDefinition(ctx, "s2", testNow); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateInstance(ctx, Instance{InstanceID: "i2", StrategyID: "s2", SpaceID: "space", ViewID: "view_a", CreatedAt: testNow, UpdatedAt: testNow}); err == nil || !strings.Contains(err.Error(), "s2 不存在") {
		t.Fatalf("新建实例不能引用已删除的定义：%v", err)
	}
	if err := repo.UpdateInstance(ctx, Instance{InstanceID: "i1", StrategyID: "s2", SpaceID: "space", ViewID: "view_a", UpdatedAt: testNow}); err == nil || !strings.Contains(err.Error(), "s2 不存在") {
		t.Fatalf("改绑不能引用已删除的定义：%v", err)
	}
}

// 每个空间只保留最近 keep 个已结束的回放；运行中与排队中的不删，记录来源实例与会话。
func TestDeleteReplaysBeyondKeepsLatestPerSpace(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	instance, session := "i1", "session-1"
	for i := 0; i < 5; i++ {
		for _, space := range []string{"a", "b"} {
			replay := Replay{ReplayID: fmt.Sprintf("%s-%d", space, i), DSLYaml: "name: demo", DSLHash: testHash, SpaceID: space, ViewID: "view", StartTime: testNow, EndTime: testNow.Add(time.Hour), CreatedAt: testNow.Add(time.Duration(i) * time.Minute), InstanceID: &instance, SessionID: &session}
			if err := repo.CreateReplay(ctx, replay); err != nil {
				t.Fatal(err)
			}
		}
	}
	// 让除最早一个之外的回放都结束：FinishReplay 只接受运行中的任务，先认领。
	for {
		claimed, found, err := repo.ClaimNextReplay(ctx, testNow)
		if err != nil {
			t.Fatal(err)
		}
		if !found {
			break
		}
		if strings.HasSuffix(claimed.ReplayID, "-0") {
			continue
		}
		if err := repo.FinishReplay(ctx, claimed.ReplayID, ReplayDone, []byte(`{}`), "", testNow); err != nil {
			t.Fatal(err)
		}
	}
	deleted, err := repo.DeleteReplaysBeyond(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 4 {
		t.Fatalf("每个空间保留最近 2 个，应删除 2×2 个已结束的回放：%d", deleted)
	}
	for _, space := range []string{"a", "b"} {
		replays, total, err := repo.ListReplays(ctx, space, 0, 10)
		if err != nil || total != 3 {
			t.Fatalf("空间 %s 应剩最近 2 个与仍在运行的最早一个：total=%d err=%v", space, total, err)
		}
		if replays[0].InstanceID == nil || *replays[0].InstanceID != instance || replays[0].SessionID == nil || *replays[0].SessionID != session {
			t.Fatalf("回放应记录来源实例与会话：%+v", replays[0])
		}
	}
	if _, err := repo.DeleteReplaysBeyond(ctx, 0); err == nil {
		t.Fatal("保留个数必须大于 0")
	}
}

// 取消、超时与数据库关闭的英文原文不能直接返回给接口调用方。
func TestFriendlyMessageForContextAndClosedDatabase(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{err: fmt.Errorf("读取实例：%w", context.Canceled), want: "请求已取消"},
		{err: fmt.Errorf("读取实例：%w", context.DeadlineExceeded), want: "请求超时"},
		{err: fmt.Errorf("读取实例：%w", sql.ErrConnDone), want: "策略数据库不可用"},
	} {
		message, replaced := FriendlyMessage(tc.err)
		if !replaced || !strings.Contains(message, tc.want) || strings.ContainsAny(message, "abcdefghijklmnopqrstuvwxyz") {
			t.Fatalf("%v 应转为中文说明：%s replaced=%v", tc.err, message, replaced)
		}
	}
}
