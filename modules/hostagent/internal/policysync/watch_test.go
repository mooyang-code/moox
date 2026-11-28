package policysync

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/notification"
	"github.com/stretchr/testify/require"
)

// watchHarness 是直推判断的测试台：可控时钟、记下推送的消息；拉取结果由每次 Observe 直接给出。
type watchHarness struct {
	watch *Watch
	clock time.Time
	sent  []notification.Message
	local bool
}

func newWatchHarness() *watchHarness {
	h := &watchHarness{clock: time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)}
	h.watch = &Watch{host: "storage", local: func() bool { return h.local }, now: func() time.Time { return h.clock },
		send: func(_ context.Context, message notification.Message) error {
			h.sent = append(h.sent, message)
			return nil
		}}
	return h
}

// startedMs 是测试里 Monitor 的启动时刻（毫秒）。
var startedMs = time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC).UnixMilli()

func (h *watchHarness) tick(startedAt int64, err error) {
	h.watch.Observe(context.Background(), startedAt, err)
	h.clock = h.clock.Add(time.Minute)
}

func (h *watchHarness) titles() []string {
	out := make([]string, 0, len(h.sent))
	for _, message := range h.sent {
		out = append(out, message.Title)
	}
	return out
}

// 非控制机上连续 3 次拉取失败推送“控制机或 Monitor 不可达”（只推一次），之后第一次成功推一条恢复；控制机上不推。
func TestWatchPushesUnreachableAfterThreeFailuresAndRecovers(t *testing.T) {
	h := newWatchHarness()
	down := errors.New("dial control:11003: connection refused")
	h.tick(startedMs, nil)
	h.tick(0, down)
	h.tick(0, down)
	require.Empty(t, h.sent, "两次失败还不推送")
	h.tick(0, down)
	require.Equal(t, []string{"【MooX】控制机或 Monitor 不可达（由 storage 观测）"}, h.titles())
	require.Equal(t, notification.SeverityCritical, h.sent[0].Severity)
	require.Contains(t, h.sent[0].Body, "connection refused")
	h.tick(0, down)
	require.Len(t, h.sent, 1, "持续失败不重复推送")
	h.tick(startedMs, nil)
	require.Len(t, h.sent, 2)
	require.Equal(t, "【MooX】控制机与 Monitor 已恢复可达（由 storage 观测）", h.sent[1].Title)
	require.Equal(t, notification.SeverityInfo, h.sent[1].Severity)
	require.Equal(t, h.sent[0].Key, h.sent[1].Key, "恢复与告警是同一个对象")

	control := newWatchHarness()
	control.local = true
	for i := 0; i < 5; i++ {
		control.tick(0, down)
	}
	require.Empty(t, control.sent, "本机部署了 Monitor 时不推送")
}

// 10 分钟内 Monitor 的启动时间变了 3 次推送“Monitor 反复重启”（每次拉起后都能应答，连续失败发现不了）；
// 窗口里的变化少于 3 次后推一条恢复。第一次看到的启动时间不算变化。
func TestWatchPushesMonitorRestartLoopAndRecovers(t *testing.T) {
	h := newWatchHarness()
	h.tick(startedMs+1, nil)
	h.tick(startedMs+2, nil)
	h.tick(startedMs+2, nil)
	h.tick(startedMs+3, nil)
	require.Empty(t, h.sent, "10 分钟内变了 2 次还不推送")
	h.tick(startedMs+4, nil)
	require.Equal(t, []string{"【MooX】Monitor 反复重启（由 storage 观测）"}, h.titles())
	require.Contains(t, h.sent[0].Body, "变了 3 次")
	require.Contains(t, h.sent[0].Body, "连续部署控制机三次也会出现这一条")
	for i := 0; i < 7; i++ {
		h.tick(startedMs+4, nil)
	}
	require.Len(t, h.sent, 1, "还在窗口里，不推恢复")
	h.tick(startedMs+4, nil)
	require.Len(t, h.sent, 2)
	require.Equal(t, "【MooX】Monitor 不再反复重启（由 storage 观测）", h.sent[1].Title)
}

// 拉取的结果交给直推判断：响应里的 Monitor 启动时间传过去，失败时传错误。
func TestSyncerHandsFetchResultToWatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "host-agent", "host-policy.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	var fetchErr error
	syncer, err := New(path, func(context.Context, *monitorpb.SyncHostAgentReq) (*monitorpb.SyncHostAgentRsp, error) {
		return &monitorpb.SyncHostAgentRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}, MonitorStartedAt: startedMs}, fetchErr
	})
	require.NoError(t, err)
	var seen []string
	h := newWatchHarness()
	h.watch.send = func(_ context.Context, message notification.Message) error {
		seen = append(seen, message.Title)
		return nil
	}
	syncer.SetWatch(h.watch)
	_, err = syncer.SyncOnce(t.Context(), nil)
	require.NoError(t, err)
	require.Equal(t, startedMs, h.watch.lastStart)
	fetchErr = errors.New("gateway unavailable")
	for i := 0; i < UnreachableAfter; i++ {
		_, _ = syncer.SyncOnce(t.Context(), nil)
	}
	require.Equal(t, []string{"【MooX】控制机或 Monitor 不可达（由 storage 观测）"}, seen)
}

// “不可达”没有送达（全新安装时渠道还没缓存）就不推“已恢复”：用户不能收到一条从没告警过的恢复。
func TestWatchRecoversOnlyAfterTheAlertWasDelivered(t *testing.T) {
	h := newWatchHarness()
	deliver := false
	h.watch.send = func(_ context.Context, message notification.Message) error {
		if !deliver {
			return errors.New("主机策略缓存里没有告警渠道")
		}
		h.sent = append(h.sent, message)
		return nil
	}
	down := errors.New("dial control:11003: connection refused")
	for i := 0; i < UnreachableAfter; i++ {
		h.tick(0, down)
	}
	deliver = true
	h.tick(startedMs, nil)
	require.Empty(t, h.sent, "告警没送达，恢复也不推")
	for i := 0; i < UnreachableAfter; i++ {
		h.tick(0, down)
	}
	h.tick(startedMs, nil)
	require.Equal(t, []string{"【MooX】控制机或 Monitor 不可达（由 storage 观测）", "【MooX】控制机与 Monitor 已恢复可达（由 storage 观测）"}, h.titles())
}

// 本机写主机策略缓存失败时 Monitor 是可达的：不算拉取失败，不推“不可达”。
func TestSyncerLocalWriteFailureIsNotUnreachable(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	require.NoError(t, os.WriteFile(blocked, []byte("x"), 0o600))
	syncer, err := New(filepath.Join(blocked, "host-policy.json"), func(context.Context, *monitorpb.SyncHostAgentReq) (*monitorpb.SyncHostAgentRsp, error) {
		return &monitorpb.SyncHostAgentRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}, MonitorStartedAt: startedMs}, nil
	})
	require.NoError(t, err)
	h := newWatchHarness()
	syncer.SetWatch(h.watch)
	for i := 0; i < UnreachableAfter+1; i++ {
		_, err = syncer.SyncOnce(t.Context(), nil)
		require.Error(t, err, "写缓存失败照样报错")
	}
	require.Empty(t, h.sent)
	require.Equal(t, startedMs, h.watch.lastStart)
}
