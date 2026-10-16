package healthview

import (
	"context"
	"sort"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/observability"
	pb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
)

// IncidentWindow 是概览回看告警经过的时间窗口。
const IncidentWindow = 48 * time.Hour

// maxIncidentEvents 是窗口内最多读取的事件数（只保留最新的）：持续告警每 5 分钟一条提醒，单个检查 48 小时约 580 条。
var maxIncidentEvents = 20000

const maxIncidents = 300

type incidentKey struct{ space, check string }

type incident struct {
	key          incidentKey
	triggeredAt  time.Time
	resolvedAt   time.Time
	firstEventAt time.Time
	reason       string
	reminders    int32
	notifyFailed bool
	open         bool
}

// incidents 把窗口内的告警事件按检查归并成一条条“触发 → 恢复”的经过。
//   - 恢复通知发送失败时评估器已写入恢复事件、告警仍在进行，随后的发送失败、重试恢复或继续提醒都归入同一条经过；
//   - 触发早于窗口（或被事件数上限截掉）的经过，从更早的触发事件补齐触发时间和原因；
//   - 进行中与否以当前告警集合为准：没有恢复记录又不在告警的经过是被撤销的（检查停用或删除）。
func (b Builder) incidents(ctx context.Context, spaceID string, facts observability.Overview, active map[incidentKey]bool, titles *checkTitles, now time.Time) ([]*pb.AlertIncident, error) {
	since := now.Add(-IncidentWindow)
	var spaces []string
	if spaceID != "" {
		spaces = []string{spaceID, "", hostmetrics.SpaceID}
	}
	events, err := b.Alerts.ListEventsSince(ctx, spaces, since, maxIncidentEvents)
	if err != nil {
		return nil, err
	}
	// 读满上限说明更早的窗口内事件被截掉了，补查触发时要从已读到的最早事件往前找。
	lookupBefore := since
	if len(events) >= maxIncidentEvents && len(events) > 0 {
		lookupBefore = events[0].CreatedAt
	}
	latest := make(map[incidentKey]*incident)
	var all []*incident
	for _, event := range events {
		key := incidentKey{space: event.SpaceID, check: event.CheckID}
		current := latest[key]
		if event.EventType == domain.AlertEventTriggered {
			current = &incident{key: key, triggeredAt: event.CreatedAt, firstEventAt: event.CreatedAt, reason: eventReason(event), open: true}
			latest[key] = current
			all = append(all, current)
			continue
		}
		if current == nil {
			if event.EventType != domain.AlertEventReminder && event.EventType != domain.AlertEventResolved {
				// 发送失败等记录不足以构成一条经过。
				continue
			}
			current = &incident{key: key, firstEventAt: event.CreatedAt, open: true}
			trigger, err := b.Alerts.LatestTriggeredEventBefore(ctx, key.space, key.check, lookupBefore)
			if err != nil {
				return nil, err
			}
			if trigger != nil {
				current.triggeredAt, current.reason = trigger.CreatedAt, eventReason(*trigger)
			}
			latest[key] = current
			all = append(all, current)
		}
		switch event.EventType {
		case domain.AlertEventReminder:
			// 恢复通知失败后告警仍在进行，再次失败时继续提醒：同一条经过重新进行中。
			current.reminders++
			current.open, current.resolvedAt = true, time.Time{}
		case domain.AlertEventResolved:
			current.open, current.resolvedAt = false, event.CreatedAt
		case domain.AlertEventSendFailed:
			current.notifyFailed = true
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].sortTime().After(all[j].sortTime()) })
	if len(all) > maxIncidents {
		all = all[:maxIncidents]
	}
	out := make([]*pb.AlertIncident, 0, len(all))
	for _, item := range all {
		object := alertObject(item.key.space, item.key.check, facts)
		title, err := titles.title(ctx, item.key.space, item.key.check, object, facts)
		if err != nil {
			return nil, err
		}
		out = append(out, &pb.AlertIncident{
			Id:            item.key.space + ":" + item.key.check + "@" + stamp(item.sortTime()),
			Severity:      alertSeverity(item.key.check, object),
			Object:        object,
			Title:         title,
			Reason:        item.reason,
			TriggeredAt:   stamp(item.triggeredAt),
			ResolvedAt:    stamp(item.resolvedAt),
			Active:        latest[item.key] == item && active[item.key],
			ReminderCount: item.reminders,
			NotifyFailed:  item.notifyFailed,
		})
	}
	return out, nil
}

func (i *incident) sortTime() time.Time {
	if !i.triggeredAt.IsZero() {
		return i.triggeredAt
	}
	return i.firstEventAt
}
