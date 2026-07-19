package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/config"
	"github.com/mooyang-code/moox/modules/storage/internal/health"
	"github.com/mooyang-code/moox/packages/healthz"
	"trpc.group/trpc-go/trpc-go/server"
)

type readyState interface {
	Ready() bool
}

type contextReadyState interface {
	ReadyContext(context.Context) bool
}

type storageHealthDependencies struct {
	eventbus readyState
	view     readyState
	primary  readyState
}

func registerStorageHealth(s *server.Server, storage storageconfig.StorageConfig, deps storageHealthDependencies) error {
	serviceName := storageServiceName(storage)
	state := health.New("storage", serviceName, "", "")
	state.SnapshotFunc = storageHealthSnapshot(storage, state, deps)
	if s == nil {
		return fmt.Errorf("storage health service is unavailable")
	}
	if err := health.Register(s.Service("trpc.moox.storage.Health"), state); err != nil {
		return fmt.Errorf("storage health server failed to start: %w", err)
	}
	return nil
}

func storageHealthSnapshot(storage storageconfig.StorageConfig, state *health.State, deps storageHealthDependencies) healthz.SnapshotFunc {
	return func(ctx context.Context) healthz.Response {
		serviceName := storageServiceName(storage)
		roleSummary := storageRoleSummary(storage)
		rootReady := storage.Root != "" && pathExists(storage.Root)
		metadataRequired := roleSummary != "shard"
		metadataReady := !metadataRequired || (storage.Metadata.Path != "" && pathExists(storage.Metadata.Path))
		eventbusReady := !needsRowsCommittedBus(storage) || (deps.eventbus != nil && deps.eventbus.Ready())
		viewRequired := shouldRegisterViewQueryRole(storage) || shouldStartViewBuilderRole(storage) || shouldStartViewIndexRole(storage)
		viewRuntimeReady := !viewRequired || readyWithContext(ctx, deps.view)
		primaryReady := (!storage.HasRole("primary") && !storage.HasRole("shard")) || (deps.primary != nil && deps.primary.Ready())
		ready := rootReady && metadataReady && eventbusReady && viewRuntimeReady && primaryReady
		state.SetReady(ready)
		rsp := healthz.Base("storage", serviceName, "", "", storageStartedAt, ready)
		rsp.Service = serviceName
		rsp.Details = map[string]any{
			"service":            serviceName,
			"role":               roleSummary,
			"roles":              roleSummary,
			"root":               storage.Root,
			"eventbus_type":      storage.EventBus.Type,
			"metadata_path":      storage.Metadata.Path,
			"view_max_workers":   storage.View.MaxWorkers,
			"primary_service":    storage.Primary.ServiceName,
			"root_ready":         rootReady,
			"metadata_ready":     metadataReady,
			"eventbus_ready":     eventbusReady,
			"view_runtime_ready": viewRuntimeReady,
			"primary_ready":      primaryReady,
		}
		return rsp
	}
}

func readyWithContext(ctx context.Context, dependency readyState) bool {
	if dependency == nil {
		return false
	}
	if candidate, ok := dependency.(contextReadyState); ok {
		return candidate.ReadyContext(ctx)
	}
	return dependency.Ready()
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func storageServiceName(storage storageconfig.StorageConfig) string {
	roleSummary := storageRoleSummary(storage)
	switch roleSummary {
	case "primary":
		return "storage-primary"
	case "shard":
		return "storage-shard"
	case "view":
		return "storage-view"
	case "access,view":
		return "storage"
	default:
		if roleSummary == "" {
			return "storage"
		}
		return "storage-" + strings.ReplaceAll(strings.ReplaceAll(roleSummary, "_", "-"), ",", "-")
	}
}

func storageRoleSummary(storage storageconfig.StorageConfig) string {
	return strings.Join(storage.Roles, ",")
}
