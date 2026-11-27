package sysdeploy

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/service/keys"
	secretdao "github.com/mooyang-code/moox/modules/admin/internal/service/secret/dao"
	secretmodel "github.com/mooyang-code/moox/modules/admin/internal/service/secret/model"
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
	keyStore      *keys.Store
	controlHostID string
	// encryptionKey 返回加密主机 SSH 凭据用的主密钥，只在真正要写凭据时才调用。
	encryptionKey func() (string, error)
}

func NewTopologyDAO(db *gorm.DB, controlHostID string) (*TopologyDAO, error) {
	if db == nil || controlHostID == "" {
		return nil, fmt.Errorf("topology requires database and control host ID")
	}
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return nil, err
	}
	keyStore, err := keys.NewStore(db)
	if err != nil {
		return nil, err
	}
	return &TopologyDAO{db: db, catalog: catalog, keyStore: keyStore, controlHostID: controlHostID}, nil
}

// WithEncryptionKey 指定加密主机 SSH 凭据的显式主密钥。
func (d *TopologyDAO) WithEncryptionKey(key string) *TopologyDAO {
	trimmed := strings.TrimSpace(key)
	d.encryptionKey = func() (string, error) {
		if trimmed == "" {
			return "", secretdao.ErrMissingEncryptionKey
		}
		return trimmed, nil
	}
	return d
}

// WithEncryptionKeyFunc 指定取得 SSH 凭据主密钥的方式，离线 bootstrap 在进程环境之外使用。
// 主机拓扑校验通过、真正要写凭据时才会调用，所以拓扑非法时不会因此创建主密钥。
func (d *TopologyDAO) WithEncryptionKeyFunc(fn func() (string, error)) *TopologyDAO {
	d.encryptionKey = fn
	return d
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
		t.Hosts = append(t.Hosts, servicecatalog.Host{ID: h.HostID, Address: h.Address, PrivateAddress: h.PrivateAddress, Region: h.Region, Status: h.Status, Reachability: reachability, ExternalEntry: h.ExternalEntry})
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

// Compile 按登记的部署与服务目录算一台主机的路由与目录，只用于展示和校验；网关自己按快照算。
func (d *TopologyDAO) Compile(ctx context.Context, hostID string) (servicecatalog.CompiledHost, error) {
	hosts, deployments, err := d.Read(ctx)
	if err != nil {
		return servicecatalog.CompiledHost{}, err
	}
	if !slices.ContainsFunc(hosts, func(h HostRecord) bool { return h.HostID == hostID }) {
		return servicecatalog.CompiledHost{}, gorm.ErrRecordNotFound
	}
	// 网关那一侧的 Compile 不校验拓扑（它信任 Admin）；Admin 自己算之前校验一遍。
	topology := d.topology(hosts, deployments)
	if err := d.catalog.ValidateTopology(topology); err != nil {
		return servicecatalog.CompiledHost{}, err
	}
	return d.catalog.Compile(topology, hostID)
}

func (d *TopologyDAO) GetGatewayStatus(ctx context.Context, hostID string) (*HostGatewayStatus, error) {
	status := &HostGatewayStatus{}
	if err := d.db.WithContext(ctx).Where("c_host_id = ?", hostID).First(status).Error; err != nil {
		return nil, err
	}
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

// ReplaceTopology 用 moox.toml 的整份声明覆盖登记：主机与部署的启停状态都取自声明；声明里没有的部署与主机删除
// （主机连同它的网关状态、部署与控制台 SSH 凭据）；签名公钥与 SSH 指纹不是声明的内容，保留库里的值。
// 返回连接信息变了或被删除的主机，调用方据此关掉它们的控制台会话。
func (d *TopologyDAO) ReplaceTopology(ctx context.Context, specs []HostSpec) ([]string, error) {
	if len(specs) == 0 || len(specs) > 1024 {
		return nil, fmt.Errorf("%w: topology requires 1..1024 hosts", ErrInvalidTopology)
	}
	var touched []string
	err := d.mutate(ctx, func(tx *gorm.DB) error {
		touched = nil
		hosts, deployments, err := d.records(tx)
		if err != nil {
			return err
		}
		previous := map[string]HostRecord{}
		for _, host := range hosts {
			previous[host.HostID] = host
		}
		var nextHosts []HostRecord
		var nextDeployments []DeploymentRecord
		seen := map[string]bool{}
		for _, spec := range specs {
			if seen[spec.HostID] {
				return fmt.Errorf("%w: duplicate host %q", ErrInvalidTopology, spec.HostID)
			}
			seen[spec.HostID] = true
			host, hostDeployments, err := d.declare(spec, previous, deployments)
			if err != nil {
				return err
			}
			nextHosts = append(nextHosts, host)
			nextDeployments = append(nextDeployments, hostDeployments...)
		}
		if err := d.catalog.ValidateTopology(d.topology(nextHosts, nextDeployments)); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidTopology, err)
		}
		for _, host := range hosts {
			if seen[host.HostID] {
				continue
			}
			for _, deployment := range deployments {
				if component, ok := d.catalog.Component(deployment.ComponentID); deployment.HostID == host.HostID && ok && component.Protected && component.Scope != servicecatalog.ScopeHost {
					return fmt.Errorf("%w: protected component %q cannot be removed", ErrInvalidTopology, component.ID)
				}
			}
			if err := deleteHostRows(tx, host.HostID); err != nil {
				return err
			}
			touched = append(touched, host.HostID)
		}
		specByHost := map[string]HostSpec{}
		for _, spec := range specs {
			specByHost[spec.HostID] = spec
		}
		now := time.Now().UTC()
		for _, host := range nextHosts {
			if old, ok := previous[host.HostID]; !ok || connectionChanged(old, host) {
				touched = append(touched, host.HostID)
			}
			host.UpdatedAt = now
			if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "c_host_id"}}, DoUpdates: clause.AssignmentColumns([]string{
				"c_label", "c_address", "c_private_address", "c_region", "c_status", "c_reachability", "c_external_entry", "c_description", "c_ssh_port", "c_ssh_user", "c_mtime",
			})}).Create(&host).Error; err != nil {
				return err
			}
			if err := d.writeSSHCredential(tx, host.HostID, specByHost[host.HostID].SSH); err != nil {
				return err
			}
			var componentIDs []string
			for _, deployment := range nextDeployments {
				if deployment.HostID != host.HostID {
					continue
				}
				componentIDs = append(componentIDs, deployment.ComponentID)
				if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "c_host_id"}, {Name: "c_component_id"}}, DoUpdates: clause.AssignmentColumns([]string{"c_status"})}).Create(&deployment).Error; err != nil {
					return err
				}
			}
			if err := tx.Where("c_host_id = ? AND c_component_id NOT IN ?", host.HostID, componentIDs).Delete(&DeploymentRecord{}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	return touched, err
}

// connectionChanged 报告控制台连这台主机要用的信息是否变了（地址、可达性、SSH 端口与用户、启停）。
func connectionChanged(old, next HostRecord) bool {
	return old.Address != next.Address || old.PrivateAddress != next.PrivateAddress || old.Reachability != next.Reachability ||
		old.SSHPort != next.SSHPort || old.SSHUser != next.SSHUser || old.Status != next.Status
}

// declare 把一台主机的声明换成登记行：主机级组件自动补上并保持启用，受保护组件不能被移除。
func (d *TopologyDAO) declare(spec HostSpec, previous map[string]HostRecord, deployments []DeploymentRecord) (HostRecord, []DeploymentRecord, error) {
	if len(spec.Description) > 4096 || len(spec.Components) > len(d.catalog.Components) {
		return HostRecord{}, nil, fmt.Errorf("%w: host description or component list exceeds limit", ErrInvalidTopology)
	}
	reachability, ok := servicecatalog.ParseReachability(spec.Reachability)
	if !ok {
		return HostRecord{}, nil, fmt.Errorf("%w: unknown host reachability %q", ErrInvalidTopology, spec.Reachability)
	}
	if len([]rune(spec.Label)) > 64 {
		return HostRecord{}, nil, fmt.Errorf("%w: host label exceeds 64 characters", ErrInvalidTopology)
	}
	sshPort, sshUser, err := d.validateSSH(spec, reachability)
	if err != nil {
		return HostRecord{}, nil, err
	}
	wanted := map[string]bool{}
	for _, id := range spec.Components {
		component, ok := d.catalog.Component(id)
		if !ok || wanted[id] || component.Scope == servicecatalog.ScopeHost {
			return HostRecord{}, nil, fmt.Errorf("%w: unknown, duplicate or automatic component %q", ErrInvalidTopology, id)
		}
		wanted[id] = true
	}
	disabled := map[string]bool{}
	for _, id := range spec.DisabledComponents {
		if !wanted[id] || disabled[id] {
			return HostRecord{}, nil, fmt.Errorf("%w: disabled component %q is not deployed on %q or duplicated", ErrInvalidTopology, id, spec.HostID)
		}
		disabled[id] = true
	}
	for _, deployment := range deployments {
		if deployment.HostID != spec.HostID || wanted[deployment.ComponentID] {
			continue
		}
		// 主机级组件下面会自动补上，不算移除。
		if component, ok := d.catalog.Component(deployment.ComponentID); ok && component.Protected && component.Scope != servicecatalog.ScopeHost {
			return HostRecord{}, nil, fmt.Errorf("%w: protected component %q cannot be removed", ErrInvalidTopology, component.ID)
		}
	}
	status := func(enabled bool) string {
		if enabled {
			return servicecatalog.Enabled
		}
		return servicecatalog.Disabled
	}
	host := HostRecord{HostID: spec.HostID, Label: spec.Label, Address: spec.Address, PrivateAddress: spec.PrivateAddress, Region: spec.Region, Reachability: servicecatalog.ReachabilityName(reachability), ExternalEntry: spec.ExternalEntry, Description: spec.Description, SSHPort: sshPort, SSHUser: sshUser, Status: status(spec.Enabled)}
	if old, ok := previous[spec.HostID]; ok {
		host.CreatedAt, host.SSHFingerprint, host.SignPublicKey = old.CreatedAt, old.SSHFingerprint, old.SignPublicKey
	}
	var out []DeploymentRecord
	for _, component := range d.catalog.Components {
		if component.Scope == servicecatalog.ScopeHost {
			out = append(out, DeploymentRecord{HostID: spec.HostID, ComponentID: component.ID, Status: servicecatalog.Enabled})
		}
	}
	ids := slices.Sorted(maps.Keys(wanted))
	for _, id := range ids {
		out = append(out, DeploymentRecord{HostID: spec.HostID, ComponentID: id, Status: status(!disabled[id])})
	}
	return host, out, nil
}

// deleteHostRows 删一台主机的全部登记：网关状态、部署、控制台 SSH 凭据与主机行。
func deleteHostRows(tx *gorm.DB, hostID string) error {
	if err := tx.Where("c_host_id = ?", hostID).Delete(&HostGatewayStatus{}).Error; err != nil {
		return err
	}
	if err := tx.Where("c_host_id = ?", hostID).Delete(&DeploymentRecord{}).Error; err != nil {
		return err
	}
	if err := secretdao.DeleteSystemSecret(tx, secretmodel.SSHHostSecretID(hostID)); err != nil {
		return err
	}
	return tx.Where("c_host_id = ?", hostID).Delete(&HostRecord{}).Error
}

// validateSSH 校验 SSH 信息并返回要写入 t_hosts 的端口与用户；没有 SSH 信息时端口为 0。
// 只能出站的主机不能被控制台直连，不接受 SSH 信息。
func (d *TopologyDAO) validateSSH(spec HostSpec, reachability string) (int, string, error) {
	if spec.SSH == nil {
		return 0, "", nil
	}
	if reachability == servicecatalog.ReachabilityOutbound {
		return 0, "", fmt.Errorf("%w: outbound host %q cannot carry SSH credentials", ErrInvalidTopology, spec.HostID)
	}
	ssh := spec.SSH
	if ssh.Port < 1 || ssh.Port > 65535 || strings.TrimSpace(ssh.User) == "" || len(ssh.User) > 128 {
		return 0, "", fmt.Errorf("%w: SSH requires a port in 1..65535 and a user name", ErrInvalidTopology)
	}
	if err := (secretmodel.SSHCredential{Password: ssh.Password, PrivateKey: ssh.PrivateKey, Passphrase: ssh.Passphrase}).Validate(); err != nil {
		return 0, "", fmt.Errorf("%w: %v", ErrInvalidTopology, err)
	}
	return ssh.Port, strings.TrimSpace(ssh.User), nil
}

// writeSSHCredential 在同一事务里写入或清除主机的 SSH 凭据；凭据不进日志和错误信息。
func (d *TopologyDAO) writeSSHCredential(tx *gorm.DB, hostID string, ssh *SSHSpec) error {
	secretID := secretmodel.SSHHostSecretID(hostID)
	if ssh == nil {
		return secretdao.DeleteSystemSecret(tx, secretID)
	}
	credential := secretmodel.SSHCredential{Password: ssh.Password, PrivateKey: ssh.PrivateKey, Passphrase: ssh.Passphrase}
	plain, err := credential.Encode()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidTopology, err)
	}
	if d.encryptionKey == nil {
		return secretdao.ErrMissingEncryptionKey
	}
	key, err := d.encryptionKey()
	if err != nil {
		return err
	}
	return secretdao.PutSystemSecret(tx, key, &secretmodel.Secret{
		SecretID: secretID, Name: "SSH " + hostID, Category: secretmodel.SSHCategory,
		SecretType: credential.Type(), KeyID: strings.TrimSpace(ssh.User), SecretValue: plain,
		ExtraConfig: "{}", Status: "active", Creator: "moox-cli",
	})
}
