package bootstrap

import (
	"context"
	"encoding/json"
	"time"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
)

// gatewayHeartbeatGrace 与网关心跳告警的判定一致（observability.gatewayGrace）：超过 2 分钟没有心跳即视为停止。
const gatewayHeartbeatGrace = 2 * time.Minute

// gatewayHeartbeatAlive 返回“某主机的网关在给定时刻是否仍有心跳”：Monitor 缓存的 Admin 网关状态本身要新鲜，
// 且其中网关的最近心跳距该时刻不超过 2 分钟。缓存读不到或过期时按“没有心跳”处理，告警保持“主机失联”。
func gatewayHeartbeatAlive(gateways *store.GatewayRepository) func(context.Context, string, time.Time) bool {
	return func(ctx context.Context, hostID string, at time.Time) bool {
		if gateways == nil {
			return false
		}
		rows, err := gateways.List(ctx)
		if err != nil {
			return false
		}
		for _, row := range rows {
			if row.HostID != hostID {
				continue
			}
			if row.ReadError != "" || row.ObservedAt == nil || at.Sub(*row.ObservedAt) > gatewayHeartbeatGrace {
				return false
			}
			var status adminpb.HostGatewayRuntimeStatus
			if json.Unmarshal([]byte(row.StatusJSON), &status) != nil {
				return false
			}
			seen, err := time.Parse(time.RFC3339Nano, status.GetLastSeenAt())
			return err == nil && at.Sub(seen) <= gatewayHeartbeatGrace
		}
		return false
	}
}
