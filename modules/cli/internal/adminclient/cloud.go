package adminclient

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
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

// CloudAccount 是一个云账号（不含密钥明文）。
type CloudAccount struct {
	AccountID          string `json:"account_id"`
	AccountName        string `json:"account_name"`
	Provider           string `json:"provider"`
	CredentialSecretID string `json:"credential_secret_id"`
	AppID              string `json:"app_id"`
	COSRegion          string `json:"cos_region"`
	COSBucket          string `json:"cos_bucket"`
}

// CloudAccountInput 登记一个腾讯云账号与代码包上传用的 COS 桶；同一个账号可以在所有配置的地域建函数。不含 SecretID/SecretKey。
type CloudAccountInput struct {
	AccountID          string `json:"account_id"`
	AccountName        string `json:"account_name"`
	Provider           string `json:"provider"`
	CredentialSecretID string `json:"credential_secret_id"`
	AppID              string `json:"app_id"`
	COSRegion          string `json:"cos_region"`
	COSBucket          string `json:"cos_bucket"`
}

// UploadPackageRequest 是上传代码包的参数；空间取 Client.SpaceID。
type UploadPackageRequest struct {
	PackageName    string `json:"package_name"`
	Version        string `json:"version"`
	Runtime        string `json:"runtime"`
	CloudAccountID string `json:"cloud_account_id"`
	Filename       string `json:"filename,omitempty"`
}

// FunctionPackage 是一次上传的代码包；Status 是 pending（已发上传地址）或 available（COS 对象已核对）。
type FunctionPackage struct {
	SpaceID        string `json:"space_id"`
	PackageID      string `json:"package_id"`
	PackageName    string `json:"package_name"`
	Version        string `json:"version"`
	Status         string `json:"status"`
	CloudAccountID string `json:"cloud_account_id"`
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
	ReadAt                 string `json:"read_at,omitempty"`
	ReadError              string `json:"read_error,omitempty"`
	TimerMatches           bool   `json:"timer_matches"`
	PackageLatest          bool   `json:"package_latest"`
	EnvironmentMatches     bool   `json:"environment_matches"`
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

// ListCloudAccounts 返回云账号；provider 非空时只留这个厂商的（目前只有 tencent）。
func (c *Client) ListCloudAccounts(ctx context.Context, provider string) ([]CloudAccount, error) {
	var rsp struct {
		collectorRsp
		Accounts []CloudAccount `json:"accounts"`
	}
	if err := c.callCollectorChecked(ctx, "ListCloudAccounts", map[string]any{}, &rsp); err != nil {
		return nil, err
	}
	provider = strings.TrimSpace(provider)
	accounts := make([]CloudAccount, 0, len(rsp.Accounts))
	for _, account := range rsp.Accounts {
		if provider == "" || account.Provider == provider {
			accounts = append(accounts, account)
		}
	}
	return accounts, nil
}

// CreateCloudAccount 登记一个云账号；同 ID 的账号已存在时 Collector 拒绝。
func (c *Client) CreateCloudAccount(ctx context.Context, input CloudAccountInput) (*CloudAccount, error) {
	var rsp struct {
		collectorRsp
		Account *CloudAccount `json:"account"`
	}
	if err := c.callCollectorChecked(ctx, "CreateCloudAccount", map[string]any{"account": input}, &rsp); err != nil {
		return nil, err
	}
	if rsp.Account == nil || strings.TrimSpace(rsp.Account.AccountID) == "" {
		return nil, fmt.Errorf("CreateCloudAccount 没有返回账号")
	}
	return rsp.Account, nil
}

// ListFunctionPackages 返回 Client.SpaceID 空间的代码包，按上传先后倒序。
func (c *Client) ListFunctionPackages(ctx context.Context) ([]FunctionPackage, error) {
	var rsp struct {
		collectorRsp
		Packages []FunctionPackage `json:"packages"`
	}
	if err := c.callCollectorChecked(ctx, "ListFunctionPackages", map[string]any{"space_id": c.SpaceID}, &rsp); err != nil {
		return nil, err
	}
	return rsp.Packages, nil
}

// LatestPackage 返回 Client.SpaceID 空间里指定名称的最新可用代码包 ID；没有则 found=false。
func (c *Client) LatestPackage(ctx context.Context, name string) (id string, found bool, err error) {
	packages, err := c.ListFunctionPackages(ctx)
	if err != nil {
		return "", false, err
	}
	for _, pkg := range packages {
		if pkg.PackageName == name && pkg.Status == functionPackageAvailable && pkg.PackageID != "" {
			return pkg.PackageID, true, nil
		}
	}
	return "", false, nil
}

// UploadPackage 两步上传：InitPackageUpload 取预签名地址 → PUT 到 COS → CompletePackageUpload 核对大小与 MD5。
func (c *Client) UploadPackage(ctx context.Context, req UploadPackageRequest, data []byte) (*FunctionPackage, error) {
	if strings.TrimSpace(c.SpaceID) == "" {
		return nil, fmt.Errorf("上传代码包要指定空间")
	}
	var initRsp struct {
		collectorRsp
		Package   *FunctionPackage `json:"package"`
		UploadURL string           `json:"upload_url"`
	}
	if err := c.callCollectorChecked(ctx, "InitPackageUpload", map[string]any{
		"space_id": c.SpaceID, "package_name": req.PackageName, "version": req.Version, "runtime": req.Runtime,
		"cloud_account_id": req.CloudAccountID, "filename": req.Filename,
	}, &initRsp); err != nil {
		return nil, err
	}
	if initRsp.Package == nil || initRsp.Package.PackageID == "" || initRsp.UploadURL == "" {
		return nil, fmt.Errorf("InitPackageUpload 没有返回 package_id 或上传地址")
	}

	putReq, err := http.NewRequestWithContext(ctx, http.MethodPut, initRsp.UploadURL, bytes.NewReader(data))
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
	var completeRsp struct {
		collectorRsp
		Package *FunctionPackage `json:"package"`
	}
	if err := c.callCollectorChecked(ctx, "CompletePackageUpload", map[string]any{
		"space_id": c.SpaceID, "package_id": initRsp.Package.PackageID, "file_md5": hex.EncodeToString(sum[:]), "file_size": len(data),
	}, &completeRsp); err != nil {
		return nil, err
	}
	if completeRsp.Package != nil {
		return completeRsp.Package, nil
	}
	return initRsp.Package, nil
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

// DeleteCloudFunctions 按名字或前缀删 Client.SpaceID 空间的云函数；计划里的函数删掉后，下一轮对账重建。
func (c *Client) DeleteCloudFunctions(ctx context.Context, names []string, prefix string) ([]CloudFunctionDeletion, error) {
	var rsp struct {
		collectorRsp
		Results []CloudFunctionDeletion `json:"results"`
	}
	if err := c.callCollectorChecked(ctx, "DeleteCloudFunctions", map[string]any{"space_id": c.SpaceID, "names": names, "prefix": prefix}, &rsp); err != nil {
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
