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
	PendingSince                           time.Time
	ExpectedHash, AppliedHash              string
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
	routes.ExpectedHash, routes.AppliedHash = row.ExpectedHash, row.AppliedHash
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
		return append(signals, relaySignals(status, now, true)...)
	}
	if heartbeat.Status != "healthy" {
		// control 网关心跳停了，会话状态已经过时，不能据此判断反向连接。
		signals = append(signals, relaySignals(status, now, true)...)
	} else {
		signals = append(signals, relaySignals(status, now, false)...)
	}
	heartbeat, conflict, routes = &signals[0], &signals[1], &signals[2]
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
		if row.HashMismatchSince != nil {
			routes.PendingSince = *row.HashMismatchSince
		}
		routes.Status, routes.Reason = "unknown", "主机网关正在同步新的路由快照"
		if row.HashMismatchSince != nil && now.Sub(*row.HashMismatchSince) > gatewayGrace {
			routes.Status, routes.Reason = "down", "主机网关超过 2 分钟未应用期望路由快照"
		}
		routes.RawError = fmt.Sprintf("expected_hash=%s applied_hash=%s last_error=%s", row.ExpectedHash, row.AppliedHash, status.GetLastError())
	}
	return signals
}

// RelaySignalKind 是出站主机反向连接的信号类型；信号挂在出站主机上，数据来自 control 网关的心跳。
const RelaySignalKind = "relay"

// relaySignals 把 control 网关上报的各出站主机会话转换成信号。stale 表示 control 的心跳不可确认，只能给出 unknown。
func relaySignals(status *adminpb.HostGatewayRuntimeStatus, now time.Time, stale bool) []GatewaySignal {
	var signals []GatewaySignal
	for _, session := range status.GetRelaySessions() {
		signal := GatewaySignal{HostID: session.GetHostId(), Kind: RelaySignalKind, CheckedAt: now,
			RawError: fmt.Sprintf("instance_id=%s connected_at=%s reconnects=%d streams=%d", session.GetInstanceId(), session.GetConnectedAt(), session.GetReconnects(), session.GetStreams())}
		switch {
		case stale:
			signal.Status, signal.Reason = "unknown", "控制主机网关心跳不可确认，暂时无法判断反向连接"
		case session.GetOnline():
			signal.Status, signal.Reason = "healthy", "主机网关与控制主机的反向连接在线"
		default:
			signal.Status, signal.Reason = "down", "主机网关与控制主机的反向连接已断开"
		}
		signals = append(signals, signal)
	}
	return signals
}
