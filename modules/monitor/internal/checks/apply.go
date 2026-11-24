package checks

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/notifier"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/packages/notification"
	"trpc.group/trpc-go/trpc-go/log"
)

// ReasonRemoved 是检查不再产出（对象停用、删除或配置关闭）时恢复事件的原因。
const ReasonRemoved = "已停用或移除，不再检查"

// fixTimeout 是推送失败后改回检查行、补记事件的时限：推送失败常常正是因为父 context 到期，改用不受取消影响的短期限 context。
const fixTimeout = 5 * time.Second

// Applier 按一轮事实推进检查表并推送通知。三个域在同一个 goroutine 里顺序执行，互不删除对方的检查。
type Applier struct {
	Checks *store.CheckRepository
	// Sender 返回发给全部通知渠道的发送器；返回 nil 表示没有渠道或告警屏蔽中，只记录、不推送。
	Sender func(context.Context) (notification.Sender, error)
	// KlineInterval 是 K 线新鲜度的评估间隔，决定 K 线检查的失败阈值。
	KlineInterval time.Duration
	Now           func() time.Time
}

func (a *Applier) now() time.Time {
	if a.Now != nil {
		return a.Now().UTC()
	}
	return time.Now().UTC()
}

// Load 读出某个域的全部检查行。
func (a *Applier) Load(ctx context.Context, d Domain) (map[store.CheckKey]domain.Check, error) {
	all, err := a.Checks.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	rows := make(map[store.CheckKey]domain.Check, len(all))
	for _, row := range all {
		if DomainOf(row.CheckID) == d {
			rows[store.KeyOf(row)] = row
		}
	}
	return rows, nil
}

// pending 是一条要推送的事件，以及推送全部失败时怎么改回检查行。
type pending struct {
	event domain.AlertEvent
	row   domain.Check
	// removed 表示检查行已删除：恢复推送失败只记 send_failed，不重试。
	removed bool
}

// Apply 推进一轮：有事实的检查按 Step 推进；本域里既没有事实、也没有冻结的旧行删除，删除时在告警的写一条恢复事件。
// 所有行的更新、删除和事件在一个事务里写入，提交后再逐条推送。只有写库失败才返回错误：推送失败已记成 send_failed 事件
// 并改回检查行（下一轮重发），只写日志，不能让一次渠道故障把整轮评估判成失败（那会让 Monitor 判自己评估停滞、主机采样被重复计数）。
func (a *Applier) Apply(ctx context.Context, round Round) error {
	rows, err := a.Load(ctx, round.Domain)
	if err != nil {
		return err
	}
	now := a.now()
	facts := make(map[store.CheckKey]Fact, len(round.Facts))
	order := make([]store.CheckKey, 0, len(round.Facts))
	for _, fact := range round.Facts {
		if DomainOf(fact.CheckID) != round.Domain {
			return fmt.Errorf("检查 %s 不属于这一轮的评估域", fact.CheckID)
		}
		key := fact.Key()
		if _, seen := facts[key]; !seen {
			order = append(order, key)
		}
		facts[key] = fact
	}
	var (
		upserts []domain.Check
		deletes []store.CheckKey
		events  []domain.AlertEvent
		sends   []pending
	)
	record := func(row domain.Check, eventType, reason, diagnostic string, removed bool) {
		event := domain.AlertEvent{
			EventID: uuid.NewString(), SpaceID: row.SpaceID, CheckID: row.CheckID, Title: row.Name, Severity: row.Severity,
			EventType: eventType, Message: message(eventType, row, reason, diagnostic, now),
			Payload: payload(eventType, reason, diagnostic), CreatedAt: now,
		}
		events = append(events, event)
		sends = append(sends, pending{event: event, row: row, removed: removed})
	}
	for _, key := range order {
		fact := facts[key]
		next, types := Step(rows[key], fact, RuleFor(fact.CheckID, a.KlineInterval), now)
		upserts = append(upserts, next)
		for _, eventType := range types {
			record(next, eventType, fact.Reason, fact.Diagnostic, false)
		}
	}
	for key, row := range rows {
		if _, produced := facts[key]; produced || round.frozen(key) {
			continue
		}
		deletes = append(deletes, key)
		if row.Firing() {
			row.AlertStatus = domain.AlertStatusOK
			record(row, domain.AlertEventResolved, ReasonRemoved, "", true)
		}
	}
	if err := a.Checks.SaveRound(ctx, upserts, deletes, events); err != nil {
		return err
	}
	a.send(ctx, sends)
	return nil
}

// send 逐条推送。triggered、reminder 全部渠道失败：记 send_failed、把 c_notified_at 置空（下一轮按提醒重发）；
// resolved 全部失败：记 send_failed、改回告警中并清零成功计数（下一次达到恢复阈值时重发）。部分渠道送达只记 send_failed，不重试。
// 因删除而写的恢复推送失败只记 send_failed。失败都写日志。
func (a *Applier) send(ctx context.Context, sends []pending) {
	if a.Sender == nil || len(sends) == 0 {
		return
	}
	for _, item := range sends {
		sender, err := a.Sender(ctx)
		if err == nil && sender == nil {
			continue
		}
		if err == nil {
			err = sender.Send(ctx, notification.Message{
				Key: item.event.SpaceID + "/" + item.event.CheckID, Severity: notificationSeverity(item.event.EventType, item.event.Severity),
				Title: item.event.Title, Body: item.event.Message,
			})
			if err != nil {
				err = fmt.Errorf("send alert notification: %w", err)
			}
		}
		if err == nil {
			continue
		}
		partial := notifier.IsPartial(err)
		// 部分渠道已送达时不按未送达重试，免得已收到的群重复收到。
		if fixErr := a.recordSendFailure(ctx, item, err, !partial); fixErr != nil {
			log.ErrorContextf(ctx, "monitor alert send failure not recorded check=%s: %v", item.event.CheckID, fixErr)
		}
		log.WarnContextf(ctx, "monitor alert notification failed check=%s partial=%t: %v", item.event.CheckID, partial, err)
	}
}

func (a *Applier) recordSendFailure(ctx context.Context, item pending, sendErr error, retry bool) error {
	fixCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fixTimeout)
	defer cancel()
	now := a.now()
	failed := domain.AlertEvent{
		EventID: uuid.NewString(), SpaceID: item.row.SpaceID, CheckID: item.row.CheckID, Title: item.row.Name, Severity: item.row.Severity,
		EventType: domain.AlertEventSendFailed, Message: "通知发送失败：" + sendErr.Error(),
		Payload: payload(domain.AlertEventSendFailed, item.row.FailureReason, ""), CreatedAt: now,
	}
	var upserts []domain.Check
	if retry && !item.removed {
		row := item.row
		row.NotifiedAt = nil
		if item.event.EventType == domain.AlertEventResolved {
			row.AlertStatus, row.SuccessCount = domain.AlertStatusFiring, 0
		}
		upserts = append(upserts, row)
	}
	return a.Checks.SaveRound(fixCtx, upserts, nil, []domain.AlertEvent{failed})
}
