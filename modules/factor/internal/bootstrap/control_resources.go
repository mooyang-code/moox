package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/registry"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	"github.com/mooyang-code/moox/packages/gatewayauth"
)

// ControlResources owns the authoritative catalog and management dependencies.
// It opens no listeners, consumers or Python workers. Callers must cancel and
// drain management RPCs and the catalog responder before closing the store.
type ControlResources struct {
	Store    *store.Store
	Registry *registry.Service
	Metadata *registry.MetadataSync

	ctx       context.Context
	cancel    context.CancelFunc
	closeOnce sync.Once
	closeErr  error
}

// OpenControlResources validates persisted source artifacts without performing
// remote reconciliation. Management RPC and catalog ingress remain separate.
func OpenControlResources(ctx context.Context, cfg *ControlConfig) (*ControlResources, error) {
	if ctx == nil || cfg == nil {
		return nil, errors.New("control context and configuration are required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.ArtifactsDir) == "" {
		return nil, errors.New("control artifacts_dir is required")
	}
	if err := gatewayauth.ValidateTargetNode(cfg.Storage.GatewayNodeID); err != nil {
		return nil, err
	}
	if err := validateRoleStorage(cfg.Database, cfg.Storage, cfg.EventBus.URLs); err != nil {
		return nil, err
	}
	credentials, err := gatewayauth.ResolveCredentials(cfg.Storage.KeyID, cfg.Storage.HMACKeyFile)
	if err != nil {
		return nil, fmt.Errorf("load control storage credentials: %w", err)
	}
	db, err := store.Open(&store.Options{Path: cfg.Database.Path, MaxIdleConns: cfg.Database.MaxIdleConns, MaxOpenConns: cfg.Database.MaxOpenConns, ConnMaxLifetime: cfg.Database.ConnMaxLifetime, ConnMaxIdleTime: cfg.Database.ConnMaxIdleTime})
	if err != nil {
		return nil, err
	}
	if err := db.ApplySchema(factorschema.AllSQL()); err != nil {
		return nil, errors.Join(fmt.Errorf("initialize control catalog schema: %w", err), db.Close())
	}
	runCtx, cancel := context.WithCancel(ctx)
	metadata := registry.NewMetadataSync(newMetadataClient(cfg.Storage.GatewayTarget, cfg.Storage.GatewayNodeID, credentials), factorAuthInfo())
	r := &ControlResources{Store: db, Metadata: metadata, ctx: runCtx, cancel: cancel}
	r.Registry = registry.NewService(db.Factors(), metadata, registry.Options{FactorsDir: cfg.ArtifactsDir}).WithBindings(db.Bindings())
	if err := r.Registry.EnsureSourceArtifacts(runCtx); err != nil {
		return nil, errors.Join(fmt.Errorf("restore control source artifacts: %w", err), r.Close())
	}
	if err := bootstrapEmptyControlCatalog(runCtx, db); err != nil {
		return nil, errors.Join(fmt.Errorf("bootstrap control catalog revision: %w", err), r.Close())
	}
	if err := runCtx.Err(); err != nil {
		return nil, errors.Join(err, r.Close())
	}
	return r, nil
}

func bootstrapEmptyControlCatalog(ctx context.Context, db *store.Store) error {
	snapshot, err := db.CatalogSnapshot(ctx)
	if err != nil {
		return err
	}
	if snapshot.Revision > 0 {
		return nil
	}
	if len(snapshot.Factors) != 0 || len(snapshot.Bindings) != 0 {
		return errors.New("unversioned control catalog contains definitions")
	}
	_, err = db.ReplaceCatalogSnapshot(ctx, domain.CatalogSnapshot{Revision: 1})
	return err
}

func (r *ControlResources) Context() context.Context { return r.ctx }

// Cancel signals shutdown without closing SQLite underneath in-flight RPCs.
func (r *ControlResources) Cancel() { r.cancel() }

// Close is idempotent and requires external callers to have drained first.
func (r *ControlResources) Close() error {
	r.cancel()
	r.closeOnce.Do(func() { r.closeErr = r.Store.Close() })
	return r.closeErr
}
