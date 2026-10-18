// Package notifier 把一条告警推送给全部通知渠道。
//
// 送达规则：至少一个渠道送达就算送达。部分渠道失败时返回 *PartialError，调用方只记录失败的渠道、不再重试，
// 否则一个坏掉的机器人会让已经收到的群在每次重试时重复收到同一条告警；全部失败才返回普通错误，由调用方按未送达重试。
package notifier

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/packages/notification"
)

// Lister 列出全部通知渠道。
type Lister interface {
	List(context.Context) ([]domain.NotificationChannel, error)
}

// Sender 返回发给全部渠道的发送器；没有渠道时返回 nil，调用方只记录告警、不推送。
func Sender(ctx context.Context, lister Lister) (notification.Sender, error) {
	if lister == nil {
		return nil, nil
	}
	channels, err := lister.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("读取通知渠道失败：%w", err)
	}
	if len(channels) == 0 {
		return nil, nil
	}
	out := make(fanout, 0, len(channels))
	for _, channel := range channels {
		sender, err := notification.NewSender(notification.ChannelConfig{Type: notification.ChannelType(channel.ChannelType), WebhookURL: channel.WebhookURL})
		out = append(out, target{label: Label(channel), sender: sender, err: err})
	}
	return out, nil
}

// Label 给出渠道在失败记录里的称呼：平台、域名和 URL 末 4 位。失败记录对所有登录用户可见，不能带出完整 URL。
func Label(channel domain.NotificationChannel) string {
	platform := map[string]string{"wecom": "企业微信", "feishu": "飞书"}[channel.ChannelType]
	if platform == "" {
		platform = channel.ChannelType
	}
	raw := strings.TrimSpace(channel.WebhookURL)
	host := ""
	if parsed, err := url.Parse(raw); err == nil {
		host = parsed.Host
	}
	tail := raw
	if runes := []rune(raw); len(runes) > 4 {
		tail = string(runes[len(runes)-4:])
	}
	return fmt.Sprintf("%s（%s …%s）", platform, host, tail)
}

// PartialError 表示部分渠道失败、但至少一个渠道已送达。
type PartialError struct {
	Failures []error
}

func (e *PartialError) Error() string {
	return "部分通知渠道发送失败：" + errors.Join(e.Failures...).Error()
}

// IsPartial 判断错误是否只是部分渠道失败（消息已送达）。
func IsPartial(err error) bool {
	var partial *PartialError
	return errors.As(err, &partial)
}

type target struct {
	label  string
	sender notification.Sender
	// err 是渠道配置本身的问题（例如库里的 URL 已不合法），每次发送都按失败计。
	err error
}

type fanout []target

// Send 依次发给每个渠道。全部送达返回 nil，部分失败返回 *PartialError，全部失败返回各渠道错误的合并。
func (f fanout) Send(ctx context.Context, message notification.Message) error {
	var failures []error
	for _, item := range f {
		err := item.err
		if err == nil {
			err = item.sender.Send(ctx, message)
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("%s：%w", item.label, err))
		}
	}
	switch {
	case len(failures) == 0:
		return nil
	case len(failures) < len(f):
		return &PartialError{Failures: failures}
	default:
		return errors.Join(failures...)
	}
}
