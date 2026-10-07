package store

import (
	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
)

type RunnerFilter struct {
	StrategyID string
	SpaceID    string
	Status     domain.RunnerStatus
}
