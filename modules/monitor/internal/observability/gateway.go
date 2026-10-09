package observability

import (
	"context"
	"fmt"
	"strings"
	"time"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/monitor/internal/alerttext"
)

// 主机网关的告警阈值：心跳每次拉取快照时上报，超过 GatewayHeartbeatGrace 没有心跳、或已应用的哈希与期望哈希不一致
// 超过这个时长才告警，避免网关重启、快照刚下发时的短暂不一致产生告警。
const GatewayHeartbeatGrace = 2 * time.Minute

// 主机网关心跳状态，与 SysDeploy 返回的 HostGatewayStatus.state 一致。
const (
	gatewayOnline        = "online"
	gatewayOffline       = "offline"
	gatewayNeverReported = "never_reported"
	gatewayConflict      = "conflict"
)

// HostGatewaySource 读取主机及其主机网关的心跳状态（SysDeploy 的 ListHosts）。
type HostGatewaySource interface {
	Hosts(context.Context) ([]*adminpb.DeployHost, error)
}

// GatewayHostStatus 是一台主机的主机网关状态与告警判断。
type GatewayHostStatus struct {
	HostID, Address, HostStatus, GatewayState string
	InstanceID, ConflictInstanceID            string
	ExpectedHash, AppliedHash, LastError      string
	LastSeenAt, OutOfSyncSince                time.Time
	ConflictSeenAt, CreatedAt                 time.Time
	// Enabled 表示主机启用，只有启用的主机才检查。
	Enabled bool
	// Healthy 为 false 时应当告警，Reason 是中文原因。
	Healthy bool
	Reason  string
}

// buildGatewayHosts 读取主机网关状态；来源不可用时返回错误，调用方据此保留上一次的告警状态。
func (b Builder) buildGatewayHosts(ctx context.Context, now time.Time) ([]GatewayHostStatus, error) {
	if b.GatewayHosts == nil {
		return nil, nil
	}
	hosts, err := b.GatewayHosts.Hosts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]GatewayHostStatus, 0, len(hosts))
	for _, host := range hosts {
		if host != nil {
			out = append(out, EvaluateGatewayHost(host, now))
		}
	}
	return out, nil
}

// EvaluateGatewayHost 判断一台主机的主机网关是否需要告警：已启用的主机超过 2 分钟没有心跳、从未上报心跳超过 2 分钟、
// 网关实例冲突（两个实例交替上报），或已应用的哈希与期望哈希不一致超过 2 分钟。实例被替换（正常重启）不告警。
func EvaluateGatewayHost(host *adminpb.DeployHost, now time.Time) GatewayHostStatus {
	gateway := host.GetGateway()
	status := GatewayHostStatus{
		HostID: host.GetHostId(), Address: host.GetAddress(), HostStatus: host.GetStatus(),
		GatewayState: gateway.GetState(), InstanceID: gateway.GetInstanceId(), ConflictInstanceID: gateway.GetConflictInstanceId(),
		ExpectedHash: gateway.GetExpectedHash(), AppliedHash: gateway.GetAppliedHash(), LastError: gateway.GetLastError(),
		LastSeenAt: parseTime(gateway.GetLastSeenAt()), OutOfSyncSince: parseTime(gateway.GetOutOfSyncSince()),
		ConflictSeenAt: parseTime(gateway.GetConflictSeenAt()), CreatedAt: parseTime(host.GetCreatedAt()),
		Enabled: host.GetStatus() == "enabled",
	}
	switch {
	case !status.Enabled:
		status.Healthy, status.Reason = true, "主机已停用，不再检查主机网关"
	case status.GatewayState == gatewayConflict:
		status.Reason = fmt.Sprintf("主机网关实例冲突：%s 与 %s 交替上报心跳（最近一次 %s），同一台主机上可能运行着两个主机网关",
			status.InstanceID, status.ConflictInstanceID, alerttext.Time(status.ConflictSeenAt))
	case status.GatewayState == gatewayNeverReported:
		if !status.CreatedAt.IsZero() && now.Sub(status.CreatedAt) < GatewayHeartbeatGrace {
			status.Healthy, status.Reason = true, "等待主机网关第一次心跳"
		} else {
			status.Reason = "主机网关从未上报心跳：确认主机网关在运行，并能连上 control 的网关控制"
		}
	case status.GatewayState == gatewayOffline || (!status.LastSeenAt.IsZero() && now.Sub(status.LastSeenAt) > GatewayHeartbeatGrace):
		status.Reason = fmt.Sprintf("主机网关超过 %s没有心跳（最近一次 %s）", alerttext.Duration(GatewayHeartbeatGrace), alerttext.Time(status.LastSeenAt))
		if strings.TrimSpace(status.LastError) != "" {
			status.Reason += "；最近错误：" + strings.TrimSpace(status.LastError)
		}
	case status.ExpectedHash != "" && status.AppliedHash != status.ExpectedHash &&
		!status.OutOfSyncSince.IsZero() && now.Sub(status.OutOfSyncSince) > GatewayHeartbeatGrace:
		status.Reason = fmt.Sprintf("主机网关的路由已 %s 未同步：已应用 %s，期望 %s",
			alerttext.Duration(now.Sub(status.OutOfSyncSince)), shortHash(status.AppliedHash), shortHash(status.ExpectedHash))
		if strings.TrimSpace(status.LastError) != "" {
			status.Reason += "；最近错误：" + strings.TrimSpace(status.LastError)
		}
	default:
		status.Healthy, status.Reason = true, "主机网关在线，路由已同步"
	}
	return status
}

func shortHash(hash string) string {
	hash = strings.TrimSpace(hash)
	if hash == "" {
		return "（无）"
	}
	if len(hash) > 12 {
		return hash[:12]
	}
	return hash
}

func parseTime(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return parsed.UTC()
	}
	return time.Time{}
}
