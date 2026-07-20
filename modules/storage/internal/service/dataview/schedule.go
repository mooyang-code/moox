//go:build legacy_storage

package view

import (
	"context"
	"net/url"
	"strings"
	"sync"

	"trpc.group/trpc-go/trpc-go/log"
)

var defaultMaintenance struct {
	sync.RWMutex
	value *MaintenanceManager
}

func SetDefaultMaintenance(manager *MaintenanceManager) {
	defaultMaintenance.Lock()
	defer defaultMaintenance.Unlock()
	defaultMaintenance.value = manager
}

func currentDefaultMaintenance() *MaintenanceManager {
	defaultMaintenance.RLock()
	defer defaultMaintenance.RUnlock()
	return defaultMaintenance.value
}

func HandleSchedule(ctx context.Context, params string) error {
	values, err := url.ParseQuery(strings.TrimPrefix(strings.TrimSpace(params), "?"))
	if err != nil {
		log.WarnContextf(ctx, "[ViewBuilder] invalid schedule params %q: %v", params, err)
		return nil
	}
	op := strings.ToLower(strings.TrimSpace(values.Get("op")))
	if op != "maintain" {
		log.WarnContextf(ctx, "[ViewBuilder] unsupported schedule op %q, skip", op)
		return nil
	}
	manager := currentDefaultMaintenance()
	if manager == nil {
		log.WarnContext(ctx, "[ViewBuilder] maintenance manager is not initialized, skip schedule")
		return nil
	}
	changed, err := manager.MaintainViewIndexes(ctx, strings.TrimSpace(values.Get("space_id")))
	if err != nil {
		log.ErrorContextf(ctx, "[ViewBuilder] maintain schedule failed: %v", err)
		return nil
	}
	log.InfoContextf(ctx, "[ViewBuilder] maintain schedule activated %d View index(es)", changed)
	return nil
}
