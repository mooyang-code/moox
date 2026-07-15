package sysdeploy

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strconv"

	"github.com/mooyang-code/moox/packages/gatewayproxy"
	"gorm.io/gorm"
)

type routeExtraConfig struct {
	TimeoutMS    *int64 `json:"timeout_ms"`
	MaxBodyBytes *int64 `json:"max_body_bytes"`
}

func (d *DAO) CompileGatewaySnapshot(ctx context.Context, nodeID string) (gatewayproxy.Snapshot, error) {
	var snapshot gatewayproxy.Snapshot
	err := d.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		compiled, err := (&DAO{db: tx}).compileGatewaySnapshot(ctx, nodeID)
		if err != nil {
			return err
		}
		snapshot = compiled
		return nil
	})
	return snapshot, err
}

func (d *DAO) compileGatewaySnapshot(ctx context.Context, nodeID string) (gatewayproxy.Snapshot, error) {
	node, err := d.GetGatewayNode(ctx, nodeID)
	if err != nil {
		return gatewayproxy.Snapshot{}, err
	}
	routes := []gatewayproxy.Route{}
	if node.Status == "enabled" {
		var rows []Deployment
		if err := d.db.WithContext(ctx).Where("c_node_id = ? AND c_status = ? AND c_gateway_enabled = ?", nodeID, "active", true).Order("c_gateway_service_id ASC").Find(&rows).Error; err != nil {
			return gatewayproxy.Snapshot{}, err
		}
		for _, row := range rows {
			extra, err := parseRouteExtraConfig(row.ExtraConfig)
			if err != nil {
				return gatewayproxy.Snapshot{}, fmt.Errorf("deployment %s/%s extra_config: %w", row.NodeID, row.ServiceName, err)
			}
			route := gatewayproxy.Route{ServiceID: row.GatewayServiceID, Address: net.JoinHostPort(row.Host, strconv.Itoa(int(row.Port))), ServicePath: row.GatewayPath}
			if extra.TimeoutMS != nil {
				route.TimeoutMS = *extra.TimeoutMS
			}
			if extra.MaxBodyBytes != nil {
				route.MaxBodyBytes = *extra.MaxBodyBytes
			}
			routes = append(routes, route)
		}
	}
	snapshot, err := gatewayproxy.NormalizeAndHash(nodeID, routes)
	if err != nil {
		return gatewayproxy.Snapshot{}, err
	}
	snapshot.Disabled = node.Status == "disabled"
	if err := d.db.WithContext(ctx).Model(&GatewayNode{}).Where("c_node_id = ?", nodeID).Updates(map[string]interface{}{"c_route_hash": snapshot.RouteHash, "c_route_count": len(snapshot.Routes)}).Error; err != nil {
		return gatewayproxy.Snapshot{}, err
	}
	return snapshot, nil
}

func parseRouteExtraConfig(raw string) (routeExtraConfig, error) {
	if raw == "" {
		raw = "{}"
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &object); err != nil || object == nil {
		return routeExtraConfig{}, fmt.Errorf("must be a JSON object")
	}
	var extra routeExtraConfig
	for key, target := range map[string]**int64{"timeout_ms": &extra.TimeoutMS, "max_body_bytes": &extra.MaxBodyBytes} {
		value, ok := object[key]
		if !ok {
			continue
		}
		var decoded int64
		if string(value) == "null" || json.Unmarshal(value, &decoded) != nil {
			return extra, fmt.Errorf("%s must be an integer", key)
		}
		*target = &decoded
	}
	return extra, nil
}
