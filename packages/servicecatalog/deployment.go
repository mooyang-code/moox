package servicecatalog

import (
	"errors"
	"fmt"
	"sort"
)

// Host 是纳入 MooX 部署的一台主机，主机 ID 即主机名。
type Host struct {
	ID             string
	Address        string
	PrivateAddress string
	Region         string
	Enabled        bool
}

// Placement 是一条部署：哪个组件部署在哪台主机、是否启用。
type Placement struct {
	HostID      string
	ComponentID string
	Enabled     bool
}

// Deployment 是全部主机与部署，来自 Admin 的 t_hosts、t_placements。
type Deployment struct {
	Hosts      []Host
	Placements []Placement
}

// ValidateDeployment 按组件目录校验全部主机与部署：部署范围、副本数、受保护对象。
// SyncHostPlacements、离线初始化和启停操作都用它校验修改后的完整状态，任何一项不通过就整体拒绝。
func (c *Catalog) ValidateDeployment(deployment Deployment) error {
	hosts := make(map[string]Host, len(deployment.Hosts))
	addresses := map[string]string{}
	for _, host := range deployment.Hosts {
		if !identifierPattern.MatchString(host.ID) {
			return fmt.Errorf("主机 ID %q 只能包含小写字母、数字和连字符，且以字母开头", host.ID)
		}
		if _, exists := hosts[host.ID]; exists {
			return fmt.Errorf("主机 %s 重复", host.ID)
		}
		if host.Address == "" {
			return fmt.Errorf("主机 %s 缺少地址", host.ID)
		}
		if previous, exists := addresses[host.Address]; exists {
			return fmt.Errorf("主机 %s 与 %s 的地址相同：%s", host.ID, previous, host.Address)
		}
		addresses[host.Address] = host.ID
		if host.ID == ControlHostID && !host.Enabled {
			return errors.New("control 主机受保护，不能停用")
		}
		hosts[host.ID] = host
	}
	seen := map[[2]string]bool{}
	enabledCount := map[string]int{}
	placedOnControl := map[string]bool{}
	for _, placement := range deployment.Placements {
		component, ok := c.components[placement.ComponentID]
		if !ok {
			return fmt.Errorf("组件 %q 不在组件目录中", placement.ComponentID)
		}
		if _, ok := hosts[placement.HostID]; !ok {
			return fmt.Errorf("组件 %s 部署到了未登记的主机 %q", placement.ComponentID, placement.HostID)
		}
		key := [2]string{placement.HostID, placement.ComponentID}
		if seen[key] {
			return fmt.Errorf("主机 %s 上的组件 %s 重复部署", placement.HostID, placement.ComponentID)
		}
		seen[key] = true
		if component.Scope == ScopeControl && placement.HostID != ControlHostID {
			return fmt.Errorf("组件 %s 只能部署在 control 主机，不能部署到 %s", component.ID, placement.HostID)
		}
		if component.Protected && !placement.Enabled {
			return fmt.Errorf("组件 %s 受保护，不能停用", component.ID)
		}
		if placement.Enabled {
			enabledCount[component.ID]++
		}
		if placement.HostID == ControlHostID {
			placedOnControl[component.ID] = true
		}
	}
	for id, count := range enabledCount {
		component := c.components[id]
		if component.Replicas == ReplicasSingle && count > 1 {
			return fmt.Errorf("组件 %s 只允许一份，但有 %d 条启用的部署", id, count)
		}
	}
	if _, hasControl := hosts[ControlHostID]; hasControl {
		for _, component := range c.Components {
			if component.Protected && component.Scope == ScopeControl && !placedOnControl[component.ID] {
				return fmt.Errorf("组件 %s 受保护，必须部署在 control 主机", component.ID)
			}
		}
	}
	return nil
}

// EffectivePlacements 返回每台主机上实际生效的部署：「主机」范围的组件在每台主机上自动存在，
// 没有记录时视为启用；返回结果按主机 ID、组件 ID 排序。
func (c *Catalog) EffectivePlacements(deployment Deployment) []Placement {
	explicit := map[[2]string]Placement{}
	for _, placement := range deployment.Placements {
		explicit[[2]string{placement.HostID, placement.ComponentID}] = placement
	}
	out := make([]Placement, 0, len(deployment.Placements)+2*len(deployment.Hosts))
	for _, placement := range deployment.Placements {
		if _, known := c.components[placement.ComponentID]; known {
			out = append(out, placement)
		}
	}
	for _, host := range deployment.Hosts {
		for _, component := range c.Components {
			if component.Scope != ScopeHost {
				continue
			}
			if _, exists := explicit[[2]string{host.ID, component.ID}]; !exists {
				out = append(out, Placement{HostID: host.ID, ComponentID: component.ID, Enabled: true})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].HostID != out[j].HostID {
			return out[i].HostID < out[j].HostID
		}
		return out[i].ComponentID < out[j].ComponentID
	})
	return out
}

// HostComponents 返回「主机」范围的组件 ID，注册主机时自动为它们创建部署记录。
func (c *Catalog) HostComponents() []string {
	var out []string
	for _, component := range c.Components {
		if component.Scope == ScopeHost {
			out = append(out, component.ID)
		}
	}
	return out
}
