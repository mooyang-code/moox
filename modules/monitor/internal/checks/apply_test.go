package checks

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/notifier"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/mooyang-code/moox/packages/notification"
	"github.com/stretchr/testify/require"
)

type recordingSender struct {
	err  error
	sent []notification.Message
}

func (s *recordingSender) Send(_ context.Context, message notification.Message) error {
	s.sent = append(s.sent, message)
	return s.err
}

type applyFixture struct {
	t       *testing.T
	repos   *store.Repositories
	sender  *recordingSender
	applier *Applier
	now     time.Time
}

func newApplyFixture(t *testing.T) *applyFixture {
	t.Helper()
	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close() })
	require.NoError(t, mgr.ApplySchema(schema.SQL()))
	f := &applyFixture{t: t, repos: mgr.Repositories(), sender: &recordingSender{}, now: time.Date(2026, 11, 24, 3, 0, 0, 0, time.UTC)}
	f.applier = &Applier{
		Checks: f.repos.Checks,
		Sender: func(context.Context) (notification.Sender, error) { return f.sender, nil },
		Now:    func() time.Time { return f.now },
	}
	return f
}

func (f *applyFixture) apply(round Round) error {
	f.t.Helper()
	err := f.applier.Apply(context.Background(), round)
	f.now = f.now.Add(30 * time.Second)
	return err
}

func (f *applyFixture) row(checkID string) *domain.Check {
	f.t.Helper()
	row, err := f.repos.Checks.Get(context.Background(), "s", checkID)
	if err != nil {
		return nil
	}
	return row
}

func (f *applyFixture) events() []domain.AlertEvent {
	f.t.Helper()
	events, err := f.repos.Alerts.ListRecentEvents(context.Background(), 100)
	require.NoError(f.t, err)
	return events
}

func failing(checkID string) Fact {
	return Fact{SpaceID: "s", CheckID: checkID, Name: "检查 " + checkID, Status: domain.CheckStatusDown, Reason: "坏了"}
}

// fire 让一项业务检查进入告警（3 次失败）。
func (f *applyFixture) fire(checkID string) {
	f.t.Helper()
	for range 3 {
		require.NoError(f.t, f.apply(Round{Domain: DomainBusiness, Facts: []Fact{failing(checkID)}}))
	}
	require.True(f.t, f.row(checkID).Firing())
}

// 本轮不再产出的告警检查写恢复事件、推送恢复并删行；冻结的行不动；有事实的冻结行照常推进；别的域的行不删。
func TestApplyRemovesFreezesAndKeepsOtherDomains(t *testing.T) {
	f := newApplyFixture(t)
	f.fire("dataset:storage:a:1m")
	require.NoError(t, f.apply(Round{Domain: DomainHost, Facts: []Fact{{SpaceID: "s", CheckID: "host:control:presence", Name: "主机", Status: domain.CheckStatusOK}}}))
	require.NoError(t, f.apply(Round{Domain: DomainBusiness, Facts: []Fact{failing("dataset:storage:a:1m"), failing("kline_freshness:s:a:1m"), failing("reporter:control:collector")}}))

	f.sender.sent = nil
	require.NoError(t, f.apply(Round{Domain: DomainBusiness, Facts: []Fact{failing("kline_freshness:task_result_inventory")}, FrozenPrefixes: []string{"kline_freshness:"}}))
	require.Nil(t, f.row("dataset:storage:a:1m"), "不再产出的检查当轮删除")
	require.Nil(t, f.row("reporter:control:collector"), "没在告警的也删除，只是不写事件")
	require.NotNil(t, f.row("kline_freshness:s:a:1m"), "冻结前缀下没有事实的行不动")
	require.EqualValues(t, 1, f.row("kline_freshness:s:a:1m").FailureCount)
	require.EqualValues(t, 1, f.row("kline_freshness:task_result_inventory").FailureCount, "有事实的冻结行照常推进")
	require.NotNil(t, f.row("host:control:presence"), "业务域不删主机域的检查")
	events := f.events()
	require.Equal(t, domain.AlertEventResolved, events[0].EventType)
	require.Equal(t, "dataset:storage:a:1m", events[0].CheckID)
	require.Equal(t, "检查 dataset:storage:a:1m", events[0].Title)
	require.Equal(t, domain.SeverityCritical, events[0].Severity)
	require.JSONEq(t, `{"reason":"已停用或移除，不再检查"}`, events[0].Payload)
	require.Len(t, f.sender.sent, 1)
	require.Equal(t, notification.SeverityInfo, f.sender.sent[0].Severity)
}

// 推送全部失败：触发的置空 c_notified_at，下一轮按提醒重发；恢复的改回告警中，下一次达到阈值时重发。
func TestApplyRetriesAfterSendFailure(t *testing.T) {
	f := newApplyFixture(t)
	f.sender.err = errors.New("渠道不可用")
	check := "dataset:storage:a:1m"
	require.NoError(t, f.apply(Round{Domain: DomainBusiness, Facts: []Fact{failing(check)}}))
	require.NoError(t, f.apply(Round{Domain: DomainBusiness, Facts: []Fact{failing(check)}}))
	require.Error(t, f.apply(Round{Domain: DomainBusiness, Facts: []Fact{failing(check)}}))
	row := f.row(check)
	require.True(t, row.Firing())
	require.Nil(t, row.NotifiedAt, "推送没送达，下一轮重发")
	require.Equal(t, domain.AlertEventSendFailed, f.events()[0].EventType)

	f.sender.err = nil
	f.sender.sent = nil
	require.NoError(t, f.apply(Round{Domain: DomainBusiness, Facts: []Fact{failing(check)}}))
	require.Len(t, f.sender.sent, 1, "下一轮按提醒重发")
	require.Equal(t, domain.AlertEventReminder, f.events()[0].EventType)

	f.sender.err = errors.New("渠道不可用")
	passed := Fact{SpaceID: "s", CheckID: check, Name: "检查", Status: domain.CheckStatusOK}
	require.Error(t, f.apply(Round{Domain: DomainBusiness, Facts: []Fact{passed}}))
	row = f.row(check)
	require.True(t, row.Firing(), "恢复没送达，改回告警中")
	require.Zero(t, row.SuccessCount)

	f.sender.err = nil
	require.NoError(t, f.apply(Round{Domain: DomainBusiness, Facts: []Fact{passed}}))
	require.False(t, f.row(check).Firing())
	require.Equal(t, domain.AlertEventResolved, f.events()[0].EventType)
}

// 部分渠道送达只记 send_failed，不重试。
func TestApplyPartialSendDoesNotRetry(t *testing.T) {
	f := newApplyFixture(t)
	f.sender.err = &notifier.PartialError{Failures: []error{errors.New("飞书失败")}}
	check := "dataset:storage:a:1m"
	for range 3 {
		require.NoError(t, f.apply(Round{Domain: DomainBusiness, Facts: []Fact{failing(check)}}))
	}
	row := f.row(check)
	require.True(t, row.Firing())
	require.NotNil(t, row.NotifiedAt, "已有渠道送达，不按未送达重试")
	require.Equal(t, domain.AlertEventSendFailed, f.events()[0].EventType)
}

// 发送器为 nil（告警屏蔽或没有渠道）时照常推进、写事件，只是不推送。
func TestApplyWithoutSenderStillRecords(t *testing.T) {
	f := newApplyFixture(t)
	f.applier.Sender = func(context.Context) (notification.Sender, error) { return nil, nil }
	f.fire("dataset:storage:a:1m")
	require.Equal(t, domain.AlertEventTriggered, f.events()[0].EventType)
	require.Empty(t, f.sender.sent)
}

// 事实不属于本轮的域是编程错误：整轮不写。
func TestApplyRejectsFactsOfAnotherDomain(t *testing.T) {
	f := newApplyFixture(t)
	require.Error(t, f.apply(Round{Domain: DomainBusiness, Facts: []Fact{failing("host:control:cpu")}}))
	require.Nil(t, f.row("host:control:cpu"))
}
