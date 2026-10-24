package sysdeploy

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/mooyang-code/moox/packages/servicecatalog"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrInvalidTopology = errors.New("invalid host topology")

// TopologyDAO is shared by the live API and offline bootstrap/recovery commands.
// It never creates schema or seeds deployments during Admin startup.
type TopologyDAO struct {
	db            *gorm.DB
	catalog       servicecatalog.Catalog
	controlHostID string
}

func NewTopologyDAO(db *gorm.DB, controlHostID string) (*TopologyDAO, error) {
	if db == nil || controlHostID == "" {
		return nil, fmt.Errorf("topology requires database and control host ID")
	}
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return nil, err
	}
	return &TopologyDAO{db: db, catalog: catalog, controlHostID: controlHostID}, nil
}

func (d *TopologyDAO) records(tx *gorm.DB) ([]HostRecord, []DeploymentRecord, error) {
	var hosts []HostRecord
	var deployments []DeploymentRecord
	if err := tx.Order("c_host_id ASC").Find(&hosts).Error; err != nil {
		return nil, nil, err
	}
	if err := tx.Order("c_host_id ASC, c_component_id ASC").Find(&deployments).Error; err != nil {
		return nil, nil, err
	}
	return hosts, deployments, nil
}

func (d *TopologyDAO) topology(hosts []HostRecord, deployments []DeploymentRecord) servicecatalog.Topology {
	t := servicecatalog.Topology{ControlHostID: d.controlHostID}
	for _, h := range hosts {
		reachability, _ := servicecatalog.ParseReachability(h.Reachability)
		t.Hosts = append(t.Hosts, servicecatalog.Host{ID: h.HostID, Address: h.Address, PrivateAddress: h.PrivateAddress, Region: h.Region, Status: h.Status, Reachability: reachability})
	}
	for _, p := range deployments {
		t.Deployments = append(t.Deployments, servicecatalog.Deployment{HostID: p.HostID, ComponentID: p.ComponentID, Status: p.Status})
	}
	return t
}

// Read returns a consistent configuration snapshot. Runtime heartbeats are read
// separately so frequent status writes do not alter the compiled directory.
func (d *TopologyDAO) Read(ctx context.Context) (hosts []HostRecord, deployments []DeploymentRecord, err error) {
	err = d.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var readErr error
		hosts, deployments, readErr = d.records(tx)
		return readErr
	})
	return
}

func (d *TopologyDAO) Compile(ctx context.Context, hostID string) (servicecatalog.CompiledHost, error) {
	hosts, deployments, err := d.Read(ctx)
	if err != nil {
		return servicecatalog.CompiledHost{}, err
	}
	if !slices.ContainsFunc(hosts, func(h HostRecord) bool { return h.HostID == hostID }) {
		return servicecatalog.CompiledHost{}, gorm.ErrRecordNotFound
	}
	return d.catalog.Compile(d.topology(hosts, deployments), hostID)
}

func (d *TopologyDAO) GetGatewayStatus(ctx context.Context, hostID string) (*HostGatewayStatus, error) {
	status := &HostGatewayStatus{}
	if err := d.db.WithContext(ctx).Where("c_host_id = ?", hostID).First(status).Error; err != nil {
		return nil, err
	}
	status.ClearExpiredConflict(time.Now().UTC())
	return status, nil
}

func (d *TopologyDAO) mutate(ctx context.Context, fn func(*gorm.DB) error) error {
	return d.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// SQLite starts deferred transactions. Reserve the writer before reading
		// the fleet, including when it is empty, so live and offline writers
		// cannot both validate against stale single-replica state. No row changes.
		if err := tx.Exec("UPDATE t_hosts SET c_host_id = c_host_id WHERE 0").Error; err != nil {
			return err
		}
		return fn(tx)
	})
}

func (d *TopologyDAO) SyncHostDeployments(ctx context.Context, spec HostSpec) error {
	return d.SyncHosts(ctx, []HostSpec{spec})
}

// SyncHosts supports offline bootstrap of a complete fleet in one transaction.
// A live synchronization uses SyncHostDeployments for exactly one host.
func (d *TopologyDAO) SyncHosts(ctx context.Context, specs []HostSpec) error {
	if len(specs) == 0 || len(specs) > 1024 {
		return fmt.Errorf("%w: host synchronization requires 1..1024 hosts", ErrInvalidTopology)
	}
	return d.mutate(ctx, func(tx *gorm.DB) error {
		hosts, deployments, err := d.records(tx)
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, spec := range specs {
			if seen[spec.HostID] {
				return fmt.Errorf("%w: duplicate host in synchronization", ErrInvalidTopology)
			}
			seen[spec.HostID] = true
			hosts, deployments, err = d.propose(hosts, deployments, spec)
			if err != nil {
				return err
			}
		}
		if err := d.catalog.ValidateTopology(d.topology(hosts, deployments)); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidTopology, err)
		}
		for _, host := range hosts {
			if !seen[host.HostID] {
				continue
			}
			host.UpdatedAt = time.Now().UTC()
			if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "c_host_id"}}, DoUpdates: clause.AssignmentColumns([]string{
				"c_address", "c_private_address", "c_region", "c_reachability", "c_description", "c_mtime",
			})}).Create(&host).Error; err != nil {
				return err
			}
			var componentIDs []string
			for _, deployment := range deployments {
				if deployment.HostID != host.HostID {
					continue
				}
				componentIDs = append(componentIDs, deployment.ComponentID)
				if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&deployment).Error; err != nil {
					return err
				}
			}
			if err := tx.Where("c_host_id = ? AND c_component_id NOT IN ?", host.HostID, componentIDs).Delete(&DeploymentRecord{}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (d *TopologyDAO) propose(hosts []HostRecord, deployments []DeploymentRecord, spec HostSpec) ([]HostRecord, []DeploymentRecord, error) {
	if len(spec.Description) > 4096 || len(spec.Components) > len(d.catalog.Components) {
		return nil, nil, fmt.Errorf("%w: host description or component list exceeds limit", ErrInvalidTopology)
	}
	reachability, ok := servicecatalog.ParseReachability(spec.Reachability)
	if !ok {
		return nil, nil, fmt.Errorf("%w: unknown host reachability %q", ErrInvalidTopology, spec.Reachability)
	}
	wanted := map[string]bool{}
	for _, id := range spec.Components {
		component, ok := d.catalog.Component(id)
		if !ok || wanted[id] || component.Scope == servicecatalog.ScopeHost {
			return nil, nil, fmt.Errorf("%w: unknown, duplicate or automatic component %q", ErrInvalidTopology, id)
		}
		wanted[id] = true
	}
	for _, component := range d.catalog.Components {
		if component.Scope == servicecatalog.ScopeHost {
			wanted[component.ID] = true
		}
	}
	host := HostRecord{HostID: spec.HostID, Address: spec.Address, PrivateAddress: spec.PrivateAddress, Region: spec.Region, Reachability: servicecatalog.ReachabilityName(reachability), Description: spec.Description, Status: servicecatalog.Enabled}
	if i := slices.IndexFunc(hosts, func(h HostRecord) bool { return h.HostID == spec.HostID }); i >= 0 {
		host.Status, host.CreatedAt = hosts[i].Status, hosts[i].CreatedAt
		hosts[i] = host
	} else {
		hosts = append(hosts, host)
	}
	kept := make([]DeploymentRecord, 0, len(deployments)+len(wanted))
	for _, deployment := range deployments {
		if deployment.HostID != spec.HostID {
			kept = append(kept, deployment)
			continue
		}
		if wanted[deployment.ComponentID] {
			kept = append(kept, deployment)
			delete(wanted, deployment.ComponentID)
			continue
		}
		if component, ok := d.catalog.Component(deployment.ComponentID); ok && component.Protected {
			return nil, nil, fmt.Errorf("%w: protected component %q cannot be removed", ErrInvalidTopology, component.ID)
		}
	}
	for id := range wanted {
		kept = append(kept, DeploymentRecord{HostID: spec.HostID, ComponentID: id, Status: servicecatalog.Enabled})
	}
	return hosts, kept, nil
}

func (d *TopologyDAO) SetHostStatus(ctx context.Context, hostID, status string) error {
	return d.mutate(ctx, func(tx *gorm.DB) error {
		hosts, deployments, err := d.records(tx)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(hosts, func(h HostRecord) bool { return h.HostID == hostID })
		if i < 0 {
			return gorm.ErrRecordNotFound
		}
		hosts[i].Status = status
		if err := d.catalog.ValidateTopology(d.topology(hosts, deployments)); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidTopology, err)
		}
		return tx.Model(&HostRecord{}).Where("c_host_id = ?", hostID).Updates(map[string]interface{}{"c_status": status, "c_mtime": time.Now().UTC()}).Error
	})
}

func (d *TopologyDAO) SetDeploymentStatus(ctx context.Context, hostID, componentID, status string) error {
	component, ok := d.catalog.Component(componentID)
	if !ok || component.Scope == servicecatalog.ScopeHost {
		return fmt.Errorf("%w: unknown or automatic component", ErrInvalidTopology)
	}
	return d.mutate(ctx, func(tx *gorm.DB) error {
		hosts, deployments, err := d.records(tx)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(deployments, func(p DeploymentRecord) bool { return p.HostID == hostID && p.ComponentID == componentID })
		if i < 0 {
			return gorm.ErrRecordNotFound
		}
		deployments[i].Status = status
		if err := d.catalog.ValidateTopology(d.topology(hosts, deployments)); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidTopology, err)
		}
		return tx.Model(&DeploymentRecord{}).Where("c_host_id = ? AND c_component_id = ?", hostID, componentID).Updates(map[string]interface{}{"c_status": status, "c_mtime": time.Now().UTC()}).Error
	})
}

func (d *TopologyDAO) DeleteHost(ctx context.Context, hostID string) error {
	if hostID == d.controlHostID {
		return fmt.Errorf("%w: control host cannot be deleted", ErrInvalidTopology)
	}
	return d.mutate(ctx, func(tx *gorm.DB) error {
		hosts, deployments, err := d.records(tx)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(hosts, func(h HostRecord) bool { return h.HostID == hostID })
		if i < 0 {
			return gorm.ErrRecordNotFound
		}
		for _, deployment := range deployments {
			component, ok := d.catalog.Component(deployment.ComponentID)
			if deployment.HostID == hostID && (!ok || component.Scope != servicecatalog.ScopeHost) {
				return fmt.Errorf("%w: host still has component deployments", ErrInvalidTopology)
			}
		}
		hosts = slices.Delete(hosts, i, i+1)
		deployments = slices.DeleteFunc(deployments, func(p DeploymentRecord) bool { return p.HostID == hostID })
		if err := d.catalog.ValidateTopology(d.topology(hosts, deployments)); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidTopology, err)
		}
		if err := tx.Where("c_host_id = ?", hostID).Delete(&HostGatewayStatus{}).Error; err != nil {
			return err
		}
		if err := tx.Where("c_host_id = ?", hostID).Delete(&DeploymentRecord{}).Error; err != nil {
			return err
		}
		return tx.Where("c_host_id = ?", hostID).Delete(&HostRecord{}).Error
	})
}
