package trigger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/schema"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/storagepb"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
)

const rankDSL = `name: rank_demo
rules:
  - id: r
    type: rank
    score: "m"
    select: {top: 2, buffer: 1}
    weight: {total: 1}
portfolio:
  max_missing: 1
`

const previousBarDSL = `name: prev_demo
rules:
  - id: r
    type: rank
    score: "m - bars[-1].m"
    select: {top: 1}
    weight: {total: 1}
portfolio:
  max_missing: 1
`

var (
	bar0  = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	start = bar0.Add(3 * time.Hour) // 处理周期 bar_start = bar0 + 1h 时的"现在"
)

func barStart(n int) time.Time { return bar0.Add(time.Duration(n) * time.Hour) }

// fakeLoader 按 bar_start 返回帧；errs 是依次返回的错误序列。
type fakeLoader struct {
	frames      map[int64]map[string]map[string]float64
	errs        []error
	calls       int
	invalidated int
	onLoad      func()
}

// Invalidate 记录处理器在 ErrStale 后丢弃事件内缓存的次数。
func (l *fakeLoader) Invalidate() { l.invalidated++ }

func (l *fakeLoader) LoadBar(_ context.Context, spaceID string, resolved input.Resolved, _ *dsl.Program, bar input.Bar) (input.Loaded, error) {
	if spaceID != "space" {
		return input.Loaded{}, fmt.Errorf("空间不符：%s", spaceID)
	}
	l.calls++
	if l.onLoad != nil {
		l.onLoad()
	}
	if len(l.errs) > 0 {
		err := l.errs[0]
		l.errs = l.errs[1:]
		if err != nil {
			return input.Loaded{}, err
		}
	}
	boundary, err := input.FromStorageStart(resolved.Calendar, resolved.Bar, bar.BarStart)
	if err != nil {
		return input.Loaded{}, err
	}
	rows := l.frames[bar.BarStart.Unix()]
	frame := engine.Frame{BarEnd: boundary.BarEnd, BarIndex: boundary.BarIndex, Spot: resolved.Spot, Rows: map[string]engine.Row{}, Expected: map[string][]string{}, AgedOut: map[string][]string{}}
	sets := input.Sets{Expected: map[string][]string{}, AgedOut: map[string][]string{}, Subjects: map[string]input.Subject{}}
	ids := make([]string, 0, len(rows))
	for id, values := range rows {
		ids = append(ids, id)
		row := engine.Row{Values: map[string]float64{}, Previous: map[string]float64{}}
		for column, value := range values {
			if len(column) > 5 && column[:5] == "prev_" {
				row.Previous[column[5:]] = value
			} else {
				row.Values[column] = value
			}
		}
		if len(row.Previous) == 0 {
			row.Previous = nil
		}
		frame.Rows[id] = row
		sets.Subjects[id] = input.Subject{SubjectID: id, Active: true}
	}
	frame.Universe = ids
	frame.Expected["r"] = ids
	sets.Universe = ids
	sets.Expected["r"] = ids
	return input.Loaded{Frame: frame, Sets: sets, Boundary: boundary, IndexID: "idx", Revision: 1}, nil
}

type recordingObserver struct {
	results []string
}

func (o *recordingObserver) ObservePeriod(instance store.Instance, bar string, barEnd time.Time, result, reason string) {
	o.results = append(o.results, fmt.Sprintf("%s:%s:%s", barEnd.Format("15"), result, reason))
}

type harness struct {
	t        *testing.T
	repo     *store.Store
	loader   *fakeLoader
	observer *recordingObserver
	handler  *Handler
	now      time.Time
	session  string
}

func newHarness(t *testing.T, dslYaml string, account *string) *harness {
	t.Helper()
	repo, err := store.Open(filepath.Join(t.TempDir(), "strategy.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	hash := dsl.Hash([]byte(dslYaml))
	if err := repo.CreateDefinition(ctx, store.Definition{StrategyID: "s1", Name: "demo", DSLYaml: dslYaml, DSLHash: hash, CreatedAt: bar0}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateInstance(ctx, store.Instance{InstanceID: "i1", StrategyID: "s1", SpaceID: "space", ViewID: "view_a", LogicalAccountID: account, CreatedAt: bar0}); err != nil {
		t.Fatal(err)
	}
	resolved := input.Resolved{ViewID: "view_a", DatasetID: "ds", Bar: "1h", Calendar: input.DefaultCalendar, Spot: true, Columns: map[string]input.ColumnBinding{"m": {Source: input.SourceFactor, FactorID: "f", DefinitionHash: "h1"}}, Factors: map[string]string{"f": "h1"}, ViewColumns: []string{"close", "m"}}
	raw, _ := json.Marshal(resolved)
	session := "session-1"
	if err := repo.OpenSession(ctx, store.Session{SessionID: session, InstanceID: "i1", DSLHash: hash, ResolvedJSON: string(raw), CreatedAt: bar0}, dslYaml); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetInstanceEnabled(ctx, "i1", false, &session, raw, bar0); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetInstanceEnabled(ctx, "i1", true, &session, raw, bar0); err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, repo: repo, loader: &fakeLoader{frames: map[int64]map[string]map[string]float64{}}, observer: &recordingObserver{}, now: start, session: session}
	h.handler = h.newHandler()
	return h
}

func (h *harness) newHandler() *Handler {
	return &Handler{Store: h.repo, Loader: h.loader, Now: func() time.Time { return h.now }, AttemptBudget: 2, Observer: h.observer}
}

func (h *harness) frame(n int, rows map[string]map[string]float64) {
	h.loader.frames[barStart(n).Unix()] = rows
}

func (h *harness) event(n int, hashes map[string]string, statuses ...string) (*eventpb.EventMessage, *storagepb.ViewDataReady) {
	status := "complete"
	if len(statuses) > 0 {
		status = statuses[0]
	}
	factors := make([]*storagepb.FactorPeriodState, 0, len(hashes))
	for id, hash := range hashes {
		factors = append(factors, &storagepb.FactorPeriodState{FactorId: id, Status: status, DefinitionHash: hash})
	}
	message := &eventpb.EventMessage{EventId: fmt.Sprintf("event-%d", n), EventName: "event.storage.view.data_ready", SpaceId: "space"}
	payload := &storagepb.ViewDataReady{ViewId: "view_a", Frequency: "1h", PeriodTime: barStart(n).Unix(), Status: "complete", UniverseSubjectIds: []string{"A", "B", "C"}, Factors: factors}
	return message, payload
}

func (h *harness) deliver(n int) error {
	message, payload := h.event(n, map[string]string{"f": "h1"})
	return h.handler.Handle(context.Background(), message, payload)
}

func (h *harness) resultAt(n int) store.Result {
	h.t.Helper()
	result, found, err := h.repo.ResultAtBar(context.Background(), "i1", h.session, barStart(n).Add(time.Hour))
	if err != nil || !found {
		h.t.Fatalf("周期 %d 没有记录：found=%v err=%v", n, found, err)
	}
	return result
}

func (h *harness) noResultAt(n int) {
	h.t.Helper()
	if _, found, err := h.repo.ResultAtBar(context.Background(), "i1", h.session, barStart(n).Add(time.Hour)); err != nil || found {
		h.t.Fatalf("周期 %d 不应有记录：found=%v err=%v", n, found, err)
	}
}

// expectAck 断言全部终态已落库（可以 ACK）。
func expectAck(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("期望全部终态落库，实际需要重投：%v", err)
	}
}

// expectRetry 断言需要重投。
func expectRetry(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("期望需要重投，实际全部终态已落库")
	}
	if errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("期望可重投的错误，实际是事件不可处理：%v", err)
	}
}

func targetsOf(t *testing.T, result store.Result) map[string]string {
	t.Helper()
	targets, err := DecodeTargets(result.TargetsJSON)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, target := range targets {
		out[target.InstrumentID] = target.TargetWeight
	}
	return out
}

// S9 + S14：ok → skipped → 重启后的 ok：规则状态只从最近 ok 决策恢复，buffer 保留上期持有；skipped 不取消 pending。
func TestHandleRestoresStateFromLatestOkAcrossSkipAndRestart(t *testing.T) {
	account := "acct-1"
	h := newHarness(t, rankDSL, &account)
	h.frame(2, map[string]map[string]float64{"A": {"m": 3}, "B": {"m": 2}, "C": {"m": 1}})
	expectAck(t, h.deliver(2))
	first := h.resultAt(2)
	if first.Status != store.StatusOK || first.PublishStatus != store.PublishPending || len(first.EventData) == 0 {
		t.Fatalf("首期应为 ok 且待投递：%+v", first)
	}
	if got := targetsOf(t, first); got["A"] != "0.5" || got["B"] != "0.5" || len(got) != 2 {
		t.Fatalf("首期目标不符：%v", got)
	}
	items, err := h.repo.ListResultItems(context.Background(), first.ResultID)
	if err != nil || len(items) != 3 {
		t.Fatalf("解释明细应覆盖 E(r)：%+v err=%v", items, err)
	}
	// 第二期因子被跳过。
	h.now = start.Add(time.Hour)
	message, payload := h.event(3, map[string]string{"f": "h1"}, "skipped")
	expectAck(t, h.handler.Handle(context.Background(), message, payload))
	second := h.resultAt(3)
	if second.Status != store.StatusSkipped || second.SkipReason != "factor_skipped" {
		t.Fatalf("第二期应为 skipped(factor_skipped)：%+v", second)
	}
	if again, _ := h.repo.GetResult(context.Background(), first.ResultID); again.PublishStatus != store.PublishPending {
		t.Fatalf("skipped 不应取消待投递的 ok 结果：%+v", again)
	}
	// 重启：新的处理器实例沿用会话与状态。
	h.now = start.Add(2 * time.Hour)
	h.handler = h.newHandler()
	h.frame(4, map[string]map[string]float64{"A": {"m": 2}, "B": {"m": 2.5}, "C": {"m": 3}})
	expectAck(t, h.deliver(4))
	third := h.resultAt(4)
	if got := targetsOf(t, third); got["A"] != "0.5" || got["B"] != "0.5" || len(got) != 2 {
		t.Fatalf("buffer 应保留上一次 ok 决策持有的 A、B：%v", got)
	}
	var state engine.State
	if err := json.Unmarshal(third.RuleStatesJSON, &state); err != nil || len(state.Rules["r"].Held) != 2 {
		t.Fatalf("规则状态不符：%s err=%v", third.RuleStatesJSON, err)
	}
	if cancelled, _ := h.repo.GetResult(context.Background(), first.ResultID); cancelled.PublishStatus != store.PublishCancelled {
		t.Fatalf("新的 ok 决策应取消旧的待投递结果：%+v", cancelled)
	}
	if len(h.observer.results) != 3 || h.observer.results[1] != "04:skipped:factor_skipped" {
		t.Fatalf("观察记录不符：%v", h.observer.results)
	}
}

// S10：重复投递只 ACK 不重写；乱序投递记 skipped(out_of_order)，不发布。
func TestHandleDuplicateAndOutOfOrderDeliveries(t *testing.T) {
	h := newHarness(t, rankDSL, nil)
	h.frame(2, map[string]map[string]float64{"A": {"m": 1}})
	h.frame(1, map[string]map[string]float64{"A": {"m": 1}})
	expectAck(t, h.deliver(2))
	expectAck(t, h.deliver(2))
	if h.loader.calls != 1 {
		t.Fatalf("重复投递不应再次读取输入：%d", h.loader.calls)
	}
	expectAck(t, h.deliver(1))
	older := h.resultAt(1)
	if older.Status != store.StatusSkipped || older.SkipReason != store.SkipOutOfOrder {
		t.Fatalf("乱序周期应记为 skipped(out_of_order)：%+v", older)
	}
	if _, total, _ := h.repo.ListResults(context.Background(), "i1", "", 0, 10); total != 2 {
		t.Fatalf("应只有两条记录：%d", total)
	}
}

// S16：首次收到已过期周期：skipped(expired)，允许过去的 valid_until，不读输入、不更新状态。
func TestHandleExpiredPeriodOnFirstDelivery(t *testing.T) {
	h := newHarness(t, rankDSL, nil)
	h.now = barStart(1).Add(time.Hour).Add(2 * time.Hour)
	expectAck(t, h.deliver(1))
	expired := h.resultAt(1)
	if expired.Status != store.StatusSkipped || expired.SkipReason != store.SkipExpired || !expired.ValidUntil.Equal(barStart(1).Add(3*time.Hour)) {
		t.Fatalf("应为 skipped(expired)：%+v", expired)
	}
	if h.loader.calls != 0 {
		t.Fatal("过期周期不应读取输入")
	}
	if _, ok, _ := h.repo.LatestOk(context.Background(), "i1", h.session); ok {
		t.Fatal("过期周期不应产生 ok 决策")
	}
}

// S17：求值完成后、提交前跨过有效期：转为 skipped(expired)，不发布。
func TestHandleExpiryDuringEvaluation(t *testing.T) {
	account := "acct-1"
	h := newHarness(t, rankDSL, &account)
	h.frame(1, map[string]map[string]float64{"A": {"m": 1}})
	h.loader.onLoad = func() { h.now = barStart(1).Add(4 * time.Hour) }
	h.now = barStart(1).Add(90 * time.Minute)
	expectAck(t, h.deliver(1))
	result := h.resultAt(1)
	if result.Status != store.StatusSkipped || result.SkipReason != store.SkipExpired || result.PublishStatus != store.PublishNone {
		t.Fatalf("应转为 skipped(expired)：%+v", result)
	}
}

// S15：读输入的基础设施错误先重试，超过预算后记 skipped(infra_retry_exhausted) 并 ACK；落库失败 NAK 而不 ACK。
func TestHandleInfraRetryBudgetAndStoreFailure(t *testing.T) {
	h := newHarness(t, rankDSL, nil)
	h.frame(1, map[string]map[string]float64{"A": {"m": 1}})
	h.loader.errs = []error{errors.New("storage down"), errors.New("storage down")}
	expectRetry(t, h.deliver(1))
	h.noResultAt(1)
	expectAck(t, h.deliver(1))
	exhausted := h.resultAt(1)
	if exhausted.Status != store.StatusSkipped || exhausted.SkipReason != SkipInfraRetryExhausted {
		t.Fatalf("应记为 infra_retry_exhausted：%+v", exhausted)
	}
	// 瞬时落库失败（数据库只读，等同磁盘或锁问题）：NAK，且不写任何记录。
	if err := h.repo.ApplySchema(`PRAGMA query_only = ON;`); err != nil {
		t.Fatal(err)
	}
	h.frame(2, map[string]map[string]float64{"A": {"m": 1}})
	h.now = barStart(2).Add(90 * time.Minute)
	expectRetry(t, h.deliver(2))
	if err := h.repo.ApplySchema(`PRAGMA query_only = OFF;`); err != nil {
		t.Fatal(err)
	}
	h.noResultAt(2)
	expectAck(t, h.deliver(2))
	if ok := h.resultAt(2); ok.Status != store.StatusOK {
		t.Fatalf("落库恢复后应产生 ok：%+v", ok)
	}
}

// S11（处理器部分）：索引变化整体重读后成功。
func TestHandleRereadsOnStaleSnapshot(t *testing.T) {
	h := newHarness(t, rankDSL, nil)
	h.frame(1, map[string]map[string]float64{"A": {"m": 1}})
	h.loader.errs = []error{input.ErrStale, nil}
	expectAck(t, h.deliver(1))
	if h.loader.calls != 2 || h.resultAt(1).Status != store.StatusOK {
		t.Fatalf("应重读一次后成功：calls=%d", h.loader.calls)
	}
	if h.loader.invalidated != 1 {
		t.Fatalf("重读前应丢弃事件内缓存的 View 元数据：%d", h.loader.invalidated)
	}
}

// 绑定的因子定义变了：每期 skipped(factor_changed)，只有重新启用才能恢复，实例标记 degraded。
func TestHandleFactorChangedDegradesInstance(t *testing.T) {
	h := newHarness(t, rankDSL, nil)
	h.frame(1, map[string]map[string]float64{"A": {"m": 1}})
	message, payload := h.event(1, map[string]string{"f": "h2"})
	expectAck(t, h.handler.Handle(context.Background(), message, payload))
	if result := h.resultAt(1); result.Status != store.StatusSkipped || result.SkipReason != "factor_changed" {
		t.Fatalf("因子指纹变化应记 factor_changed：%+v", result)
	}
	if instance, _ := h.repo.GetInstance(context.Background(), "i1"); instance.Health != store.HealthDegraded {
		t.Fatalf("factor_changed 应标记 degraded：%+v", instance)
	}
}

// 配置错误：skipped(config_error)、实例标记 degraded；恢复后 ok 并回到健康。
func TestHandleConfigErrorDegradesInstance(t *testing.T) {
	h := newHarness(t, rankDSL, nil)
	h.frame(1, map[string]map[string]float64{"A": {"m": 1}})
	h.loader.errs = []error{&input.SkipError{Reason: input.SkipConfigError, Detail: "列已删除"}}
	expectAck(t, h.deliver(1))
	if result := h.resultAt(1); result.Status != store.StatusSkipped || result.SkipReason != input.SkipConfigError {
		t.Fatalf("应记为 config_error：%+v", result)
	}
	if instance, _ := h.repo.GetInstance(context.Background(), "i1"); instance.Health != store.HealthDegraded {
		t.Fatalf("实例应标记 degraded：%+v", instance)
	}
	h.frame(2, map[string]map[string]float64{"A": {"m": 1}})
	h.now = barStart(2).Add(90 * time.Minute)
	expectAck(t, h.deliver(2))
	if instance, _ := h.repo.GetInstance(context.Background(), "i1"); instance.Health != store.HealthOK {
		t.Fatalf("ok 后应恢复健康：%+v", instance)
	}
	h.loader.errs = []error{&input.SkipError{Reason: input.SkipAmbiguousSeries, Detail: "两个序列"}}
	h.frame(3, map[string]map[string]float64{"A": {"m": 1}})
	h.now = barStart(3).Add(90 * time.Minute)
	expectAck(t, h.deliver(3))
	if result := h.resultAt(3); result.SkipReason != input.SkipAmbiguousSeries {
		t.Fatalf("应记为 ambiguous_series：%+v", result)
	}
}

// 绑定账户的 ok 结果携带合法的目标权重事件。
func TestHandleBuildsValidTargetEvent(t *testing.T) {
	account := "acct-1"
	h := newHarness(t, rankDSL, &account)
	h.frame(1, map[string]map[string]float64{"A": {"m": 2}, "B": {"m": 1}})
	expectAck(t, h.deliver(1))
	result := h.resultAt(1)
	registry, err := events.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	message, err := registry.UnmarshalMessage(result.EventData)
	if err != nil {
		t.Fatal(err)
	}
	subject, err := registry.SubjectForMessage(message)
	if err != nil {
		t.Fatal(err)
	}
	decoded, payload, err := events.DecodeRaw(registry, result.EventData, subject, message.GetEventId(), events.ContentType)
	if err != nil {
		t.Fatalf("目标事件不符合契约：%v", err)
	}
	target, ok := payload.(*tradeeventpb.LogicalAccountTargetWeightRequested)
	if !ok || decoded.GetEventId() != result.ResultID || target.GetSessionId() != h.session || target.GetLogicalAccountId() != account || target.GetStrategyId() != "s1" {
		t.Fatalf("事件内容不符：%+v", payload)
	}
	if len(target.GetTargets()) != 2 || !target.GetValidUntil().AsTime().Equal(barStart(1).Add(3*time.Hour)) || !target.GetBarEndTime().AsTime().Equal(barStart(1).Add(time.Hour)) {
		t.Fatalf("事件时间或目标不符：%s", target.String())
	}
}

// S18 / S19（处理器部分）：用了 bars[-1] 的实例首期 previous_version_unknown 并记录指纹，次期正常；漏掉一根后再次 unknown。
func TestHandlePreviousBarVersionEvidence(t *testing.T) {
	h := newHarness(t, previousBarDSL, nil)
	h.frame(1, map[string]map[string]float64{"A": {"m": 2, "prev_m": 1}})
	h.frame(2, map[string]map[string]float64{"A": {"m": 3, "prev_m": 2}})
	h.frame(4, map[string]map[string]float64{"A": {"m": 5, "prev_m": 4}})
	expectAck(t, h.deliver(1))
	first := h.resultAt(1)
	if first.SkipReason != "previous_version_unknown" {
		t.Fatalf("首期应为 previous_version_unknown：%+v", first)
	}
	var record store.InputRecord
	if err := json.Unmarshal(first.InputJSON, &record); err != nil || record.Factors["f"] != "h1" {
		t.Fatalf("首期应记录本期指纹：%s err=%v", first.InputJSON, err)
	}
	h.now = barStart(2).Add(90 * time.Minute)
	expectAck(t, h.deliver(2))
	if second := h.resultAt(2); second.Status != store.StatusOK {
		t.Fatalf("次期应正常求值：%+v", second)
	}
	h.now = barStart(4).Add(90 * time.Minute)
	expectAck(t, h.deliver(4))
	if fourth := h.resultAt(4); fourth.SkipReason != "previous_version_unknown" {
		t.Fatalf("漏掉一根后应为 previous_version_unknown：%+v", fourth)
	}
	// S13：事件指纹变化。
	h.frame(5, map[string]map[string]float64{"A": {"m": 6, "prev_m": 5}})
	h.now = barStart(5).Add(90 * time.Minute)
	message, payload := h.event(5, map[string]string{"f": "h2"})
	expectAck(t, h.handler.Handle(context.Background(), message, payload))
	if fifth := h.resultAt(5); fifth.SkipReason != "factor_changed" {
		t.Fatalf("指纹变化应为 factor_changed：%+v", fifth)
	}
}

// 提交冲突：读输入期间出现了更新的记录 → 重读后重新求值一次。
func TestHandleCASConflictReevaluates(t *testing.T) {
	h := newHarness(t, rankDSL, nil)
	h.frame(1, map[string]map[string]float64{"A": {"m": 1}})
	expectAck(t, h.deliver(1))
	h.frame(3, map[string]map[string]float64{"A": {"m": 1}})
	h.now = barStart(3).Add(90 * time.Minute)
	injected := false
	h.loader.onLoad = func() {
		if injected {
			return
		}
		injected = true
		result := store.Result{ResultID: "injected", InstanceID: "i1", SessionID: h.session, BarEndTime: barStart(2).Add(time.Hour), ValidUntil: barStart(2).Add(3 * time.Hour), Status: store.StatusSkipped, SkipReason: "factor_missing", DSLHash: dsl.Hash([]byte(rankDSL)), InputJSON: json.RawMessage(`{}`), TargetsJSON: json.RawMessage(`[]`), RuleStatesJSON: json.RawMessage(`{}`), SummaryJSON: json.RawMessage(`{}`), PublishStatus: store.PublishNone, CreatedAt: h.now}
		if _, _, err := h.repo.CommitResult(context.Background(), store.CommitRequest{Result: result, Now: h.now}); err != nil {
			t.Fatal(err)
		}
	}
	expectAck(t, h.deliver(3))
	if h.loader.calls != 3 || h.resultAt(3).Status != store.StatusOK {
		t.Fatalf("冲突后应重新求值一次：calls=%d", h.loader.calls)
	}
}

func TestHandleAcksEventsWithoutInstances(t *testing.T) {
	h := newHarness(t, rankDSL, nil)
	message, payload := h.event(1, map[string]string{"f": "h1"})
	payload.ViewId = "other_view"
	expectAck(t, h.handler.Handle(context.Background(), message, payload))
	if h.loader.calls != 0 {
		t.Fatal("无关 View 不应读取输入")
	}
	if err := (&Handler{}).Handle(context.Background(), message, payload); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("未配置的处理器应返回 ErrInvalidEvent：%v", err)
	}
}

// 结果违反表约束等确定性写入错误不再无限重投：记一条不带明细的 skipped(config_error) 并 ACK，实例标记 degraded。
func TestHandlePermanentWriteErrorRecordsConfigError(t *testing.T) {
	h := newHarness(t, rankDSL, nil)
	if err := h.repo.ApplySchema(`CREATE TRIGGER trg_test_reject_items
BEFORE INSERT ON t_strategy_result_items
FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, '测试：拒绝写入明细');
END;`); err != nil {
		t.Fatal(err)
	}
	h.frame(1, map[string]map[string]float64{"A": {"m": 1}, "B": {"m": 2}})
	expectAck(t, h.deliver(1))
	result := h.resultAt(1)
	if result.Status != store.StatusSkipped || result.SkipReason != input.SkipConfigError {
		t.Fatalf("确定性写入错误应记为 config_error：%+v", result)
	}
	if instance, _ := h.repo.GetInstance(context.Background(), "i1"); instance.Health != store.HealthDegraded {
		t.Fatalf("实例应标记 degraded：%+v", instance)
	}
	if got := h.observer.results; len(got) != 1 || got[0] != "02:skipped:config_error" {
		t.Fatalf("观测结果不符：%v", got)
	}
}

// 结果与兜底的 skipped 都被约束拒绝时，重投不会成功：记一次模块失败后确认，不无限重投，也不写任何记录。
func TestHandlePermanentFallbackFailureAcks(t *testing.T) {
	h := newHarness(t, rankDSL, nil)
	if err := h.repo.ApplySchema(`CREATE TRIGGER trg_test_reject_results
BEFORE INSERT ON t_strategy_results
FOR EACH ROW
BEGIN
    SELECT RAISE(ABORT, '测试：拒绝写入结果');
END;`); err != nil {
		t.Fatal(err)
	}
	h.frame(1, map[string]map[string]float64{"A": {"m": 1}})
	expectAck(t, h.deliver(1))
	h.noResultAt(1)
	if got := h.observer.results; len(got) != 1 || got[0] != "02:skipped:config_error" {
		t.Fatalf("应记一次 config_error 观测：%v", got)
	}
}

// 实例只接受启用时确定的完成事件类型；其他类型的 ViewDataReady 不是它的触发信号，忽略且不写记录。
func TestHandleIgnoresOtherCompletionKinds(t *testing.T) {
	h := newHarness(t, rankDSL, nil)
	instance, _ := h.repo.GetInstance(context.Background(), "i1")
	resolved, err := input.ParseResolved(instance.ResolvedJSON)
	if err != nil {
		t.Fatal(err)
	}
	resolved.CompletionKind = events.FactorPeriodComputed.Name()
	raw, _ := json.Marshal(resolved)
	h.handler.programs = nil
	if err := h.repo.DisableInstance(context.Background(), "i1", &h.session, nil, "", bar0); err != nil {
		t.Fatal(err)
	}
	session := "session-2"
	if err := h.repo.OpenSession(context.Background(), store.Session{SessionID: session, InstanceID: "i1", DSLHash: dsl.Hash([]byte(rankDSL)), ResolvedJSON: string(raw), CreatedAt: bar0}, rankDSL); err != nil {
		t.Fatal(err)
	}
	if err := h.repo.SetInstanceEnabled(context.Background(), "i1", false, &session, raw, bar0); err != nil {
		t.Fatal(err)
	}
	if err := h.repo.SetInstanceEnabled(context.Background(), "i1", true, &session, raw, bar0); err != nil {
		t.Fatal(err)
	}
	h.session = session
	h.frame(1, map[string]map[string]float64{"A": {"m": 1}})
	message, payload := h.event(1, map[string]string{"f": "h1"})
	payload.CompletionKind = events.CollectorPeriodCompleted.Name()
	expectAck(t, h.handler.Handle(context.Background(), message, payload))
	h.noResultAt(1)
	payload.CompletionKind = events.FactorPeriodComputed.Name()
	expectAck(t, h.handler.Handle(context.Background(), message, payload))
	if result := h.resultAt(1); result.Status != store.StatusOK {
		t.Fatalf("匹配的完成事件应正常求值：%+v", result)
	}
}

// ambiguous_series 同样需要人工处理，实例标记 degraded；ok 周期恢复 degraded，但不清除 session_unverified。
func TestHandleHealthTransitions(t *testing.T) {
	h := newHarness(t, rankDSL, nil)
	h.loader.errs = []error{&input.SkipError{Reason: input.SkipAmbiguousSeries, Detail: "两个序列"}}
	h.frame(1, map[string]map[string]float64{"A": {"m": 1}})
	expectAck(t, h.deliver(1))
	if instance, _ := h.repo.GetInstance(context.Background(), "i1"); instance.Health != store.HealthDegraded {
		t.Fatalf("ambiguous_series 应标记 degraded：%+v", instance)
	}
	if err := h.repo.SetInstanceHealth(context.Background(), "i1", store.HealthSessionUnverified, bar0); err != nil {
		t.Fatal(err)
	}
	h.frame(2, map[string]map[string]float64{"A": {"m": 1}})
	h.now = barStart(2).Add(90 * time.Minute)
	expectAck(t, h.deliver(2))
	if instance, _ := h.repo.GetInstance(context.Background(), "i1"); instance.Health != store.HealthSessionUnverified {
		t.Fatalf("ok 周期不能清除 session_unverified：%+v", instance)
	}
}
