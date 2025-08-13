package api

import (
	"context"
	"fmt"

	"github.com/gin-gonic/gin"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/logic"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/model"
	"gorm.io/gorm"
)

// CloudAccountHandler 云账户管理处理器
type CloudAccountHandler struct {
	service logic.CloudAccountService
}

// NewCloudAccountHandler 创建云账户管理处理器（用于路由注册）
func NewCloudAccountHandler(db *gorm.DB) *CloudAccountHandler {
	return &CloudAccountHandler{
		service: logic.NewCloudAccountService(db),
	}
}

// NewCloudAccountSchemaHandler 创建云账户管理处理器（用于Schema系统）
func NewCloudAccountSchemaHandler(db *gorm.DB) SchemaHandler {
	return &CloudAccountHandler{
		service: logic.NewCloudAccountService(db),
	}
}

// NewCloudAccountHandlerWithService 使用已有的服务创建云账户管理处理器
func NewCloudAccountHandlerWithService(service logic.CloudAccountService) SchemaHandler {
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

		data := make([]interface{}, 0, len(accounts))
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
	data := make([]interface{}, 0, len(accounts))
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
		account := h.parseCloudAccount(params)

		if err := h.service.CreateAccount(ctx, account); err != nil {
			return &APIResponse{
				Code: 500,
				Data: []interface{}{},
			}, fmt.Errorf("failed to create cloud account: %w", err)
		}

		// 脱敏处理后返回
		account.MaskSecretKey()
		return &APIResponse{
			Code: 200,
			Data: []interface{}{account},
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

// parseCloudAccount 解析云账户参数
func (h *CloudAccountHandler) parseCloudAccount(params map[string]string) *model.CloudAccount {
	account := &model.CloudAccount{
		AccountID:    params["account_id"],
		AccountName:  params["account_name"],
		Provider:     params["provider"],
		SecretID:     params["secret_id"],
		SecretKey:    params["secret_key"],
		ExtraConfig:  params["extra_config"],
	}

	return account
}

// GetCloudAccountList 获取云账户列表
func (h *CloudAccountHandler) GetCloudAccountList(c *gin.Context) {
	ctx := c.Request.Context()
	provider := c.Query("provider")
	
	var accounts []*model.CloudAccount
	var err error
	
	if provider != "" {
		accounts, err = h.service.ListAccountsByProvider(ctx, provider)
	} else {
		accounts, err = h.service.ListAccounts(ctx)
	}
	
	if err != nil {
		c.JSON(500, gin.H{"code": 500, "message": err.Error()})
		return
	}
	c.JSON(200, gin.H{"code": 200, "data": accounts})
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
	var account model.CloudAccount
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
	var account model.CloudAccount
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
		c.JSON(500, gin.H{"code": 500, "message": err.Error()})
		return
	}
	c.JSON(200, gin.H{"code": 200, "message": "success"})
}