package healthview

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/observability"
	pb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/packages/report"
)

// Check IDs are code-owned namespaces. Parse their identity fields exactly;
// human names and substring matches never determine an alert's target.
func alertObject(spaceID, checkID string, facts observability.Overview) *pb.HealthObject {
	object := &pb.HealthObject{Type: "business", SpaceId: spaceID}
	parts := strings.Split(checkID, ":")
	object.Kind = parts[0]
	if len(parts) == 3 {
		switch parts[0] {
		case "placement", "reporter":
			object.Type, object.HostId, object.ComponentId = "component", parts[1], parts[2]
		case "console-page":
			object.HostId, object.ComponentId = parts[1], parts[2]
		case "gateway":
			object.Type, object.HostId, object.ComponentId, object.Kind = "host", parts[1], "host-gateway", parts[2]
		case "host":
			object.Type, object.AgentId, object.Kind = "host", parts[1], parts[2]
			for _, host := range facts.Hosts {
				if host.AgentID == object.AgentId {
					object.HostId = host.HostID
					break
				}
			}
		}
	}
	if len(parts) >= 4 && parts[0] == "dataset" {
		object.Type, object.Producer, object.DatasetId, object.Freq = "dataset", parts[1], strings.Join(parts[2:len(parts)-1], ":"), parts[len(parts)-1]
	}
	if len(parts) == 2 && parts[0] == "module" {
		for _, check := range report.BuiltInModuleHealthChecks() {
			if check.ID == parts[1] {
				object.ComponentId = check.Module
				if check.Module == "factor" {
					object.ComponentId = "factor-mgr"
				}
				break
			}
		}
	}
	return object
}

func (b Builder) projectAlert(ctx context.Context, state domain.AlertState, facts observability.Overview) (*pb.HealthAlert, error) {
	object := alertObject(state.SpaceID, state.CheckID, facts)
	alert := &pb.HealthAlert{Id: state.DedupeKey, Severity: "critical", Object: object, Title: state.CheckID, Reason: "监控项持续异常", LastCheckedAt: stamp(state.UpdatedAt)}
	if state.TriggeredAt != nil {
		alert.TriggeredAt = stamp(*state.TriggeredAt)
	}
	switch {
	case object.Type == "component":
		alert.Title = componentName(facts, object.ComponentId) + "（" + object.HostId + "）"
	case object.Kind == "console-page":
		alert.Title = "控制台页面（" + object.HostId + "）"
	case object.Type == "dataset":
		alert.Title = object.DatasetId + " / " + object.Freq + "（" + object.Producer + "）"
	case object.Type == "host":
		identity := object.HostId
		if identity == "" {
			identity = object.AgentId
		}
		metric := map[string]string{"cpu": "CPU 使用率", "memory": "内存使用率", "filesystem_usage": "磁盘占用率", "disk_utilization": "磁盘利用率", "network_errors": "网络错误", "presence": "在线状态", "heartbeat": "网关心跳", "route_sync": "网关路由同步", "instance_conflict": "网关实例冲突"}[object.Kind]
		if metric == "" {
			metric = object.Kind
		}
		alert.Title = "主机 " + identity + " · " + metric
	case object.ComponentId != "":
		alert.Title = componentName(facts, object.ComponentId) + "业务检查"
	}
	if b.Results != nil {
		results, err := b.Results.Recent(ctx, state.SpaceID, state.CheckID, 1)
		if err != nil {
			return nil, err
		}
		if len(results) > 0 {
			latest := &results[0]
			alert.LastCheckedAt = stamp(latest.CheckedAt)
			cause := latest
			if latest.Success {
				failed, err := b.Results.LastFailure(ctx, state.SpaceID, state.CheckID)
				if err != nil {
					return nil, err
				}
				if failed != nil {
					cause = failed
				}
			}
			alert.Reason, alert.RawError = ChineseReason(cause.ErrorMessage), rawReason(cause.ErrorMessage, cause.RawError)
			if alert.Reason == "" {
				alert.Reason = "监控项持续异常"
			}
		}
	}
	if object.Type == "dataset" {
		for _, dataset := range facts.Datasets {
			if dataset.SpaceID != state.SpaceID || dataset.Producer != object.Producer || dataset.DatasetID != object.DatasetId || !strings.EqualFold(dataset.Freq, object.Freq) {
				continue
			}
			// Keep the failed probe during recovery, rather than replacing it with a
			// now-healthy dataset. Watermarks provide additional diagnostic context.
			if dataset.Status != "healthy" {
				alert.Reason = ChineseReason(dataset.Reason)
				alert.RawError = rawReason(dataset.Reason, alert.RawError)
			}
			alert.Reason += fmt.Sprintf("；最近成功 %s；输出水位 %s；落后 %d 秒", displayStamp(dataset.LastSuccessAt), displayStamp(dataset.OutputWatermarkAt), dataset.LagSeconds)
			break
		}
	}
	if object.Type == "host" && object.AgentId != "" && b.Alerts != nil {
		event, err := b.Alerts.LatestFiringEvent(ctx, state.SpaceID, state.RuleID, state.CheckID)
		if err != nil {
			return nil, err
		}
		if event != nil {
			if event.Message != "" {
				alert.Reason, alert.RawError = ChineseReason(event.Message), rawReason(event.Message, "")
			}
			if alert.LastCheckedAt == "" {
				alert.LastCheckedAt = stamp(event.CreatedAt)
			}
		}
	}
	return alert, nil
}

func displayStamp(at time.Time) string {
	if at.IsZero() {
		return "未知"
	}
	return stamp(at)
}
