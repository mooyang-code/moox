package rpcclient

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// 云账号、代码包与云函数（CollectMgr）：Collector 直接调腾讯云，密钥本身在 Admin 的 SecretMgr，这里只登记密钥 ID。

// packageUploadTimeout 是代码包上传到 COS 的超时：跨地域上传慢但正常时有余量，COS 不响应时不会无限挂住。
const packageUploadTimeout = 2 * time.Minute

var newPackageUploadHTTPClient = func() *http.Client {
	// 腾讯云 COS 偶尔在 TLS 握手时用明文 HTTP 回应 HTTP/2 请求，Go 默认传输会报
	// “server gave HTTP response to HTTPS client”。上传固定走 HTTP/1.1，地址仍是 HTTPS，证书校验不变。
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ForceAttemptHTTP2 = false
	return &http.Client{Timeout: packageUploadTimeout, Transport: transport}
}

// functionPackageAvailable 是 COS 对象已核对、可以部署的代码包状态（与 Collector 的 store.FunctionPackageAvailable 一致）。
const functionPackageAvailable = "available"

// CloudFunction 是 ListCloudFunctions 的一项：计划里应有的内容、云上读回的状态与两者的比对。不含函数环境与任何密钥。
type CloudFunction struct {
	Name                   string `json:"name"`
	Region                 string `json:"region"`
	Namespace              string `json:"namespace"`
	Kind                   string `json:"kind"`
	Unplanned              bool   `json:"unplanned,omitempty"`
	ExpectedCron           string `json:"expected_cron,omitempty"`
	ExpectedAssignmentHash string `json:"expected_assignment_hash,omitempty"`
	ExpectedPackageID      string `json:"expected_package_id,omitempty"`
	Operation              string `json:"operation,omitempty"`
	Exists                 bool   `json:"exists"`
	Status                 string `json:"status,omitempty"`
	PackageID              string `json:"package_id,omitempty"`
	AssignmentHash         string `json:"assignment_hash,omitempty"`
	TimerCron              string `json:"timer_cron,omitempty"`
	TimerEnabled           bool   `json:"timer_enabled"`
	TimerAvailableStatus   string `json:"timer_available_status,omitempty"`
	// ReadAt 是读回时刻（Unix 毫秒）；接口 JSON 里 int64 是数字字符串，输出保持同一写法。
	ReadAt             int64  `json:"read_at,omitempty,string"`
	ReadError          string `json:"read_error,omitempty"`
	TimerMatches       bool   `json:"timer_matches"`
	PackageLatest      bool   `json:"package_latest"`
	EnvironmentMatches bool   `json:"environment_matches"`
}

// Drifted 判断函数与期望不一致：没读到、云上不存在、定时器或受管环境不一致、代码包落后、计划外。
func (f CloudFunction) Drifted() bool {
	return f.Unplanned || f.ReadError != "" || !f.Exists || !f.TimerMatches || !f.PackageLatest || !f.EnvironmentMatches
}

// CloudFunctionDeletion 是一个函数的删除结果：Done 为 false 且 Error 为空表示还在删。
type CloudFunctionDeletion struct {
	Name      string `json:"name"`
	Region    string `json:"region,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Done      bool   `json:"done"`
	Error     string `json:"error,omitempty"`
}

// FunctionEgress 是一个采集函数此刻的公网出口 IP。
type FunctionEgress struct {
	Name  string `json:"name"`
	IP    string `json:"ip,omitempty"`
	Error string `json:"error,omitempty"`
}

// collectorProtoRsp 是 CollectMgr 生成的响应消息：都带 ret_info。
type collectorProtoRsp interface {
	proto.Message
	GetRetInfo() *commonpb.RetInfo
}

// callCollectorProto 以 proto JSON 调 CollectMgr，请求与响应都是生成的消息，并检查 ret_info。
func (c *Client) callCollectorProto(ctx context.Context, method string, request proto.Message, response collectorProtoRsp) error {
	body, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(request)
	if err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	raw, err := c.gatewayJSON(ctx, "trpc.moox.collector.CollectMgr", method, json.RawMessage(body))
	if err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(raw, response); err != nil {
		return fmt.Errorf("decode %s response: %w", method, err)
	}
	info := response.GetRetInfo()
	if info == nil {
		return fmt.Errorf("%s: 响应缺少 ret_info", method)
	}
	if code := int(info.GetCode()); !isRetInfoSuccess(code) {
		return fmt.Errorf("%s: code %d: %s", method, code, info.GetMsg())
	}
	return nil
}

// callCollectorChecked 调 CollectMgr 并检查 ret_info：缺 ret_info 或返回码非 0 都报错。
func (c *Client) callCollectorChecked(ctx context.Context, method string, body any, response interface{ retInfo() *retInfo }) error {
	if err := c.callCollectorJSON(ctx, method, body, response); err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	info := response.retInfo()
	if info == nil {
		return fmt.Errorf("%s: 响应缺少 ret_info", method)
	}
	if !isRetInfoSuccess(info.Code) {
		return fmt.Errorf("%s: code %d: %s", method, info.Code, info.Msg)
	}
	return nil
}

type collectorRsp struct {
	RetInfo *retInfo `json:"ret_info"`
}

func (r *collectorRsp) retInfo() *retInfo { return r.RetInfo }

// ListCloudAccounts 返回云账号（不含密钥明文）；provider 非空时只留这个厂商的（目前只有 tencent）。
func (c *Client) ListCloudAccounts(ctx context.Context, provider string) ([]*collectorpb.CloudAccount, error) {
	rsp := &collectorpb.ListCloudAccountsRsp{}
	if err := c.callCollectorProto(ctx, "ListCloudAccounts", &collectorpb.ListCloudAccountsReq{}, rsp); err != nil {
		return nil, err
	}
	provider = strings.TrimSpace(provider)
	accounts := make([]*collectorpb.CloudAccount, 0, len(rsp.GetAccounts()))
	for _, account := range rsp.GetAccounts() {
		if provider == "" || account.GetProvider() == provider {
			accounts = append(accounts, account)
		}
	}
	return accounts, nil
}

// CreateCloudAccount 登记一个腾讯云账号与代码包上传用的 COS 桶（只登记密钥 ID，不含 SecretID/SecretKey）；
// 同 ID 的账号已存在时 Collector 拒绝。
func (c *Client) CreateCloudAccount(ctx context.Context, account *collectorpb.CloudAccount) (*collectorpb.CloudAccount, error) {
	rsp := &collectorpb.CreateCloudAccountRsp{}
	if err := c.callCollectorProto(ctx, "CreateCloudAccount", &collectorpb.CreateCloudAccountReq{Account: account}, rsp); err != nil {
		return nil, err
	}
	if strings.TrimSpace(rsp.GetAccount().GetAccountId()) == "" {
		return nil, fmt.Errorf("CreateCloudAccount 没有返回账号")
	}
	return rsp.GetAccount(), nil
}

// ListFunctionPackages 返回全局代码包列表，按上传先后倒序。
func (c *Client) ListFunctionPackages(ctx context.Context) ([]*collectorpb.FunctionPackage, error) {
	rsp := &collectorpb.ListFunctionPackagesRsp{}
	if err := c.callCollectorProto(ctx, "ListFunctionPackages", &collectorpb.ListFunctionPackagesReq{}, rsp); err != nil {
		return nil, err
	}
	return rsp.GetPackages(), nil
}

// LatestPackage 返回指定名称的最新可用代码包 ID；没有则 found=false。
func (c *Client) LatestPackage(ctx context.Context, name string) (id string, found bool, err error) {
	packages, err := c.ListFunctionPackages(ctx)
	if err != nil {
		return "", false, err
	}
	for _, pkg := range packages {
		if pkg.GetPackageName() == name && pkg.GetStatus() == functionPackageAvailable && pkg.GetPackageId() != "" {
			return pkg.GetPackageId(), true, nil
		}
	}
	return "", false, nil
}

// UploadPackage 两步上传：InitPackageUpload 取预签名地址 → PUT 到 COS → CompletePackageUpload 核对大小与 MD5。
func (c *Client) UploadPackage(ctx context.Context, req *collectorpb.InitPackageUploadReq, data []byte) (*collectorpb.FunctionPackage, error) {
	init := proto.Clone(req).(*collectorpb.InitPackageUploadReq)
	initRsp := &collectorpb.InitPackageUploadRsp{}
	if err := c.callCollectorProto(ctx, "InitPackageUpload", init, initRsp); err != nil {
		return nil, err
	}
	if initRsp.GetPackage().GetPackageId() == "" || initRsp.GetUploadUrl() == "" {
		return nil, fmt.Errorf("InitPackageUpload 没有返回 package_id 或上传地址")
	}

	putReq, err := http.NewRequestWithContext(ctx, http.MethodPut, initRsp.GetUploadUrl(), bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	putResp, err := newPackageUploadHTTPClient().Do(putReq)
	if err != nil {
		return nil, fmt.Errorf("上传到 COS 失败：%w", err)
	}
	defer putResp.Body.Close()
	if putResp.StatusCode < 200 || putResp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(putResp.Body, 512))
		return nil, fmt.Errorf("上传到 COS 失败：状态码 %d：%s", putResp.StatusCode, string(body))
	}

	sum := md5.Sum(data)
	completeRsp := &collectorpb.CompletePackageUploadRsp{}
	complete := &collectorpb.CompletePackageUploadReq{
		PackageId: initRsp.GetPackage().GetPackageId(), FileMd5: hex.EncodeToString(sum[:]), FileSize: int64(len(data)),
	}
	if err := c.callCollectorProto(ctx, "CompletePackageUpload", complete, completeRsp); err != nil {
		return nil, err
	}
	if completeRsp.GetPackage() != nil {
		return completeRsp.GetPackage(), nil
	}
	return initRsp.GetPackage(), nil
}

// ListCloudFunctions 读回 Client.SpaceID 空间计划里的函数与计划外的同前缀函数（Collector 现场调腾讯云读回）。
func (c *Client) ListCloudFunctions(ctx context.Context) ([]CloudFunction, error) {
	var rsp struct {
		collectorRsp
		Functions []CloudFunction `json:"functions"`
	}
	if err := c.callCollectorChecked(ctx, "ListCloudFunctions", map[string]any{"space_id": c.SpaceID}, &rsp); err != nil {
		return nil, err
	}
	return rsp.Functions, nil
}

// DeleteCloudFunctionsRequest 是删除条件：Names 与 Prefix 至少给一个；Namespace、Region 非空时只删这个命名空间、地域里的；
// DryRun 为 true 时 Collector 用与实际删除相同的匹配只返回会删哪些，不删。
type DeleteCloudFunctionsRequest struct {
	Names     []string `json:"names,omitempty"`
	Prefix    string   `json:"prefix,omitempty"`
	Namespace string   `json:"namespace,omitempty"`
	Region    string   `json:"region,omitempty"`
	DryRun    bool     `json:"dry_run,omitempty"`
}

// DeleteCloudFunctions 按名字或前缀删 Client.SpaceID 空间的云函数（DryRun 时只预览）；计划里的函数删掉后，下一轮对账重建。
func (c *Client) DeleteCloudFunctions(ctx context.Context, req DeleteCloudFunctionsRequest) ([]CloudFunctionDeletion, error) {
	var rsp struct {
		collectorRsp
		Results []CloudFunctionDeletion `json:"results"`
	}
	body := map[string]any{"space_id": c.SpaceID, "names": req.Names, "prefix": req.Prefix, "namespace": req.Namespace, "region": req.Region, "dry_run": req.DryRun}
	if err := c.callCollectorChecked(ctx, "DeleteCloudFunctions", body, &rsp); err != nil {
		return nil, err
	}
	return rsp.Results, nil
}

// ProbeFunctionEgress 取 Client.SpaceID 空间已生效计划里各采集函数此刻的公网出口 IP；names 为空时探测全部。
func (c *Client) ProbeFunctionEgress(ctx context.Context, names []string) ([]FunctionEgress, error) {
	var rsp struct {
		collectorRsp
		Probes []FunctionEgress `json:"probes"`
	}
	if err := c.callCollectorChecked(ctx, "ProbeFunctionEgress", map[string]any{"space_id": c.SpaceID, "names": names}, &rsp); err != nil {
		return nil, err
	}
	return rsp.Probes, nil
}
