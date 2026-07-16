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
	setupRemoteAddress = "127.0.0.1:11110"
	maxResponseBytes   = 1 << 20
)

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

func (c *Client) forwardedPost(ctx context.Context, method string, request, response proto.Message) error {
	forwardContext, cancel := context.WithCancel(ctx)
	defer cancel()
	listener, err := c.forwarder.ForwardLocal(forwardContext, setupRemoteAddress)
	if err != nil {
		return fmt.Errorf("setup_not_reachable")
	}
	defer listener.Close()
	endpoint := "http://" + listener.Addr().String() + "/trpc.moox.admin.Setup/" + method
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
