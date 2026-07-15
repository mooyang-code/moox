package sysdeploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"

	"github.com/mooyang-code/moox/packages/gatewayproxy"
	"gorm.io/gorm"
)

type routeExtraConfig struct {
	TimeoutMS      *int64   `json:"timeout_ms"`
	MaxBodyBytes   *int64   `json:"max_body_bytes"`
	GatewayMethods []string `json:"gateway_methods"`
}

type RouteConfigError struct{ Err error }

var ErrInvalidGatewayRoute = gatewayproxy.ErrInvalidGatewayRoute

func (err *RouteConfigError) Error() string { return err.Err.Error() }
func (err *RouteConfigError) Unwrap() error { return err.Err }
func (err *RouteConfigError) Is(target error) bool {
	return target == gatewayproxy.ErrInvalidGatewayRoute
}

func requiresMethodAllowlist(serviceID, servicePath string) bool {
	return serviceID == "sysdeploy" || serviceID == "secret" || servicePath == "trpc.moox.ops.SysDeploy" || servicePath == "trpc.moox.ops.SecretMgr"
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
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return gatewayproxy.Snapshot{}, fmt.Errorf("%w: %w", gatewayproxy.ErrGatewayNodeNotFound, err)
	}
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
				return gatewayproxy.Snapshot{}, &RouteConfigError{Err: fmt.Errorf("deployment %s/%s extra_config: %w", row.NodeID, row.ServiceName, err)}
			}
			if requiresMethodAllowlist(row.GatewayServiceID, row.GatewayPath) && len(extra.GatewayMethods) == 0 {
				return gatewayproxy.Snapshot{}, &RouteConfigError{Err: fmt.Errorf("%s requires nonempty gateway_methods", row.GatewayServiceID)}
			}
			route := gatewayproxy.Route{ServiceID: row.GatewayServiceID, Address: net.JoinHostPort(row.Host, strconv.Itoa(int(row.Port))), ServicePath: row.GatewayPath, AllowedMethods: extra.GatewayMethods}
			if extra.TimeoutMS != nil {
				route.TimeoutMS = *extra.TimeoutMS
			}
			if extra.MaxBodyBytes != nil {
				route.MaxBodyBytes = *extra.MaxBodyBytes
			}
			routes = append(routes, route)
		}
	}
	snapshot, err := gatewayproxy.NormalizeAndHashState(nodeID, node.Status == "disabled", routes)
	if err != nil {
		return gatewayproxy.Snapshot{}, &RouteConfigError{Err: err}
	}
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
	if value, ok := object["gateway_methods"]; ok {
		if string(value) == "null" || json.Unmarshal(value, &extra.GatewayMethods) != nil {
			return extra, fmt.Errorf("gateway_methods must be an array of strings")
		}
	}
	return extra, nil
}
