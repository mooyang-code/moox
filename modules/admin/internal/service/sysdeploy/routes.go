package sysdeploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayproxy"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

type routeExtraConfig struct {
	TimeoutMS      *int64   `json:"timeout_ms"`
	MaxBodyBytes   *int64   `json:"max_body_bytes"`
	GatewayMethods []string `json:"gateway_methods"`
	GatewayCallers []string `json:"gateway_callers"`
	GatewayRoutes  []struct {
		ServicePath    string   `json:"service_path"`
		Port           int32    `json:"port"`
		TimeoutMS      *int64   `json:"timeout_ms"`
		MaxBodyBytes   *int64   `json:"max_body_bytes"`
		GatewayMethods []string `json:"gateway_methods"`
		GatewayCallers []string `json:"gateway_callers"`
	} `json:"gateway_routes"`
}

type RouteConfigError struct{ Err error }

var ErrInvalidGatewayRoute = gatewayproxy.ErrInvalidGatewayRoute

func (err *RouteConfigError) Error() string { return err.Err.Error() }
func (err *RouteConfigError) Unwrap() error { return err.Err }
func (err *RouteConfigError) Is(target error) bool {
	return target == gatewayproxy.ErrInvalidGatewayRoute
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
	if err == nil {
		// Keep operator-facing route bookkeeping outside the read transaction.
		// A concurrent writer can otherwise turn an otherwise valid snapshot
		// into SQLITE_BUSY_SNAPSHOT while the Gateway is pulling routes.
		bookkeepingCtx, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
		updateErr := updateGatewayNodeRouteBookkeeping(bookkeepingCtx, d.db, nodeID, snapshot)
		cancel()
		if updateErr != nil {
			log.WarnContextf(ctx, "record gateway route snapshot status failed node_id=%s: %v", nodeID, updateErr)
		}
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
			compiled, err := deploymentGatewayRoutes(row, extra)
			if err != nil {
				return gatewayproxy.Snapshot{}, &RouteConfigError{Err: err}
			}
			routes = append(routes, compiled...)
		}
	}
	snapshot, err := gatewayproxy.NormalizeAndHashState(nodeID, node.Status == "disabled", routes)
	if err != nil {
		return gatewayproxy.Snapshot{}, &RouteConfigError{Err: err}
	}
	return snapshot, nil
}

func updateGatewayNodeRouteBookkeeping(ctx context.Context, db *gorm.DB, nodeID string, snapshot gatewayproxy.Snapshot) error {
	values := map[string]interface{}{"c_route_hash": snapshot.RouteHash, "c_route_count": len(snapshot.Routes)}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		lastErr = db.WithContext(ctx).Model(&GatewayNode{}).Where("c_node_id = ?", nodeID).Updates(values).Error
		if lastErr == nil || !isSQLiteLockError(lastErr) || attempt == 2 {
			return lastErr
		}
		wait := time.Duration(50*(1<<attempt)) * time.Millisecond
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return lastErr
}

func isSQLiteLockError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "database is locked") || strings.Contains(message, "sqlite_busy") || strings.Contains(message, "database table is locked")
}

func deploymentGatewayRoutes(row Deployment, extra routeExtraConfig) ([]gatewayproxy.Route, error) {
	basePort := row.Port
	switch row.GatewayServiceID {
	case "storage-primary":
		basePort = 20100
	case "storage-view":
		basePort = 20103
	case "factormgr":
		basePort = 11403
	}
	base := gatewayproxy.Route{ServiceID: row.GatewayServiceID, Address: net.JoinHostPort(row.Host, strconv.Itoa(int(basePort))), ServicePath: row.GatewayPath, AllowedMethods: extra.GatewayMethods, AllowedCallers: extra.GatewayCallers}
	if extra.TimeoutMS != nil {
		base.TimeoutMS = *extra.TimeoutMS
	}
	if extra.MaxBodyBytes != nil {
		base.MaxBodyBytes = *extra.MaxBodyBytes
	}
	routes := []gatewayproxy.Route{base}
	for _, item := range extra.GatewayRoutes {
		if item.ServicePath == "" || item.Port < 1 {
			return nil, fmt.Errorf("gateway_routes entries require service_path and positive port")
		}
		route := gatewayproxy.Route{ServiceID: row.GatewayServiceID, Address: net.JoinHostPort(row.Host, strconv.Itoa(int(item.Port))), ServicePath: item.ServicePath, AllowedMethods: item.GatewayMethods, AllowedCallers: item.GatewayCallers}
		if item.TimeoutMS != nil {
			route.TimeoutMS = *item.TimeoutMS
		}
		if item.MaxBodyBytes != nil {
			route.MaxBodyBytes = *item.MaxBodyBytes
		}
		routes = append(routes, route)
	}
	for _, route := range routes {
		if len(route.AllowedMethods) == 0 || len(route.AllowedCallers) == 0 {
			return nil, fmt.Errorf("%s/%s requires explicit nonempty gateway_methods and gateway_callers", row.GatewayServiceID, route.ServicePath)
		}
	}
	return routes, nil
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
	if value, ok := object["gateway_callers"]; ok {
		if string(value) == "null" || json.Unmarshal(value, &extra.GatewayCallers) != nil {
			return extra, fmt.Errorf("gateway_callers must be an array of strings")
		}
	}
	if value, ok := object["gateway_routes"]; ok {
		if string(value) == "null" || json.Unmarshal(value, &extra.GatewayRoutes) != nil {
			return extra, fmt.Errorf("gateway_routes must be an array of route objects")
		}
	}
	return extra, nil
}
