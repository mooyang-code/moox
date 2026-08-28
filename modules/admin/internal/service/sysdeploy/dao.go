package sysdeploy

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

type DAO struct {
	db *gorm.DB
}

func NewDAO(db *gorm.DB) *DAO { return &DAO{db: db} }

func (d *DAO) Create(ctx context.Context, item *Deployment) error {
	if item == nil {
		return fmt.Errorf("deployment is required")
	}
	normalizeDeployment(item)
	if item.NodeID == "" || item.ServiceName == "" {
		return fmt.Errorf("node_id and service_name are required")
	}
	if exists, err := d.exists(ctx, item.NodeID, item.ServiceName); err != nil {
		return err
	} else if exists {
		return fmt.Errorf("service deployment already exists: %s/%s", item.NodeID, item.ServiceName)
	}
	now := time.Now()
	item.CreatedAt = now
	item.UpdatedAt = now
	if err := d.db.WithContext(ctx).Create(item).Error; err != nil {
		if isUniqueConstraintError(err) {
			return fmt.Errorf("service deployment already exists: %s", item.ServiceName)
		}
		return err
	}
	return nil
}

func (d *DAO) Update(ctx context.Context, nodeID, serviceName string, item *Deployment) error {
	nodeID = strings.TrimSpace(nodeID)
	serviceName = strings.TrimSpace(serviceName)
	if serviceName == "" {
		return fmt.Errorf("service_name is required")
	}
	if item == nil {
		return fmt.Errorf("deployment is required")
	}
	item.NodeID, item.ServiceName = nodeID, serviceName
	normalizeDeployment(item)
	result := d.db.WithContext(ctx).Model(&Deployment{}).
		Where("c_node_id = ? AND c_service_name = ?", nodeID, serviceName).
		Updates(map[string]interface{}{
			"c_service_kind":       item.ServiceKind,
			"c_protocol":           item.Protocol,
			"c_host":               item.Host,
			"c_port":               item.Port,
			"c_gateway_path":       item.GatewayPath,
			"c_gateway_service_id": item.GatewayServiceID,
			"c_gateway_enabled":    item.GatewayEnabled,
			"c_scope":              item.Scope,
			"c_status":             item.Status,
			"c_description":        item.Description,
			"c_extra_config":       item.ExtraConfig,
			"c_mtime":              time.Now(),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("service deployment not found: %s: %w", serviceName, gorm.ErrRecordNotFound)
	}
	return nil
}

func (d *DAO) Delete(ctx context.Context, nodeID, serviceName string) error {
	nodeID = strings.TrimSpace(nodeID)
	serviceName = strings.TrimSpace(serviceName)
	if serviceName == "" {
		return fmt.Errorf("service_name is required")
	}
	result := d.db.WithContext(ctx).
		Where("c_node_id = ? AND c_service_name = ?", nodeID, serviceName).
		Delete(&Deployment{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("service deployment not found: %s: %w", serviceName, gorm.ErrRecordNotFound)
	}
	return nil
}

func (d *DAO) Get(ctx context.Context, nodeID, serviceName string) (*Deployment, error) {
	nodeID = strings.TrimSpace(nodeID)
	serviceName = strings.TrimSpace(serviceName)
	if serviceName == "" {
		return nil, fmt.Errorf("service_name is required")
	}
	var row Deployment
	err := d.db.WithContext(ctx).Where("c_node_id = ? AND c_service_name = ?", nodeID, serviceName).First(&row).Error
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (d *DAO) List(ctx context.Context, filter ListFilter, offset int, limit int) ([]Deployment, int64, error) {
	query := d.db.WithContext(ctx).Model(&Deployment{})
	if filter.NodeID != "" {
		query = query.Where("c_node_id = ?", filter.NodeID)
	}
	if filter.ServiceName != "" {
		query = query.Where("c_service_name LIKE ?", "%"+filter.ServiceName+"%")
	}
	if filter.ServiceKind != "" {
		query = query.Where("c_service_kind = ?", filter.ServiceKind)
	}
	if filter.Scope != "" {
		query = query.Where("c_scope = ?", filter.Scope)
	}
	if filter.Status != "" {
		query = query.Where("c_status = ?", filter.Status)
	}
	if filter.GatewayEnabled != nil {
		query = query.Where("c_gateway_enabled = ?", *filter.GatewayEnabled)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []Deployment
	if err := query.Order("c_node_id ASC, c_scope DESC, c_service_kind ASC, c_service_name ASC").Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

func (d *DAO) ListActive(ctx context.Context, nodeID string) ([]Deployment, error) {
	var rows []Deployment
	query := d.db.WithContext(ctx).Where("c_status = ?", "active")
	if nodeID != "" {
		query = query.Where("c_node_id = ?", nodeID)
	}
	err := query.Order("c_node_id ASC, c_scope DESC, c_service_kind ASC, c_service_name ASC").
		Find(&rows).Error
	return rows, err
}

func (d *DAO) SeedDefaults(
	ctx context.Context,
	rows []Deployment,
	retireNodeID string,
	retireServiceNames []string,
) error {
	return d.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if retireNodeID != "" && len(retireServiceNames) > 0 {
			if err := tx.Where(
				"c_node_id = ? AND c_service_name IN ?",
				retireNodeID,
				retireServiceNames,
			).Delete(&Deployment{}).Error; err != nil {
				return err
			}
		}
		return (&DAO{db: tx}).seedDefaultRows(ctx, rows)
	})
}

func (d *DAO) seedDefaultRows(ctx context.Context, rows []Deployment) error {
	for i := range rows {
		item := rows[i]
		normalizeDeployment(&item)
		if item.ServiceName == "" {
			continue
		}
		exists, err := d.exists(ctx, item.NodeID, item.ServiceName)
		if err != nil {
			return err
		}
		if exists {
			if err := d.backfillDefaultExtraConfig(ctx, &item); err != nil {
				return err
			}
			continue
		}
		if err := d.Create(ctx, &item); err != nil {
			return err
		}
	}
	return nil
}

func (d *DAO) backfillDefaultExtraConfig(ctx context.Context, item *Deployment) error {
	row, err := d.Get(ctx, item.NodeID, item.ServiceName)
	if err != nil {
		return err
	}
	next, changed := mergeDefaultExtraConfig(row.ExtraConfig, item.ExtraConfig)
	updates := map[string]interface{}{}
	if changed {
		updates["c_extra_config"] = next
	}
	if row.GatewayEnabled != item.GatewayEnabled {
		updates["c_gateway_enabled"] = item.GatewayEnabled
	}
	if row.GatewayServiceID != item.GatewayServiceID {
		updates["c_gateway_service_id"] = item.GatewayServiceID
	}
	if len(updates) == 0 {
		return nil
	}
	updates["c_mtime"] = time.Now()
	return d.db.WithContext(ctx).Model(&Deployment{}).
		Where("c_node_id = ? AND c_service_name = ?", item.NodeID, item.ServiceName).
		Updates(updates).Error
}

func mergeDefaultExtraConfig(existingRaw, defaultRaw string) (string, bool) {
	defaultRaw = strings.TrimSpace(defaultRaw)
	if defaultRaw == "" || defaultRaw == "{}" {
		return existingRaw, false
	}
	defaults := map[string]interface{}{}
	if err := json.Unmarshal([]byte(defaultRaw), &defaults); err != nil || len(defaults) == 0 {
		return existingRaw, false
	}
	existing := map[string]interface{}{}
	existingRaw = strings.TrimSpace(existingRaw)
	if existingRaw != "" {
		_ = json.Unmarshal([]byte(existingRaw), &existing)
	}
	changed := false
	for key, value := range defaults {
		if existingValue, ok := existing[key]; ok {
			// Gateway method ACLs are versioned defaults. Preserve any
			// operator-added methods, but append methods introduced by a newer
			// release so an existing deployment does not keep a stale route
			// snapshot (for example, ListViews after the View API was added).
			if key == "gateway_methods" {
				merged, listChanged := mergeDefaultGatewayMethods(existingValue, value)
				if listChanged {
					existing[key] = merged
					changed = true
				}
			}
			if key == "gateway_routes" {
				merged, listChanged := mergeDefaultGatewayRoutes(existingValue, value)
				if listChanged {
					existing[key] = merged
					changed = true
				}
			}
			continue
		}
		existing[key] = value
		changed = true
	}
	if !changed {
		return existingRaw, false
	}
	raw, err := json.Marshal(existing)
	if err != nil {
		return existingRaw, false
	}
	return string(raw), true
}

func mergeDefaultGatewayRoutes(existingValue, defaultValue any) ([]map[string]any, bool) {
	existing, ok := gatewayRouteObjects(existingValue)
	if !ok {
		return nil, false
	}
	defaults, ok := gatewayRouteObjects(defaultValue)
	if !ok {
		return nil, false
	}
	if len(existing) == 0 {
		seeded := make([]map[string]any, 0, len(defaults))
		for _, defaultRoute := range defaults {
			seeded = append(seeded, cloneGatewayRoute(defaultRoute))
		}
		return seeded, len(seeded) > 0
	}
	// A non-empty route list is operator-owned. Never restore deleted default
	// routes or broaden their callers; only migrate the legacy mixed time-series
	// route and ensure the new skill read endpoint exists.
	return migrateReadTimeSeriesGatewayRoutes(existing, defaults)
}

func migrateReadTimeSeriesGatewayRoutes(existing, defaults []map[string]any) ([]map[string]any, bool) {
	var defaultRoute map[string]any
	var defaultMethods []string
	for _, candidate := range defaults {
		methods, ok := gatewayRouteStrings(candidate, "gateway_methods")
		if ok && sameStringSet(methods, []string{"ReadTimeSeriesRows"}) {
			defaultRoute = candidate
			defaultMethods = methods
			break
		}
	}
	if defaultRoute == nil {
		return existing, false
	}
	defaultServicePath := fmt.Sprint(defaultRoute["service_path"])
	changed := false
	for _, route := range existing {
		if fmt.Sprint(route["service_path"]) != defaultServicePath {
			continue
		}
		methods, ok := gatewayRouteStrings(route, "gateway_methods")
		if !ok || !containsStringSet(methods, []string{"*"}) {
			continue
		}
		readRoute, migrated := migrateStorageWildcardRoute(route, defaults, defaultRoute, defaultMethods)
		if migrated {
			existing = append(existing, readRoute)
			changed = true
		}
	}

	removedRoutes := make(map[int]struct{})
	var readRoute map[string]any
	readRouteFromMixed := false
	for index, route := range existing {
		methods, methodsOK := gatewayRouteStrings(route, "gateway_methods")
		callers, callersOK := gatewayRouteStrings(route, "gateway_callers")
		if fmt.Sprint(route["service_path"]) != defaultServicePath || !methodsOK || !callersOK || !sameStringValueSet(methods, defaultMethods) {
			continue
		}
		if readRoute == nil {
			readRoute = route
			continue
		}
		mergedCallers, callersChanged := appendMissingStrings(mustGatewayRouteStrings(readRoute, "gateway_callers"), callers)
		if callersChanged {
			readRoute["gateway_callers"] = mergedCallers
			changed = true
		}
		if mergeMissingGatewayRouteMetadata(readRoute, route) {
			changed = true
		}
		removedRoutes[index] = struct{}{}
		changed = true
	}

	for index, route := range existing {
		if _, removed := removedRoutes[index]; removed {
			continue
		}
		methods, methodsOK := gatewayRouteStrings(route, "gateway_methods")
		callers, callersOK := gatewayRouteStrings(route, "gateway_callers")
		if fmt.Sprint(route["service_path"]) != defaultServicePath || !methodsOK || !callersOK || sameStringValueSet(methods, defaultMethods) || !containsStringSet(methods, defaultMethods) {
			continue
		}
		readCallers, _ := appendMissingStrings(callers, []string{"moox-skill"})
		if readRoute == nil {
			readRoute = cloneGatewayRoute(route)
			readRoute["gateway_methods"] = defaultMethods
			readRoute["gateway_callers"] = readCallers
			existing = append(existing, readRoute)
			readRouteFromMixed = true
		} else if readRouteFromMixed {
			mergedCallers, callersChanged := appendMissingStrings(mustGatewayRouteStrings(readRoute, "gateway_callers"), readCallers)
			if callersChanged {
				readRoute["gateway_callers"] = mergedCallers
			}
			mergeMissingGatewayRouteMetadata(readRoute, route)
		}

		remainingMethods := subtractStringSet(methods, defaultMethods)
		filteredCallers := subtractStringSet(callers, []string{"moox-skill"})
		if len(filteredCallers) != len(callers) {
			route["gateway_callers"] = filteredCallers
		}
		if len(filteredCallers) == 0 {
			removedRoutes[index] = struct{}{}
		} else if generalMethods := defaultGatewayMethodSubset(defaults, defaultRoute, remainingMethods); len(generalMethods) > 0 && !sameStringValueSet(generalMethods, remainingMethods) {
			customRoute := cloneGatewayRoute(route)
			customRoute["gateway_methods"] = subtractStringSet(remainingMethods, generalMethods)
			customRoute["gateway_callers"] = filteredCallers
			existing = append(existing, customRoute)
			remainingMethods = generalMethods
		}
		route["gateway_methods"] = remainingMethods
		changed = true
	}
	if readRoute == nil {
		readRoute = cloneGatewayRoute(defaultRoute)
		readRoute["gateway_callers"] = []string{"moox-skill"}
		existing = append(existing, readRoute)
		changed = true
	}
	if normalizeGatewayRouteEndpoint(readRoute, defaultRoute) {
		changed = true
	}
	if len(removedRoutes) > 0 {
		retained := make([]map[string]any, 0, len(existing)-len(removedRoutes))
		for index, route := range existing {
			if _, remove := removedRoutes[index]; !remove {
				retained = append(retained, route)
			}
		}
		existing = retained
	}
	return existing, changed
}

func migrateStorageWildcardRoute(route map[string]any, defaults []map[string]any, defaultReadRoute map[string]any, defaultReadMethods []string) (map[string]any, bool) {
	defaultGeneralRoute := findDefaultStorageGeneralRoute(defaults, defaultReadRoute)
	generalMethods := defaultReadMethods
	generalCallers := mustGatewayRouteStrings(defaultReadRoute, "gateway_callers")
	if defaultGeneralRoute != nil {
		if methods, ok := gatewayRouteStrings(defaultGeneralRoute, "gateway_methods"); ok {
			generalMethods = methods
		}
		if callers, ok := gatewayRouteStrings(defaultGeneralRoute, "gateway_callers"); ok {
			generalCallers = callers
		}
	}

	callers, _ := gatewayRouteStrings(route, "gateway_callers")
	wildcardCallers := containsStringSet(callers, []string{"*"})
	if wildcardCallers {
		callers = append([]string(nil), generalCallers...)
	} else {
		callers = append([]string(nil), callers...)
	}
	route["gateway_methods"] = append([]string(nil), generalMethods...)
	route["gateway_callers"] = callers
	normalizeGatewayRouteEndpoint(route, defaultReadRoute)

	readRoute := cloneGatewayRoute(route)
	readRoute["gateway_methods"] = append([]string(nil), defaultReadMethods...)
	readCallers := append([]string(nil), callers...)
	if wildcardCallers {
		readCallers = mustGatewayRouteStrings(defaultReadRoute, "gateway_callers")
	} else {
		readCallers, _ = appendMissingStrings(readCallers, []string{"moox-skill"})
	}
	readRoute["gateway_callers"] = readCallers
	normalizeGatewayRouteEndpoint(readRoute, defaultReadRoute)
	return readRoute, true
}

func findDefaultStorageGeneralRoute(defaults []map[string]any, readRoute map[string]any) map[string]any {
	for _, candidate := range defaults {
		if !sameGatewayRouteEndpoint(candidate, readRoute) {
			continue
		}
		methods, ok := gatewayRouteStrings(candidate, "gateway_methods")
		if !ok || sameStringSet(methods, []string{"ReadTimeSeriesRows"}) || containsStringSet(methods, []string{"*"}) {
			continue
		}
		return candidate
	}
	return nil
}

func normalizeGatewayRouteEndpoint(route, defaultRoute map[string]any) bool {
	changed := false
	for _, key := range []string{"service_path", "port"} {
		if fmt.Sprint(route[key]) == fmt.Sprint(defaultRoute[key]) {
			continue
		}
		route[key] = defaultRoute[key]
		changed = true
	}
	return changed
}

func mustGatewayRouteStrings(route map[string]any, key string) []string {
	values, _ := gatewayRouteStrings(route, key)
	return values
}

func mergeMissingGatewayRouteMetadata(target, source map[string]any) bool {
	changed := false
	for key, value := range source {
		switch key {
		case "service_path", "port", "gateway_methods", "gateway_callers":
			continue
		}
		if _, exists := target[key]; !exists {
			target[key] = value
			changed = true
		}
	}
	return changed
}

func cloneGatewayRoute(route map[string]any) map[string]any {
	cloned := make(map[string]any, len(route))
	for key, value := range route {
		cloned[key] = value
	}
	return cloned
}

func defaultGatewayMethodSubset(defaults []map[string]any, readRoute map[string]any, methods []string) []string {
	for _, candidate := range defaults {
		if !sameGatewayRouteEndpoint(candidate, readRoute) {
			continue
		}
		candidateMethods, ok := gatewayRouteStrings(candidate, "gateway_methods")
		if ok && !sameStringSet(candidateMethods, []string{"ReadTimeSeriesRows"}) && containsStringSet(methods, candidateMethods) {
			return candidateMethods
		}
	}
	return nil
}

func appendMissingStrings(existing, defaults []string) ([]string, bool) {
	seen := make(map[string]struct{}, len(existing)+len(defaults))
	for _, value := range existing {
		seen[value] = struct{}{}
	}
	merged := append([]string(nil), existing...)
	changed := false
	for _, value := range defaults {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		merged = append(merged, value)
		changed = true
	}
	return merged, changed
}

func gatewayRouteObjects(value any) ([]map[string]any, bool) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, false
	}
	var routes []map[string]any
	if json.Unmarshal(raw, &routes) != nil {
		return nil, false
	}
	return routes, true
}

func gatewayRouteStrings(route map[string]any, key string) ([]string, bool) {
	raw, err := json.Marshal(route[key])
	if err != nil {
		return nil, false
	}
	var values []string
	if json.Unmarshal(raw, &values) != nil || len(values) == 0 {
		return nil, false
	}
	return values, true
}

func sameGatewayRouteEndpoint(left, right map[string]any) bool {
	return fmt.Sprint(left["service_path"]) == fmt.Sprint(right["service_path"]) &&
		fmt.Sprint(left["port"]) == fmt.Sprint(right["port"])
}

func sameStringSet(left, right []string) bool {
	return len(left) == len(right) && containsStringSet(left, right)
}

func sameStringValueSet(left, right []string) bool {
	return containsStringSet(left, right) && containsStringSet(right, left)
}

func containsStringSet(haystack, needles []string) bool {
	seen := make(map[string]struct{}, len(haystack))
	for _, value := range haystack {
		seen[value] = struct{}{}
	}
	for _, value := range needles {
		if _, ok := seen[value]; !ok {
			return false
		}
	}
	return true
}

func subtractStringSet(values, removed []string) []string {
	removedSet := make(map[string]struct{}, len(removed))
	for _, value := range removed {
		removedSet[value] = struct{}{}
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := removedSet[value]; !ok {
			result = append(result, value)
		}
	}
	return result
}

func mergeDefaultGatewayMethods(existingValue, defaultValue any) ([]string, bool) {
	existingRaw, err := json.Marshal(existingValue)
	if err != nil {
		return nil, false
	}
	defaultRaw, err := json.Marshal(defaultValue)
	if err != nil {
		return nil, false
	}
	var existing, defaults []string
	if json.Unmarshal(existingRaw, &existing) != nil || json.Unmarshal(defaultRaw, &defaults) != nil {
		return nil, false
	}
	seen := make(map[string]struct{}, len(existing)+len(defaults))
	for _, method := range existing {
		method = strings.TrimSpace(method)
		if method != "" {
			seen[method] = struct{}{}
		}
	}
	if _, wildcard := seen["*"]; wildcard {
		return existing, false
	}
	merged := append([]string(nil), existing...)
	changed := false
	for _, method := range defaults {
		method = strings.TrimSpace(method)
		if method == "" {
			continue
		}
		if _, ok := seen[method]; ok {
			continue
		}
		seen[method] = struct{}{}
		merged = append(merged, method)
		changed = true
	}
	return merged, changed
}

func (d *DAO) exists(ctx context.Context, nodeID, serviceName string) (bool, error) {
	var count int64
	err := d.db.WithContext(ctx).Model(&Deployment{}).
		Where("c_node_id = ? AND c_service_name = ?", nodeID, serviceName).
		Count(&count).Error
	return count > 0, err
}

type ListFilter struct {
	NodeID         string
	ServiceName    string
	ServiceKind    string
	Scope          string
	Status         string
	GatewayEnabled *bool
}

func normalizeDeployment(item *Deployment) {
	item.NodeID = strings.TrimSpace(item.NodeID)
	item.ServiceName = strings.TrimSpace(item.ServiceName)
	item.ServiceKind = strings.TrimSpace(item.ServiceKind)
	item.Protocol = strings.TrimSpace(item.Protocol)
	item.Host = strings.TrimSpace(item.Host)
	item.GatewayPath = strings.TrimSpace(item.GatewayPath)
	item.GatewayServiceID = strings.TrimSpace(item.GatewayServiceID)
	item.Scope = strings.TrimSpace(item.Scope)
	item.Status = strings.TrimSpace(item.Status)
	item.Description = strings.TrimSpace(item.Description)
	item.ExtraConfig = strings.TrimSpace(item.ExtraConfig)
	if item.ServiceKind == "" {
		item.ServiceKind = "service"
	}
	if item.Protocol == "" {
		item.Protocol = "http"
	}
	if item.Scope == "" {
		item.Scope = "public"
	}
	if item.Status == "" {
		item.Status = "active"
	}
	if item.ExtraConfig == "" {
		item.ExtraConfig = "{}"
	}
}

func isUniqueConstraintError(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "UNIQUE constraint")
}
