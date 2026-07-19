package sysdeploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/doctor"
	"trpc.group/trpc-go/trpc-go/client"
)

var errAdminUnavailable = errors.New("admin sysdeploy unavailable")

type Source interface {
	DesiredDeployments(context.Context) ([]*adminpb.ServiceDeployment, error)
}

type deploymentClient interface {
	ListServiceDeployments(context.Context, *adminpb.ListServiceDeploymentsReq, ...client.Option) (*adminpb.ListServiceDeploymentsRsp, error)
}

type ClientSource struct {
	client deploymentClient
}

func NewClientSource(target string) *ClientSource {
	return &ClientSource{client: adminpb.NewSysDeployClientProxy(
		client.WithTarget(target),
		client.WithProtocol("http"),
		client.WithNetwork("tcp"),
	)}
}

func (s *ClientSource) DesiredDeployments(ctx context.Context) ([]*adminpb.ServiceDeployment, error) {
	const pageSize = 100
	const maxDeployments = 500
	deployments := make([]*adminpb.ServiceDeployment, 0, pageSize)
	for page := uint32(1); page <= maxDeployments/pageSize; page++ {
		rsp, err := s.client.ListServiceDeployments(ctx, &adminpb.ListServiceDeploymentsReq{Page: &commonpb.Page{Page: page, Size: pageSize}})
		if err != nil {
			return nil, err
		}
		if rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
			return nil, fmt.Errorf("%w: %s", errAdminUnavailable, rsp.GetRetInfo().GetMsg())
		}
		if len(deployments)+len(rsp.GetDeployments()) > maxDeployments {
			return nil, fmt.Errorf("sysdeploy returned more than %d deployments", maxDeployments)
		}
		deployments = append(deployments, rsp.GetDeployments()...)
		if !rsp.GetPageResult().GetHasMore() {
			return deployments, nil
		}
	}
	return nil, fmt.Errorf("sysdeploy returned more than %d deployments", maxDeployments)
}

type Syncer struct {
	checks *store.CheckRepository
	source Source
}

func NewSyncer(checks *store.CheckRepository, source Source) *Syncer {
	return &Syncer{checks: checks, source: source}
}

func (s *Syncer) Sync(ctx context.Context) (int, error) {
	if s.source == nil {
		return 0, nil
	}
	deployments, err := s.source.DesiredDeployments(ctx)
	if err != nil {
		return 0, err
	}
	return s.SyncDeployments(ctx, deployments)
}

func (s *Syncer) SyncDeployments(ctx context.Context, deployments []*adminpb.ServiceDeployment) (int, error) {
	manifest, err := doctor.LoadEmbeddedManifest()
	if err != nil {
		return 0, err
	}
	processes := make(map[string]bool, len(manifest.Components))
	for _, component := range manifest.Components {
		processes[component.ServiceName] = true
	}
	synced := 0
	activeIDs := map[string]struct{}{}
	for _, deployment := range deployments {
		if deployment == nil || !processes[deployment.GetServiceName()] {
			continue
		}
		check, ok := checkFromDeployment(deployment)
		if !ok {
			continue
		}
		activeIDs[check.CheckID] = struct{}{}
		existing, err := s.checks.Get(ctx, check.SpaceID, check.CheckID)
		if err == nil {
			if existing.Source != domain.CheckSourceSysDeploy {
				continue
			}
			if err := s.checks.Update(ctx, check); err != nil {
				return synced, err
			}
			synced++
			continue
		}
		if err := s.checks.Create(ctx, check); err != nil {
			return synced, err
		}
		synced++
	}
	disabled, err := s.checks.DisableSysDeployChecksExcept(ctx, "", activeIDs)
	if err != nil {
		return synced, err
	}
	synced += int(disabled)
	return synced, nil
}

type extraConfig struct {
	HealthURL      string `json:"health_url"`
	HealthKind     string `json:"health_kind"`
	MonitorEnabled *bool  `json:"monitor_enabled"`
}

func checkFromDeployment(deployment *adminpb.ServiceDeployment) (*domain.Check, bool) {
	if deployment == nil || deployment.GetStatus() != "active" || deployment.GetServiceName() == "" {
		return nil, false
	}
	extra := parseExtra(deployment.GetExtraConfig())
	if extra.MonitorEnabled != nil && !*extra.MonitorEnabled {
		return nil, false
	}
	check := &domain.Check{
		CheckID:         deployment.GetServiceName(),
		Name:            deployment.GetServiceName(),
		GroupName:       "moox-system",
		IntervalSeconds: 30,
		TimeoutMS:       3000,
		ExpectedStatus:  "200-299",
		Enabled:         true,
		Source:          domain.CheckSourceSysDeploy,
		Labels:          "{}",
		Description:     deployment.GetDescription(),
		Method:          "GET",
		Headers:         "{}",
	}
	if strings.TrimSpace(extra.HealthURL) != "" {
		check.Kind = domain.CheckKindHTTP
		check.URL = strings.TrimSpace(extra.HealthURL)
		kind := strings.ToLower(strings.TrimSpace(extra.HealthKind))
		if kind == "" {
			if strings.HasSuffix(strings.TrimRight(check.URL, "/"), "/healthz") {
				kind = "liveness"
			} else {
				kind = "readiness"
			}
		}
		if kind == "readiness" || kind == "ready" {
			check.BodyContains = `"ready":true`
		}
		return check, true
	}
	if deployment.GetProtocol() == "http" && deployment.GetHost() != "" && deployment.GetPort() > 0 {
		check.Kind = domain.CheckKindTCP
		check.TCPHost = deployment.GetHost()
		check.TCPPort = int(deployment.GetPort())
		return check, true
	}
	return nil, false
}

func parseExtra(raw string) extraConfig {
	var extra extraConfig
	_ = json.Unmarshal([]byte(raw), &extra)
	return extra
}
