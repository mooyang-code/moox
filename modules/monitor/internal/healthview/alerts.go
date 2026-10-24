package healthview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/observability"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	pb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/packages/report"
	"gorm.io/gorm"
)

// Check IDs are code-owned namespaces. Parse their identity fields exactly;
// human names and substring matches never determine an alert's target.
func alertObject(spaceID, checkID string, facts observability.Overview) *pb.HealthObject {
	object := &pb.HealthObject{Type: "business", SpaceId: spaceID}
	parts := strings.Split(checkID, ":")
	object.Kind = parts[0]
	if len(parts) == 3 {
		switch parts[0] {
		case "deployment", "reporter":
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

// alertSeverity 区分告警轻重：critical 表示服务不可用、数据中断、主机失联或网关异常；
// warning 表示还在工作但有风险，例如资源超过阈值、运行指标上报中断、采集任务清单过期、指标序列新增过快。
func alertSeverity(checkID string, object *pb.HealthObject) string {
	switch {
	case object.Kind == "reporter", object.Kind == "metrics_ingest", checkID == monmetrics.TaskResultInventoryCheckID:
		return "warning"
	case object.Type == "host" && hostmetrics.MetricLabel(object.Kind) != "":
		return "warning"
	default:
		return "critical"
	}
}

// fallbackTitle 在检查名称缺失时（主机阈值、主机失联没有检查记录）用身份字段拼出标题。
func fallbackTitle(checkID string, object *pb.HealthObject, facts observability.Overview) string {
	switch {
	case object.Type == "component":
		return componentName(facts, object.ComponentId) + "（" + object.HostId + "）"
	case object.Kind == "console-page":
		return "控制台页面（" + object.HostId + "）"
	case object.Type == "dataset":
		return object.DatasetId + " · " + object.Freq
	case object.Type == "host":
		identity := object.HostId
		if identity == "" {
			identity = object.AgentId
		}
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

// eventReason 取告警事件记录的问题描述：主机阈值告警由事件里的指标值和阈值生成，其余告警取 payload 的 reason。
func eventReason(event domain.AlertEvent) string {
	var payload struct {
		Reason    string   `json:"reason"`
		Metric    string   `json:"metric"`
		Value     *float64 `json:"value"`
		Threshold *float64 `json:"threshold"`
	}
	if json.Unmarshal([]byte(event.Payload), &payload) != nil {
		return ""
	}
	if payload.Reason != "" {
		return ChineseReason(payload.Reason)
	}
	if label := hostmetrics.MetricLabel(payload.Metric); label != "" && payload.Value != nil && payload.Threshold != nil {
		return fmt.Sprintf("%s %.1f%%，超过阈值 %s%%", label, *payload.Value, strconv.FormatFloat(*payload.Threshold, 'f', -1, 64))
	}
	return ""
}

func (b Builder) projectAlert(ctx context.Context, state domain.AlertState, facts observability.Overview, titles *checkTitles) (*pb.HealthAlert, error) {
	object := alertObject(state.SpaceID, state.CheckID, facts)
	title, err := titles.title(ctx, state.SpaceID, state.CheckID, object, facts)
	if err != nil {
		return nil, err
	}
	alert := &pb.HealthAlert{Id: state.DedupeKey, Severity: alertSeverity(state.CheckID, object), Object: object, Title: title, Reason: "监控项持续异常", LastCheckedAt: stamp(state.UpdatedAt)}
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
	if object.Type == "host" && object.AgentId != "" && b.Alerts != nil {
		event, err := b.Alerts.LatestFiringEvent(ctx, state.SpaceID, state.RuleID, state.CheckID)
		if err != nil {
			return nil, err
		}
		if event != nil {
			if reason := eventReason(*event); reason != "" {
				alert.Reason = reason
			}
			if alert.LastCheckedAt == "" {
				alert.LastCheckedAt = stamp(event.CreatedAt)
			}
		}
	}
	return alert, nil
}
