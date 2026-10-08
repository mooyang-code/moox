package placement

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/servicecatalog"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	// ErrNotFound 表示主机或部署不存在。
	ErrNotFound = errors.New("主机或部署不存在")
	// ErrInvalid 表示修改后的部署不符合组件目录，或参数不合法。
	ErrInvalid = errors.New("部署校验未通过")
)

// Service 是部署主机与部署的业务逻辑，同时供 Admin 的 RPC 和离线命令使用。
type Service struct {
	db      *gorm.DB
	catalog *servicecatalog.Catalog
	now     func() time.Time
}

// NewService 创建服务；catalog 为空时使用内嵌的组件目录。
func NewService(db *gorm.DB, catalog *servicecatalog.Catalog) *Service {
	if catalog == nil {
		catalog = servicecatalog.Default()
	}
	return &Service{db: db, catalog: catalog, now: func() time.Time { return time.Now().UTC() }}
}

// WithClock 替换时钟，供测试使用。
func (s *Service) WithClock(now func() time.Time) *Service {
	s.now = now
	return s
}

// Catalog 返回使用的组件目录。
func (s *Service) Catalog() *servicecatalog.Catalog { return s.catalog }

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// state 是一次事务中载入的完整状态。
type state struct {
	hosts      []Host
	placements []Placement
}

func loadState(tx *gorm.DB) (state, error) {
	var out state
	if err := tx.Order("c_host_id").Find(&out.hosts).Error; err != nil {
		return state{}, fmt.Errorf("读取主机: %w", err)
	}
	if err := tx.Order("c_host_id, c_component_id").Find(&out.placements).Error; err != nil {
		return state{}, fmt.Errorf("读取部署: %w", err)
	}
	return out, nil
}

func (st state) deployment() servicecatalog.Deployment {
	deployment := servicecatalog.Deployment{}
	for _, host := range st.hosts {
		deployment.Hosts = append(deployment.Hosts, servicecatalog.Host{
			ID: host.HostID, Address: host.Address, PrivateAddress: host.PrivateAddress, Region: host.Region,
			Enabled: host.Status == StatusEnabled,
		})
	}
	for _, placement := range st.placements {
		deployment.Placements = append(deployment.Placements, servicecatalog.Placement{
			HostID: placement.HostID, ComponentID: placement.ComponentID, Enabled: placement.Status == StatusEnabled,
		})
	}
	return deployment
}

func (st state) host(id string) (Host, bool) {
	for _, host := range st.hosts {
		if host.HostID == id {
			return host, true
		}
	}
	return Host{}, false
}

func (st state) placement(hostID, componentID string) (Placement, bool) {
	for _, placement := range st.placements {
		if placement.HostID == hostID && placement.ComponentID == componentID {
			return placement, true
		}
	}
	return Placement{}, false
}

func (s *Service) validate(st state) error {
	if err := s.catalog.ValidateDeployment(st.deployment()); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return nil
}

// Deployment 返回当前全部主机与部署。
func (s *Service) Deployment(ctx context.Context) (servicecatalog.Deployment, error) {
	st, err := loadState(s.db.WithContext(ctx))
	if err != nil {
		return servicecatalog.Deployment{}, err
	}
	return st.deployment(), nil
}

// Compile 按当前部署编译服务目录和每台主机的路由。
func (s *Service) Compile(ctx context.Context) (servicecatalog.Compiled, error) {
	deployment, err := s.Deployment(ctx)
	if err != nil {
		return servicecatalog.Compiled{}, err
	}
	return s.catalog.Compile(deployment)
}

// ListHosts 返回全部主机。
func (s *Service) ListHosts(ctx context.Context) ([]Host, error) {
	var hosts []Host
	if err := s.db.WithContext(ctx).Order("c_host_id").Find(&hosts).Error; err != nil {
		return nil, fmt.Errorf("读取主机: %w", err)
	}
	return hosts, nil
}

// GetHost 返回一台主机。
func (s *Service) GetHost(ctx context.Context, hostID string) (Host, error) {
	var host Host
	err := s.db.WithContext(ctx).Where("c_host_id = ?", hostID).Take(&host).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Host{}, fmt.Errorf("%w: 主机 %s", ErrNotFound, hostID)
	}
	if err != nil {
		return Host{}, fmt.Errorf("读取主机 %s: %w", hostID, err)
	}
	return host, nil
}

// ListPlacements 按主机、组件筛选部署；空字符串表示不筛选。
func (s *Service) ListPlacements(ctx context.Context, hostID, componentID string) ([]Placement, error) {
	query := s.db.WithContext(ctx).Order("c_host_id, c_component_id")
	if hostID != "" {
		query = query.Where("c_host_id = ?", hostID)
	}
	if componentID != "" {
		query = query.Where("c_component_id = ?", componentID)
	}
	var placements []Placement
	if err := query.Find(&placements).Error; err != nil {
		return nil, fmt.Errorf("读取部署: %w", err)
	}
	return placements, nil
}

func validStatus(status string) bool { return status == StatusEnabled || status == StatusDisabled }

// SetHostStatus 启用或停用整台主机；control 主机受保护，不能停用。
func (s *Service) SetHostStatus(ctx context.Context, hostID, status string) (Host, error) {
	if !validStatus(status) {
		return Host{}, invalid("状态 %q 无效，可选 enabled、disabled", status)
	}
	var updated Host
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		st, err := loadState(tx)
		if err != nil {
			return err
		}
		host, ok := st.host(hostID)
		if !ok {
			return fmt.Errorf("%w: 主机 %s", ErrNotFound, hostID)
		}
		for i := range st.hosts {
			if st.hosts[i].HostID == hostID {
				st.hosts[i].Status = status
			}
		}
		if err := s.validate(st); err != nil {
			return err
		}
		now := s.now()
		if err := tx.Model(&Host{}).Where("c_host_id = ?", hostID).Updates(map[string]any{"c_status": status, "c_mtime": now}).Error; err != nil {
			return fmt.Errorf("更新主机 %s: %w", hostID, err)
		}
		host.Status, host.UpdatedAt = status, now
		updated = host
		return nil
	})
	return updated, err
}

// SetPlacementStatus 启用或停用一条部署；受保护的组件不能停用。「主机」范围的组件没有记录时自动补上。
func (s *Service) SetPlacementStatus(ctx context.Context, hostID, componentID, status string) (Placement, error) {
	if !validStatus(status) {
		return Placement{}, invalid("状态 %q 无效，可选 enabled、disabled", status)
	}
	var updated Placement
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		st, err := loadState(tx)
		if err != nil {
			return err
		}
		if _, ok := st.host(hostID); !ok {
			return fmt.Errorf("%w: 主机 %s", ErrNotFound, hostID)
		}
		component, known := s.catalog.Component(componentID)
		current, exists := st.placement(hostID, componentID)
		if !exists && !(known && component.Scope == servicecatalog.ScopeHost) {
			return fmt.Errorf("%w: 主机 %s 上没有组件 %s 的部署", ErrNotFound, hostID, componentID)
		}
		now := s.now()
		if !exists {
			current = Placement{HostID: hostID, ComponentID: componentID, CreatedAt: now}
			st.placements = append(st.placements, current)
		}
		for i := range st.placements {
			if st.placements[i].HostID == hostID && st.placements[i].ComponentID == componentID {
				st.placements[i].Status = status
			}
		}
		if err := s.validate(st); err != nil {
			return err
		}
		current.Status, current.UpdatedAt = status, now
		if !exists {
			if err := tx.Create(&current).Error; err != nil {
				return fmt.Errorf("写入部署 %s/%s: %w", hostID, componentID, err)
			}
		} else if err := tx.Model(&Placement{}).Where("c_host_id = ? AND c_component_id = ?", hostID, componentID).
			Updates(map[string]any{"c_status": status, "c_mtime": now}).Error; err != nil {
			return fmt.Errorf("更新部署 %s/%s: %w", hostID, componentID, err)
		}
		updated = current
		return nil
	})
	return updated, err
}

// SyncHostPlacements 按 moox.toml 同步一台主机及其完整组件列表（设计文档 3.8）：
// 整体校验后在一个事务里写入主机、补上缺少的部署（默认启用）、删除列表里已经没有的部署；
// 已有部署的启用状态保持不变。「主机」范围的组件由系统自动维护，不需要写进列表。
func (s *Service) SyncHostPlacements(ctx context.Context, spec HostSpec, components []string) (SyncResult, error) {
	spec.HostID = strings.TrimSpace(spec.HostID)
	spec.Address = strings.TrimSpace(spec.Address)
	if spec.HostID == "" || spec.Address == "" {
		return SyncResult{}, invalid("主机 ID 和地址不能为空")
	}
	wanted := map[string]bool{}
	for _, id := range components {
		id = strings.TrimSpace(id)
		component, ok := s.catalog.Component(id)
		if !ok {
			return SyncResult{}, invalid("组件 %q 不在组件目录中", id)
		}
		if component.Scope == servicecatalog.ScopeHost {
			return SyncResult{}, invalid("组件 %s 每台主机自动部署，不要写进部署表", id)
		}
		if wanted[id] {
			return SyncResult{}, invalid("组件 %s 重复", id)
		}
		wanted[id] = true
	}
	for _, id := range s.catalog.HostComponents() {
		wanted[id] = true
	}
	var result SyncResult
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		st, err := loadState(tx)
		if err != nil {
			return err
		}
		now := s.now()
		existingHost, hostExists := st.host(spec.HostID)
		host := Host{
			HostID: spec.HostID, Address: spec.Address, PrivateAddress: strings.TrimSpace(spec.PrivateAddress),
			Region: strings.TrimSpace(spec.Region), Description: strings.TrimSpace(spec.Description),
			Status: StatusEnabled, CreatedAt: now, UpdatedAt: now,
		}
		if hostExists {
			host.Status, host.CreatedAt = existingHost.Status, existingHost.CreatedAt
			for i := range st.hosts {
				if st.hosts[i].HostID == spec.HostID {
					st.hosts[i] = host
				}
			}
		} else {
			st.hosts = append(st.hosts, host)
		}
		next := st.placements[:0:0]
		var added, removed, kept []string
		for _, placement := range st.placements {
			if placement.HostID != spec.HostID {
				next = append(next, placement)
				continue
			}
			if wanted[placement.ComponentID] {
				next = append(next, placement)
				kept = append(kept, placement.ComponentID)
				continue
			}
			removed = append(removed, placement.ComponentID)
		}
		for _, id := range sortedKeys(wanted) {
			if _, exists := st.placement(spec.HostID, id); exists {
				continue
			}
			next = append(next, Placement{HostID: spec.HostID, ComponentID: id, Status: StatusEnabled, CreatedAt: now, UpdatedAt: now})
			added = append(added, id)
		}
		st.placements = next
		if err := s.validate(st); err != nil {
			return err
		}
		if hostExists {
			if err := tx.Model(&Host{}).Where("c_host_id = ?", spec.HostID).Updates(map[string]any{
				"c_address": host.Address, "c_private_address": host.PrivateAddress, "c_region": host.Region,
				"c_description": host.Description, "c_mtime": now,
			}).Error; err != nil {
				return fmt.Errorf("更新主机 %s: %w", spec.HostID, err)
			}
		} else if err := tx.Create(&host).Error; err != nil {
			return fmt.Errorf("写入主机 %s: %w", spec.HostID, err)
		}
		if len(removed) > 0 {
			if err := tx.Where("c_host_id = ? AND c_component_id IN ?", spec.HostID, removed).Delete(&Placement{}).Error; err != nil {
				return fmt.Errorf("删除部署: %w", err)
			}
		}
		for _, id := range added {
			row := Placement{HostID: spec.HostID, ComponentID: id, Status: StatusEnabled, CreatedAt: now, UpdatedAt: now}
			if err := tx.Create(&row).Error; err != nil {
				return fmt.Errorf("写入部署 %s/%s: %w", spec.HostID, id, err)
			}
		}
		sort.Strings(kept)
		result = SyncResult{HostCreated: !hostExists, Added: added, Removed: removed, Kept: kept}
		return nil
	})
	return result, err
}

// DeleteHost 删除一台主机：control 主机受保护；主机上仍有非「主机」范围的部署时拒绝。
func (s *Service) DeleteHost(ctx context.Context, hostID string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		st, err := loadState(tx)
		if err != nil {
			return err
		}
		if _, ok := st.host(hostID); !ok {
			return fmt.Errorf("%w: 主机 %s", ErrNotFound, hostID)
		}
		if hostID == servicecatalog.ControlHostID {
			return invalid("control 主机受保护，不能删除")
		}
		var remaining []string
		for _, placement := range st.placements {
			if placement.HostID != hostID {
				continue
			}
			if component, ok := s.catalog.Component(placement.ComponentID); ok && component.Scope == servicecatalog.ScopeHost {
				continue
			}
			remaining = append(remaining, placement.ComponentID)
		}
		if len(remaining) > 0 {
			return invalid("主机 %s 上仍有部署：%s；先从 moox.toml 的部署表中移除并同步", hostID, strings.Join(remaining, "、"))
		}
		if err := tx.Where("c_host_id = ?", hostID).Delete(&Placement{}).Error; err != nil {
			return fmt.Errorf("删除部署: %w", err)
		}
		if err := tx.Where("c_host_id = ?", hostID).Delete(&GatewayStatus{}).Error; err != nil {
			return fmt.Errorf("删除主机网关状态: %w", err)
		}
		if err := tx.Where("c_host_id = ?", hostID).Delete(&Host{}).Error; err != nil {
			return fmt.Errorf("删除主机: %w", err)
		}
		return nil
	})
}

// ListGatewayStatus 返回全部主机网关状态，按主机 ID 索引。
func (s *Service) ListGatewayStatus(ctx context.Context) (map[string]GatewayStatus, error) {
	var rows []GatewayStatus
	if err := s.db.WithContext(ctx).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("读取主机网关状态: %w", err)
	}
	out := make(map[string]GatewayStatus, len(rows))
	for _, row := range rows {
		out[row.HostID] = row
	}
	return out, nil
}

// SetExpectedHash 记录下发给主机网关的快照哈希，并据此维护「哈希不一致」的起始时间。
func (s *Service) SetExpectedHash(ctx context.Context, hostID, hash string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, err := loadGatewayStatus(tx, hostID)
		if err != nil {
			return err
		}
		if row.ExpectedHash == hash {
			return nil
		}
		row.ExpectedHash = hash
		s.updateMismatch(&row)
		return saveGatewayStatus(tx, row)
	})
}

// RecordGatewayReport 记录一次心跳，并区分实例替换与冲突（设计文档 3.8）：
//   - 实例 ID 变化记为替换，不告警；
//   - 5 分钟内被替换掉的旧实例又上报（A→B→A），记为冲突；
//   - 冲突在 5 分钟内没有再发生交替就清除。
func (s *Service) RecordGatewayReport(ctx context.Context, report GatewayReport) (GatewayStatus, error) {
	if strings.TrimSpace(report.InstanceID) == "" {
		return GatewayStatus{}, invalid("心跳缺少实例 ID")
	}
	var saved GatewayStatus
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, err := loadGatewayStatus(tx, report.HostID)
		if err != nil {
			return err
		}
		now := s.now()
		switch {
		case row.InstanceID == "" || row.InstanceID == report.InstanceID:
			row.InstanceID = report.InstanceID
		case report.InstanceID == row.PreviousInstanceID && row.ReplacedAt != nil && now.Sub(*row.ReplacedAt) < AlternationWindow:
			conflictAt := now
			row.ConflictInstanceID, row.ConflictSeenAt = row.InstanceID, &conflictAt
			row.PreviousInstanceID, row.InstanceID = row.InstanceID, report.InstanceID
			replacedAt := now
			row.ReplacedAt = &replacedAt
		default:
			row.PreviousInstanceID, row.InstanceID = row.InstanceID, report.InstanceID
			replacedAt := now
			row.ReplacedAt = &replacedAt
		}
		if row.ConflictSeenAt != nil && now.Sub(*row.ConflictSeenAt) >= AlternationWindow {
			row.ConflictInstanceID, row.ConflictSeenAt = "", nil
		}
		seenAt := now
		row.Version, row.AppliedHash, row.RouteCount, row.LastError, row.LastSeenAt =
			report.Version, report.AppliedHash, report.RouteCount, report.LastError, &seenAt
		if report.CertificateNotAfter != nil {
			notAfter := report.CertificateNotAfter.UTC()
			row.CertificateNotAfter = &notAfter
		}
		s.updateMismatch(&row)
		if err := saveGatewayStatus(tx, row); err != nil {
			return err
		}
		saved = row
		return nil
	})
	return saved, err
}

func (s *Service) updateMismatch(row *GatewayStatus) {
	if row.ExpectedHash == "" || row.AppliedHash == row.ExpectedHash {
		row.MismatchSince = nil
		return
	}
	if row.MismatchSince == nil {
		since := s.now()
		row.MismatchSince = &since
	}
}

func loadGatewayStatus(tx *gorm.DB, hostID string) (GatewayStatus, error) {
	var host Host
	err := tx.Where("c_host_id = ?", hostID).Take(&host).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return GatewayStatus{}, fmt.Errorf("%w: 主机 %s", ErrNotFound, hostID)
	}
	if err != nil {
		return GatewayStatus{}, fmt.Errorf("读取主机 %s: %w", hostID, err)
	}
	var row GatewayStatus
	err = tx.Where("c_host_id = ?", hostID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return GatewayStatus{HostID: hostID}, nil
	}
	if err != nil {
		return GatewayStatus{}, fmt.Errorf("读取主机网关状态 %s: %w", hostID, err)
	}
	return row, nil
}

func saveGatewayStatus(tx *gorm.DB, row GatewayStatus) error {
	if err := tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&row).Error; err != nil {
		return fmt.Errorf("写入主机网关状态 %s: %w", row.HostID, err)
	}
	return nil
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
