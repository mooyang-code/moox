package rpc

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/notification"
	"gorm.io/gorm"
)

var errNotificationUnavailable = errors.New("notification channels are unavailable")

// ListNotificationChannels 返回全部推送渠道，含完整 Webhook URL；控制台只允许全局管理员调用。密钥不返回，只给出是否设置。
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

// ExportNotificationChannels 导出全部推送渠道（含完整 Webhook URL 与签名密钥），只给 moox-cli 卸载前备份使用。
func (s *Service) ExportNotificationChannels(ctx context.Context, _ *monitorpb.ExportNotificationChannelsReq) (*monitorpb.ExportNotificationChannelsRsp, error) {
	if s.notifications == nil {
		return &monitorpb.ExportNotificationChannelsRsp{RetInfo: inner(errNotificationUnavailable)}, nil
	}
	channels, err := s.notifications.List(ctx)
	if err != nil {
		return &monitorpb.ExportNotificationChannelsRsp{RetInfo: inner(err)}, nil
	}
	out := make([]*monitorpb.ExportedNotificationChannel, 0, len(channels))
	for _, channel := range channels {
		out = append(out, &monitorpb.ExportedNotificationChannel{
			ChannelId:   channel.ChannelID,
			ChannelType: channel.ChannelType,
			WebhookUrl:  channel.WebhookURL,
			Secret:      channel.Secret,
			CreatedAt:   millis(channel.CreatedAt),
		})
	}
	return &monitorpb.ExportNotificationChannelsRsp{RetInfo: success(), Channels: out}, nil
}

// ImportNotificationChannels 按 webhook_url 幂等覆盖导入告警渠道，只给 moox-cli 重装恢复使用。
// 任意一条校验失败（URL、平台、密钥不合法或同批内重复）时整批拒绝，不修改现有渠道。
func (s *Service) ImportNotificationChannels(ctx context.Context, req *monitorpb.ImportNotificationChannelsReq) (*monitorpb.ImportNotificationChannelsRsp, error) {
	if s.notifications == nil {
		return &monitorpb.ImportNotificationChannelsRsp{RetInfo: inner(errNotificationUnavailable)}, nil
	}
	raw := req.GetChannels()
	items := make([]store.NotificationChannelInput, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for i, ch := range raw {
		typ, url, secret := strings.TrimSpace(ch.GetChannelType()), strings.TrimSpace(ch.GetWebhookUrl()), strings.TrimSpace(ch.GetSecret())
		if url == "" {
			return &monitorpb.ImportNotificationChannelsRsp{RetInfo: invalid(fmt.Errorf("第 %d 条渠道的 Webhook URL 不能为空", i+1))}, nil
		}
		if _, err := notification.NewSender(notification.ChannelConfig{Type: notification.ChannelType(typ), WebhookURL: url, Secret: secret}); err != nil {
			return &monitorpb.ImportNotificationChannelsRsp{RetInfo: invalid(fmt.Errorf("第 %d 条渠道无效：%w", i+1, err))}, nil
		}
		if _, dup := seen[url]; dup {
			return &monitorpb.ImportNotificationChannelsRsp{RetInfo: invalid(fmt.Errorf("第 %d 条渠道的 Webhook URL 与前面重复", i+1))}, nil
		}
		seen[url] = struct{}{}
		items = append(items, store.NotificationChannelInput{ChannelType: typ, WebhookURL: url, Secret: secret})
	}
	created, updated, err := s.notifications.Import(ctx, items)
	if err != nil {
		return &monitorpb.ImportNotificationChannelsRsp{RetInfo: inner(err)}, nil
	}
	return &monitorpb.ImportNotificationChannelsRsp{RetInfo: success(), Created: int32(created), Updated: int32(updated)}, nil
}

// AddNotificationChannel 新增一个推送渠道；URL 必须是所选平台的 HTTPS 机器人地址（通用 Webhook 不限域名），同一个 URL 不能重复添加。
// 密钥选填：飞书（签名校验）、钉钉（加签）、通用 Webhook（签名头）使用，企业微信不接受；要改密钥就删掉渠道重加。
func (s *Service) AddNotificationChannel(ctx context.Context, req *monitorpb.AddNotificationChannelReq) (*monitorpb.AddNotificationChannelRsp, error) {
	if s.notifications == nil {
		return &monitorpb.AddNotificationChannelRsp{RetInfo: inner(errNotificationUnavailable)}, nil
	}
	typ, url, secret := strings.TrimSpace(req.GetChannelType()), strings.TrimSpace(req.GetWebhookUrl()), strings.TrimSpace(req.GetSecret())
	if url == "" {
		return &monitorpb.AddNotificationChannelRsp{RetInfo: invalid(errors.New("Webhook URL 不能为空"))}, nil
	}
	if _, err := notification.NewSender(notification.ChannelConfig{Type: notification.ChannelType(typ), WebhookURL: url, Secret: secret}); err != nil {
		return &monitorpb.AddNotificationChannelRsp{RetInfo: invalid(err)}, nil
	}
	channel, err := s.notifications.Add(ctx, typ, url, secret)
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
		CreatedAt: millis(channel.CreatedAt), HasSecret: channel.Secret != "",
	}
}
