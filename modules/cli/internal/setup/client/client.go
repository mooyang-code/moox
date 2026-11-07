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

// 交易所密钥在 SecretMgr 里的状态，与 Admin 的 setup 服务一致。
const (
	ExchangeKeyCreated = "created" // 本次新建
	ExchangeKeyExists  = "exists"  // 已存在，以管理台为准，不覆盖
	ExchangeKeyDeleted = "deleted" // 已在管理台删除，不重建
	ExchangeKeyMissing = "missing" // 还没有（只出现在状态查询里）
)

// ExchangeKeyResult 是一把交易所密钥（[[exchange_keys]]）的处理结果，不含密钥值。
type ExchangeKeyResult struct {
	SecretID string `json:"secret_id"`
	State    string `json:"state"`
	ReadOnly bool   `json:"readonly,omitempty"`
	Note     string `json:"note,omitempty"`
}

// exchangeKeyNotes 把状态翻成给人看的说明。交易所密钥只建不改，所以“已存在”和“已删除”都要讲清楚为什么没动。
var exchangeKeyNotes = map[string]string{
	ExchangeKeyCreated: "已写入密钥管理",
	ExchangeKeyExists:  "已存在，以管理台为准，未覆盖",
	ExchangeKeyDeleted: "已在管理台删除，未重建",
	ExchangeKeyMissing: "密钥管理里还没有",
}

type ApplyResult struct {
	Action          string              `json:"action"`
	Users           int                 `json:"users"`
	Secrets         int                 `json:"secrets"`
	Spaces          int                 `json:"spaces"`
	SpacesCreated   int                 `json:"spaces_created"`
	SpacesUnchanged int                 `json:"spaces_unchanged"`
	ExchangeKeys    []ExchangeKeyResult `json:"exchange_keys,omitempty"`
}

type StatusResult struct {
	State        string              `json:"state"`
	Users        int                 `json:"users"`
	Secrets      int                 `json:"secrets"`
	Spaces       int                 `json:"spaces"`
	Missing      int                 `json:"missing"`
	Conflicts    int                 `json:"conflicts"`
	ExchangeKeys []ExchangeKeyResult `json:"exchange_keys,omitempty"`
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
	if err := checkExchangeKeysAnswered(snapshot.Manifest, response.GetExchangeKeys()); err != nil {
		return ApplyResult{}, err
	}
	return ApplyResult{
		Action: response.GetAction(), Users: int(response.GetUsers()),
		Secrets: int(response.GetSecrets()),
		Spaces:  int(response.GetSpaces()), SpacesCreated: int(response.GetSpacesCreated()),
		SpacesUnchanged: int(response.GetSpacesUnchanged()),
		ExchangeKeys:    exchangeKeyResults(response.GetExchangeKeys()),
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
	if err := checkExchangeKeysAnswered(snapshot.Manifest, response.GetExchangeKeys()); err != nil {
		return StatusResult{}, err
	}
	return StatusResult{
		State: response.GetState(), Users: int(response.GetUsers()), Secrets: int(response.GetSecrets()),
		Spaces:  int(response.GetSpaces()),
		Missing: int(response.GetMissing()), Conflicts: int(response.GetConflicts()),
		ExchangeKeys: exchangeKeyResults(response.GetExchangeKeys()),
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
		Spaces:       spacesToPB(spaces),
		ExchangeKeys: exchangeKeysToPB(manifest.ExchangeKeys),
	}
}

func statusRequest(manifest setupconfig.Manifest, spaces []Space) *pb.GetSetupStatusReq {
	return &pb.GetSetupStatusReq{
		Admin:        &pb.SetupAdmin{Username: manifest.Admin.Username, Password: manifest.Admin.Password},
		TencentCloud: &pb.SetupTencentCloud{SecretId: manifest.TencentCloud.SecretID, SecretKey: manifest.TencentCloud.SecretKey},
		Spaces:       spacesToPB(spaces),
		ExchangeKeys: exchangeKeysToPB(manifest.ExchangeKeys),
	}
}

func exchangeKeysToPB(keys []setupconfig.ExchangeKey) []*pb.SetupExchangeKey {
	result := make([]*pb.SetupExchangeKey, 0, len(keys))
	for _, key := range keys {
		result = append(result, &pb.SetupExchangeKey{
			Name: key.Name, Exchange: key.Exchange, Environment: key.Environment,
			ApiKey: key.APIKey, SecretKey: key.SecretKey, Passphrase: key.Passphrase, Readonly: key.ReadOnly,
		})
	}
	return result
}

// checkExchangeKeysAnswered 防止新 CLI 配没升级的 Admin：proto3 会静默丢掉不认识的字段，旧的 Admin 照常返回成功却一把交易所密钥都没写。
// moox.toml 里有交易所密钥而响应里没有逐把结果（条数对不上）时报错，提示先升级管理后台。
func checkExchangeKeysAnswered(manifest setupconfig.Manifest, answered []*pb.SetupExchangeKeyResult) error {
	if len(manifest.ExchangeKeys) > 0 && len(answered) != len(manifest.ExchangeKeys) {
		return fmt.Errorf("setup_admin_outdated: moox.toml 里有 %d 把交易所密钥，管理后台只返回了 %d 把的处理结果：它可能还是不认识 [[exchange_keys]] 的旧版本，没有写入任何密钥；请先升级管理后台（Admin）再运行 apply 或 init", len(manifest.ExchangeKeys), len(answered))
	}
	return nil
}

func exchangeKeyResults(items []*pb.SetupExchangeKeyResult) []ExchangeKeyResult {
	var results []ExchangeKeyResult
	for _, item := range items {
		results = append(results, ExchangeKeyResult{
			SecretID: item.GetSecretId(), State: item.GetState(), ReadOnly: item.GetReadonly(), Note: exchangeKeyNotes[item.GetState()],
		})
	}
	return results
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
