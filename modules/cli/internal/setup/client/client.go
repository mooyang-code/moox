package client

import (
	"context"
	"fmt"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/errs"
)

const (
	maxResponseBytes       = 1 << 20
	secretMgrService       = "trpc.moox.ops.SecretMgr"
	tencentDefaultSecretID = "tencent-default"
)

type Client struct {
	gateway gatewayclient.Invoker
}

type ApplyResult struct {
	Action   string `json:"action"`
	SecretID string `json:"secret_id,omitempty"`
	// Kept 是与 moox.secrets.toml 不一致、以管理台为准保留的项（tencent_cloud）。
	Kept []string `json:"kept,omitempty"`
}

func New(gateway gatewayclient.Invoker) *Client {
	return &Client{gateway: gateway}
}

// Apply 把秘密文件里的腾讯云默认密钥经 SecretMgr.EnsureInitialSecret 写入管理台，规则为只建不改。
func (c *Client) Apply(ctx context.Context, secrets *topology.Secrets) (ApplyResult, error) {
	if secrets == nil || c.gateway == nil {
		return ApplyResult{}, fmt.Errorf("setup_client_invalid")
	}
	request := ensureInitialSecretRequest(secrets)
	response := &pb.EnsureInitialSecretRsp{}
	if err := c.invoke(ctx, secretMgrService, "EnsureInitialSecret", request, response); err != nil {
		return ApplyResult{}, err
	}
	if err := checkRetInfo(response.GetRetInfo()); err != nil {
		return ApplyResult{}, err
	}
	result := ApplyResult{
		Action:   response.GetAction(),
		SecretID: response.GetSecretId(),
	}
	if result.Action == "kept" {
		result.Kept = []string{"tencent_cloud"}
	}
	return result, nil
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

func checkRetInfo(retInfo *commonpb.RetInfo) error {
	if retInfo == nil {
		return fmt.Errorf("setup_response_invalid")
	}
	if retInfo.GetCode() == commonpb.ErrorCode_SUCCESS {
		return nil
	}
	if retInfo.GetCode() == commonpb.ErrorCode_INVALID_PARAM {
		return fmt.Errorf("setup_invalid")
	}
	return fmt.Errorf("setup_remote_failed")
}

func ensureInitialSecretRequest(manifest *topology.Secrets) *pb.EnsureInitialSecretReq {
	return &pb.EnsureInitialSecretReq{
		Secret: &pb.Secret{
			SecretId:    tencentDefaultSecretID,
			Name:        "Tencent Cloud Default",
			Description: "默认腾讯云账号管理密钥（由 setup init 写入）",
			Category:    "cloud",
			Provider:    "tencent",
			SecretType:  "api_key",
			KeyId:       manifest.TencentCloud.SecretID,
			SecretValue: manifest.TencentCloud.SecretKey,
			ExtraConfig: "{}",
			Status:      "active",
			Creator:     "setup",
		},
	}
}
