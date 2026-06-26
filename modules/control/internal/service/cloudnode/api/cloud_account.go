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
