package store

import "gorm.io/gorm"

// Repositories is the monitor control-plane persistence graph. Bootstrap
// creates it once and injects the individual capabilities into services,
// keeping database construction out of orchestration code.
type Repositories struct {
	Checks  *CheckRepository
	Results *ResultRepository
	Alerts  *AlertRepository
	Peers   *PeerRepository
}

func NewRepositories(db *gorm.DB) *Repositories {
	return &Repositories{
		Checks:  NewCheckRepository(db),
		Results: NewResultRepository(db),
		Alerts:  NewAlertRepository(db),
		Peers:   NewPeerRepository(db),
	}
}
