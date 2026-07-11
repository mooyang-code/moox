package reconciliation

import (
	"context"
	"github.com/mooyang-code/moox/modules/trade/internal/application/command"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

type Reconciler struct {
	Store  *store.Store
	Engine *command.Engine
}

func (r Reconciler) Order(ctx context.Context, space, id string) (store.OrderRecord, error) {
	v, e := r.Store.GetOrder(ctx, space, id)
	if e != nil {
		return v, e
	}
	if v.State == string(order.SubmitUnknown) {
		return r.Engine.ResolveUnknown(ctx, space, id)
	}
	return v, nil
}
