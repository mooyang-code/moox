package healthview

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/mooyang-code/moox/modules/monitor/internal/alerting"
	"github.com/mooyang-code/moox/modules/monitor/internal/alerttext"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/observability"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	pb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/packages/report"
	"gorm.io/gorm"
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
		case "gateway":
			object.Type, object.HostId, object.ComponentId, object.Kind = "host", parts[1], "host-gateway", parts[2]
		case "host":
			object.Type, object.HostId, object.Kind = "host", parts[1], parts[2]
		}
	}
	// 关注项检查按登记过的种类给出 attention_kind，前端不用再从告警编号里截取。
	if len(parts) == 2 && observability.AttentionCheckName(parts[0], parts[1]) != "" {
		object.AttentionKind = parts[1]
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

// fallbackTitle 在检查记录已经不在时（例如对象删除后检查被清理，告警经过里还留着它）用身份字段拼出标题。
func fallbackTitle(checkID string, object *pb.HealthObject, facts observability.Overview) string {
	switch {
	case object.Type == "component":
		return componentName(facts, object.ComponentId) + "（" + object.HostId + "）"
	case object.Type == "dataset":
		return object.DatasetId + " · " + object.Freq
	case object.Type == "host":
		identity := object.HostId
		switch object.Kind {
		case "presence":
			return "主机 " + identity + " · 失联"
		case "status":
			return "主机网关（" + identity + "）"
		}
		if label := hostmetrics.MetricLabel(object.Kind); label != "" {
			return "主机 " + identity + " · " + label
		}
		return "主机 " + identity
	case object.ComponentId != "":
		return componentName(facts, object.ComponentId) + "业务检查"
	}
	return checkID
}

// checkTitles 缓存检查名称：页面标题与企业微信、飞书推送的标题一致。
type checkTitles struct {
	checks *store.CheckRepository
	names  map[string]string
}

func newCheckTitles(checks *store.CheckRepository) *checkTitles {
	return &checkTitles{checks: checks, names: make(map[string]string)}
}

func (t *checkTitles) title(ctx context.Context, spaceID, checkID string, object *pb.HealthObject, facts observability.Overview) (string, error) {
	key := spaceID + "\x00" + checkID
	if name, ok := t.names[key]; ok {
		return name, nil
	}
	name := ""
	if t.checks != nil {
		check, err := t.checks.Get(ctx, spaceID, checkID)
		switch {
		case err == nil:
			name = strings.TrimSpace(check.Name)
		case !errors.Is(err, gorm.ErrRecordNotFound):
			return "", err
		}
	}
	if name == "" {
		name = fallbackTitle(checkID, object, facts)
	}
	t.names[key] = name
	return name, nil
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

func (b Builder) projectAlert(ctx context.Context, state domain.AlertState, facts observability.Overview, titles *checkTitles) (*pb.HealthAlert, error) {
	object := alertObject(state.SpaceID, state.CheckID)
	title, err := titles.title(ctx, state.SpaceID, state.CheckID, object, facts)
	if err != nil {
		return nil, err
	}
	alert := &pb.HealthAlert{Id: state.DedupeKey, Severity: alerting.Severity(state.CheckID), Object: object, Title: title, Reason: "监控项持续异常", LastCheckedAt: stamp(state.UpdatedAt)}
	if state.TriggeredAt != nil {
		alert.TriggeredAt = stamp(*state.TriggeredAt)
	}
	if b.Results != nil {
		results, err := b.Results.Recent(ctx, state.SpaceID, state.CheckID, 1)
		if err != nil {
			return nil, err
		}
		if len(results) > 0 {
			latest := &results[0]
			alert.LastCheckedAt = stamp(latest.CheckedAt)
			alert.Recovering = latest.Success
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
			// 恢复确认期间保留失败时的原因，不用已经恢复的数据集状态覆盖；水位等事实单独给出。
			alert.Dataset = projectDataset(dataset)
			if dataset.Status != "healthy" {
				alert.Reason = ChineseReason(dataset.Reason)
				alert.RawError = rawReason(dataset.Reason, alert.RawError)
			}
			break
		}
	}
	if _, _, hostCheck := hostmetrics.ParseCheckID(state.CheckID); hostCheck && b.Alerts != nil {
		event, err := b.Alerts.LatestFiringEvent(ctx, state.SpaceID, state.RuleID, state.CheckID)
		if err != nil {
			return nil, err
		}
		if event != nil {
			alert.Processes = eventProcesses(*event)
		}
	}
	return alert, nil
}
