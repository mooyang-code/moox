package rpc

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/packages/spacecontext"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
)

// shadowStore 打开一个空库，建好两个空间里的组合 logical-1（目标历史按组合挂外键）。
func shadowStore(t *testing.T) *store.Store {
	t.Helper()
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tradeStore.Close() })
	for _, space := range []string{"space-1", "space-2"} {
		if err := tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
			return tx.CreateLogicalAccount(store.LogicalAccountRecord{SpaceID: space, LogicalAccountID: "logical-1", Name: "shadow", Environment: "PAPER", SettlementAsset: "USDT", PauseReason: "test"})
		}); err != nil {
			t.Fatal(err)
		}
	}
	return tradeStore
}

func shadowRecord(space, instanceID, targetID string, barEnd int64) store.TargetHistoryRecord {
	return store.TargetHistoryRecord{
		SpaceID: space, TargetID: targetID, LogicalAccountID: "logical-1", InstanceID: instanceID, SessionID: "session-1",
		SessionStartedAt: 1000, StrategyID: "strategy-1", BarEndTime: barEnd, ValidUntil: barEnd + 7_200_000, RequestHash: "hash",
		WeightsJSON:         `[{"instrument_id":"BTC-USDT","target_weight":"0.6"},{"instrument_id":"ETH-USDT","target_weight":"0.2","hold":true}]`,
		Equity:              "1000",
		EquitySourceTime:    barEnd - 1000,
		ReferencePricesJSON: `{"BTC-USDT":{"instrument_id":"BTC-USDT","trading_account_id":"account-1","exchange_symbol":"BTCUSDT","price":"60000","updated_at":1}}`,
		QuantityTargetsJSON: `[{"instrument_id":"BTC-USDT","quantity":"0.01"},{"instrument_id":"ETH-USDT","hold":true}]`,
		RecordedAt:          barEnd + 1000,
	}
}

// 影子目标按周期从新到旧列出，只列当前空间、这个实例的；权重、参考价、换算出的数量按写入时的形状解开；
// 不填 limit 取默认条数，超过上限、缺实例 ID、缺空间都拒绝。
func TestListShadowTargets(t *testing.T) {
	tradeStore := shadowStore(t)
	ctx := context.Background()
	for i, record := range []store.TargetHistoryRecord{
		shadowRecord("space-1", "instance-1", "t1", 3_600_000),
		shadowRecord("space-1", "instance-1", "t2", 7_200_000),
		shadowRecord("space-1", "instance-2", "t3", 7_200_000),
		shadowRecord("space-2", "instance-1", "t4", 7_200_000),
	} {
		if _, err := tradeStore.RecordShadowTarget(ctx, record); err != nil {
			t.Fatalf("记录 %d：%v", i, err)
		}
	}
	server := &ConsoleServer{Store: tradeStore}
	scoped := spacecontext.WithSpaceID(ctx, "space-1")

	rsp, err := server.ListShadowTargets(scoped, &tradepb.ListShadowTargetsReq{InstanceId: "instance-1"})
	if err != nil || rsp.GetRetInfo().GetCode() != tradepb.ErrorCode_SUCCESS {
		t.Fatalf("list = %+v err=%v", rsp, err)
	}
	targets := rsp.GetTargets()
	if len(targets) != 2 || targets[0].GetTargetId() != "t2" || targets[1].GetTargetId() != "t1" {
		t.Fatalf("应按周期从新到旧列出本空间这个实例的影子目标：%+v", targets)
	}
	first := targets[0]
	if first.GetEquity() != "1000" || first.GetSessionId() != "session-1" || first.GetBarEndTime() != 7_200_000 || first.GetReferencePrices()["BTC-USDT"] != "60000" {
		t.Fatalf("字段不符：%+v", first)
	}
	if len(first.GetWeights()) != 2 || first.GetWeights()[0].GetTargetWeight() != "0.6" || !first.GetWeights()[1].GetHold() {
		t.Fatalf("权重不符：%+v", first.GetWeights())
	}
	if len(first.GetTargets()) != 2 || first.GetTargets()[0].GetQuantity() != "0.01" || !first.GetTargets()[1].GetHold() || first.GetTargets()[1].GetQuantity() != "" {
		t.Fatalf("换算出的数量不符：%+v", first.GetTargets())
	}

	limited, _ := server.ListShadowTargets(scoped, &tradepb.ListShadowTargetsReq{InstanceId: "instance-1", Limit: 1})
	if len(limited.GetTargets()) != 1 || limited.GetTargets()[0].GetTargetId() != "t2" {
		t.Fatalf("limit 应限制条数：%+v", limited.GetTargets())
	}
	for name, call := range map[string]func() *tradepb.ListShadowTargetsRsp{
		"缺实例 ID": func() *tradepb.ListShadowTargetsRsp {
			rsp, _ := server.ListShadowTargets(scoped, &tradepb.ListShadowTargetsReq{})
			return rsp
		},
		"超过上限": func() *tradepb.ListShadowTargetsRsp {
			rsp, _ := server.ListShadowTargets(scoped, &tradepb.ListShadowTargetsReq{InstanceId: "instance-1", Limit: 201})
			return rsp
		},
		"缺空间": func() *tradepb.ListShadowTargetsRsp {
			rsp, _ := server.ListShadowTargets(ctx, &tradepb.ListShadowTargetsReq{InstanceId: "instance-1"})
			return rsp
		},
	} {
		if rsp := call(); rsp.GetRetInfo().GetCode() == tradepb.ErrorCode_SUCCESS {
			t.Fatalf("%s 应被拒绝：%+v", name, rsp)
		}
	}
}

// 不填 limit 时列出默认的 20 条（最新的 20 期）。
func TestListShadowTargetsDefaultsToTwentyItems(t *testing.T) {
	tradeStore := shadowStore(t)
	ctx := context.Background()
	for i := range 25 {
		record := shadowRecord("space-1", "instance-1", fmt.Sprintf("t%02d", i), int64(i+1)*3_600_000)
		if _, err := tradeStore.RecordShadowTarget(ctx, record); err != nil {
			t.Fatalf("记录 %d：%v", i, err)
		}
	}
	server := &ConsoleServer{Store: tradeStore}
	rsp, err := server.ListShadowTargets(spacecontext.WithSpaceID(ctx, "space-1"), &tradepb.ListShadowTargetsReq{InstanceId: "instance-1"})
	if err != nil || rsp.GetRetInfo().GetCode() != tradepb.ErrorCode_SUCCESS {
		t.Fatalf("list = %+v err=%v", rsp, err)
	}
	targets := rsp.GetTargets()
	if len(targets) != defaultShadowTargets || defaultShadowTargets != 20 {
		t.Fatalf("不填 limit 应列出 20 条：%d", len(targets))
	}
	if targets[0].GetTargetId() != "t24" || targets[19].GetTargetId() != "t05" {
		t.Fatalf("应是最新的 20 期：%s … %s", targets[0].GetTargetId(), targets[19].GetTargetId())
	}
}
