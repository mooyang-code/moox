package servicecatalog

import (
	"strings"
	"testing"
)

func TestValidateDeploymentRejects(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Deployment)
		want   string
	}{
		{"control 组件放到其他主机", func(d *Deployment) {
			d.Placements = append(d.Placements, Placement{HostID: "storage", ComponentID: "monitor", Enabled: false})
		}, "只能部署在 control"},
		{"single 组件两条启用的部署", func(d *Deployment) {
			d.Placements = append(d.Placements, Placement{HostID: "storage", ComponentID: "collector", Enabled: true})
		}, "只允许一份"},
		{"停用受保护组件", func(d *Deployment) { setPlacement(d, "control", "admin", false) }, "受保护"},
		{"停用 control 主机", func(d *Deployment) { d.Hosts[0].Enabled = false }, "control 主机受保护"},
		{"移除受保护组件", func(d *Deployment) { removePlacement(d, "control", "web-host") }, "必须部署在 control"},
		{"未知组件", func(d *Deployment) {
			d.Placements = append(d.Placements, Placement{HostID: "storage", ComponentID: "collector-subject", Enabled: true})
		}, "不在组件目录中"},
		{"未登记的主机", func(d *Deployment) {
			d.Placements = append(d.Placements, Placement{HostID: "compute-2", ComponentID: "access", Enabled: true})
		}, "未登记的主机"},
		{"重复部署", func(d *Deployment) {
			d.Placements = append(d.Placements, Placement{HostID: "storage", ComponentID: "access", Enabled: true})
		}, "重复部署"},
		{"主机地址重复", func(d *Deployment) { d.Hosts[2].Address = d.Hosts[1].Address }, "地址相同"},
		{"主机 ID 非法", func(d *Deployment) { d.Hosts[2].ID = "Compute_1" }, "主机 ID"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deployment := productionDeployment()
			tc.mutate(&deployment)
			err := Default().ValidateDeployment(deployment)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("期望报错包含 %q，实际 %v", tc.want, err)
			}
		})
	}
}

func TestValidateDeploymentAcceptsMultiAccessAndDisabledSingle(t *testing.T) {
	deployment := productionDeployment()
	// 停用的部署不计入副本数。
	deployment.Placements = append(deployment.Placements, Placement{HostID: "compute-1", ComponentID: "collector", Enabled: false})
	if err := Default().ValidateDeployment(deployment); err != nil {
		t.Fatal(err)
	}
}

func TestEffectivePlacementsAddsHostComponents(t *testing.T) {
	deployment := productionDeployment()
	deployment.Placements = append(deployment.Placements, Placement{HostID: "storage", ComponentID: "host-agent", Enabled: false})
	placements := Default().EffectivePlacements(deployment)
	found := map[string]bool{}
	for _, placement := range placements {
		if placement.ComponentID == "host-gateway" && placement.Enabled {
			found[placement.HostID] = true
		}
		if placement.HostID == "storage" && placement.ComponentID == "host-agent" && placement.Enabled {
			t.Fatal("显式停用的主机组件应当保持停用")
		}
	}
	if len(found) != 3 {
		t.Fatalf("每台主机都应当有主机网关: %v", found)
	}
}

func setPlacement(d *Deployment, host, component string, enabled bool) {
	for i := range d.Placements {
		if d.Placements[i].HostID == host && d.Placements[i].ComponentID == component {
			d.Placements[i].Enabled = enabled
		}
	}
}

func removePlacement(d *Deployment, host, component string) {
	out := d.Placements[:0]
	for _, placement := range d.Placements {
		if placement.HostID == host && placement.ComponentID == component {
			continue
		}
		out = append(out, placement)
	}
	d.Placements = out
}
