package replay

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
)

// recheckFixture 返回一个 1h 的 crypto 复查场景：区间在覆盖之内，复查本身应当通过。
func recheckFixture(t *testing.T, client *fakeClient) (*Runner, store.Replay, input.Resolved, input.ViewInfo) {
	t.Helper()
	backoff := recheckRereadBackoff
	recheckRereadBackoff = time.Millisecond
	t.Cleanup(func() { recheckRereadBackoff = backoff })
	view, err := client.GetView(context.Background(), "crypto", "view_a")
	if err != nil {
		t.Fatal(err)
	}
	resolved := input.Resolved{ViewID: "view_a", Bar: "1h", Calendar: input.DefaultCalendar}
	job := store.Replay{ReplayID: "p1", SpaceID: "crypto", ViewID: "view_a", EndTime: origin.Add(20 * time.Hour)}
	return &Runner{Store: openStore(t), Client: client}, job, resolved, view
}

// 复查读取覆盖统计遇到传输失败时按退避重试，与分段读取一致：长回放不因一次瞬时失败而失败。
func TestRecheckRetriesTransportFailures(t *testing.T) {
	client := &fakeClient{marketType: "spot", coverageTransport: 2}
	runner, job, resolved, view := recheckFixture(t, client)
	if _, end, err := runner.recheckWindow(context.Background(), job, resolved, nil, view, origin.Add(10*time.Hour), job.EndTime, true, nil); err != nil || !end.Equal(job.EndTime) || client.coverageCalls != 3 {
		t.Fatalf("传输失败应重试后通过：end=%s calls=%d err=%v", end, client.coverageCalls, err)
	}
}

// 复查的退避同样确认取消：回放已取消时立即放弃，不再按退避反复读取覆盖统计。
func TestRecheckStopsWhenCancelled(t *testing.T) {
	client := &fakeClient{marketType: "spot", coverageStale: 10}
	runner, job, resolved, view := recheckFixture(t, client)
	aborted := errors.New("回放已取消")
	abort := func(context.Context) error { return aborted }
	if _, _, err := runner.recheckWindow(context.Background(), job, resolved, nil, view, origin.Add(10*time.Hour), job.EndTime, true, abort); !errors.Is(err, aborted) || client.coverageCalls != 1 {
		t.Fatalf("取消后不应再读取覆盖统计：calls=%d err=%v", client.coverageCalls, err)
	}
}

// pause 睡眠之后还要确认回放没有被取消：取消可能就落在退避期间，醒来后不能再读 Storage。
func TestPauseChecksAbortAfterSleep(t *testing.T) {
	aborted := errors.New("回放已取消")
	calls := 0
	err := pause(context.Background(), func(context.Context) error {
		calls++
		if calls > 1 {
			return aborted
		}
		return nil
	}, time.Millisecond)
	if !errors.Is(err, aborted) || calls != 2 {
		t.Fatalf("睡眠前后都应确认取消：calls=%d err=%v", calls, err)
	}
	if err := pause(context.Background(), nil, time.Millisecond); err != nil {
		t.Fatalf("没有取消钩子时只等待：%v", err)
	}
}

// 覆盖统计读取遇到 ErrStale 后按最新的 View 重读，这次读取 View 的传输失败同样按退避重试，而不是让复查直接失败。
func TestRecheckRetriesViewReadAfterStale(t *testing.T) {
	client := &fakeClient{marketType: "spot", coverageStale: 1}
	runner, job, resolved, view := recheckFixture(t, client)
	client.viewTransport, client.viewCalls = 2, 0
	if _, end, err := runner.recheckWindow(context.Background(), job, resolved, nil, view, origin.Add(10*time.Hour), job.EndTime, true, nil); err != nil || !end.Equal(job.EndTime) {
		t.Fatalf("读取 View 的传输失败应重试后通过：end=%s err=%v", end, err)
	}
	if client.viewCalls != 3 {
		t.Fatalf("读取 View 应失败两次后第三次成功：calls=%d", client.viewCalls)
	}
}

// 传输失败的退避同样确认取消：回放已取消时立即放弃，不再按退避重复读取覆盖统计。
func TestRecheckTransportBackoffStopsOnCancel(t *testing.T) {
	client := &fakeClient{marketType: "spot", coverageTransport: 10}
	runner, job, resolved, view := recheckFixture(t, client)
	aborted := errors.New("回放已取消")
	abort := func(context.Context) error {
		if client.coverageCalls >= 1 {
			return aborted
		}
		return nil
	}
	if _, _, err := runner.recheckWindow(context.Background(), job, resolved, nil, view, origin.Add(10*time.Hour), job.EndTime, true, abort); !errors.Is(err, aborted) || client.coverageCalls != 1 {
		t.Fatalf("取消后不应再读取覆盖统计：calls=%d err=%v", client.coverageCalls, err)
	}
}

// 取消钩子：已取消返回 errCancelled，运行中返回空；读取状态失败按错误返回（不当作“没取消”），与逐期检查一致。
func TestAbortHookReportsCancelAndStatusReadFailure(t *testing.T) {
	repo := openStore(t)
	job := submitReplay(t, repo, replayDSL, "idx", origin.Add(2*time.Hour), 4)
	runner := &Runner{Store: repo}
	abort := runner.abortHook(job.ReplayID)
	if err := abort(context.Background()); err != nil {
		t.Fatalf("排队中的回放不应放弃：%v", err)
	}
	if err := repo.CancelReplay(context.Background(), job.ReplayID, origin); err != nil {
		t.Fatal(err)
	}
	if err := abort(context.Background()); !errors.Is(err, errCancelled) {
		t.Fatalf("已取消的回放应返回 errCancelled：%v", err)
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	if err := abort(context.Background()); err == nil || errors.Is(err, errCancelled) {
		t.Fatalf("读取状态失败应按错误返回：%v", err)
	}
}
