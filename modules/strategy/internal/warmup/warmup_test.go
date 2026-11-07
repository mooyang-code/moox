package warmup

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/pool"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/schema"
)

var origin = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

// fakeClient 是只有一个 1h 现货 View 的模拟数据：A、B、C 三个标的，close 按标的与小时确定，mom 是同期涨幅。
type fakeClient struct {
	mu          sync.Mutex
	indexedFrom time.Time
	queries     int
	// failFirst 是前几次读取行依次返回的错误；gate 非空时每次读取行之前先等它。
	failFirst []error
	gate      chan struct{}
}

func price(id string, hour int) float64 {
	switch id {
	case "A":
		return 100 + float64(hour)
	case "B":
		return 50 + float64(hour%5)*2
	default:
		return 10 + float64(hour%3)
	}
}

func (f *fakeClient) GetView(context.Context, string, string) (input.ViewInfo, error) {
	return input.ViewInfo{ViewID: "view_factor_a", DatasetID: "ds", Frequency: "1h", Status: "active", ActiveIndexID: "idx", Generation: "idx", Columns: []input.ViewColumn{
		{Name: "close", Attributes: map[string]string{}},
		{Name: "mom", Attributes: map[string]string{"column_kind": "output", "member_key": "mom", "factor_id": "mom", "member_kind": "fixed", "output_name": "mom"}},
	}}, nil
}

func (f *fakeClient) ViewGeneration(context.Context, string, string) (string, error) {
	return "idx", nil
}

func (f *fakeClient) ViewCoverage(context.Context, string, input.ViewInfo, bool) (input.Coverage, error) {
	from := f.indexedFrom
	if from.IsZero() {
		from = origin
	}
	return input.Coverage{IndexedFrom: from, IndexedTo: origin.Add(1000 * time.Hour)}, nil
}

func (f *fakeClient) GetDataset(_ context.Context, _, datasetID string) (input.DatasetInfo, error) {
	return input.DatasetInfo{DatasetID: datasetID, Status: "active", Frequency: "1h", Retention: "forever", Attributes: map[string]string{}, SubjectTags: []string{"binance_spot"}}, nil
}

func (f *fakeClient) GetTag(context.Context, string, string) (input.TagInfo, error) {
	return input.TagInfo{MarketType: "spot"}, nil
}

func (f *fakeClient) ListDatasetSubjects(context.Context, string, string) ([]input.Subject, error) {
	return []input.Subject{{SubjectID: "A", Active: true}, {SubjectID: "B", Active: true}, {SubjectID: "C", Active: true}}, nil
}

func (f *fakeClient) ListTagMembers(context.Context, string, string) ([]string, error) {
	return nil, nil
}

func (f *fakeClient) GetFactor(context.Context, string) (input.FactorInfo, error) {
	return input.FactorInfo{FactorID: "mom", DefinitionHash: "sha256:mom"}, nil
}

func (f *fakeClient) QueryRows(ctx context.Context, _ string, query input.Query) ([]input.Row, uint64, error) {
	f.mu.Lock()
	f.queries++
	var failure error
	if len(f.failFirst) > 0 {
		failure, f.failFirst = f.failFirst[0], f.failFirst[1:]
	}
	gate := f.gate
	f.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, 0, ctx.Err()
		}
	}
	if failure != nil {
		return nil, 0, failure
	}
	var rows []input.Row
	for at := query.Start.Truncate(time.Hour); at.Before(query.End); at = at.Add(time.Hour) {
		if at.Before(query.Start) {
			continue
		}
		hour := int(at.Sub(origin) / time.Hour)
		for _, subject := range query.Subjects {
			value := price(subject.SubjectID, hour)
			rows = append(rows, input.Row{SubjectID: subject.SubjectID, DataTime: at, SeriesTag: "venue:binance", Values: map[string]float64{"close": value, "mom": value - price(subject.SubjectID, hour-1)}})
		}
	}
	return rows, 1, nil
}

const memberDSL = `name: member
bar: 1h
market: {select: spot}
rules:
  - id: r
    type: rank
    score: "mom"
    select: {top: 1}
    weight: {total: 1}
portfolio: {max_missing: 1}
`

const poolYaml = `name: pool
bar: 1h
strategy_pool:
  members:
    - {id: a, strategy: s_a}
  allocation: {method: fixed, ratios: {a: 1}}
  timing: {window: 5}
`

type harness struct {
	repo    *store.Store
	client  *fakeClient
	manager *Manager
	logs    []string
	mu      sync.Mutex
}

// newHarness 搭一个已启用的池实例（会话带成员快照、预热结果为空）。
func newHarness(t *testing.T) *harness {
	t.Helper()
	repo, err := store.Open(filepath.Join(t.TempDir(), "strategy.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatal(err)
	}
	h := &harness{repo: repo, client: &fakeClient{}}
	ctx := context.Background()
	strategy, err := dsl.Parse([]byte(poolYaml))
	if err != nil {
		t.Fatal(err)
	}
	member, err := dsl.Parse([]byte(memberDSL))
	if err != nil {
		t.Fatal(err)
	}
	pooled, err := input.ResolvePool(ctx, h.client, "crypto", []string{"ftask_a"}, strategy, []input.PoolMemberDef{{ID: "a", StrategyID: "s_a", DSLYaml: memberDSL, DSLHash: dsl.Hash([]byte(memberDSL)), Strategy: member}})
	if err != nil {
		t.Fatal(err)
	}
	hash := dsl.Hash([]byte(poolYaml))
	if err := repo.CreateDefinition(ctx, store.Definition{StrategyID: "s1", Name: "pool", DSLYaml: poolYaml, DSLHash: hash, CreatedAt: origin}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateInstance(ctx, store.Instance{InstanceID: "i1", StrategyID: "s1", SpaceID: "crypto", FactorTasks: map[string]string{"spot": "ftask_a"}, CreatedAt: origin}); err != nil {
		t.Fatal(err)
	}
	resolved, _ := json.Marshal(pooled.Resolved)
	members, _ := pooled.MembersJSON()
	session := "session-1"
	if err := repo.OpenSession(ctx, store.Session{SessionID: session, InstanceID: "i1", DSLHash: hash, ResolvedJSON: string(resolved), MembersJSON: members, CreatedAt: origin}, poolYaml); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetInstanceEnabled(ctx, "i1", false, &session, resolved, origin); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetInstanceEnabled(ctx, "i1", true, &session, resolved, origin); err != nil {
		t.Fatal(err)
	}
	h.manager = &Manager{Store: repo, Client: h.client, Now: func() time.Time { return origin.Add(100*time.Hour + 30*time.Minute) }, ChunkBars: 4,
		RetryDelay: time.Millisecond, MaxAttempts: 3, Logf: func(format string, args ...any) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.logs = append(h.logs, strings.TrimSpace(format))
		}}
	t.Cleanup(h.manager.Close)
	return h
}

func (h *harness) session(t *testing.T) store.Session {
	t.Helper()
	session, err := h.repo.GetSession(context.Background(), "session-1")
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待超时：%s", what)
}

func TestWarmUpWritesStateAndReleasesTheGate(t *testing.T) {
	h := newHarness(t)
	h.manager.Start(context.Background(), "i1", "session-1")
	eventually(t, "预热完成", func() bool { return h.session(t).WarmedThrough != nil })
	session := h.session(t)
	// 现在是第 100.5 小时：最新已收盘的一根是 bar_end = 第 100 小时的那根；它可能还没写完，回放的终点取最后一根确认写完的
	// （再往前一根，bar_end = 第 99 小时）。更晚的周期由实时事件照常处理。
	if want := origin.Add(99 * time.Hour); !session.WarmedThrough.Equal(want) {
		t.Fatalf("预热终点 %v，期望 %v", session.WarmedThrough, want)
	}
	var state engine.State
	if err := json.Unmarshal([]byte(session.WarmState), &state); err != nil {
		t.Fatal(err)
	}
	poolState, err := pool.DecodeState(state.Pool)
	if err != nil || !poolState.Started || len(poolState.Series["a"]) != 6 {
		t.Fatalf("预热状态要带成员净值序列（窗口 5 + 1 期）：%+v %v", poolState, err)
	}
	eventually(t, "门禁放行", func() bool { return h.manager.Blocked("i1") == "" })
}

func TestWarmUpBlocksUntilDoneAndStopCancels(t *testing.T) {
	h := newHarness(t)
	h.client.gate = make(chan struct{})
	h.manager.Start(context.Background(), "i1", "session-1")
	if got := h.manager.Blocked("i1"); got != "策略池预热未完成" {
		t.Fatalf("预热期间不允许确认运行：%q", got)
	}
	if status, _ := h.manager.State("i1"); status != StatusRunning {
		t.Fatalf("状态 %v", status)
	}
	// 重复启动同一会话不新开任务。
	h.manager.Start(context.Background(), "i1", "session-1")
	close(h.client.gate)
	eventually(t, "预热完成", func() bool { return h.session(t).WarmedThrough != nil && h.manager.Blocked("i1") == "" })

	other := newHarness(t)
	other.client.gate = make(chan struct{})
	other.manager.Start(context.Background(), "i1", "session-1")
	other.manager.Stop("i1")
	if got := other.manager.Blocked("i1"); got != "" {
		t.Fatalf("停止后不再阻塞：%q", got)
	}
	other.manager.Close()
	if other.session(t).WarmedThrough != nil {
		t.Fatal("取消的预热不应写入结果")
	}
}

func TestWarmUpRetriesTransientErrors(t *testing.T) {
	h := newHarness(t)
	h.client.failFirst = []error{errors.New("Storage 暂时不可用"), errors.New("Storage 暂时不可用")}
	h.manager.Start(context.Background(), "i1", "session-1")
	eventually(t, "重试后完成", func() bool { return h.session(t).WarmedThrough != nil })
}

func TestWarmUpPermanentFailureMarksDegradedAndKeepsBlocking(t *testing.T) {
	h := newHarness(t)
	// 数据只覆盖到第 98 小时之后：预热起点（第 95 小时）早于覆盖起点，是永久性问题（保留期或数据不够）。
	h.client.indexedFrom = origin.Add(98 * time.Hour)
	h.manager.Start(context.Background(), "i1", "session-1")
	eventually(t, "预热失败", func() bool {
		status, _ := h.manager.State("i1")
		return status == StatusFailed
	})
	if got := h.manager.Blocked("i1"); !strings.Contains(got, "预热失败") || !strings.Contains(got, "成员 a") {
		t.Fatalf("失败要说明原因：%q", got)
	}
	instance, err := h.repo.GetInstance(context.Background(), "i1")
	if err != nil || instance.Health != store.HealthDegraded {
		t.Fatalf("预热失败应标 degraded：%+v %v", instance, err)
	}
	if h.session(t).WarmedThrough != nil {
		t.Fatal("失败不写预热结果")
	}
}

func TestWarmUpExhaustedRetriesFail(t *testing.T) {
	h := newHarness(t)
	err := errors.New("Storage 暂时不可用")
	h.client.failFirst = []error{err, err, err, err, err}
	h.manager.Start(context.Background(), "i1", "session-1")
	eventually(t, "重试用尽", func() bool {
		status, _ := h.manager.State("i1")
		return status == StatusFailed
	})
}

func TestResumeRestartsPendingSessionsAfterRestart(t *testing.T) {
	h := newHarness(t)
	// 进程在预热中途重启：会话里没有预热结果，新的管理器启动时重新开始。
	if err := h.manager.Resume(context.Background()); err != nil {
		t.Fatal(err)
	}
	if status, _ := h.manager.State("i1"); status != StatusRunning && h.session(t).WarmedThrough == nil {
		t.Fatalf("启动时应重新开始预热：%v", status)
	}
	eventually(t, "预热完成", func() bool { return h.session(t).WarmedThrough != nil })
	// 已经预热过的会话不再重新开始。
	h.manager.Close()
	again := &Manager{Store: h.repo, Client: h.client, Now: h.manager.Now}
	t.Cleanup(again.Close)
	if err := again.Resume(context.Background()); err != nil {
		t.Fatal(err)
	}
	if status, _ := again.State("i1"); status != StatusNone {
		t.Fatalf("已预热的会话不重新开始：%v", status)
	}
}

func TestStaleSessionIsNotWarmed(t *testing.T) {
	h := newHarness(t)
	if err := h.repo.CloseSession(context.Background(), "session-1", origin); err != nil {
		t.Fatal(err)
	}
	h.manager.Start(context.Background(), "i1", "session-1")
	eventually(t, "任务退出", func() bool {
		status, _ := h.manager.State("i1")
		return status == StatusNone
	})
	if h.session(t).WarmedThrough != nil {
		t.Fatal("已关闭的会话不写预热结果")
	}
}

func TestWarmBarsIncludeLongestHoldingPeriod(t *testing.T) {
	strategy, err := dsl.Parse([]byte(poolYaml))
	if err != nil {
		t.Fatal(err)
	}
	holding := `name: h
bar: 1h
market: {select: spot}
rules:
  - id: r
    type: rank
    score: "mom"
    select: {top: 1}
    weight: {total: 1}
    holding: {bars: 12, offsets: [0, 6]}
`
	member, err := dsl.Parse([]byte(holding))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := dsl.Compile(member, []string{"mom"})
	if err != nil {
		t.Fatal(err)
	}
	// 择时窗口 5、成员持有期 12：取大者 12，再加 1。
	if got := WarmBars(strategy, []*dsl.Program{plain}); got != 13 {
		t.Fatalf("预热期数 %d，期望 13", got)
	}
}
