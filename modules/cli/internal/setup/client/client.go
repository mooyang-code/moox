package client

import (
	"context"
	"fmt"
	"strings"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/errs"
)

const (
	maxResponseBytes      = 1 << 20
	TradeGatewayHTTPSPort = 11001
)

type Client struct {
	gateway gatewayclient.Invoker
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

func New(gateway gatewayclient.Invoker) *Client {
	return &Client{gateway: gateway}
}

func (c *Client) Apply(ctx context.Context, snapshot *setupconfig.Snapshot) (ApplyResult, error) {
	return c.ApplyWithSpaces(ctx, snapshot, nil)
}

func (c *Client) ApplyWithSpaces(
	ctx context.Context,
	snapshot *setupconfig.Snapshot,
	spaces []Space,
) (ApplyResult, error) {
	if snapshot == nil || c.gateway == nil {
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
	if snapshot == nil || c.gateway == nil {
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

func (c *Client) forwardedPost(ctx context.Context, method string, request, response proto.Message) error {
	return c.invoke(ctx, "trpc.moox.admin.Setup", method, request, response)
}

func (c *Client) invoke(ctx context.Context, service, method string, request, response proto.Message) error {
	if c == nil || c.gateway == nil {
		return fmt.Errorf("setup requires the operator's SSH gateway client")
	}
	if err := c.gateway.Invoke(ctx, service, method, request, response); err != nil {
		if errs.Code(err) == errs.RetClientDecodeFail {
			return fmt.Errorf("setup_response_invalid")
		}
		return fmt.Errorf("setup_not_reachable")
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
		ControlHost:  hostToPB(manifest.ControlHost()),
		OtherHosts:   hostsToPB(manifest.OtherHosts()),
		Spaces:       spacesToPB(spaces),
	}
}

func statusRequest(manifest setupconfig.Manifest, spaces []Space) *pb.GetSetupStatusReq {
	return &pb.GetSetupStatusReq{
		Admin:        &pb.SetupAdmin{Username: manifest.Admin.Username, Password: manifest.Admin.Password},
		TencentCloud: &pb.SetupTencentCloud{SecretId: manifest.TencentCloud.SecretID, SecretKey: manifest.TencentCloud.SecretKey},
		ControlHost:  hostToPB(manifest.ControlHost()),
		OtherHosts:   hostsToPB(manifest.OtherHosts()),
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
