package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
)

const gatewayGrace = 2 * time.Minute

type GatewaySignal struct {
	HostID, Kind, Status, Reason, RawError string
	CheckedAt                              time.Time
}

func (b Builder) gatewaySignals(ctx context.Context, now time.Time) ([]GatewaySignal, error) {
	if b.Gateways == nil {
		return nil, nil
	}
	rows, err := b.Gateways.List(ctx)
	if err != nil {
		return nil, err
	}
	var signals []GatewaySignal
	for _, row := range rows {
		var status adminpb.HostGatewayRuntimeStatus
		if row.StatusJSON != "" {
			if err := json.Unmarshal([]byte(row.StatusJSON), &status); err != nil {
				return nil, fmt.Errorf("gateway %s cached status: %w", row.HostID, err)
			}
		}
		signals = append(signals, gatewayObservationSignals(row, &status, now)...)
	}
	return signals, nil
}

func gatewayObservationSignals(row domain.GatewayObservation, status *adminpb.HostGatewayRuntimeStatus, now time.Time) []GatewaySignal {
	signals := []GatewaySignal{
		{HostID: row.HostID, Kind: "heartbeat", Status: "healthy", Reason: "主机网关心跳正常", CheckedAt: now},
		{HostID: row.HostID, Kind: "instance_conflict", Status: "healthy", Reason: "主机网关实例正常", CheckedAt: now},
		{HostID: row.HostID, Kind: "route_sync", Status: "healthy", Reason: "主机网关路由已同步", CheckedAt: now},
	}
	heartbeat, conflict, routes := &signals[0], &signals[1], &signals[2]
	seen, _ := time.Parse(time.RFC3339Nano, status.GetLastSeenAt())
	if seen.IsZero() {
		start := row.HostEnabledAt
		if start.IsZero() {
			start = row.FirstObservedAt
		}
		heartbeat.Status, heartbeat.Reason = "unknown", "主机网关尚未上报，等待首次心跳"
		if now.Sub(start) > gatewayGrace {
			heartbeat.Status, heartbeat.Reason = "down", "已启用的主机超过 2 分钟没有网关心跳"
		}
	} else if now.Sub(seen) > gatewayGrace {
		heartbeat.Status, heartbeat.Reason = "down", "主机网关超过 2 分钟没有心跳"
		heartbeat.RawError = "last_seen_at=" + status.GetLastSeenAt()
	}
	if row.ReadError != "" || row.ObservedAt == nil || now.Sub(*row.ObservedAt) > gatewayGrace {
		if row.ObservedAt == nil {
			heartbeat.Status, heartbeat.Reason = "unknown", "尚未取得可确认的主机网关心跳"
		}
		conflict.Status, conflict.Reason = "unknown", "暂时无法确认主机网关实例状态"
		routes.Status, routes.Reason = "unknown", "暂时无法确认主机网关路由同步状态"
		for index := range signals {
			signals[index].RawError = row.ReadError
		}
		return signals
	}
	if status.GetInstanceId() == "" {
		conflict.Status, conflict.Reason = "unknown", "主机网关尚未上报实例身份"
	}
	if status.GetConflictInstanceId() != "" {
		conflict.Status, conflict.Reason = "down", "同一主机有多个网关实例交替上报"
		conflict.RawError = fmt.Sprintf("instance_id=%s conflict_instance_id=%s conflict_seen_at=%s", status.GetInstanceId(), status.GetConflictInstanceId(), status.GetConflictSeenAt())
	}
	if row.ExpectedHash == "" {
		routes.Status, routes.Reason = "unknown", "尚未取得主机网关的期望路由快照"
	} else if row.ExpectedHash != row.AppliedHash {
		routes.Status, routes.Reason = "unknown", "主机网关正在同步新的路由快照"
		if row.HashMismatchSince != nil && now.Sub(*row.HashMismatchSince) > gatewayGrace {
			routes.Status, routes.Reason = "down", "主机网关超过 2 分钟未应用期望路由快照"
		}
		routes.RawError = fmt.Sprintf("expected_hash=%s applied_hash=%s last_error=%s", row.ExpectedHash, row.AppliedHash, status.GetLastError())
	}
	return signals
}
