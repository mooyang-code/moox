package adminclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// CloudAccount 云账户（脱敏，仅用于列举与取 account_id）。
type CloudAccount struct {
	AccountID   string `json:"account_id"`
	AccountName string `json:"account_name"`
	Provider    string `json:"provider"`
	AppID       string `json:"app_id"`
	COSRegion   string `json:"cos_region"`
	COSBucket   string `json:"cos_bucket"`
	IsDeleted   string `json:"is_deleted"`
}

// COSAccountInfo 云账户凭证信息（reveal=true 时含明文 secret_id/secret_key）。
// 这些是腾讯云账户通用凭证，可用于 SCF/COS/Lighthouse 等同账户下的腾讯云 API。
type COSAccountInfo struct {
	AccountID string `json:"account_id"`
	Provider  string `json:"provider"`
	AppID     string `json:"app_id"`
	COSRegion string `json:"cos_region"`
	COSBucket string `json:"cos_bucket"`
	SecretID  string `json:"secret_id"`
	SecretKey string `json:"secret_key"`
}

// ListCloudAccounts 调 cloudnode/ListCloudAccounts，返回脱敏账户列表。
// 配置 ServiceAuth 后走 /api/service/cloudnode/ListCloudAccounts。
func (c *Client) ListCloudAccounts(ctx context.Context, provider string) ([]CloudAccount, error) {
	body := map[string]string{}
	if provider != "" {
		body["provider"] = provider
	}
	raw, err := c.postJSON(ctx, http.MethodPost, "/api/admin/cloudnode/ListCloudAccounts", body)
	if err != nil {
		return nil, err
	}
	var resp struct {
		RetInfo  *retInfo       `json:"ret_info"`
		Accounts []CloudAccount `json:"accounts"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	if resp.RetInfo != nil && !isRetInfoSuccess(resp.RetInfo.Code) {
		return nil, fmt.Errorf("ListCloudAccounts: code %d: %s", resp.RetInfo.Code, resp.RetInfo.Msg)
	}
	return resp.Accounts, nil
}

// GetCOSAccountInfo 调 cloudnode/GetCOSAccountInfo（reveal=true），返回明文凭证。
// 配置 ServiceAuth 后走 /api/service/cloudnode/GetCOSAccountInfo。
func (c *Client) GetCOSAccountInfo(ctx context.Context, accountID string) (*COSAccountInfo, error) {
	body := map[string]any{"account_id": accountID, "reveal": true}
	raw, err := c.postJSON(ctx, http.MethodPost, "/api/admin/cloudnode/GetCOSAccountInfo", body)
	if err != nil {
		return nil, err
	}
	var resp struct {
		RetInfo *retInfo        `json:"ret_info"`
		Info    *COSAccountInfo `json:"info"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	if resp.RetInfo != nil && !isRetInfoSuccess(resp.RetInfo.Code) {
		return nil, fmt.Errorf("GetCOSAccountInfo: code %d: %s", resp.RetInfo.Code, resp.RetInfo.Msg)
	}
	if resp.Info == nil {
		return nil, fmt.Errorf("GetCOSAccountInfo: empty info for %s", accountID)
	}
	return resp.Info, nil
}
