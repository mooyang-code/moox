package api

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/modules/control/internal/common"
	"github.com/mooyang-code/moox/modules/control/internal/errors"
	cloudnodemgr "github.com/mooyang-code/moox/modules/control/internal/service/cloudnode"
	"github.com/mooyang-code/moox/modules/control/internal/service/cloudnode/model"

	"github.com/gin-gonic/gin"
)

// CloudAccountHandler 云账户管理处理器
type CloudAccountHandler struct {
	service cloudnodemgr.Service
}

// NewCloudAccountHandler 使用已有的服务创建云账户管理处理器
func NewCloudAccountHandler(service cloudnodemgr.Service) *CloudAccountHandler {
	return &CloudAccountHandler{
		service: service,
	}
}

// SchemaID 返回表名
func (h *CloudAccountHandler) SchemaID() string {
	return model.CloudAccountTableName
}

// GetHandle 处理GET请求
func (h *CloudAccountHandler) GetHandle(ctx context.Context, params map[string]string) (*APIResponse, error) {
	// 支持根据account_id查询单个账户
	if accountID, ok := params["account_id"]; ok && accountID != "" {
		account, err := h.service.GetAccount(ctx, accountID)
		if err != nil {
			return &APIResponse{
				Code: 500,
				Data: []interface{}{},
			}, fmt.Errorf("failed to get cloud account: %w", err)
		}

		if account == nil {
			return &APIResponse{
				Code: 404,
				Data: []interface{}{},
			}, nil
		}

		return &APIResponse{
			Code: 200,
			Data: []interface{}{account},
		}, nil
	}

	// 支持按云厂商查询
	if provider, ok := params["provider"]; ok && provider != "" {
		accounts, err := h.service.ListAccountsByProvider(ctx, provider)
		if err != nil {
			return &APIResponse{
				Code: 500,
				Data: []interface{}{},
			}, fmt.Errorf("failed to list cloud accounts by provider: %w", err)
		}

		var data []interface{}
		for _, account := range accounts {
			data = append(data, account)
		}

		return &APIResponse{
			Code: 200,
			Data: data,
		}, nil
	}

	// 获取所有账户列表
	accounts, err := h.service.ListAccounts(ctx)
	if err != nil {
		return &APIResponse{
			Code: 500,
			Data: []interface{}{},
		}, fmt.Errorf("failed to list cloud accounts: %w", err)
	}

	// 转换为接口切片
	var data []interface{}
	for _, account := range accounts {
		data = append(data, account)
	}

	return &APIResponse{
		Code: 200,
		Data: data,
	}, nil
}

// PostHandle 处理POST请求
func (h *CloudAccountHandler) PostHandle(ctx context.Context, params map[string]string) (*APIResponse, error) {
	action := params["_action"]

	switch action {
	case "create":
		// 创建云账户
		accountDTO := h.parseCloudAccount(params)

		if err := h.service.CreateAccount(ctx, accountDTO); err != nil {
			return &APIResponse{
				Code: 500,
				Data: []interface{}{},
			}, fmt.Errorf("failed to create cloud account: %w", err)
		}

		// 脱敏处理（DTO中已经不包含敏感信息）
		accountDTO.SecretKey = "****"
		if len(accountDTO.SecretID) > 4 {
			accountDTO.SecretID = accountDTO.SecretID[:4] + "****"
		}

		return &APIResponse{
			Code: 200,
			Data: []interface{}{accountDTO},
		}, nil

	case "update":
		// 更新云账户
		account := h.parseCloudAccount(params)

		if err := h.service.UpdateAccount(ctx, account); err != nil {
			return &APIResponse{
				Code: 500,
				Data: []interface{}{},
			}, fmt.Errorf("failed to update cloud account: %w", err)
		}

		return &APIResponse{
			Code: 200,
			Data: []interface{}{"success"},
		}, nil

	case "delete":
		// 删除云账户
		accountID := params["account_id"]
		if err := h.service.DeleteAccount(ctx, accountID); err != nil {
			return &APIResponse{
				Code: 500,
				Data: []interface{}{},
			}, fmt.Errorf("failed to delete cloud account: %w", err)
		}

		return &APIResponse{
			Code: 200,
			Data: []interface{}{"success"},
		}, nil

	default:
		return &APIResponse{
			Code: 400,
			Data: []interface{}{},
		}, fmt.Errorf("invalid action: %s", action)
	}
}

// parseCloudAccount 解析云账户参数（返回DTO）
func (h *CloudAccountHandler) parseCloudAccount(params map[string]string) *cloudnodemgr.CloudAccountDTO {
	account := &cloudnodemgr.CloudAccountDTO{
		AccountID:   params["account_id"],
		AccountName: params["account_name"],
		Provider:    params["provider"],
		SecretID:    params["secret_id"],
		SecretKey:   params["secret_key"],
		AppID:       params["app_id"],
		COSRegion:   params["cos_region"],
		COSBucket:   params["cos_bucket"],
		ExtraConfig: params["extra_config"],
	}

	return account
}

// GetCloudAccountList 获取云账户列表
func (h *CloudAccountHandler) GetCloudAccountList(c *gin.Context) {
	ctx := c.Request.Context()
	provider := c.Query("provider")

	var accounts []*cloudnodemgr.CloudAccountDTO
	var err error

	if provider != "" {
		accounts, err = h.service.ListAccountsByProvider(ctx, provider)
	} else {
		accounts, err = h.service.ListAccounts(ctx)
	}

	if err != nil {
		common.HandleAppError(c, errors.Internal("查询云账户列表失败", err))
		return
	}

	// 计算总数
	total := int64(len(accounts))

	// 使用新的分页列表响应格式
	common.PaginatedListResponse(c, "查询成功", accounts, total)
}

// GetCloudAccountDetail 获取云账户详情
func (h *CloudAccountHandler) GetCloudAccountDetail(c *gin.Context) {
	ctx := c.Request.Context()
	accountID := c.Query("account_id")
	if accountID == "" {
		c.JSON(400, gin.H{"code": 400, "message": "account_id is required"})
		return
	}

	account, err := h.service.GetAccount(ctx, accountID)
	if err != nil {
		c.JSON(500, gin.H{"code": 500, "message": err.Error()})
		return
	}
	c.JSON(200, gin.H{"code": 200, "data": account})
}

// CreateCloudAccount 创建云账户
func (h *CloudAccountHandler) CreateCloudAccount(c *gin.Context) {
	ctx := c.Request.Context()
	var account cloudnodemgr.CloudAccountDTO
	if err := c.ShouldBindJSON(&account); err != nil {
		c.JSON(400, gin.H{"code": 400, "message": err.Error()})
		return
	}

	if err := h.service.CreateAccount(ctx, &account); err != nil {
		c.JSON(500, gin.H{"code": 500, "message": err.Error()})
		return
	}
	c.JSON(200, gin.H{"code": 200, "data": account})
}

// UpdateCloudAccount 更新云账户
func (h *CloudAccountHandler) UpdateCloudAccount(c *gin.Context) {
	ctx := c.Request.Context()
	var account cloudnodemgr.CloudAccountDTO
	if err := c.ShouldBindJSON(&account); err != nil {
		c.JSON(400, gin.H{"code": 400, "message": err.Error()})
		return
	}

	if err := h.service.UpdateAccount(ctx, &account); err != nil {
		c.JSON(500, gin.H{"code": 500, "message": err.Error()})
		return
	}
	c.JSON(200, gin.H{"code": 200, "message": "success"})
}

// DeleteCloudAccount 删除云账户
func (h *CloudAccountHandler) DeleteCloudAccount(c *gin.Context) {
	ctx := c.Request.Context()
	accountID := c.Query("account_id")
	if accountID == "" {
		c.JSON(400, gin.H{"code": 400, "message": "account_id is required"})
		return
	}

	if err := h.service.DeleteAccount(ctx, accountID); err != nil {
		common.HandleAppError(c, err)
		return
	}
	c.JSON(200, gin.H{"code": 200, "message": "success"})
}

// COSInfoResponse COS 账户信息响应（供 scf-publish 等外部工具消费）。
// 默认对 SecretID/SecretKey 脱敏；当 reveal=true 时返回原始明文凭证，
// 仅供受信的发布工具在签名为后台 service-auth 的内网调用中使用。
//
// 安全说明：reveal=true 走 /api/service/cloudnode/GetCOSAccountInfo 路径，
// 该路径受 gateway service_auth HMAC 签名鉴权保护，未签名请求会被拒绝。
type COSInfoResponse struct {
	AccountID string `json:"account_id"`
	Provider  string `json:"provider"`
	AppID     string `json:"app_id"`
	COSRegion string `json:"cos_region"`
	COSBucket string `json:"cos_bucket"`
	SecretID  string `json:"secret_id"`
	SecretKey string `json:"secret_key"`
}

// GetCOSAccountInfo 获取 COS 账户信息（含 bucket/region/appid/凭证）。
// GET /api/v1/cloud_account/cos-info?account_id=xxx&reveal=true
//
// 当 reveal=true 时返回原始凭证明文，否则脱敏。该接口通过后台服务 API
// /api/service/cloudnode/GetCOSAccountInfo 暴露，受 HMAC 签名鉴权保护。
func (h *CloudAccountHandler) GetCOSAccountInfo(c *gin.Context) {
	ctx := c.Request.Context()
	accountID := c.Query("account_id")
	if accountID == "" {
		c.JSON(400, gin.H{"code": 400, "message": "account_id is required"})
		return
	}
	reveal := c.Query("reveal") == "true"

	info, err := h.service.GetCOSAccountInfo(ctx, accountID)
	if err != nil {
		common.HandleAppError(c, errors.Internal("获取 COS 账户信息失败", err))
		return
	}
	if info == nil {
		c.JSON(404, gin.H{"code": 404, "message": "cloud account not found"})
		return
	}

	resp := &COSInfoResponse{
		AccountID: accountID,
		Provider:  info.Provider,
		AppID:     info.AppID,
		COSRegion: info.COSRegion,
		COSBucket: info.COSBucket,
	}
	if reveal {
		resp.SecretID = info.SecretID
		resp.SecretKey = info.SecretKey
	} else {
		resp.SecretID = maskSecret(info.SecretID)
		resp.SecretKey = maskSecret(info.SecretKey)
	}
	c.JSON(200, gin.H{"code": 200, "data": resp})
}

// maskSecret 对凭证做简单脱敏：长度<=8 全隐藏，否则保留首3尾3。
func maskSecret(s string) string {
	if s == "" {
		return ""
	}
	if len(s) <= 8 {
		return "********"
	}
	return s[:3] + "********" + s[len(s)-3:]
}
