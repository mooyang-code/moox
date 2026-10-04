package metrics

import (
	"context"
	"errors"
	"strings"

	"github.com/mooyang-code/moox/modules/monitor/internal/store"
)

type ProducerAuthorizer interface {
	IsRegistered(context.Context, string, string) (bool, error)
}

type CheckProducerAuthorizer struct {
	Checks            *store.CheckRepository
	ExternalProducers map[string]struct{}
}

func (a CheckProducerAuthorizer) IsRegistered(ctx context.Context, serviceName, nodeID string) (bool, error) {
	if a.Checks == nil {
		return false, errors.New("check producer authorizer is not initialized")
	}
	serviceName, nodeID = strings.TrimSpace(serviceName), strings.TrimSpace(nodeID)
	if serviceName == "" || nodeID == "" {
		return false, nil
	}
	if _, ok := a.ExternalProducers[serviceName]; ok {
		return true, nil
	}
	return a.Checks.IsSysDeployRegistered(ctx, serviceName, nodeID)
}
