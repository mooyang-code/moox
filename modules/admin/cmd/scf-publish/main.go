// Command scf-publish 把本地构建好的 collector SCF zip 包发布到腾讯云 SCF。
//
// 与早期版本不同：本工具不再直接读控制面 SQLite DB，也不再硬编码
// region/cos-bucket/cos-appid/function/namespace 等环境相关信息，而是通过
// 控制面后台 API 动态获取：
//
//  1. POST /api/service/cloudnode/GetSCFDeployInfo  → 按 node_id 拿到
//     function_name / namespace / region / cloud_account_id
//  2. POST /api/service/cloudnode/GetCOSAccountInfo → 按上一步的
//     cloud_account_id 拿到 COS bucket / region / appid / 凭证
//     （reveal=true 返回明文凭证，仅用于上传 COS）
//
// 两个 API 都走 gateway 的 /api/service/* 路径，使用 moox-auth-v1 HMAC
// 签名鉴权（与 collector 心跳上报同链路），签名参数来自控制面 gateway.yaml
// 的 service_auth 配置。
//
// 用法:
//
//	scf-publish -zip <path-to-collector-scf.zip> \
//	           -server-url http://<control-ip>:<port> \
//	           -node-id <scf-node-id> \
//	           -auth-access-key <service_auth.access_key> \
//	           -auth-secret-key <service_auth.secret_key> \
//	           [-cos-path <object-key-in-cos>]
package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
	scf "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/scf/v20180416"
	"github.com/tencentyun/cos-go-sdk-v5"
)

const (
	defaultAuthVersion = "moox-auth-v1"
	defaultExpireSec   = int64(1800)
)

// ========== 数据结构 ==========

// scfDeployInfoResp 控制面 GetSCFDeployInfo 返回的部署信息。
type scfDeployInfoResp struct {
	NodeID         string `json:"node_id"`
	FunctionName   string `json:"function_name"`
	Namespace      string `json:"namespace"`
	Region         string `json:"region"`
	NodeType       string `json:"node_type"`
	CloudAccountID string `json:"cloud_account_id"`
}

// cosAccountInfoResp 控制面 GetCOSAccountInfo 返回的 COS 凭证信息。
type cosAccountInfoResp struct {
	AccountID string `json:"account_id"`
	Provider  string `json:"provider"`
	AppID     string `json:"app_id"`
	COSRegion string `json:"cos_region"`
	COSBucket string `json:"cos_bucket"`
	SecretID  string `json:"secret_id"`
	SecretKey string `json:"secret_key"`
}

func main() {
	zipPath := flag.String("zip", "", "本地 collector-scf-*.zip 路径（必填）")
	serverURL := flag.String("server-url", "", "控制面地址，形如 http://ip:port（必填）")
	nodeID := flag.String("node-id", "", "SCF 节点 ID（必填，用于查询函数名/命名空间/region/云账户）")
	authAccessKey := flag.String("auth-access-key", "moox-service", "service_auth.access_key（与控制面 gateway.yaml 一致）")
	authSecretKey := flag.String("auth-secret-key", "", "service_auth.secret_key（必填，与控制面 gateway.yaml 一致）")
	authVersion := flag.String("auth-version", defaultAuthVersion, "service_auth.version")
	cosPath := flag.String("cos-path", "", "COS 对象 key（默认 collector-scf/collector-scf-<timestamp>.zip）")
	flag.Parse()

	if *zipPath == "" || *serverURL == "" || *nodeID == "" || *authSecretKey == "" {
		fmt.Fprintln(os.Stderr, "❌ -zip、-server-url、-node-id、-auth-secret-key 均为必填")
		flag.Usage()
		os.Exit(2)
	}
	if *cosPath == "" {
		*cosPath = fmt.Sprintf("collector-scf/collector-scf-%d.zip", time.Now().Unix())
	}
	srvURL := strings.TrimRight(*serverURL, "/")

	// 1. 通过 API 查询 SCF 部署信息
	deploy, err := getSCFDeployInfo(srvURL, *nodeID, *authVersion, *authAccessKey, *authSecretKey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ 查询 SCF 部署信息失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✅ SCF 部署信息: function=%s, namespace=%s, region=%s, account=%s\n",
		deploy.FunctionName, deploy.Namespace, deploy.Region, deploy.CloudAccountID)

	// 2. 通过 API 查询 COS 凭证（reveal=true 拿明文）
	cosInfo, err := getCOSAccountInfo(srvURL, deploy.CloudAccountID, *authVersion, *authAccessKey, *authSecretKey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ 查询 COS 账户信息失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✅ COS 账户信息: bucket=%s, region=%s, appid=%s\n", cosInfo.COSBucket, cosInfo.COSRegion, cosInfo.AppID)

	// 3. 上传 zip 到 COS
	zipData, err := ioutil.ReadFile(*zipPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ 读 zip 失败: %v\n", err)
		os.Exit(1)
	}
	if err := uploadToCOS(cosInfo.SecretID, cosInfo.SecretKey, cosInfo.COSBucket, cosInfo.COSRegion, *cosPath, zipData); err != nil {
		fmt.Fprintf(os.Stderr, "❌ 上传 COS 失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✅ 已上传 zip 到 COS: bucket=%s, path=%s, size=%d\n", cosInfo.COSBucket, *cosPath, len(zipData))

	// 4. UpdateFunctionCode
	if err := updateFunctionCode(cosInfo.SecretID, cosInfo.SecretKey, deploy.Region, deploy.FunctionName, deploy.Namespace, cosInfo.COSBucket, *cosPath, cosInfo.COSRegion); err != nil {
		fmt.Fprintf(os.Stderr, "❌ 更新函数代码失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✅ SCF 函数代码已更新: function=%s, namespace=%s, region=%s\n", deploy.FunctionName, deploy.Namespace, deploy.Region)
	fmt.Println("🎉 发布完成，等待 SCF 冷启动后即可生效")
}

// ========== 控制面 API 调用 ==========

// getSCFDeployInfo 查询 SCF 部署信息。
func getSCFDeployInfo(serverURL, nodeID, authVersion, accessKey, secretKey string) (*scfDeployInfoResp, error) {
	body := map[string]string{"node_id": nodeID}
	var resp scfDeployInfoResp
	if err := callControlAPI(serverURL, "cloudnode", "GetSCFDeployInfo", body, accessKey, secretKey, authVersion, &resp); err != nil {
		return nil, fmt.Errorf("GetSCFDeployInfo: %w", err)
	}
	if resp.FunctionName == "" {
		return nil, fmt.Errorf("节点 %s 不存在或未返回 function_name", nodeID)
	}
	return &resp, nil
}

// getCOSAccountInfo 查询 COS 账户信息（reveal=true 返回明文凭证）。
func getCOSAccountInfo(serverURL, accountID, authVersion, accessKey, secretKey string) (*cosAccountInfoResp, error) {
	body := map[string]string{"account_id": accountID, "reveal": "true"}
	var resp cosAccountInfoResp
	if err := callControlAPI(serverURL, "cloudnode", "GetCOSAccountInfo", body, accessKey, secretKey, authVersion, &resp); err != nil {
		return nil, fmt.Errorf("GetCOSAccountInfo: %w", err)
	}
	if resp.COSBucket == "" || resp.SecretID == "" {
		return nil, fmt.Errorf("账户 %s 未配置 COS bucket 或凭证为空", accountID)
	}
	return &resp, nil
}

// callControlAPI 调用控制面后台 API（HMAC 签名），解析 {code,data} 响应到 out。
func callControlAPI(serverURL, service, method string, reqBody interface{}, accessKey, secretKey, authVersion string, out interface{}) error {
	apiURL := fmt.Sprintf("%s/api/service/%s/%s", serverURL, service, method)

	data, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshal body: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, apiURL, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Auth", generateAuthHeader(authVersion, accessKey, secretKey, string(data)))

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("http do: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("http %d: %s", resp.StatusCode, string(respBody))
	}

	// 解析 {code, data} 或 {code, message} 结构
	var envelope struct {
		Code int             `json:"code"`
		Msg  string          `json:"message,omitempty"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		return fmt.Errorf("unmarshal envelope: %w (body=%s)", err, string(respBody))
	}
	if envelope.Code != 200 {
		return fmt.Errorf("control api code=%d msg=%s", envelope.Code, envelope.Msg)
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return fmt.Errorf("control api returned empty data (msg=%s)", envelope.Msg)
	}
	if err := json.Unmarshal(envelope.Data, out); err != nil {
		return fmt.Errorf("unmarshal data: %w (data=%s)", err, string(envelope.Data))
	}
	return nil
}

// generateAuthHeader 生成 moox-auth-v1/$access_key/$ts/$expire/$signature 签名头。
// 与 collector adminapi.GenerateAuthHeader 保持一致。
func generateAuthHeader(version, accessKey, secretKey, body string) string {
	ts := time.Now().Unix()
	prefix := fmt.Sprintf("%s/%s/%d/%d", version, accessKey, ts, defaultExpireSec)
	signKeyHex := hmacSHA256Hex(secretKey, prefix)
	signature := hmacSHA256Hex(signKeyHex, body)
	return fmt.Sprintf("%s/%s", prefix, signature)
}

func hmacSHA256Hex(key, data string) string {
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write([]byte(data))
	return hex.EncodeToString(mac.Sum(nil))
}

// ========== COS 上传 ==========

func uploadToCOS(sid, sk, bucket, region, key string, data []byte) error {
	bucketURL, _ := url.Parse(fmt.Sprintf("https://%s.cos.%s.myqcloud.com", bucket, region))
	baseURL := &cos.BaseURL{BucketURL: bucketURL}
	client := cos.NewClient(baseURL, &http.Client{
		Transport: &cos.AuthorizationTransport{SecretID: sid, SecretKey: sk},
	})
	ctx := context.Background()
	_, err := client.Object.Put(ctx, key, bytes.NewReader(data), nil)
	return err
}

// ========== SCF UpdateFunctionCode ==========

func updateFunctionCode(sid, sk, region, function, namespace, cosBucket, cosPath, cosRegion string) error {
	credential := common.NewCredential(sid, sk)
	cpf := profile.NewClientProfile()
	cpf.HttpProfile.Endpoint = "scf.tencentcloudapi.com"
	cpf.HttpProfile.ReqTimeout = 240
	client, err := scf.NewClient(credential, region, cpf)
	if err != nil {
		return fmt.Errorf("new scf client: %w", err)
	}
	req := scf.NewUpdateFunctionCodeRequest()
	req.FunctionName = common.StringPtr(function)
	req.Namespace = common.StringPtr(namespace)
	req.CosBucketName = common.StringPtr(cosBucket)
	req.CosObjectName = common.StringPtr(cosPath)
	req.CosBucketRegion = common.StringPtr(cosRegion)
	req.Handler = common.StringPtr("main")
	resp, err := client.UpdateFunctionCode(req)
	if err != nil {
		return err
	}
	log.Printf("UpdateFunctionCode resp: %+v", resp.Response)
	return nil
}
