package metrics

import (
	"context"
	"errors"

	"github.com/mooyang-code/moox/modules/monitor/internal/store"
)

type ProducerAuthorizer interface {
	IsRegistered(context.Context, string, string) (bool, error)
}

type CheckProducerAuthorizer struct {
	Checks *store.CheckRepository
}

func (a CheckProducerAuthorizer) IsRegistered(ctx context.Context, serviceName, nodeID string) (bool, error) {
	if a.Checks == nil {
		return false, errors.New("check producer authorizer is not initialized")
	}
	return a.Checks.IsSysDeployRegistered(ctx, serviceName, nodeID)
}
