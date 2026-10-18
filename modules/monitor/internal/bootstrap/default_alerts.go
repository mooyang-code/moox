package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/packages/notification"
	"gorm.io/gorm"
)

const (
	// 仅防止死循环；足以容纳 1024 台主机的全部探针及历史记录。
	maxDefaultCheckPages = 400
)

func ensureDefaultCheckAlertRules(ctx context.Context, repositories *store.Repositories) error {
	if repositories == nil {
		return fmt.Errorf("default alerts require repositories")
	}
	checks, err := listDefaultAlertChecks(ctx, repositories)
	if err != nil {
		return err
	}
	for _, check := range checks {
		failureThreshold, successThreshold := 3, 2
		if check.Kind == domain.CheckKindExternal {
			failureThreshold, successThreshold = externalFailureThreshold(check.IntervalSeconds), 1
		}
		if strings.HasPrefix(check.CheckID, "kline_freshness:") {
			successThreshold = 2
		}
		rule := &domain.AlertRule{
			SpaceID: check.SpaceID, RuleID: "default:" + check.CheckID, CheckID: check.CheckID,
			FailureThreshold: failureThreshold, SuccessThreshold: successThreshold,
			MinimumReminderIntervalSeconds: 300, SendOnResolved: true, Enabled: true,
			Description: "Default MooX monitoring notification",
		}
		existing, err := repositories.Alerts.GetRule(ctx, check.SpaceID, rule.RuleID)
		switch {
		case err == nil:
			rule.ID = existing.ID
			if err := repositories.Alerts.UpdateRule(ctx, rule); err != nil {
				return err
			}
		case errors.Is(err, gorm.ErrRecordNotFound):
			if err := repositories.Alerts.CreateRule(ctx, rule); err != nil {
				return err
			}
		default:
			return err
		}
	}
	return nil
}

// externalAlertGrace 是外部检查从第一次失败到告警的宽限时间。外部检查（运行指标上报、采集计划对账、
// 行情探针、数据集新鲜度等）每个周期评估一次，服务重启时会有一两个周期读不到指标；原来失败一次就告警，
// 一次重启就推送一对“新告警 / 已恢复”（2026-10-14～15 两天 122 次告警，大多 29 秒内自愈）。
// 持续失败满 60 秒才告警，重启造成的短暂中断不再推送，真正的故障最多晚一分钟。
const externalAlertGrace = 60 * time.Second

// externalFailureThreshold 把宽限时间换算成连续失败次数：首次失败之后还要再失败满宽限时间。
func externalFailureThreshold(intervalSeconds int) int {
	if intervalSeconds <= 0 {
		intervalSeconds = 30
	}
	interval := time.Duration(intervalSeconds) * time.Second
	return 1 + int((externalAlertGrace+interval-1)/interval)
}

func listDefaultAlertChecks(ctx context.Context, repositories *store.Repositories) ([]domain.Check, error) {
	all, err := listAllMonitorChecks(ctx, repositories)
	if err != nil {
		return nil, err
	}
	checks := make([]domain.Check, 0, len(all))
	for _, check := range all {
		if !check.Enabled {
			continue
		}
		checks = append(checks, check)
	}
	return checks, nil
}

func listAllMonitorChecks(ctx context.Context, repositories *store.Repositories) ([]domain.Check, error) {
	if repositories == nil {
		return nil, fmt.Errorf("default alerts require repositories")
	}
	checks := make([]domain.Check, 0, 500)
	// 按页读完整个检查集合（含已停用的历史检查），不能因总量上限阻断 Monitor 启动。
	for page := 1; page <= maxDefaultCheckPages; page++ {
		batch, err := repositories.Checks.List(ctx, store.ListChecksOptions{Page: store.Page{Page: page, PageSize: 500}})
		if err != nil {
			return nil, err
		}
		checks = append(checks, batch...)
		if len(batch) < 500 {
			return checks, nil
		}
	}
	return nil, fmt.Errorf("default alert checks exceed %d pages", maxDefaultCheckPages)
}

// 主机资源告警需要连续超过阈值的采样次数。HostAgent 每分钟采样一次（整分 0 秒），
// CPU 3 次约 3 分钟、内存 5 次约 5 分钟、磁盘 3 次约 3 分钟。不改成更密的采样：
// mooxsys 视图每个序列只保留最近 5000 根，每分钟采样约能保留 3.5 天的主机指标历史。
var hostFailureThresholds = map[string]int{
	hostmetrics.HostMetricCPU:             3,
	hostmetrics.HostMetricMemory:          5,
	hostmetrics.HostMetricFilesystemUsage: 3,
}

func ensureDefaultHostAlertRules(ctx context.Context, repositories *store.Repositories, registry *hostmetrics.Registry) error {
	if repositories == nil || registry == nil {
		return fmt.Errorf("default host alerts require repositories and registry")
	}
	agents, err := registry.List(ctx, time.Now().UTC())
	if err != nil {
		return err
	}
	definitions := map[string]string{
		// CPU 阈值取 80%、连续约 3 分钟：2026-10-14 两个 1h 因子集回填，storage-node 吃掉 4 核主机的
		// 330%–380% CPU（整机 82%–95%），读窗口一再超时，却没有一次连续 5 分钟都不低于 90%，原来的规则一条告警也没出。
		hostmetrics.HostMetricCPU:    `{"threshold":80,"recovery_threshold":70}`,
		hostmetrics.HostMetricMemory: `{"threshold":90,"recovery_threshold":80}`,
		// 磁盘写满是 Storage / View 故障的首要原因：在写满之前尽早告警，留出清理时间。
		hostmetrics.HostMetricFilesystemUsage: `{"threshold":85,"recovery_threshold":80}`,
	}
	// 磁盘繁忙度、网络错误包两类告警已精简掉（个人系统噪声大、价值低）：删除存量规则。
	for _, retired := range []string{"disk_utilization", "network_errors"} {
		for _, agent := range agents {
			checkID := hostmetrics.HostRulePrefix + agent.AgentID + ":" + retired
			if err := repositories.Alerts.DeleteRule(ctx, hostmetrics.SpaceID, "default:"+checkID); err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
		}
	}
	for _, agent := range agents {
		for metric, definition := range definitions {
			failureThreshold := hostFailureThresholds[metric]
			checkID := hostmetrics.HostRuleKey(agent.AgentID, metric)
			rule := &domain.AlertRule{
				SpaceID: hostmetrics.SpaceID, RuleID: "default:" + checkID, CheckID: checkID,
				FailureThreshold: failureThreshold, SuccessThreshold: 1,
				MinimumReminderIntervalSeconds: 300, SendOnResolved: true, Enabled: true,
				Description: definition,
			}
			existing, err := repositories.Alerts.GetRule(ctx, rule.SpaceID, rule.RuleID)
			switch {
			case err == nil:
				rule.ID = existing.ID
				if err := repositories.Alerts.UpdateRule(ctx, rule); err != nil {
					return err
				}
			case errors.Is(err, gorm.ErrRecordNotFound):
				if err := repositories.Alerts.CreateRule(ctx, rule); err != nil {
					return err
				}
			default:
				return err
			}
		}
	}
	return nil
}

// seedNotificationChannel 在首次建库时按部署配置（MOOX_NOTIFICATION_*）写入第一个推送渠道；没有配置推送地址时不写入。
func seedNotificationChannel(ctx context.Context, repositories *store.Repositories) error {
	if repositories == nil || repositories.Notifications == nil {
		return fmt.Errorf("notification repository is unavailable")
	}
	typ := strings.TrimSpace(os.Getenv("MOOX_NOTIFICATION_CHANNEL_TYPE"))
	if typ == "" {
		typ = string(notification.ChannelTypeWeCom)
	}
	url := strings.TrimSpace(os.Getenv("MOOX_NOTIFICATION_WEBHOOK_URL"))
	if url == "" {
		return nil
	}
	if _, err := notification.NewSender(notification.ChannelConfig{Type: notification.ChannelType(typ), WebhookURL: url}); err != nil {
		return fmt.Errorf("notification channel: %w", err)
	}
	_, err := repositories.Notifications.Add(ctx, typ, url)
	return err
}
