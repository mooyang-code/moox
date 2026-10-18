package notifier

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/packages/notification"
)

type stubSender struct {
	err   error
	calls int
}

func (s *stubSender) Send(context.Context, notification.Message) error {
	s.calls++
	return s.err
}

func TestFanoutDeliversToEveryChannelAndReportsPartialFailure(t *testing.T) {
	ok, broken := &stubSender{}, &stubSender{err: errors.New("notification: WeCom returned HTTP 404")}
	sender := fanout{{label: "企业微信（a …1111）", sender: ok}, {label: "飞书（b …2222）", sender: broken}}
	err := sender.Send(t.Context(), notification.Message{})
	if !IsPartial(err) {
		t.Fatalf("one delivered channel must make the send partial, got %v", err)
	}
	if ok.calls != 1 || broken.calls != 1 {
		t.Fatalf("every channel must be tried once: ok=%d broken=%d", ok.calls, broken.calls)
	}
	if !strings.Contains(err.Error(), "飞书（b …2222）") || strings.Contains(err.Error(), "企业微信") {
		t.Fatalf("partial error must name only the failed channel: %v", err)
	}
}

func TestFanoutFailsWhenNoChannelDelivers(t *testing.T) {
	sender := fanout{
		{label: "企业微信（a …1111）", sender: &stubSender{err: errors.New("timeout")}},
		{label: "飞书（b …2222）", err: errors.New("notification: webhook host is not approved for the selected platform")},
	}
	err := sender.Send(t.Context(), notification.Message{})
	if err == nil || IsPartial(err) {
		t.Fatalf("all channels failed, want a full failure, got %v", err)
	}
}

type staticLister []domain.NotificationChannel

func (l staticLister) List(context.Context) ([]domain.NotificationChannel, error) { return l, nil }

func TestSenderIsNilWithoutChannels(t *testing.T) {
	sender, err := Sender(t.Context(), staticLister{})
	if err != nil || sender != nil {
		t.Fatalf("no channels must mean no sender: %v %v", sender, err)
	}
	sender, err = Sender(t.Context(), staticLister{{ChannelType: "wecom", WebhookURL: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=abcd"}})
	if err != nil || sender == nil {
		t.Fatalf("a configured channel must produce a sender: %v %v", sender, err)
	}
}

func TestLabelHidesTheWebhookKey(t *testing.T) {
	label := Label(domain.NotificationChannel{ChannelType: "wecom", WebhookURL: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=0f6c3c1e-1234-4abc-9def-5a7b8c9d0e1f"})
	if label != "企业微信（qyapi.weixin.qq.com …0e1f）" {
		t.Fatalf("label = %q", label)
	}
}
