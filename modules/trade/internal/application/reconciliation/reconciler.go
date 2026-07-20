package reconciliation

import (
	"context"
	"github.com/mooyang-code/moox/modules/trade/internal/application/command"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/telemetry"
	"time"
)

type Reconciler struct {
	Store  *store.Store
	Engine *command.Engine
}

func (r Reconciler) Order(ctx context.Context, space, id string) (store.OrderRecord, error) {
	v, e := r.Store.GetOrder(ctx, space, id)
	if e != nil {
		telemetry.RecordModuleStage("reconcile", "error", time.Time{})
		return v, e
	}
	if v.State == string(order.SubmitUnknown) {
		resolved, err := r.Engine.ResolveUnknown(ctx, space, id)
		result := "success"
		if err != nil {
			result = "error"
		}
		telemetry.RecordModuleStage("reconcile", result, time.Now())
		return resolved, err
	}
	telemetry.RecordModuleStage("reconcile", "success", time.Now())
	return v, nil
}
