package tencent

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"

	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	tchttp "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/http"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
)

// GetUserAppID 用访问密钥查询所属主账号的 AppId（CAM GetUserAppId）。AppId 与主账号 UIN 不同：
// SCF 代码包的 COS 桶名、云函数账号登记都用 AppId。用通用请求调用，不额外引入 CAM SDK 模块。
func GetUserAppID(ctx context.Context, options IdentityOptions) (string, error) {
	if err := options.Credentials.validate(); err != nil {
		return "", err
	}
	clientProfile := profile.NewClientProfile()
	clientProfile.HttpProfile.ReqTimeout = 15
	clientProfile.HttpProfile.Endpoint = "cam.tencentcloudapi.com"
	if endpoint := strings.TrimSpace(options.Endpoint); endpoint != "" {
		parsed, err := url.Parse(endpoint)
		if err != nil || parsed.Host == "" {
			return "", ErrRequestFailed
		}
		clientProfile.HttpProfile.Scheme = strings.ToUpper(parsed.Scheme)
		clientProfile.HttpProfile.Endpoint = parsed.Host
	}
	credential := common.NewCredential(strings.TrimSpace(options.Credentials.SecretID), options.Credentials.SecretKey)
	client := common.NewCommonClient(credential, "", clientProfile)
	client.WithHttpTransport(httpTransport(options.HTTPClient))
	request := tchttp.NewCommonRequest("cam", "2019-01-16", "GetUserAppId")
	request.SetContext(ctx)
	if err := request.SetActionParameters(map[string]any{}); err != nil {
		return "", ErrRequestFailed
	}
	response := tchttp.NewCommonResponse()
	if err := client.Send(request, response); err != nil {
		return "", sanitizedSDKError(err)
	}
	var body struct {
		Response struct {
			AppID json.Number `json:"AppId"`
		} `json:"Response"`
	}
	if err := json.Unmarshal(response.GetBody(), &body); err != nil {
		return "", ErrRequestFailed
	}
	appID := body.Response.AppID.String()
	if _, err := strconv.ParseUint(appID, 10, 64); err != nil || appID == "0" {
		return "", ErrRequestFailed
	}
	return appID, nil
}
