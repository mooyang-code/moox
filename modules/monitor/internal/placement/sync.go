// Package placement 按组件目录和 SysDeploy 的部署生成健康检查：每条部署对应一个检查 placement:<主机>:<组件>，
// 探测方式取自组件目录的 health.kind。
package placement

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/client"
)

const (
	// checkPrefix 是部署检查 ID 的前缀。
	checkPrefix = "placement:"
	// controlHostID 是 Monitor 所在的主机；探测本机组件走回环地址。
	controlHostID = servicecatalog.ControlHostID
	// StatusEnabled 是 SysDeploy 中启用的主机和部署的状态。
	StatusEnabled = "enabled"
)

// CheckID 返回部署对应的健康检查 ID。
func CheckID(hostID, componentID string) string {
	return checkPrefix + strings.TrimSpace(hostID) + ":" + strings.TrimSpace(componentID)
}

// ParseCheckID 从健康检查 ID 解析出主机和组件；不是部署检查时 ok 为 false。
func ParseCheckID(checkID string) (hostID, componentID string, ok bool) {
	rest, found := strings.CutPrefix(strings.TrimSpace(checkID), checkPrefix)
	if !found {
		return "", "", false
	}
	hostID, componentID, found = strings.Cut(rest, ":")
	return hostID, componentID, found && hostID != "" && componentID != ""
}

// Labels 是部署检查携带的标签。
type Labels struct {
	HostID      string `json:"host_id"`
	ComponentID string `json:"component_id"`
}

// ParseLabels 解析部署检查的标签。
func ParseLabels(raw string) (Labels, error) {
	var labels Labels
	if err := json.Unmarshal([]byte(raw), &labels); err != nil {
		return Labels{}, err
	}
	labels.HostID, labels.ComponentID = strings.TrimSpace(labels.HostID), strings.TrimSpace(labels.ComponentID)
	if labels.HostID == "" || labels.ComponentID == "" {
		return Labels{}, errors.New("host_id 和 component_id 不能为空")
	}
	return labels, nil
}

// Source 读取 SysDeploy 的主机与部署。
type Source interface {
	Hosts(context.Context) ([]*adminpb.DeployHost, error)
	Placements(context.Context) ([]*adminpb.DeployPlacement, error)
}

type sysDeployClient interface {
	ListHosts(context.Context, *adminpb.ListDeployHostsReq, ...client.Option) (*adminpb.ListDeployHostsRsp, error)
	ListPlacements(context.Context, *adminpb.ListPlacementsReq, ...client.Option) (*adminpb.ListPlacementsRsp, error)
}

// ClientSource 经 gatewayclient（monitor 身份）调用 SysDeploy。
type ClientSource struct {
	client sysDeployClient
}

// NewClientSource 用给定的 tRPC 客户端选项创建来源。
func NewClientSource(options []client.Option) *ClientSource {
	return &ClientSource{client: adminpb.NewSysDeployClientProxy(options...)}
}

// Hosts 返回全部主机。
func (s *ClientSource) Hosts(ctx context.Context) ([]*adminpb.DeployHost, error) {
	rsp, err := s.client.ListHosts(ctx, &adminpb.ListDeployHostsReq{})
	if err != nil {
		return nil, fmt.Errorf("读取主机列表失败: %w", err)
	}
	if code := rsp.GetRetInfo().GetCode(); code != commonpb.ErrorCode_SUCCESS {
		return nil, fmt.Errorf("读取主机列表失败（%s）: %s", code, rsp.GetRetInfo().GetMsg())
	}
	return rsp.GetHosts(), nil
}

// Placements 返回全部部署。
func (s *ClientSource) Placements(ctx context.Context) ([]*adminpb.DeployPlacement, error) {
	rsp, err := s.client.ListPlacements(ctx, &adminpb.ListPlacementsReq{})
	if err != nil {
		return nil, fmt.Errorf("读取部署列表失败: %w", err)
	}
	if code := rsp.GetRetInfo().GetCode(); code != commonpb.ErrorCode_SUCCESS {
		return nil, fmt.Errorf("读取部署列表失败（%s）: %s", code, rsp.GetRetInfo().GetMsg())
	}
	return rsp.GetPlacements(), nil
}

// Syncer 让部署检查与 SysDeploy 的部署一致。
type Syncer struct {
	checks  *store.CheckRepository
	source  Source
	catalog *servicecatalog.Catalog
}

// NewSyncer 创建同步器；catalog 为空时使用内置的组件目录。
func NewSyncer(checks *store.CheckRepository, source Source, catalog *servicecatalog.Catalog) *Syncer {
	if catalog == nil {
		catalog = servicecatalog.Default()
	}
	return &Syncer{checks: checks, source: source, catalog: catalog}
}

// Sync 读取 SysDeploy 的主机和部署并同步检查，返回变化的检查数。
func (s *Syncer) Sync(ctx context.Context) (int, error) {
	if s.source == nil {
		return 0, nil
	}
	hosts, err := s.source.Hosts(ctx)
	if err != nil {
		return 0, err
	}
	placements, err := s.source.Placements(ctx)
	if err != nil {
		return 0, err
	}
	return s.SyncPlacements(ctx, hosts, placements)
}

// SyncPlacements 同步检查：启用的部署（主机也启用）生成并启用检查；停用的部署或停用的主机上的部署停用检查；
// 已删除的部署删除检查及其结果和告警规则。不探测的组件不生成检查。
func (s *Syncer) SyncPlacements(ctx context.Context, hosts []*adminpb.DeployHost, placements []*adminpb.DeployPlacement) (int, error) {
	byHost := make(map[string]*adminpb.DeployHost, len(hosts))
	for _, host := range hosts {
		if host != nil {
			byHost[strings.TrimSpace(host.GetHostId())] = host
		}
	}
	changed := 0
	wanted := map[string]struct{}{}
	var definitionErrors []error
	for _, placement := range placements {
		if placement == nil {
			continue
		}
		host := byHost[strings.TrimSpace(placement.GetHostId())]
		check, err := s.checkFor(host, placement)
		if err != nil {
			definitionErrors = append(definitionErrors, err)
			// 定义暂时无效时保留原有的检查，不当作已删除。
			wanted[CheckID(placement.GetHostId(), placement.GetComponentId())] = struct{}{}
			continue
		}
		if check == nil {
			continue
		}
		wanted[check.CheckID] = struct{}{}
		updated, err := s.upsert(ctx, check)
		if err != nil {
			return changed, err
		}
		if updated {
			changed++
		}
	}
	deleted, err := s.deleteUnwanted(ctx, wanted)
	changed += deleted
	if err != nil {
		return changed, err
	}
	return changed, errors.Join(definitionErrors...)
}

// checkFor 生成部署对应的检查；不探测的组件返回 nil。
func (s *Syncer) checkFor(host *adminpb.DeployHost, placement *adminpb.DeployPlacement) (*domain.Check, error) {
	hostID, componentID := strings.TrimSpace(placement.GetHostId()), strings.TrimSpace(placement.GetComponentId())
	component, ok := s.catalog.Component(componentID)
	if !ok {
		return nil, fmt.Errorf("部署 %s@%s 的组件不在组件目录中", componentID, hostID)
	}
	if component.Health.Kind == servicecatalog.HealthNone {
		return nil, nil
	}
	if host == nil {
		return nil, fmt.Errorf("部署 %s@%s 的主机不存在", componentID, hostID)
	}
	labels, _ := json.Marshal(Labels{HostID: hostID, ComponentID: componentID})
	check := &domain.Check{
		CheckID:         CheckID(hostID, componentID),
		Name:            component.Name + "（" + hostID + "）· 健康检查",
		GroupName:       "mooxsys",
		Kind:            domain.CheckKindHTTP,
		Method:          "GET",
		Headers:         "{}",
		IntervalSeconds: 30,
		TimeoutMS:       3000,
		Enabled:         placement.GetStatus() == StatusEnabled && host.GetStatus() == StatusEnabled,
		Source:          domain.CheckSourcePlacement,
		Labels:          string(labels),
		Description: fmt.Sprintf("确认主机 %s 上的 %s（%s）在运行，健康端口 %d 可以从 control 访问",
			hostID, component.Name, component.Binary, component.Health.Port),
	}
	url, ok := HealthURL(host, *component)
	if !ok {
		return nil, fmt.Errorf("部署 %s@%s 没有可探测的健康地址（主机没有地址）", componentID, hostID)
	}
	check.URL = url
	switch component.Health.Kind {
	case servicecatalog.HealthReadyz:
		check.ExpectedStatus = "200-299"
		check.BodyContains = `"ready":true`
	case servicecatalog.HealthHTTPS:
		check.ExpectedStatus = "200-399"
	}
	return check, nil
}

// HealthURL 返回 Monitor 探测部署时使用的地址：readyz 方式请求健康端口的 /readyz，control 上的组件走回环地址；
// https 方式请求主机公网地址的根路径（证书签给公网地址，control 也一样）。不探测或主机没有地址时 ok 为 false。
func HealthURL(host *adminpb.DeployHost, component servicecatalog.Component) (string, bool) {
	address := strings.TrimSpace(host.GetAddress())
	port := strconv.Itoa(component.Health.Port)
	switch component.Health.Kind {
	case servicecatalog.HealthReadyz:
		if host.GetHostId() == controlHostID {
			address = "127.0.0.1"
		}
		if address == "" {
			return "", false
		}
		return "http://" + net.JoinHostPort(address, port) + "/readyz", true
	case servicecatalog.HealthHTTPS:
		if address == "" {
			return "", false
		}
		return "https://" + net.JoinHostPort(address, port) + "/", true
	default:
		return "", false
	}
}

// upsert 写入检查，返回是否有变化。
func (s *Syncer) upsert(ctx context.Context, check *domain.Check) (bool, error) {
	existing, err := s.checks.Get(ctx, check.SpaceID, check.CheckID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return true, s.checks.Create(ctx, check)
		}
		return false, err
	}
	if sameDefinition(existing, check) {
		return false, nil
	}
	return true, s.checks.UpdatePlacementDefinition(ctx, check)
}

func sameDefinition(existing *domain.Check, check *domain.Check) bool {
	return existing.Source == check.Source && existing.Name == check.Name && existing.URL == check.URL &&
		existing.ExpectedStatus == check.ExpectedStatus && existing.BodyContains == check.BodyContains &&
		existing.Enabled == check.Enabled && existing.Labels == check.Labels && existing.Description == check.Description &&
		existing.IntervalSeconds == check.IntervalSeconds && existing.TimeoutMS == check.TimeoutMS
}

// legacyCheckSource 是旧版按服务部署记录生成的检查的来源。这些检查不再有调度方，也没有人维护，
// 留在库里会一直占着检查数，其中处于告警中的状态永远不会恢复，所以同步时一并清理。
const legacyCheckSource = "sysdeploy"

// deleteUnwanted 删除已经没有部署的检查和旧版遗留的检查，返回删除的数量。
func (s *Syncer) deleteUnwanted(ctx context.Context, wanted map[string]struct{}) (int, error) {
	existing, err := s.checks.ListBySource(ctx, domain.CheckSourcePlacement)
	if err != nil {
		return 0, err
	}
	legacy, err := s.checks.ListBySource(ctx, legacyCheckSource)
	if err != nil {
		return 0, err
	}
	deleted := 0
	for _, check := range append(existing, legacy...) {
		if check.Source == domain.CheckSourcePlacement {
			if _, ok := wanted[check.CheckID]; ok {
				continue
			}
		}
		if err := s.checks.DeleteWithRules(ctx, check.SpaceID, check.CheckID); err != nil {
			return deleted, err
		}
		deleted++
	}
	return deleted, nil
}
