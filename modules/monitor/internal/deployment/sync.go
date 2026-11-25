package deployment

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
)

// Syncer 定期从 Admin 同步部署拓扑（每台主机应该有哪些组件）。
// 组件健康（含主机网关）只看上报（设计 3.8），这里不生成任何探测检查。
type Syncer struct {
	topology *store.TopologyRepository
	source   Source
	now      func() time.Time
}

func NewSyncer(topology *store.TopologyRepository, source Source) *Syncer {
	return &Syncer{topology: topology, source: source, now: time.Now}
}

func (s *Syncer) Sync(ctx context.Context) error {
	if s == nil || s.source == nil || s.topology == nil {
		return fmt.Errorf("deployment synchronization is not initialized")
	}
	snapshot, err := s.source.Snapshot(ctx)
	if err != nil {
		return err
	}
	// 拓扑写库前整份校验（目录、主机与部署不重复、状态合法），不合法时不写入。
	return s.topology.Reconcile(ctx, topologySnapshot(snapshot, s.now().UTC()))
}

func topologySnapshot(snapshot Snapshot, now time.Time) domain.TopologySnapshot {
	out := domain.TopologySnapshot{Catalog: snapshot.Catalog, ObservedAt: now}
	for _, host := range snapshot.Hosts {
		out.Hosts = append(out.Hosts, domain.TopologyHost{HostID: host.GetHostId(), Address: host.GetAddress(), PrivateAddress: host.GetPrivateAddress(), Region: host.GetRegion(), Status: host.GetStatus()})
	}
	for _, deployment := range snapshot.Deployments {
		out.Deployments = append(out.Deployments, domain.TopologyDeployment{HostID: deployment.GetHostId(), ComponentID: deployment.GetComponentId(), Status: deployment.GetStatus()})
	}
	return out
}
