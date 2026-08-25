package rpc

import (
	"context"
	"errors"
	"strings"
	"time"

	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/packages/notification"
)

func (s *Service) GetNotificationChannel(ctx context.Context, _ *monitorpb.GetNotificationChannelReq) (*monitorpb.GetNotificationChannelRsp, error) {
	if s.notifications == nil {
		return &monitorpb.GetNotificationChannelRsp{RetInfo: inner(errors.New("notification channel is unavailable"))}, nil
	}
	channel, err := s.notifications.GetGlobal(ctx)
	if err != nil {
		return &monitorpb.GetNotificationChannelRsp{RetInfo: inner(err)}, nil
	}
	return &monitorpb.GetNotificationChannelRsp{RetInfo: success(), Channel: &monitorpb.NotificationChannelSetting{ChannelType: channel.ChannelType, Configured: strings.TrimSpace(channel.WebhookURL) != "", MaskedUrl: maskNotificationURL(channel.WebhookURL), UpdatedAt: channel.UpdatedAt.UTC().Format(time.RFC3339Nano)}}, nil
}

func (s *Service) UpdateNotificationChannel(ctx context.Context, req *monitorpb.UpdateNotificationChannelReq) (*monitorpb.UpdateNotificationChannelRsp, error) {
	if s.notifications == nil {
		return &monitorpb.UpdateNotificationChannelRsp{RetInfo: inner(errors.New("notification channel is unavailable"))}, nil
	}
	if req == nil {
		req = &monitorpb.UpdateNotificationChannelReq{}
	}
	typ := strings.TrimSpace(req.GetChannelType())
	if typ == "" {
		typ = string(notification.ChannelTypeWeCom)
	}
	if _, err := notification.NewSender(notification.ChannelConfig{Type: notification.ChannelType(typ), WebhookURL: req.GetWebhookUrl()}); err != nil {
		return &monitorpb.UpdateNotificationChannelRsp{RetInfo: invalid(err)}, nil
	}
	if err := s.notifications.UpdateGlobal(ctx, typ, req.GetWebhookUrl()); err != nil {
		return &monitorpb.UpdateNotificationChannelRsp{RetInfo: invalid(err)}, nil
	}
	got, err := s.GetNotificationChannel(ctx, &monitorpb.GetNotificationChannelReq{})
	if err != nil {
		return &monitorpb.UpdateNotificationChannelRsp{RetInfo: inner(err)}, nil
	}
	return &monitorpb.UpdateNotificationChannelRsp{RetInfo: got.RetInfo, Channel: got.Channel}, nil
}

func maskNotificationURL(raw string) string {
	if raw == "" {
		return ""
	}
	if len(raw) <= 12 {
		return "********"
	}
	return raw[:8] + "..." + raw[len(raw)-4:]
}
