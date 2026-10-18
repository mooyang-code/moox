package domain

import (
	"fmt"
	"time"

	"github.com/mooyang-code/moox/packages/servicecatalog"
)

// TopologySnapshot is the last complete, validated deployment discovery. It is
// independent of probes: disabled deployments and health:none remain present.
type TopologySnapshot struct {
	Catalog    servicecatalog.Catalog `json:"catalog"`
	Hosts      []TopologyHost         `json:"hosts"`
	Placements []TopologyPlacement    `json:"placements"`
	ObservedAt time.Time              `json:"observed_at"`
}

type TopologyHost struct {
	HostID         string `json:"host_id"`
	Address        string `json:"address"`
	PrivateAddress string `json:"private_address"`
	Region         string `json:"region"`
	Status         string `json:"status"`
}

// 与 SysDeploy 的规模上限一致（最多 1024 台主机，每台最多放置全部组件）。
const (
	MaxTopologyHosts      = 1024
	MaxTopologyPlacements = 1024 * 24
)

type TopologyPlacement struct {
	HostID      string `json:"host_id"`
	ComponentID string `json:"component_id"`
	Status      string `json:"status"`
}

func (s TopologySnapshot) Validate() error {
	if err := s.Catalog.Validate(); err != nil {
		return err
	}
	if s.ObservedAt.IsZero() || len(s.Hosts) > MaxTopologyHosts || len(s.Placements) > MaxTopologyPlacements {
		return fmt.Errorf("invalid topology timestamp or size")
	}
	validStatus := func(status string) bool { return status == servicecatalog.Enabled || status == servicecatalog.Disabled }
	hosts := make(map[string]bool, len(s.Hosts))
	for _, host := range s.Hosts {
		if !servicecatalog.ValidHostID(host.HostID) || hosts[host.HostID] || !validStatus(host.Status) ||
			!servicecatalog.ValidHostAddress(host.Address) || (host.PrivateAddress != "" && !servicecatalog.ValidHostAddress(host.PrivateAddress)) {
			return fmt.Errorf("invalid or duplicate topology host %q", host.HostID)
		}
		hosts[host.HostID] = true
	}
	placements := make(map[string]bool, len(s.Placements))
	for _, placement := range s.Placements {
		key := placement.HostID + ":" + placement.ComponentID
		_, known := s.Catalog.Component(placement.ComponentID)
		if !hosts[placement.HostID] || !known || !validStatus(placement.Status) || placements[key] {
			return fmt.Errorf("invalid or duplicate topology placement %q", key)
		}
		placements[key] = true
	}
	return nil
}
