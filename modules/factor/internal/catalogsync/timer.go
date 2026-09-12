package catalogsync

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/packages/timerjob"
)

// NewReconcileJob polls after the startup synchronization. The tRPC service may
// tick more frequently than interval; unsuccessful attempts are also throttled.
func NewReconcileJob(interval, timeout time.Duration, now func() time.Time, syncCatalog func(context.Context) (SyncResult, error)) (*timerjob.Job, error) {
	if interval <= 0 || now == nil || syncCatalog == nil {
		return nil, fmt.Errorf("catalog reconciliation requires a positive interval, clock and synchronizer")
	}
	next := now().Add(interval)
	return timerjob.New("factor_catalog_reconcile", timeout, func(ctx context.Context) error {
		// timerjob serializes invocations, including this deadline state.
		current := now()
		if current.Before(next) {
			return nil
		}
		next = current.Add(interval)
		_, err := syncCatalog(ctx)
		return err
	})
}
