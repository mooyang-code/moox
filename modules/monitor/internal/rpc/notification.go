package rpc

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/notification"
	"gorm.io/gorm"
)

var errNotificationUnavailable = errors.New("notification channels are unavailable")

// ListNotificationChannels 返回全部推送渠道，含完整 Webhook URL；控制台只允许全局管理员调用。
func (s *Service) ListNotificationChannels(ctx context.Context, _ *monitorpb.ListNotificationChannelsReq) (*monitorpb.ListNotificationChannelsRsp, error) {
	if s.notifications == nil {
		return &monitorpb.ListNotificationChannelsRsp{RetInfo: inner(errNotificationUnavailable)}, nil
	}
	channels, err := s.notifications.List(ctx)
	if err != nil {
		return &monitorpb.ListNotificationChannelsRsp{RetInfo: inner(err)}, nil
	}
	out := make([]*monitorpb.NotificationChannel, 0, len(channels))
	for _, channel := range channels {
		out = append(out, notificationChannelPB(channel))
	}
	return &monitorpb.ListNotificationChannelsRsp{RetInfo: success(), Channels: out}, nil
}

// AddNotificationChannel 新增一个推送渠道；URL 必须是所选平台的 HTTPS 机器人地址，同一个 URL 不能重复添加。
func (s *Service) AddNotificationChannel(ctx context.Context, req *monitorpb.AddNotificationChannelReq) (*monitorpb.AddNotificationChannelRsp, error) {
	if s.notifications == nil {
		return &monitorpb.AddNotificationChannelRsp{RetInfo: inner(errNotificationUnavailable)}, nil
	}
	typ, url := strings.TrimSpace(req.GetChannelType()), strings.TrimSpace(req.GetWebhookUrl())
	if url == "" {
		return &monitorpb.AddNotificationChannelRsp{RetInfo: invalid(errors.New("Webhook URL 不能为空"))}, nil
	}
	if _, err := notification.NewSender(notification.ChannelConfig{Type: notification.ChannelType(typ), WebhookURL: url}); err != nil {
		return &monitorpb.AddNotificationChannelRsp{RetInfo: invalid(err)}, nil
	}
	channel, err := s.notifications.Add(ctx, typ, url)
	if err != nil {
		return &monitorpb.AddNotificationChannelRsp{RetInfo: invalid(err)}, nil
	}
	return &monitorpb.AddNotificationChannelRsp{RetInfo: success(), Channel: notificationChannelPB(*channel)}, nil
}

// DeleteNotificationChannel 删除一个推送渠道；删光后告警只记录、不推送。
func (s *Service) DeleteNotificationChannel(ctx context.Context, req *monitorpb.DeleteNotificationChannelReq) (*monitorpb.DeleteNotificationChannelRsp, error) {
	if s.notifications == nil {
		return &monitorpb.DeleteNotificationChannelRsp{RetInfo: inner(errNotificationUnavailable)}, nil
	}
	id := strings.TrimSpace(req.GetChannelId())
	if id == "" {
		return &monitorpb.DeleteNotificationChannelRsp{RetInfo: invalid(errors.New("channel_id 不能为空"))}, nil
	}
	if err := s.notifications.Delete(ctx, id); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return &monitorpb.DeleteNotificationChannelRsp{RetInfo: retInfo(commonpb.ErrorCode_NOT_FOUND, "通知渠道不存在")}, nil
		}
		return &monitorpb.DeleteNotificationChannelRsp{RetInfo: inner(err)}, nil
	}
	return &monitorpb.DeleteNotificationChannelRsp{RetInfo: success()}, nil
}

func notificationChannelPB(channel domain.NotificationChannel) *monitorpb.NotificationChannel {
	return &monitorpb.NotificationChannel{
		ChannelId: channel.ChannelID, ChannelType: channel.ChannelType, WebhookUrl: channel.WebhookURL,
		CreatedAt: channel.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
}
