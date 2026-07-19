package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
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

type ApplyResult struct {
	Action  string `json:"action"`
	Users   int    `json:"users"`
	Secrets int    `json:"secrets"`
	Hosts   int    `json:"hosts"`
}

type StatusResult struct {
	State     string `json:"state"`
	Users     int    `json:"users"`
	Secrets   int    `json:"secrets"`
	Hosts     int    `json:"hosts"`
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
	if snapshot == nil || c.forwarder == nil {
		return ApplyResult{}, fmt.Errorf("setup_client_invalid")
	}
	if err := snapshot.VerifyUnchanged(); err != nil {
		return ApplyResult{}, fmt.Errorf("config_changed")
	}
	request := applyRequest(snapshot.Manifest)
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
	}, nil
}

func (c *Client) Status(ctx context.Context, snapshot *setupconfig.Snapshot) (StatusResult, error) {
	if snapshot == nil || c.forwarder == nil {
		return StatusResult{}, fmt.Errorf("setup_client_invalid")
	}
	if err := snapshot.VerifyUnchanged(); err != nil {
		return StatusResult{}, fmt.Errorf("config_changed")
	}
	request := statusRequest(snapshot.Manifest)
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
		Hosts: int(response.GetHosts()), Missing: int(response.GetMissing()), Conflicts: int(response.GetConflicts()),
	}, nil
}

func (c *Client) ApplyStoragePlacement(ctx context.Context, host string) (StoragePlacementResult, error) {
	host = strings.TrimSpace(host)
	if c == nil || c.forwarder == nil || host == "" {
		return StoragePlacementResult{}, fmt.Errorf("storage_placement_invalid")
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
		deployment.Host = host
		extraConfig, err := storageExtraConfigForHost(deployment.GetExtraConfig(), host)
		if err != nil {
			return StoragePlacementResult{}, fmt.Errorf("storage_placement_failed")
		}
		deployment.ExtraConfig = extraConfig
		updateResponse := &pb.UpdateServiceDeploymentRsp{}
		if err := c.forwardedPostTo(ctx, sysDeployRemoteAddress, "trpc.moox.ops.SysDeploy", "UpdateServiceDeployment",
			&pb.UpdateServiceDeploymentReq{NodeId: "control", ServiceName: serviceName, Deployment: deployment}, updateResponse); err != nil {
			return StoragePlacementResult{}, fmt.Errorf("storage_placement_failed")
		}
		if err := checkRetInfo(updateResponse.GetRetInfo()); err != nil {
			return StoragePlacementResult{}, fmt.Errorf("storage_placement_failed")
		}
	}
	return StoragePlacementResult{Deployments: len(storageDeploymentNames)}, nil
}

func storageExtraConfigForHost(raw, host string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return raw, nil
	}
	var extra map[string]any
	if err := json.Unmarshal([]byte(raw), &extra); err != nil {
		return "", err
	}
	healthURL, ok := extra["health_url"].(string)
	if !ok || strings.TrimSpace(healthURL) == "" {
		return raw, nil
	}
	parsed, err := url.Parse(healthURL)
	if err != nil {
		return "", err
	}
	_, port, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		return "", err
	}
	parsed.Host = net.JoinHostPort(host, port)
	extra["health_url"] = parsed.String()
	encoded, err := json.Marshal(extra)
	return string(encoded), err
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

func applyRequest(manifest setupconfig.Manifest) *pb.ApplySetupReq {
	return &pb.ApplySetupReq{
		Admin:        &pb.SetupAdmin{Username: manifest.Admin.Username, Password: manifest.Admin.Password},
		TencentCloud: &pb.SetupTencentCloud{SecretId: manifest.TencentCloud.SecretID, SecretKey: manifest.TencentCloud.SecretKey},
		ControlHost:  hostToPB(manifest.ControlHost),
		OtherHosts:   hostsToPB(manifest.OtherHosts),
	}
}

func statusRequest(manifest setupconfig.Manifest) *pb.GetSetupStatusReq {
	return &pb.GetSetupStatusReq{
		Admin:        &pb.SetupAdmin{Username: manifest.Admin.Username, Password: manifest.Admin.Password},
		TencentCloud: &pb.SetupTencentCloud{SecretId: manifest.TencentCloud.SecretID, SecretKey: manifest.TencentCloud.SecretKey},
		ControlHost:  hostToPB(manifest.ControlHost),
		OtherHosts:   hostsToPB(manifest.OtherHosts),
	}
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
