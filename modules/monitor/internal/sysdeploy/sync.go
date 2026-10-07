package sysdeploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/monitor/internal/alerttext"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/doctor"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/log"
)

var errAdminUnavailable = errors.New("admin sysdeploy unavailable")

type Source interface {
	DesiredDeployments(context.Context) ([]*adminpb.ServiceDeployment, error)
	// NodeHosts maps each node to the host monitor reaches it on.
	NodeHosts(context.Context) (map[string]string, error)
}

type deploymentClient interface {
	ListServiceDeployments(context.Context, *adminpb.ListServiceDeploymentsReq, ...client.Option) (*adminpb.ListServiceDeploymentsRsp, error)
	ListGatewayNodes(context.Context, *adminpb.ListGatewayNodesReq, ...client.Option) (*adminpb.ListGatewayNodesRsp, error)
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

// NodeHosts reads every node's public host from its gateway node record.
func (s *ClientSource) NodeHosts(ctx context.Context) (map[string]string, error) {
	const pageSize = 100
	const maxNodes = 500
	hosts := make(map[string]string)
	for page := uint32(1); page <= maxNodes/pageSize; page++ {
		rsp, err := s.client.ListGatewayNodes(ctx, &adminpb.ListGatewayNodesReq{Page: &commonpb.Page{Page: page, Size: pageSize}})
		if err != nil {
			return nil, err
		}
		if rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
			return nil, fmt.Errorf("%w: %s", errAdminUnavailable, rsp.GetRetInfo().GetMsg())
		}
		for _, node := range rsp.GetNodes() {
			if host := publicHost(node.GetPublicAddress()); host != "" {
				hosts[strings.TrimSpace(node.GetNodeId())] = host
			}
		}
		if !rsp.GetPageResult().GetHasMore() {
			return hosts, nil
		}
	}
	return nil, fmt.Errorf("sysdeploy returned more than %d gateway nodes", maxNodes)
}

// publicHost extracts the host of a gateway public address, which may be a
// URL ("https://host:port") or a bare host.
func publicHost(address string) string {
	address = strings.TrimSpace(address)
	if address == "" {
		return ""
	}
	if !strings.Contains(address, "://") {
		address = "//" + address
	}
	parsed, err := url.Parse(address)
	if err != nil {
		return ""
	}
	return parsed.Hostname()
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
	nodeHosts, err := s.source.NodeHosts(ctx)
	if err != nil {
		return 0, err
	}
	return s.SyncDeployments(ctx, deployments, nodeHosts)
}

// SyncDeployments reconciles the sysdeploy checks. A health endpoint a remote
// node exposes on loopback is probed on that node's host from nodeHosts.
func (s *Syncer) SyncDeployments(ctx context.Context, deployments []*adminpb.ServiceDeployment, nodeHosts map[string]string) (int, error) {
	manifest, err := doctor.LoadEmbeddedManifest()
	if err != nil {
		return 0, err
	}
	processes := make(map[string]bool, len(manifest.Components))
	for _, component := range manifest.Components {
		processes[component.ServiceName] = true
	}
	synced := 0
	var definitionErrors []error
	activeIDs := map[string]struct{}{}
	for _, deployment := range deployments {
		if deployment == nil || !processes[deployment.GetServiceName()] {
			continue
		}
		check, err := checkFromDeployment(deployment, nodeHosts)
		if err != nil {
			if deployment.GetStatus() == "active" && strings.TrimSpace(deployment.GetNodeId()) != "" && strings.TrimSpace(deployment.GetServiceName()) != "" {
				// Keep a previously valid check from being auto-disabled merely
				// because this deployment's current health metadata is invalid.
				activeIDs[sysDeployCheckID(deployment.GetNodeId(), deployment.GetServiceName())] = struct{}{}
			}
			// A malformed deployment, or a remote one whose node has no known
			// host, must not prevent valid services from being registered in
			// the monitor store. Keep the error for observability, but continue
			// reconciling the remaining rows.
			definitionErrors = append(definitionErrors, err)
			continue
		}
		if check == nil {
			continue
		}
		activeIDs[check.CheckID] = struct{}{}
		existing, err := s.checks.Get(ctx, check.SpaceID, check.CheckID)
		if err == nil {
			if existing.Source != domain.CheckSourceSysDeploy {
				continue
			}
			if err := s.checks.UpdateSysDeployDefinition(ctx, check); err != nil {
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
	for _, definitionErr := range definitionErrors {
		log.WarnContextf(ctx, "monitor sysdeploy definition skipped: %v", definitionErr)
	}
	if len(definitionErrors) > 0 && synced == 0 {
		return synced, errors.Join(definitionErrors...)
	}
	return synced, nil
}

type extraConfig struct {
	HealthURL          string `json:"health_url"`
	HealthKind         string `json:"health_kind"`
	HealthBodyContains string `json:"health_body_contains"`
	MonitorEnabled     *bool  `json:"monitor_enabled"`
}

func checkFromDeployment(deployment *adminpb.ServiceDeployment, nodeHosts map[string]string) (*domain.Check, error) {
	if deployment == nil || deployment.GetStatus() != "active" ||
		strings.TrimSpace(deployment.GetNodeId()) == "" ||
		strings.TrimSpace(deployment.GetServiceName()) == "" {
		return nil, nil
	}
	nodeID := strings.TrimSpace(deployment.GetNodeId())
	serviceName := strings.TrimSpace(deployment.GetServiceName())
	extra := parseExtra(deployment.GetExtraConfig())
	if extra.MonitorEnabled != nil && !*extra.MonitorEnabled {
		return nil, nil
	}
	labels, _ := json.Marshal(map[string]string{
		"node_id":      nodeID,
		"service_name": serviceName,
	})
	check := &domain.Check{
		CheckID:         sysDeployCheckID(nodeID, serviceName),
		Name:            alerttext.Service(serviceName) + "（" + alerttext.Node(nodeID) + "）· 健康检查",
		GroupName:       "mooxsys",
		IntervalSeconds: 30,
		TimeoutMS:       3000,
		ExpectedStatus:  "200-299",
		Enabled:         true,
		Source:          domain.CheckSourceSysDeploy,
		Labels:          string(labels),
		Description:     deployment.GetDescription(),
		Method:          "GET",
		Headers:         "{}",
	}
	if strings.TrimSpace(extra.HealthURL) != "" {
		healthURL, err := resolveHealthURL(extra.HealthURL, nodeID, nodeHosts)
		if err != nil {
			return nil, fmt.Errorf("sysdeploy %s@%s health URL: %w", serviceName, nodeID, err)
		}
		check.Kind = domain.CheckKindHTTP
		check.URL = healthURL
		kind := strings.ToLower(strings.TrimSpace(extra.HealthKind))
		if kind == "" {
			if strings.HasSuffix(strings.TrimRight(check.URL, "/"), "/healthz") {
				kind = "liveness"
			} else {
				kind = "readiness"
			}
		}
		if kind == "readiness" || kind == "ready" {
			check.BodyContains = strings.TrimSpace(extra.HealthBodyContains)
			if check.BodyContains == "" {
				check.BodyContains = `"ready":true`
			}
		}
		return check, nil
	}
	if deployment.GetProtocol() == "http" && deployment.GetHost() != "" && deployment.GetPort() > 0 {
		host, err := resolveHost(deployment.GetHost(), nodeID, nodeHosts)
		if err != nil {
			return nil, fmt.Errorf("sysdeploy %s@%s TCP host: %w", serviceName, nodeID, err)
		}
		check.Kind = domain.CheckKindTCP
		check.TCPHost = host
		check.TCPPort = int(deployment.GetPort())
		return check, nil
	}
	return nil, nil
}

func sysDeployCheckID(nodeID, serviceName string) string {
	return "sysdeploy:" + strings.TrimSpace(nodeID) + ":" + strings.TrimSpace(serviceName)
}

// resolveHealthURL validates a health URL and points a remote node's loopback
// endpoint at that node's host, keeping the port and path.
func resolveHealthURL(raw, nodeID string, nodeHosts map[string]string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", errors.New("must be an absolute HTTP URL")
	}
	host, err := resolveHost(parsed.Hostname(), nodeID, nodeHosts)
	if err != nil {
		return "", err
	}
	if port := parsed.Port(); port != "" {
		parsed.Host = net.JoinHostPort(host, port)
	} else {
		parsed.Host = host
	}
	return parsed.String(), nil
}

// resolveHost keeps a routable host, and replaces loopback on a remote node,
// which monitor cannot reach, with that node's host.
func resolveHost(host, nodeID string, nodeHosts map[string]string) (string, error) {
	if nodeID == "control" || !isLoopbackHost(host) {
		return host, nil
	}
	if resolved := nodeHosts[nodeID]; resolved != "" {
		return resolved, nil
	}
	return "", fmt.Errorf("loopback on node %s, whose host is unknown", nodeID)
}

func isLoopbackHost(host string) bool {
	host = strings.TrimSpace(host)
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func parseExtra(raw string) extraConfig {
	var extra extraConfig
	_ = json.Unmarshal([]byte(raw), &extra)
	return extra
}
