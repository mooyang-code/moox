package catalogsync

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
)

type Replica interface {
	SnapshotReader
	ReplaceCatalogSnapshot(context.Context, domain.CatalogSnapshot) (bool, error)
}

// Activation must hold the engine execution fence while draining old tasks,
// cleaning retired ownership and invoking commit. It must invoke commit
// synchronously, exactly once, and must not retain it beyond the call.
type Activation func(ctx context.Context, previous, next domain.CatalogSnapshot, commit func() error) error

type Synchronizer struct {
	root     string
	replica  Replica
	fetch    func(context.Context) (*domain.CatalogSnapshot, error)
	activate Activation
	gate     chan struct{}
}

type SyncResult struct {
	Revision int64
	Applied  bool
}

func NewSynchronizer(root string, replica Replica, fetch func(context.Context) (*domain.CatalogSnapshot, error), activate Activation) (*Synchronizer, error) {
	if root == "" || replica == nil || fetch == nil || activate == nil {
		return nil, fmt.Errorf("catalog synchronization requires artifacts, replica, fetcher and fenced activation")
	}
	return &Synchronizer{root: root, replica: replica, fetch: fetch, activate: activate, gate: make(chan struct{}, 1)}, nil
}

// Sync is used on startup, notifications and periodic reconciliation. Each
// invocation fetches an authoritative snapshot; notification payloads are not
// applied directly, so dropped or reordered notifications cannot lose updates.
func (s *Synchronizer) Sync(ctx context.Context) (result SyncResult, err error) {
	select {
	case s.gate <- struct{}{}:
		defer func() { <-s.gate }()
	case <-ctx.Done():
		return result, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	previous, err := s.replica.CatalogSnapshot(ctx)
	if err != nil {
		return result, err
	}
	if previous == nil {
		return result, fmt.Errorf("local catalog is missing")
	}
	result.Revision = previous.Revision
	next, err := s.fetch(ctx)
	if err != nil {
		return result, err
	}
	if next == nil || next.Revision < 0 {
		return result, fmt.Errorf("invalid authoritative catalog")
	}
	if next.Revision < previous.Revision {
		return result, fmt.Errorf("authoritative catalog revision regressed")
	}
	if next.Revision == 0 {
		if len(next.Factors) != 0 || len(next.Bindings) != 0 {
			return result, fmt.Errorf("unversioned catalog contains definitions")
		}
		return result, nil
	}
	local, err := PrepareArtifacts(ctx, s.root, *next)
	if err != nil {
		return result, err
	}
	if local.Revision == previous.Revision {
		// The replica checks canonical equality, not revision alone.
		_, err = s.replica.ReplaceCatalogSnapshot(ctx, local)
		return result, err
	}
	committed := false
	err = s.activate(ctx, *previous, local, func() error {
		if committed {
			return fmt.Errorf("catalog activation committed more than once")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		changed, err := s.replica.ReplaceCatalogSnapshot(ctx, local)
		if err != nil {
			return err
		}
		committed = true
		result.Applied, result.Revision = changed, local.Revision
		return nil
	})
	if err == nil && !committed {
		return result, fmt.Errorf("catalog activation did not commit")
	}
	return result, err
}
