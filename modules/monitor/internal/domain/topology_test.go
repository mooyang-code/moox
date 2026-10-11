package domain

import (
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/servicecatalog"
)

// SysDeploy 允许 1024 台主机、每台两个自动登记的主机组件；监控端必须接收这个规模的拓扑。
func TestTopologySnapshotAcceptsSysDeployScale(t *testing.T) {
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	snapshot := TopologySnapshot{Catalog: catalog, ObservedAt: time.Now().UTC()}
	for i := 0; i < 751; i++ {
		host := fmt.Sprintf("h%d", i)
		snapshot.Hosts = append(snapshot.Hosts, TopologyHost{HostID: host, Address: fmt.Sprintf("10.0.%d.%d", i/250, i%250+1), Status: servicecatalog.Enabled})
		for _, component := range []string{"host-gateway", "host-agent"} {
			snapshot.Placements = append(snapshot.Placements, TopologyPlacement{HostID: host, ComponentID: component, Status: servicecatalog.Enabled})
		}
	}
	if len(snapshot.Placements) <= 1500 {
		t.Fatal("测试夹具必须超过原先 1500 的上限")
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("Validate() 拒绝了 SysDeploy 允许的拓扑规模: %v", err)
	}
	snapshot.Hosts = append(snapshot.Hosts, make([]TopologyHost, MaxTopologyHosts)...)
	if snapshot.Validate() == nil {
		t.Fatal("超过主机上限的拓扑必须被拒绝")
	}
}
