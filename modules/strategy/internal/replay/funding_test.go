package replay

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/store"
)

// 单策略回放的资金费：永续多头满仓、每根费率 0.001，每根按上一根调仓之前的名义价值付费；不写 funding_column 时与原来一致，
// 局限说明按是否计资金费分别写。View 没有这一列时回放失败。
func TestReplayFundingColumnChargesSwapHoldings(t *testing.T) {
	run := func(column string, withColumn bool) (store.Replay, []store.ReplayBar) {
		repo := openStore(t)
		client := newDualClient()
		if withColumn {
			client.swap.fundingRate = 0.001
		}
		job := startMarketReplay(t, repo, client, "p1", swapOnlyDSL, 10, map[string]store.ReplayTask{"swap": swapTask}, 0, 0)
		job.FundingColumn = column
		(&Runner{Store: repo, Client: client, ChunkBars: 4, InitialEquity: 100}).Execute(context.Background(), job)
		done, err := repo.GetReplay(context.Background(), "p1")
		if err != nil {
			t.Fatal(err)
		}
		if done.Status != store.ReplayDone {
			return done, nil
		}
		return done, barsOf(t, repo, "p1")
	}
	_, plain := run("", true)
	funded, charged := run("funding_rate", true)
	if len(plain) != 10 || len(charged) != 10 {
		t.Fatalf("应各有 10 根：%d %d", len(plain), len(charged))
	}
	// 第 1 根开仓，第 2 根按第 1 根调仓前的空仓结算（不付），从第 3 根起每根付上一根名义价值的 0.1%。
	if plain[1].Equity != charged[1].Equity {
		t.Fatalf("第 2 根还没有要付费的持仓：%v / %v", plain[1].Equity, charged[1].Equity)
	}
	if !(charged[len(charged)-1].Equity < plain[len(plain)-1].Equity) {
		t.Fatalf("计资金费后权益应更低：%v / %v", charged[len(charged)-1].Equity, plain[len(plain)-1].Equity)
	}
	// 满仓多头时，第 3 根付的资金费是第 2 根收盘调仓之前的名义价值（约等于权益，随第 2 根的涨跌略有偏离）的 0.1%。
	ratioBefore := charged[1].Equity / plain[1].Equity
	ratioAfter := charged[2].Equity / plain[2].Equity
	if math.Abs(ratioBefore-1) > 1e-12 || math.Abs(ratioAfter-(1-0.001)) > 1e-4 || ratioAfter >= 1 {
		t.Fatalf("第 3 根应扣 0.1%%：%v %v", ratioBefore, ratioAfter)
	}
	var metrics Metrics
	if err := json.Unmarshal(funded.MetricsJSON, &metrics); err != nil {
		t.Fatal(err)
	}
	if text := strings.Join(metrics.Limitations, "；"); !strings.Contains(text, "资金费按 funding_rate 列结算") || strings.Contains(text, "未计资金费") {
		t.Fatalf("局限说明应写明按列结算资金费：%v", metrics.Limitations)
	}
	missing, _ := run("funding_rate", false)
	if missing.Status != store.ReplayFailed || !strings.Contains(missing.Error, "没有资金费列") {
		t.Fatalf("View 没有资金费列时回放失败：%+v", missing)
	}
}
