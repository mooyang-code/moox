package client

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const (
	setupRemoteAddress     = "127.0.0.1:11110"
	sysDeployRemoteAddress = "127.0.0.1:11109"
	maxResponseBytes       = 1 << 20
)

var storageDeploymentNames = []string{
	"storage-primary", "storage-view",
}

type Forwarder interface {
	ForwardLocal(context.Context, string) (net.Listener, error)
}

type Client struct {
	forwarder Forwarder
	timeout   time.Duration
}

type Space struct {
	SpaceID        string `json:"space_id"`
	Name           string `json:"name"`
	Description    string `json:"description"`
	Owner          string `json:"owner"`
	Market         string `json:"market"`
	Timezone       string `json:"timezone"`
	Status         string `json:"status"`
	AttributesJSON string `json:"attributes_json"`
}

type ApplyResult struct {
	Action          string `json:"action"`
	Users           int    `json:"users"`
	Secrets         int    `json:"secrets"`
	Hosts           int    `json:"hosts"`
	Spaces          int    `json:"spaces"`
	SpacesCreated   int    `json:"spaces_created"`
	SpacesUnchanged int    `json:"spaces_unchanged"`
}

type StatusResult struct {
	State     string `json:"state"`
	Users     int    `json:"users"`
	Secrets   int    `json:"secrets"`
	Hosts     int    `json:"hosts"`
	Spaces    int    `json:"spaces"`
	Missing   int    `json:"missing"`
	Conflicts int    `json:"conflicts"`
}

type StoragePlacementResult struct {
	Deployments int `json:"deployments"`
}

func New(forwarder Forwarder) *Client {
	return &Client{forwarder: forwarder, timeout: 30 * time.Second}
}

func (c *Client) Apply(ctx context.Context, snapshot *setupconfig.Snapshot) (ApplyResult, error) {
	return c.ApplyWithSpaces(ctx, snapshot, nil)
}

func (c *Client) ApplyWithSpaces(
	ctx context.Context,
	snapshot *setupconfig.Snapshot,
	spaces []Space,
) (ApplyResult, error) {
	if snapshot == nil || c.forwarder == nil {
		return ApplyResult{}, fmt.Errorf("setup_client_invalid")
	}
	if err := snapshot.VerifyUnchanged(); err != nil {
		return ApplyResult{}, fmt.Errorf("config_changed")
	}
	request := applyRequest(snapshot.Manifest, spaces)
	response := &pb.ApplySetupRsp{}
	if err := c.forwardedPost(ctx, "ApplySetup", request, response); err != nil {
		return ApplyResult{}, err
	}
	if err := snapshot.VerifyUnchanged(); err != nil {
		return ApplyResult{}, fmt.Errorf("config_changed")
	}
	if err := checkRetInfo(response.GetRetInfo()); err != nil {
		return ApplyResult{}, err
	}
	return ApplyResult{
		Action: response.GetAction(), Users: int(response.GetUsers()),
		Secrets: int(response.GetSecrets()), Hosts: int(response.GetHosts()),
		Spaces: int(response.GetSpaces()), SpacesCreated: int(response.GetSpacesCreated()),
		SpacesUnchanged: int(response.GetSpacesUnchanged()),
	}, nil
}

func (c *Client) Status(ctx context.Context, snapshot *setupconfig.Snapshot) (StatusResult, error) {
	return c.StatusWithSpaces(ctx, snapshot, nil)
}

func (c *Client) StatusWithSpaces(
	ctx context.Context,
	snapshot *setupconfig.Snapshot,
	spaces []Space,
) (StatusResult, error) {
	if snapshot == nil || c.forwarder == nil {
		return StatusResult{}, fmt.Errorf("setup_client_invalid")
	}
	if err := snapshot.VerifyUnchanged(); err != nil {
		return StatusResult{}, fmt.Errorf("config_changed")
	}
	request := statusRequest(snapshot.Manifest, spaces)
	response := &pb.GetSetupStatusRsp{}
	if err := c.forwardedPost(ctx, "GetSetupStatus", request, response); err != nil {
		return StatusResult{}, err
	}
	if err := snapshot.VerifyUnchanged(); err != nil {
		return StatusResult{}, fmt.Errorf("config_changed")
	}
	if err := checkRetInfo(response.GetRetInfo()); err != nil {
		return StatusResult{}, err
	}
	return StatusResult{
		State: response.GetState(), Users: int(response.GetUsers()), Secrets: int(response.GetSecrets()),
		Hosts: int(response.GetHosts()), Spaces: int(response.GetSpaces()),
		Missing: int(response.GetMissing()), Conflicts: int(response.GetConflicts()),
	}, nil
}

func (c *Client) ApplyStoragePlacement(ctx context.Context, nodeID, host string) (StoragePlacementResult, error) {
	if _, err := c.PrepareStoragePlacement(ctx, nodeID, host); err != nil {
		return StoragePlacementResult{}, err
	}
	// The HTTP gateway remains on control for CloudNode and other control-plane
	// RPCs. Only the native Storage ingress moves to the Storage host.
	getResponse := &pb.GetServiceDeploymentRsp{}
	if err := c.forwardedPostTo(ctx, sysDeployRemoteAddress, "trpc.moox.ops.SysDeploy", "GetServiceDeployment",
		&pb.GetServiceDeploymentReq{NodeId: "control", ServiceName: "service_gateway_native"}, getResponse); err != nil || checkRetInfo(getResponse.GetRetInfo()) != nil || getResponse.GetDeployment() == nil {
		return StoragePlacementResult{}, fmt.Errorf("storage_placement_failed")
	}
	deployment := proto.Clone(getResponse.GetDeployment()).(*pb.ServiceDeployment)
	deployment.Host = strings.TrimSpace(host)
	if err := c.upsertDeployment(ctx, deployment); err != nil {
		return StoragePlacementResult{}, fmt.Errorf("storage_placement_failed")
	}
	return StoragePlacementResult{Deployments: len(storageDeploymentNames)}, nil
}

// ActivateStoragePlacement enables the local Storage routes after a control
// host receives the separately managed Storage package. Initial control-plane
// setup intentionally marks these deployments disabled when Storage has not
// been installed yet; leaving them disabled means the native gateway cannot
// resolve PrimaryStore/Metadata requests even though the processes are ready.
func (c *Client) ActivateStoragePlacement(ctx context.Context, nodeID string) (StoragePlacementResult, error) {
	nodeID = strings.TrimSpace(nodeID)
	if c == nil || c.forwarder == nil || nodeID == "" {
		return StoragePlacementResult{}, fmt.Errorf("storage_placement_invalid")
	}
	for _, serviceName := range storageDeploymentNames {
		getResponse := &pb.GetServiceDeploymentRsp{}
		if err := c.forwardedPostTo(ctx, sysDeployRemoteAddress, "trpc.moox.ops.SysDeploy", "GetServiceDeployment",
			&pb.GetServiceDeploymentReq{NodeId: nodeID, ServiceName: serviceName}, getResponse); err != nil {
			return StoragePlacementResult{}, fmt.Errorf("storage_placement_failed")
		}
		if err := checkRetInfo(getResponse.GetRetInfo()); err != nil || getResponse.GetDeployment() == nil {
			return StoragePlacementResult{}, fmt.Errorf("storage_placement_failed")
		}
		deployment := proto.Clone(getResponse.GetDeployment()).(*pb.ServiceDeployment)
		deployment.Status = "active"
		deployment.GatewayEnabled = true
		if err := c.upsertDeployment(ctx, deployment); err != nil {
			return StoragePlacementResult{}, fmt.Errorf("storage_placement_failed")
		}
	}
	return StoragePlacementResult{Deployments: len(storageDeploymentNames)}, nil
}

// PrepareStoragePlacement creates the remote Gateway node and its local
// routes before the Gateway starts. It deliberately leaves SCF discovery on
// the current native endpoint until Storage readiness has succeeded.
func (c *Client) PrepareStoragePlacement(ctx context.Context, nodeID, host string) (StoragePlacementResult, error) {
	nodeID, host = strings.TrimSpace(nodeID), strings.TrimSpace(host)
	if c == nil || c.forwarder == nil || nodeID == "" || host == "" {
		return StoragePlacementResult{}, fmt.Errorf("storage_placement_invalid")
	}
	if err := c.ensureGatewayNode(ctx, nodeID, host); err != nil {
		return StoragePlacementResult{}, fmt.Errorf("storage_placement_failed")
	}
	for _, serviceName := range storageDeploymentNames {
		getResponse := &pb.GetServiceDeploymentRsp{}
		if err := c.forwardedPostTo(ctx, sysDeployRemoteAddress, "trpc.moox.ops.SysDeploy", "GetServiceDeployment",
			&pb.GetServiceDeploymentReq{NodeId: "control", ServiceName: serviceName}, getResponse); err != nil {
			return StoragePlacementResult{}, fmt.Errorf("storage_placement_failed")
		}
		if err := checkRetInfo(getResponse.GetRetInfo()); err != nil || getResponse.GetDeployment() == nil {
			return StoragePlacementResult{}, fmt.Errorf("storage_placement_failed")
		}
		deployment := proto.Clone(getResponse.GetDeployment()).(*pb.ServiceDeployment)
		deployment.Id = 0
		deployment.NodeId = nodeID
		// Each remote Gateway reaches its local Storage processes through loopback.
		// The public address belongs only to the Gateway node, not to its routes.
		deployment.Host = "127.0.0.1"
		if err := c.upsertDeployment(ctx, deployment); err != nil {
			return StoragePlacementResult{}, fmt.Errorf("storage_placement_failed")
		}
	}
	return StoragePlacementResult{Deployments: len(storageDeploymentNames)}, nil
}

func (c *Client) ensureGatewayNode(ctx context.Context, nodeID, host string) error {
	response := &pb.ListGatewayNodesRsp{}
	if err := c.forwardedPostTo(ctx, sysDeployRemoteAddress, "trpc.moox.ops.SysDeploy", "ListGatewayNodes", &pb.ListGatewayNodesReq{NodeId: nodeID}, response); err != nil || checkRetInfo(response.GetRetInfo()) != nil {
		return fmt.Errorf("gateway_node_lookup_failed")
	}
	node := &pb.GatewayNode{NodeId: nodeID, Name: nodeID, PublicAddress: "https://" + net.JoinHostPort(host, "11001"), Status: "enabled"}
	if len(response.GetNodes()) == 0 {
		created := &pb.CreateGatewayNodeRsp{}
		if err := c.forwardedPostTo(ctx, sysDeployRemoteAddress, "trpc.moox.ops.SysDeploy", "CreateGatewayNode", &pb.CreateGatewayNodeReq{Node: node}, created); err != nil || checkRetInfo(created.GetRetInfo()) != nil {
			return fmt.Errorf("gateway_node_create_failed")
		}
		return nil
	}
	updated := &pb.UpdateGatewayNodeRsp{}
	if err := c.forwardedPostTo(ctx, sysDeployRemoteAddress, "trpc.moox.ops.SysDeploy", "UpdateGatewayNode", &pb.UpdateGatewayNodeReq{NodeId: nodeID, Node: node}, updated); err != nil || checkRetInfo(updated.GetRetInfo()) != nil {
		return fmt.Errorf("gateway_node_update_failed")
	}
	return nil
}

func (c *Client) upsertDeployment(ctx context.Context, deployment *pb.ServiceDeployment) error {
	getResponse := &pb.GetServiceDeploymentRsp{}
	err := c.forwardedPostTo(ctx, sysDeployRemoteAddress, "trpc.moox.ops.SysDeploy", "GetServiceDeployment", &pb.GetServiceDeploymentReq{NodeId: deployment.GetNodeId(), ServiceName: deployment.GetServiceName()}, getResponse)
	if err == nil && checkRetInfo(getResponse.GetRetInfo()) == nil {
		response := &pb.UpdateServiceDeploymentRsp{}
		if err := c.forwardedPostTo(ctx, sysDeployRemoteAddress, "trpc.moox.ops.SysDeploy", "UpdateServiceDeployment", &pb.UpdateServiceDeploymentReq{NodeId: deployment.GetNodeId(), ServiceName: deployment.GetServiceName(), Deployment: deployment}, response); err != nil || checkRetInfo(response.GetRetInfo()) != nil {
			return fmt.Errorf("deployment_update_failed")
		}
		return nil
	}
	response := &pb.CreateServiceDeploymentRsp{}
	if err := c.forwardedPostTo(ctx, sysDeployRemoteAddress, "trpc.moox.ops.SysDeploy", "CreateServiceDeployment", &pb.CreateServiceDeploymentReq{Deployment: deployment}, response); err != nil || checkRetInfo(response.GetRetInfo()) != nil {
		return fmt.Errorf("deployment_create_failed")
	}
	return nil
}

func (c *Client) forwardedPost(ctx context.Context, method string, request, response proto.Message) error {
	return c.forwardedPostTo(ctx, setupRemoteAddress, "trpc.moox.admin.Setup", method, request, response)
}

func (c *Client) forwardedPostTo(ctx context.Context, remoteAddress, service, method string, request, response proto.Message) error {
	forwardContext, cancel := context.WithCancel(ctx)
	defer cancel()
	listener, err := c.forwarder.ForwardLocal(forwardContext, remoteAddress)
	if err != nil {
		return fmt.Errorf("setup_not_reachable")
	}
	defer listener.Close()
	endpoint := "http://" + listener.Addr().String() + "/" + service + "/" + method
	return postProto(ctx, endpoint, request, response, c.timeout)
}

func postProto(ctx context.Context, endpoint string, request, response proto.Message, timeout time.Duration) error {
	raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(request)
	if err != nil {
		return fmt.Errorf("setup_request_invalid")
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("setup_request_invalid")
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpClient := &http.Client{Timeout: timeout}
	httpResponse, err := httpClient.Do(httpRequest)
	if err != nil {
		return fmt.Errorf("setup_not_reachable")
	}
	defer httpResponse.Body.Close()
	if httpResponse.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(httpResponse.Body, maxResponseBytes))
		return fmt.Errorf("setup_remote_failed")
	}
	body, err := io.ReadAll(io.LimitReader(httpResponse.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes {
		return fmt.Errorf("setup_response_invalid")
	}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(body, response); err != nil {
		return fmt.Errorf("setup_response_invalid")
	}
	return nil
}

func checkRetInfo(retInfo *pb.RetInfo) error {
	if retInfo == nil {
		return fmt.Errorf("setup_response_invalid")
	}
	if retInfo.GetCode() == pb.ErrorCode_SUCCESS {
		return nil
	}
	switch strings.TrimSpace(retInfo.GetMsg()) {
	case "setup_conflict", "setup_invalid", "setup_storage_failed":
		return fmt.Errorf("%s", retInfo.GetMsg())
	default:
		return fmt.Errorf("setup_remote_failed")
	}
}

func applyRequest(manifest setupconfig.Manifest, spaces []Space) *pb.ApplySetupReq {
	return &pb.ApplySetupReq{
		Admin:        &pb.SetupAdmin{Username: manifest.Admin.Username, Password: manifest.Admin.Password},
		TencentCloud: &pb.SetupTencentCloud{SecretId: manifest.TencentCloud.SecretID, SecretKey: manifest.TencentCloud.SecretKey},
		ControlHost:  hostToPB(manifest.ControlHost),
		OtherHosts:   hostsToPB(manifest.OtherHosts),
		Spaces:       spacesToPB(spaces),
	}
}

func statusRequest(manifest setupconfig.Manifest, spaces []Space) *pb.GetSetupStatusReq {
	return &pb.GetSetupStatusReq{
		Admin:        &pb.SetupAdmin{Username: manifest.Admin.Username, Password: manifest.Admin.Password},
		TencentCloud: &pb.SetupTencentCloud{SecretId: manifest.TencentCloud.SecretID, SecretKey: manifest.TencentCloud.SecretKey},
		ControlHost:  hostToPB(manifest.ControlHost),
		OtherHosts:   hostsToPB(manifest.OtherHosts),
		Spaces:       spacesToPB(spaces),
	}
}

func spacesToPB(spaces []Space) []*pb.SetupSpace {
	result := make([]*pb.SetupSpace, 0, len(spaces))
	for _, space := range spaces {
		result = append(result, &pb.SetupSpace{
			SpaceId: space.SpaceID, Name: space.Name, Description: space.Description,
			Owner: space.Owner, Market: space.Market, Timezone: space.Timezone,
			Status: space.Status, AttributesJson: space.AttributesJSON,
		})
	}
	return result
}

func hostsToPB(hosts []setupconfig.Host) []*pb.SetupHost {
	result := make([]*pb.SetupHost, 0, len(hosts))
	for _, host := range hosts {
		result = append(result, hostToPB(host))
	}
	return result
}

func hostToPB(host setupconfig.Host) *pb.SetupHost {
	return &pb.SetupHost{
		Name: host.Name, Address: host.Address, Port: int32(host.Port),
		Username: host.Username, Password: host.Password,
	}
}
