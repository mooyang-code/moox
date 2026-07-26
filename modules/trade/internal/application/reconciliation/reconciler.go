package reconciliation

import (
	"github.com/mooyang-code/moox/modules/trade/internal/application/command"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

type Reconciler struct {
	Store  *store.Store
	Engine *command.Engine
}
