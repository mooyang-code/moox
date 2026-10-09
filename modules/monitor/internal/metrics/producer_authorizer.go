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

// CheckProducerAuthorizer 只接受已登记部署的上报：上报的服务名是组件 ID，节点是主机 ID。ExternalProducers 是没有
// 部署记录的上报方，例如 SCF 采集函数。
type CheckProducerAuthorizer struct {
	Checks            *store.CheckRepository
	ExternalProducers map[string]struct{}
}

func (a CheckProducerAuthorizer) IsRegistered(ctx context.Context, componentID, hostID string) (bool, error) {
	if a.Checks == nil {
		return false, errors.New("上报方校验未初始化")
	}
	componentID, hostID = strings.TrimSpace(componentID), strings.TrimSpace(hostID)
	if componentID == "" || hostID == "" {
		return false, nil
	}
	if _, ok := a.ExternalProducers[componentID]; ok {
		return true, nil
	}
	return a.Checks.IsPlacementRegistered(ctx, componentID, hostID)
}
