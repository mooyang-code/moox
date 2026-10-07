package storageio

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/compiler"
	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
)

// ViewCatalog is the narrow metadata surface needed by strategy compilation.
// Concrete deployments can adapt Storage Metadata RPC clients without making
// the compiler depend on generated transport types.
type ViewCatalog interface {
	compiler.StorageCatalog
}

type CatalogClient struct {
	GetViewFunc     func(context.Context, string) (compiler.ViewDescriptor, error)
	ListColumnsFunc func(context.Context, string) ([]compiler.ViewColumn, error)
}

type PeriodReader interface {
	Load(context.Context, domain.StrategyRunner, compiler.CompiledStrategy, time.Time) (input.EvaluationInput, error)
}
