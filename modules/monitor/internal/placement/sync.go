package placement

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"time"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/packages/servicecatalog"
)

type Syncer struct {
	checks   *store.CheckRepository
	source   Source
	https    map[string]domain.HTTPSConfig
	gateways *store.GatewayRepository
	now      func() time.Time
}

func NewSyncer(checks *store.CheckRepository, source Source, https map[string]domain.HTTPSConfig, gateways *store.GatewayRepository) *Syncer {
	return &Syncer{checks: checks, source: source, https: https, gateways: gateways, now: time.Now}
}

func CheckID(hostID, componentID string) string { return "placement:" + hostID + ":" + componentID }

func (s *Syncer) Sync(ctx context.Context) (int, error) {
	if s == nil || s.source == nil || s.checks == nil {
		return 0, fmt.Errorf("placement synchronization is not initialized")
	}
	snapshot, err := s.source.Snapshot(ctx)
	if err != nil {
		return 0, err
	}
	checks, err := Checks(snapshot, s.https)
	if err != nil {
		return 0, err
	}
	count, err := s.checks.ReconcilePlacements(ctx, checks)
	if err != nil {
		return count, err
	}
	return count, s.syncGateways(ctx, snapshot)
}

// Checks validates the complete discovery result before any database mutation.
// Disabled definitions remain registered; deleted deployments and health:none
// have no check. A page probe never replaces the proxy process's readiness.
func Checks(snapshot Snapshot, https map[string]domain.HTTPSConfig) ([]domain.Check, error) {
	if err := snapshot.Catalog.Validate(); err != nil {
		return nil, err
	}
	for id, target := range https {
		component, ok := snapshot.Catalog.Component(id)
		if !ok || (id != "console-proxy" && component.Health.Kind != "https") {
			return nil, fmt.Errorf("HTTPS probe references unsupported component %q", id)
		}
		if err := target.Validate(); err != nil {
			return nil, fmt.Errorf("HTTPS probe %s: %w", id, err)
		}
	}
	hosts := make(map[string]*adminpb.DeploymentHost, len(snapshot.Hosts))
	for _, host := range snapshot.Hosts {
		if !servicecatalog.ValidHostID(host.GetHostId()) || hosts[host.GetHostId()] != nil || !validStatus(host.GetStatus()) || !servicecatalog.ValidHostAddress(host.GetAddress()) || (host.GetPrivateAddress() != "" && !servicecatalog.ValidHostAddress(host.GetPrivateAddress())) {
			return nil, fmt.Errorf("invalid or duplicate deployment host %q", host.GetHostId())
		}
		hosts[host.GetHostId()] = host
	}
	seen := map[string]bool{}
	var checks []domain.Check
	for _, placement := range snapshot.Placements {
		host := hosts[placement.GetHostId()]
		component, ok := snapshot.Catalog.Component(placement.GetComponentId())
		id := CheckID(placement.GetHostId(), placement.GetComponentId())
		if host == nil || !ok || !validStatus(placement.GetStatus()) || seen[id] {
			return nil, fmt.Errorf("invalid or duplicate placement %q", id)
		}
		seen[id] = true
		if component.ID == "console-proxy" {
			if _, ok := https[component.ID]; !ok {
				return nil, fmt.Errorf("console-proxy requires an explicit HTTPS page probe")
			}
		}
		if component.Health.Kind == "none" {
			continue
		}
		check := baseCheck(host, placement, component)
		switch component.Health.Kind {
		case "readyz":
			address := host.GetPrivateAddress()
			if address == "" {
				address = host.GetAddress()
			}
			if component.Health.Loopback {
				address = "127.0.0.1"
			}
			check.URL = (&url.URL{Scheme: "http", Host: net.JoinHostPort(address, strconv.Itoa(component.Health.Port)), Path: "/readyz"}).String()
			check.BodyContains = component.Health.ReadyBody
		case "https":
			target, ok := https[component.ID]
			if !ok {
				return nil, fmt.Errorf("component %s requires explicit HTTPS probe configuration", component.ID)
			}
			applyHTTPS(&check, target)
		}
		checks = append(checks, check)
		if target, ok := https[component.ID]; ok && component.ID == "console-proxy" {
			page := baseCheck(host, placement, component)
			page.CheckID = "console-page:" + host.GetHostId() + ":" + component.ID
			page.Name = "控制台 HTTPS 页面（" + host.GetHostId() + "）"
			page.GroupName = "console_page"
			page.Labels = labels(host.GetHostId(), component.ID, "console_page")
			applyHTTPS(&page, target)
			checks = append(checks, page)
		}
	}
	return checks, nil
}

func baseCheck(host *adminpb.DeploymentHost, placement *adminpb.ComponentPlacement, component servicecatalog.Component) domain.Check {
	return domain.Check{
		CheckID: CheckID(host.GetHostId(), component.ID), Name: component.Name + "（" + host.GetHostId() + "）· 健康检查",
		GroupName: "components", Kind: domain.CheckKindHTTP, Method: "GET", Headers: "{}",
		IntervalSeconds: 30, TimeoutMS: 3000, ExpectedStatus: "200-299",
		Enabled: host.GetStatus() == servicecatalog.Enabled && placement.GetStatus() == servicecatalog.Enabled,
		Source:  domain.CheckSourcePlacement, Labels: labels(host.GetHostId(), component.ID, "component"), Description: component.Doctor.Description,
	}
}

func labels(hostID, componentID, checkType string) string {
	raw, _ := json.Marshal(map[string]string{"host_id": hostID, "component_id": componentID, "check_type": checkType})
	return string(raw)
}

func applyHTTPS(check *domain.Check, target domain.HTTPSConfig) {
	check.URL, check.ConnectAddress, check.ServerName = target.URL, target.ConnectAddress, target.ServerName
	check.TrustMode, check.CAFile, check.CABaseline = target.TrustMode, target.CAFile, target.CABaseline
	check.ExpectedStatus = "200-399"
}

func validStatus(status string) bool {
	return status == servicecatalog.Enabled || status == servicecatalog.Disabled
}
