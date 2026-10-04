package trigger

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/packages/events"
)

// SetLocator resolves an enabled set and its current exact event filters.
type SetLocator interface {
	EnabledSetByDataset(context.Context, string, string, string) (domain.FactorSet, []domain.FactorDef, bool, error)
	FilterSubjects(context.Context) ([]string, error)
}

// SetRepository is the catalog surface used by StoreSetLocator.
type SetRepository interface {
	EnabledSetByDataset(context.Context, string, string, string) (domain.FactorSet, []domain.FactorDef, bool, error)
	ListSets(context.Context) ([]domain.FactorSet, error)
}

// StoreSetLocator adapts the Factor SQLite store to the trigger catalog contract.
type StoreSetLocator struct {
	store    SetRepository
	registry *events.Registry
}

func NewStoreSetLocator(db SetRepository) (*StoreSetLocator, error) {
	if db == nil {
		return nil, fmt.Errorf("factor set repository is required")
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return nil, err
	}
	return &StoreSetLocator{store: db, registry: registry}, nil
}

func (l *StoreSetLocator) EnabledSetByDataset(ctx context.Context, spaceID, datasetID, freq string) (domain.FactorSet, []domain.FactorDef, bool, error) {
	if l == nil || l.store == nil {
		return domain.FactorSet{}, nil, false, fmt.Errorf("factor set locator is not initialized")
	}
	return l.store.EnabledSetByDataset(ctx, spaceID, datasetID, freq)
}

func (l *StoreSetLocator) FilterSubjects(ctx context.Context) ([]string, error) {
	if l == nil || l.store == nil || l.registry == nil {
		return nil, fmt.Errorf("factor set locator is not initialized")
	}
	sets, err := l.store.ListSets(ctx)
	if err != nil {
		return nil, err
	}
	filters := make([]string, 0, len(sets))
	for _, set := range sets {
		if set.Status != domain.SetStatusEnabled {
			continue
		}
		subject, err := l.registry.RenderSubject(events.CollectorPeriodCompleted, set.SpaceID, set.SourceDatasetID)
		if err != nil {
			return nil, fmt.Errorf("render collector period subject for set %s: %w", set.SetID, err)
		}
		filters = append(filters, subject)
	}
	return NormalizeFilters(filters), nil
}

type PeriodStore interface {
	ComputedExists(context.Context, string, string, string, int64) (bool, error)
	DatasetColumns(context.Context, string, string) ([]string, error)
}

type PipelineRunner interface {
	Run(context.Context, pipeline.Plan) (pipeline.Outcome, error)
}

type SetLocks interface {
	LockContext(context.Context, string) (func(), error)
}

// NormalizeFilters trims, de-duplicates and sorts subject filters.
func NormalizeFilters(filters []string) []string {
	seen := make(map[string]struct{}, len(filters))
	out := make([]string, 0, len(filters))
	for _, filter := range filters {
		filter = strings.TrimSpace(filter)
		if filter == "" {
			continue
		}
		if _, ok := seen[filter]; ok {
			continue
		}
		seen[filter] = struct{}{}
		out = append(out, filter)
	}
	sort.Strings(out)
	return out
}

var _ SetLocator = (*StoreSetLocator)(nil)
