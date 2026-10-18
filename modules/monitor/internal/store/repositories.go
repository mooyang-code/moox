package store

import (
	"errors"

	"gorm.io/gorm"
)

var (
	ErrResourceReferenced = errors.New("resource is still referenced")
	ErrInvalidReference   = errors.New("referenced resource is missing or disabled")
)

// Repositories is the monitor control-plane persistence graph. Bootstrap
// creates it once and injects the individual capabilities into services,
// keeping database construction out of orchestration code.
type Repositories struct {
	Checks          *CheckRepository
	Topology        *TopologyRepository
	ComponentHealth *ComponentHealthRepository
	Gateways        *GatewayRepository
	Results         *ResultRepository
	Alerts          *AlertRepository
	Notifications   *NotificationRepository
}

func NewRepositories(db *gorm.DB) *Repositories {
	return &Repositories{
		Checks:          NewCheckRepository(db),
		Topology:        &TopologyRepository{db: db},
		ComponentHealth: &ComponentHealthRepository{db: db},
		Gateways:        &GatewayRepository{db: db},
		Results:         NewResultRepository(db),
		Alerts:          NewAlertRepository(db),
		Notifications:   NewNotificationRepository(db),
	}
}
