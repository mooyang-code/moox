package rpc

import (
	"context"
	"errors"

	"github.com/mooyang-code/moox/modules/monitor/internal/healthview"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
)

func (s *Service) GetHealthOverview(ctx context.Context, req *monitorpb.GetHealthOverviewReq) (*monitorpb.GetHealthOverviewRsp, error) {
	if s.healthView == nil {
		return &monitorpb.GetHealthOverviewRsp{RetInfo: inner(errors.New("health overview is unavailable"))}, nil
	}
	var spaceID string
	if req != nil {
		spaceID = req.GetSpaceId()
	}
	overview, err := s.healthView.Build(ctx, spaceID)
	if err != nil {
		return &monitorpb.GetHealthOverviewRsp{RetInfo: inner(err)}, nil
	}
	return &monitorpb.GetHealthOverviewRsp{RetInfo: success(), Overview: healthOverviewToPB(overview)}, nil
}

func healthOverviewToPB(value healthview.Overview) *monitorpb.HealthOverview {
	out := &monitorpb.HealthOverview{GeneratedAt: timeToString(value.GeneratedAt), NotificationChannelType: value.NotificationType, NotificationConfigured: value.NotificationConfigured, NotificationWebhookMasked: value.NotificationMasked, Alerts: make([]*monitorpb.HealthAlert, 0, len(value.Alerts)), BusinessItems: make([]*monitorpb.HealthItem, 0, len(value.BusinessItems)), ServiceItems: make([]*monitorpb.HealthItem, 0, len(value.ServiceItems))}
	for _, item := range value.Alerts {
		out.Alerts = append(out.Alerts, &monitorpb.HealthAlert{Id: item.ID, Title: item.Title, Status: item.Status, Reason: item.Reason, CheckedAt: timeToString(item.CheckedAt), Severity: item.Severity})
	}
	for _, item := range value.BusinessItems {
		out.BusinessItems = append(out.BusinessItems, healthItemToPB(item))
	}
	for _, item := range value.ServiceItems {
		out.ServiceItems = append(out.ServiceItems, healthItemToPB(item))
	}
	return out
}

func healthItemToPB(item healthview.Item) *monitorpb.HealthItem {
	out := &monitorpb.HealthItem{Id: item.ID, Group: item.Group, Name: item.Name, Description: item.Description, Status: item.Status, Reason: item.Reason, CheckedAt: timeToString(item.CheckedAt), OmittedInstanceCount: item.OmittedInstanceCount, Instances: make([]*monitorpb.HealthInstance, 0, len(item.Instances))}
	for _, instance := range item.Instances {
		out.Instances = append(out.Instances, &monitorpb.HealthInstance{Name: instance.Name, NodeId: instance.NodeID, InstanceId: instance.InstanceID, Status: instance.Status, Conclusion: instance.Conclusion, LastCheckedAt: timeToString(instance.LastCheckedAt)})
	}
	return out
}
