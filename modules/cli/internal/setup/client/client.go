// Package client 经 gatewayclient 以 moox-cli 身份调用 Admin：初始化（Setup）与部署记录同步（SysDeploy）。
package client

import (
	"context"
	"fmt"
	"strings"
	"time"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/gatewayroute"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/errs"
)

const (
	setupService     = "trpc.moox.admin.Setup"
	sysDeployService = "trpc.moox.ops.SysDeploy"
)

// Invoker 是 setup 客户端用到的 gatewayclient 能力。
type Invoker interface {
	Invoke(ctx context.Context, servicePath, method string, req, rsp any, opts ...gatewayclient.CallOption) error
}

// Client 经 gatewayclient 以 moox-cli 身份调用 Admin 的 Setup 和 SysDeploy。
type Client struct {
	gateway Invoker
	timeout time.Duration
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

// New 创建 setup 客户端；单次调用超时与组件目录中 Setup 的超时一致。
func New(gateway Invoker) *Client {
	return &Client{gateway: gateway, timeout: 2 * time.Minute}
}

func (c *Client) Apply(ctx context.Context, snapshot *setupconfig.Snapshot) (ApplyResult, error) {
	return c.ApplyWithSpaces(ctx, snapshot, nil)
}

// ApplyWithSpaces 写入初始用户、云凭据、SSH 主机和业务空间。
func (c *Client) ApplyWithSpaces(ctx context.Context, snapshot *setupconfig.Snapshot, spaces []Space) (ApplyResult, error) {
	if snapshot == nil || c.gateway == nil {
		return ApplyResult{}, fmt.Errorf("setup_client_invalid")
	}
	if err := snapshot.VerifyUnchanged(); err != nil {
		return ApplyResult{}, fmt.Errorf("config_changed")
	}
	response := &pb.ApplySetupRsp{}
	if err := c.call(ctx, setupService, "ApplySetup", applyRequest(snapshot.Manifest, spaces), response); err != nil {
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

// StatusWithSpaces 检查初始化记录是否与 moox.toml 一致。
func (c *Client) StatusWithSpaces(ctx context.Context, snapshot *setupconfig.Snapshot, spaces []Space) (StatusResult, error) {
	if snapshot == nil || c.gateway == nil {
		return StatusResult{}, fmt.Errorf("setup_client_invalid")
	}
	if err := snapshot.VerifyUnchanged(); err != nil {
		return StatusResult{}, fmt.Errorf("config_changed")
	}
	response := &pb.GetSetupStatusRsp{}
	if err := c.call(ctx, setupService, "GetSetupStatus", statusRequest(snapshot.Manifest, spaces), response); err != nil {
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

// SyncHostPlacements 按部署表同步一台主机及其完整组件列表（SysDeploy.SyncHostPlacements）：缺少的部署补上并启用，
// 列表里已经没有的部署删除，已有部署的启用状态保持不变。
func (c *Client) SyncHostPlacements(ctx context.Context, host setupconfig.Host, components []string) error {
	request := &pb.SyncHostPlacementsReq{
		Host: &pb.DeployHostSpec{
			HostId: host.ID, Address: host.Address, PrivateAddress: host.PrivateAddress, Region: host.Region,
		},
		Components: append([]string{}, components...),
	}
	response := &pb.SyncHostPlacementsRsp{}
	if err := c.call(ctx, sysDeployService, "SyncHostPlacements", request, response); err != nil {
		return err
	}
	if response.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		return fmt.Errorf("同步部署记录被拒绝：%s", response.GetRetInfo().GetMsg())
	}
	return nil
}

// call 以 PB 序列化经 gatewayclient 调用 service/method。错误只返回固定的错误码，不带远端细节，
// 避免把请求中的口令等内容带进输出。
func (c *Client) call(ctx context.Context, service, method string, request, response proto.Message) error {
	if c == nil || c.gateway == nil {
		return fmt.Errorf("setup_not_reachable")
	}
	if err := c.gateway.Invoke(ctx, service, method, request, response, gatewayclient.WithTimeout(c.timeout)); err != nil {
		switch errs.Code(err) {
		case errs.RetClientConnectFail, errs.RetClientNetErr, errs.RetClientTimeout, errs.RetClientFullLinkTimeout,
			gatewayroute.RetServiceNotHere, gatewayroute.RetHostDisabled:
			return fmt.Errorf("setup_not_reachable")
		default:
			return fmt.Errorf("setup_remote_failed")
		}
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
	control, others := setupHosts(manifest)
	return &pb.ApplySetupReq{
		Admin:        &pb.SetupAdmin{Username: manifest.Admin.Username, Password: manifest.Admin.Password},
		TencentCloud: &pb.SetupTencentCloud{SecretId: manifest.TencentCloud.SecretID, SecretKey: manifest.TencentCloud.SecretKey},
		ControlHost:  control,
		OtherHosts:   others,
		Spaces:       spacesToPB(spaces),
	}
}

func statusRequest(manifest setupconfig.Manifest, spaces []Space) *pb.GetSetupStatusReq {
	control, others := setupHosts(manifest)
	return &pb.GetSetupStatusReq{
		Admin:        &pb.SetupAdmin{Username: manifest.Admin.Username, Password: manifest.Admin.Password},
		TencentCloud: &pb.SetupTencentCloud{SecretId: manifest.TencentCloud.SecretID, SecretKey: manifest.TencentCloud.SecretKey},
		ControlHost:  control,
		OtherHosts:   others,
		Spaces:       spacesToPB(spaces),
	}
}

// setupHosts 把 moox.toml 的主机写成管理台 SSH 主机：control 一台，其余按主机 ID 排序。
func setupHosts(manifest setupconfig.Manifest) (*pb.SetupHost, []*pb.SetupHost) {
	var control *pb.SetupHost
	others := []*pb.SetupHost{}
	for _, host := range manifest.HostList() {
		item := &pb.SetupHost{
			Name: host.ID, Address: host.Address, Port: int32(host.SSH.Port),
			Username: host.SSH.Username, Password: host.SSH.Password,
		}
		if host.ID == servicecatalog.ControlHostID {
			control = item
		} else {
			others = append(others, item)
		}
	}
	return control, others
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
