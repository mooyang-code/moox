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

func metricsOf(t *testing.T, replay store.Replay) Metrics {
	t.Helper()
	var metrics Metrics
	if err := json.Unmarshal(replay.MetricsJSON, &metrics); err != nil {
		t.Fatal(err)
	}
	return metrics
}

func hasNote(metrics Metrics, text string) bool {
	for _, note := range metrics.Limitations {
		if strings.Contains(note, text) {
			return true
		}
	}
	return false
}

// 跟随换代后回放失败：保存下来的部分指标横跨多代索引，也要带着换代说明。
func TestFailedReplayKeepsSwitchNote(t *testing.T) {
	repo := openStore(t)
	job := submitReplay(t, repo, replayDSL, "idx", origin.Add(2*time.Hour), 12)
	client := &fakeClient{marketType: "spot"}
	client.onQuery = func(input.Query) {
		if client.queries%2 == 1 {
			client.build = fmt.Sprintf("b%d", client.queries)
		}
	}
	(&Runner{Store: repo, Client: client, ChunkBars: 2}).Execute(context.Background(), job)
	failed, _ := repo.GetReplay(context.Background(), "p1")
	if failed.Status != store.ReplayFailed || !hasNote(metricsOf(t, failed), "重建了 3 次索引") {
		t.Fatalf("失败的回放也应带着换代说明：%+v", failed)
	}
}

// 排队期间换代、新一代截短了终点：指标说明实际回放到哪一根，与记录里提交的终点区分开。
func TestQueueTruncationIsNoted(t *testing.T) {
	repo := openStore(t)
	job := submitReplay(t, repo, replayDSL, "idx_old", origin.Add(2*time.Hour), 12)
	client := &fakeClient{marketType: "spot", indexedFrom: origin, indexedTo: origin.Add(8 * time.Hour)}
	(&Runner{Store: repo, Client: client, ChunkBars: 4}).Execute(context.Background(), job)
	done, _ := repo.GetReplay(context.Background(), "p1")
	metrics := metricsOf(t, done)
	if done.Status != store.ReplayDone || metrics.Bars != 6 || !hasNote(metrics, "排队期间 View 重建了索引") || !hasNote(metrics, "最后一根 K 线结束于 2026-09-01T08:00:00Z") {
		t.Fatalf("排队期间截短应写进说明：%+v", metrics)
	}
}

// 换代后复查时覆盖问题有具体原因（例如超出内嵌日历），要说出来，而不是一律“覆盖范围暂时未知”。
func TestRecheckReportsCoverageReason(t *testing.T) {
	repo := openStore(t)
	shanghai := time.FixedZone("CST", 8*3600)
	client := &fakeClient{marketType: "spot", indexedFrom: time.Date(2027, 1, 4, 0, 0, 0, 0, shanghai), indexedTo: time.Date(2027, 3, 1, 0, 0, 0, 0, shanghai)}
	runner := &Runner{Store: repo, Client: client}
	view, err := client.GetView(context.Background(), "crypto", "view_a")
	if err != nil {
		t.Fatal(err)
	}
	resolved := input.Resolved{ViewID: "view_a", Bar: "1d", Calendar: "cn_stock"}
	job := store.Replay{ReplayID: "p1", SpaceID: "crypto", ViewID: "view_a", EndTime: time.Date(2027, 2, 1, 0, 0, 0, 0, shanghai)}
	_, _, err = runner.recheckWindow(context.Background(), job, resolved, nil, view, time.Date(2026, 12, 1, 0, 0, 0, 0, shanghai), job.EndTime, true)
	if err == nil || !strings.Contains(err.Error(), "超出 A 股内嵌交易日历") || strings.Contains(err.Error(), "暂时未知") {
		t.Fatalf("复查应说明具体原因：%v", err)
	}
}

// 写入冲突退避期间回放被取消：退避前就放弃，不再继续读 Storage。
func TestCancelStopsStaleBackoff(t *testing.T) {
	repo := openStore(t)
	job := submitReplay(t, repo, replayDSL, "idx", origin.Add(2*time.Hour), 12)
	client := &fakeClient{marketType: "spot"}
	client.onQuery = func(input.Query) {
		if client.queries == 1 {
			if err := repo.CancelReplay(context.Background(), "p1", origin); err != nil {
				t.Error(err)
			}
		}
	}
	client.failQuery = func(input.Query) error { return input.ErrStale }
	started := time.Now()
	(&Runner{Store: repo, Client: client, ChunkBars: 4}).Execute(context.Background(), job)
	cancelled, _ := repo.GetReplay(context.Background(), "p1")
	if cancelled.Status != store.ReplayCancelled || client.queries != 1 || time.Since(started) > 2*time.Second {
		t.Fatalf("取消应在退避前生效：status=%s queries=%d（%s）", cancelled.Status, client.queries, time.Since(started))
	}
}

// View 被删除时，回放的失败原因说回放无法继续，而不是让用户去重新绑定实例。
func TestDeletedViewFailsReplayPlainly(t *testing.T) {
	repo := openStore(t)
	job := submitReplay(t, repo, replayDSL, "idx", origin.Add(2*time.Hour), 4)
	(&Runner{Store: repo, Client: &fakeClient{marketType: "spot", viewDeleted: true}, ChunkBars: 4}).Execute(context.Background(), job)
	failed, _ := repo.GetReplay(context.Background(), "p1")
	if failed.Status != store.ReplayFailed || failed.Error != "View view_a 已不存在，回放无法继续" {
		t.Fatalf("View 被删除的失败原因不符：%+v", failed)
	}
}
