package replay

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
)

// 换代次数按整个回放累计：每段都换一次代的 View 在第 4 次换代时失败，而不是每段各自重新计数、永远跟下去。
func TestReplayCountsIndexSwitchesPerReplay(t *testing.T) {
	repo := openStore(t)
	job := submitReplay(t, repo, replayDSL, "idx", origin.Add(2*time.Hour), 12)
	client := &fakeClient{marketType: "spot"}
	// 每段的第一次读取前都换一代（槽位名不变、构建 ID 变），切换后的重读不再换代。
	client.onQuery = func(input.Query) {
		if client.queries%2 == 1 {
			client.build = fmt.Sprintf("b%d", client.queries)
		}
	}
	(&Runner{Store: repo, Client: client, ChunkBars: 2}).Execute(context.Background(), job)
	failed, _ := repo.GetReplay(context.Background(), "p1")
	if failed.Status != store.ReplayFailed || !strings.Contains(failed.Error, "持续变化") {
		t.Fatalf("整个回放内换代超过上限应失败：%+v", failed)
	}
	if written := len(barsOf(t, repo, "p1")); written != 6 {
		t.Fatalf("前三次换代都应跟随过去（写入 3 段共 6 根）：%d", written)
	}
}

// 回放过程中换代、新一代的最新一根早于原终点：剩余区间按新索引截短，指标说明终点被截短。
func TestReplayTruncatesAfterIndexSwitch(t *testing.T) {
	repo := openStore(t)
	job := submitReplay(t, repo, replayDSL, "idx", origin.Add(2*time.Hour), 12)
	client := &fakeClient{marketType: "spot", switchAtQuery: 2, switchedFrom: origin, switchedTo: origin.Add(8 * time.Hour)}
	(&Runner{Store: repo, Client: client, ChunkBars: 4}).Execute(context.Background(), job)
	done, _ := repo.GetReplay(context.Background(), "p1")
	if done.Status != store.ReplayDone {
		t.Fatalf("新一代仍覆盖剩余区间的前段时应完成：%+v", done)
	}
	bars := barsOf(t, repo, "p1")
	if len(bars) != 6 || !bars[len(bars)-1].BarEndTime.Equal(origin.Add(8*time.Hour)) {
		t.Fatalf("终点应截到新一代最新一根之前（02:00 至 07:00 共 6 根）：%d", len(bars))
	}
	var metrics Metrics
	if err := json.Unmarshal(done.MetricsJSON, &metrics); err != nil {
		t.Fatal(err)
	}
	noted := false
	for _, note := range metrics.Limitations {
		noted = noted || strings.Contains(note, "终点按新索引截到")
	}
	if !noted || metrics.Bars != 6 {
		t.Fatalf("指标应说明终点被截短：%+v", metrics)
	}
}

// 回放过程中复查失败时说的是“剩余区间”的起点，不是回放起点。
func TestReplayMidRunRecheckNamesRemainingRange(t *testing.T) {
	repo := openStore(t)
	job := submitReplay(t, repo, replayDSL, "idx", origin.Add(2*time.Hour), 12)
	client := &fakeClient{marketType: "spot", switchAtQuery: 2, switchedFrom: origin.Add(20 * time.Hour)}
	(&Runner{Store: repo, Client: client, ChunkBars: 4}).Execute(context.Background(), job)
	failed, _ := repo.GetReplay(context.Background(), "p1")
	if failed.Status != store.ReplayFailed || !strings.Contains(failed.Error, "剩余区间从") || strings.Contains(failed.Error, "回放起点") {
		t.Fatalf("应说明剩余区间不再被覆盖：%+v", failed)
	}
}

// 复查时读取覆盖统计遇到活动索引又换了槽位（ErrStale）：按最新的 View 重读，而不是让回放失败。
func TestReplayRecheckRereadsAfterStaleCoverage(t *testing.T) {
	repo := openStore(t)
	job := submitReplay(t, repo, replayDSL, "idx", origin.Add(2*time.Hour), 12)
	client := &fakeClient{marketType: "spot", switchAtQuery: 2, switchedFrom: origin, coverageStale: 1}
	(&Runner{Store: repo, Client: client, ChunkBars: 4}).Execute(context.Background(), job)
	done, _ := repo.GetReplay(context.Background(), "p1")
	if done.Status != store.ReplayDone || len(barsOf(t, repo, "p1")) != 12 || client.coverageCalls != 2 {
		t.Fatalf("覆盖统计冲突一次后应重读并完成：%+v coverage=%d", done, client.coverageCalls)
	}
}
