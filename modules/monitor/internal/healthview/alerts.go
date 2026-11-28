package healthview

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/mooyang-code/moox/modules/monitor/internal/alerttext"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/observability"
	pb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/packages/doctor"
	"github.com/mooyang-code/moox/packages/report"
)

// Check IDs are code-owned namespaces. Parse their identity fields exactly;
// human names and substring matches never determine an alert's target.
func alertObject(spaceID, checkID string) *pb.HealthObject {
	object := &pb.HealthObject{Type: "business", SpaceId: spaceID}
	parts := strings.Split(checkID, ":")
	object.Kind = parts[0]
	if len(parts) == 3 {
		switch parts[0] {
		case "reporter", "ready", "restart":
			object.Type, object.HostId, object.ComponentId = "component", parts[1], parts[2]
		case "host":
			object.Type, object.HostId, object.Kind = "host", parts[1], parts[2]
		}
	}
	// 关注项检查（attention:<服务名>:<kind>）给出服务名与种类，前端不用再从告警编号里截取。
	if service, kind, ok := observability.ParseAttentionCheckID(checkID); ok {
		object.ComponentId, object.AttentionKind = service, kind
	}
	if len(parts) >= 4 && parts[0] == "dataset" {
		object.Type, object.Producer, object.DatasetId, object.Frequency = "dataset", parts[1], strings.Join(parts[2:len(parts)-1], ":"), parts[len(parts)-1]
	}
	if len(parts) == 2 && parts[0] == "module" {
		for _, check := range report.BuiltInModuleHealthChecks() {
			if check.ID == parts[1] {
				object.ComponentId = doctor.ModuleComponent(check.Module)
				break
			}
		}
	}
	return object
}

// eventReason 取告警事件记录的问题描述（payload 的 reason）。
func eventReason(event domain.AlertEvent) string {
	var payload struct {
		Reason string `json:"reason"`
	}
	if json.Unmarshal([]byte(event.Payload), &payload) != nil {
		return ""
	}
	return ChineseReason(payload.Reason)
}

// eventProcessLimit 是页面上一条告警列出的进程数。
const eventProcessLimit = 5

// eventProcesses 取告警事件里记下的进程排行（主机 CPU、内存告警在触发和提醒时记录），按告警的指标排好：
// 内存告警按内存、CPU 告警按 CPU，和推送正文同一个规则。没有时返回 nil。
func eventProcesses(event domain.AlertEvent) []*pb.AlertProcess {
	detail := alerttext.DecodeProcessDetail(event.Payload)
	ranked := alerttext.RankProcesses(detail.TopProcesses, detail.ByMemory(), eventProcessLimit)
	if len(ranked) == 0 {
		return nil
	}
	out := make([]*pb.AlertProcess, 0, len(ranked))
	for _, item := range ranked {
		out = append(out, &pb.AlertProcess{Pid: int32(item.PID), Name: item.Name, CpuPercent: item.CPUPercent, CpuAvailable: item.CPUAvailable, RssBytes: item.RSSBytes})
	}
	return out
}

// alertID 是告警在页面里的编号：空间加检查 ID。
func alertID(spaceID, checkID string) string { return spaceID + "/" + checkID }

// projectAlert 把一行在告警中的检查投影成页面告警。恢复确认期间（最近一次已通过）仍显示最近一次失败的原因。
func (b Builder) projectAlert(ctx context.Context, check domain.Check, facts observability.Overview) (*pb.HealthAlert, error) {
	object := alertObject(check.SpaceID, check.CheckID)
	alert := &pb.HealthAlert{
		Id: alertID(check.SpaceID, check.CheckID), Severity: check.Severity, Object: object, Title: check.Name,
		LastCheckedAt: stamp(check.CheckedAt), Recovering: check.Status != domain.CheckStatusDown,
	}
	if check.TriggeredAt != nil {
		alert.TriggeredAt = stamp(*check.TriggeredAt)
	}
	reason, raw := check.Reason, check.RawError
	if alert.Recovering {
		reason, raw = check.FailureReason, check.FailureRawError
	}
	alert.Reason, alert.RawError = ChineseReason(reason), rawReason(reason, raw)
	if alert.Reason == "" {
		alert.Reason = "监控项持续异常"
	}
	if object.Type == "dataset" {
		for _, dataset := range facts.Datasets {
			if dataset.SpaceID != check.SpaceID || dataset.Producer != object.Producer || dataset.DatasetID != object.DatasetId || !strings.EqualFold(dataset.Freq, object.Frequency) {
				continue
			}
			// 恢复确认期间保留失败时的原因，不用已经恢复的数据集状态覆盖；水位等事实单独给出。
			alert.Dataset = projectDataset(dataset)
			if dataset.Status != "healthy" {
				alert.Reason = ChineseReason(dataset.Reason)
				alert.RawError = rawReason(dataset.Reason, alert.RawError)
			}
			break
		}
	}
	if _, _, hostCheck := hostmetrics.ParseCheckID(check.CheckID); hostCheck && b.Alerts != nil {
		event, err := b.Alerts.LatestFiringEvent(ctx, check.SpaceID, check.CheckID)
		if err != nil {
			return nil, err
		}
		if event != nil {
			alert.Processes = eventProcesses(*event)
		}
	}
	return alert, nil
}
