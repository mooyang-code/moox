package rpc

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
	"github.com/mooyang-code/moox/packages/commonpb"
)

// A 股内嵌日历过了可用截止日（最后一个交易日往前 2 个交易日）后，启用的实例每一期都会被丢弃：启用时就拒绝，
// 沿用待定会话的启用也一样。
func TestEnableRejectsExpiredStockCalendar(t *testing.T) {
	h := newHarness(t)
	h.resolver.calendar = "cn_stock"
	h.createStrategy("s1", demoDSL)
	requireOK(t, int32(h.createInstance("i1", "s1", "view_a", "", false).GetRetInfo().GetCode()), "")
	h.now = time.Date(2026, 12, 30, 2, 0, 0, 0, time.UTC) // 上海 10:00，已过 12-29 的截止日
	rsp := h.setEnabled("i1", true)
	requireFail(t, int32(rsp.GetRetInfo().GetCode()), rsp.GetRetInfo().GetMsg(), "不能启用实例")
	requireFail(t, int32(rsp.GetRetInfo().GetCode()), rsp.GetRetInfo().GetMsg(), "无法推算有效期")

	definition, err := h.service.Store.GetDefinition(h.ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(input.Resolved{ViewID: "view_a", DatasetID: "ds", Bar: "1d", Calendar: "cn_stock", Spot: true, Columns: map[string]input.ColumnBinding{}, ViewColumns: []string{"close"}})
	pending := "pending-session"
	if err := h.service.Store.OpenSession(h.ctx, store.Session{SessionID: pending, InstanceID: "i1", DSLHash: definition.DSLHash, ResolvedJSON: string(raw), CreatedAt: h.now}, definition.DSLYaml); err != nil {
		t.Fatal(err)
	}
	if err := h.service.Store.SetInstanceEnabled(h.ctx, "i1", false, &pending, raw, h.now); err != nil {
		t.Fatal(err)
	}
	calls := h.resolver.calls
	rsp = h.setEnabled("i1", true)
	requireFail(t, int32(rsp.GetRetInfo().GetCode()), rsp.GetRetInfo().GetMsg(), "不能启用实例")
	if h.resolver.calls != calls {
		t.Fatalf("沿用待定会话时不重新解析，也应检查日历：%d → %d", calls, h.resolver.calls)
	}

	h.now = time.Date(2026, 10, 9, 2, 0, 0, 0, time.UTC)
	requireOK(t, int32(h.setEnabled("i1", true).GetRetInfo().GetCode()), "")
}

// A 股日线按上海日期回放：UTC 零点输入的 [09-01, 10-01) 记录为上海日期零点，回放 09-01 到 09-30 的交易日。
func TestStartReplayStockUsesShanghaiDates(t *testing.T) {
	h := newHarness(t)
	h.now = time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	h.resolver.calendar = "cn_stock"
	shanghai := time.FixedZone("CST", 8*3600)
	h.resolver.view = input.ViewInfo{ViewID: "view_a", ActiveIndexID: "idx", Generation: "idx@b1", IndexedFrom: time.Date(2025, 1, 2, 0, 0, 0, 0, shanghai), IndexedTo: time.Date(2026, 10, 9, 0, 0, 0, 0, shanghai)}
	h.createStrategy("s1", demoDSL)
	rsp, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{StrategyId: "s1", ViewId: "view_a", StartTime: "2026-09-01", EndTime: "2026-10-01"})
	requireOK(t, int32(rsp.GetRetInfo().GetCode()), rsp.GetRetInfo().GetMsg())
	if rsp.GetReplay().GetStartTime() != "2026-08-31T16:00:00Z" || rsp.GetReplay().GetEndTime() != "2026-09-30T16:00:00Z" {
		t.Fatalf("区间应记录为上海日期零点：%s ~ %s", rsp.GetReplay().GetStartTime(), rsp.GetReplay().GetEndTime())
	}
	if rsp.GetFirstBarEnd() != "2026-09-01T07:00:00Z" || rsp.GetLastBarEnd() != "2026-09-30T07:00:00Z" {
		t.Fatalf("应回放 09-01 至 09-30：%s ~ %s（%d 根）", rsp.GetFirstBarEnd(), rsp.GetLastBarEnd(), rsp.GetBarCount())
	}
}

// 取消回放的返回码：回放不存在 → NOT_FOUND；已结束 → 参数无效（不在可取消的状态）；存储错误 → 内部错误；成功返回 cancelled。
func TestCancelReplayMapsErrors(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("s1", demoDSL)
	missing, _ := h.service.CancelReplay(h.ctx, &strategypb.CancelReplayReq{ReplayId: "nope"})
	if missing.GetRetInfo().GetCode() != commonpb.ErrorCode_NOT_FOUND {
		t.Fatalf("不存在的回放应返回 NOT_FOUND：%v", missing.GetRetInfo())
	}
	started, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{StrategyId: "s1", ViewId: "view_a", StartTime: "2026-09-02", EndTime: "2026-09-03"})
	requireOK(t, int32(started.GetRetInfo().GetCode()), started.GetRetInfo().GetMsg())
	id := started.GetReplay().GetReplayId()
	cancelled, _ := h.service.CancelReplay(h.ctx, &strategypb.CancelReplayReq{ReplayId: id})
	requireOK(t, int32(cancelled.GetRetInfo().GetCode()), cancelled.GetRetInfo().GetMsg())
	if cancelled.GetReplay().GetStatus() != store.ReplayCancelled {
		t.Fatalf("取消后应是 cancelled：%s", cancelled.GetReplay().GetStatus())
	}
	again, _ := h.service.CancelReplay(h.ctx, &strategypb.CancelReplayReq{ReplayId: id})
	if again.GetRetInfo().GetCode() != commonpb.ErrorCode_INVALID_PARAM {
		t.Fatalf("已结束的回放应返回参数无效：%v", again.GetRetInfo())
	}
	requireFail(t, int32(again.GetRetInfo().GetCode()), again.GetRetInfo().GetMsg(), "不在可取消的状态")

	other, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{StrategyId: "s1", ViewId: "view_a", StartTime: "2026-09-04", EndTime: "2026-09-05"})
	requireOK(t, int32(other.GetRetInfo().GetCode()), other.GetRetInfo().GetMsg())
	if err := h.service.Store.ApplySchema(`CREATE TRIGGER trg_test_cancel_fails BEFORE UPDATE ON t_strategy_replays BEGIN SELECT RAISE(ABORT, 'boom'); END;`); err != nil {
		t.Fatal(err)
	}
	broken, _ := h.service.CancelReplay(h.ctx, &strategypb.CancelReplayReq{ReplayId: other.GetReplay().GetReplayId()})
	if broken.GetRetInfo().GetCode() != commonpb.ErrorCode_INNER_ERR {
		t.Fatalf("存储错误应返回内部错误而不是参数无效：%v", broken.GetRetInfo())
	}
}
