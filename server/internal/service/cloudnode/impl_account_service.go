package cloudnode

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/server/internal/service/cloudnode/provider"
)

// ========== 云账户管理 ==========

// CreateAccount 创建云账户
func (s *ServiceImpl) CreateAccount(ctx context.Context, account *CloudAccountDTO) error {
	return s.accountDAO.CreateCloudAccount(ctx, cloudAccountDTOToModel(account))
}

// UpdateAccount 更新云账户
func (s *ServiceImpl) UpdateAccount(ctx context.Context, account *CloudAccountDTO) error {
	return s.accountDAO.UpdateCloudAccount(ctx, cloudAccountDTOToModel(account))
}

// DeleteAccount 删除云账户
func (s *ServiceImpl) DeleteAccount(ctx context.Context, accountID string) error {
	return s.accountDAO.DeleteCloudAccount(ctx, accountID)
}

// ========== 云账户查询 ==========

// GetAccount 获取云账户详情
func (s *ServiceImpl) GetAccount(ctx context.Context, accountID string) (*CloudAccountDTO, error) {
	account, err := s.accountDAO.GetCloudAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	return s.ConvertToCloudAccountDTO(account), nil
}

// ListAccounts 获取所有云账户列表
func (s *ServiceImpl) ListAccounts(ctx context.Context) ([]*CloudAccountDTO, error) {
	accounts, err := s.accountDAO.GetCloudAccountList(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]*CloudAccountDTO, len(accounts))
	for i, account := range accounts {
		result[i] = s.ConvertToCloudAccountDTO(account)
	}
	return result, nil
}

// ListAccountsByProvider 根据云厂商获取账户列表
func (s *ServiceImpl) ListAccountsByProvider(ctx context.Context, provider string) ([]*CloudAccountDTO, error) {
	accounts, err := s.accountDAO.GetCloudAccountsByProvider(ctx, provider)
	if err != nil {
		return nil, err
	}
	result := make([]*CloudAccountDTO, len(accounts))
	for i, account := range accounts {
		result[i] = s.ConvertToCloudAccountDTO(account)
	}
	return result, nil
}

// GetAccountWithoutMask 获取云账户（不脱敏，供provider使用）
func (s *ServiceImpl) GetAccountWithoutMask(ctx context.Context, accountID string) (*provider.CloudAccount, error) {
	account, err := s.accountDAO.GetCloudAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}

	return &provider.CloudAccount{
		AccountID: account.AccountID,
		Provider:  account.Provider,
		SecretID:  account.SecretID,
		SecretKey: account.SecretKey,
		AppID:     account.AppID,
		COSRegion: account.COSRegion,
		COSBucket: account.COSBucket,
	}, nil
}

// GetCOSAccountInfo 获取COS账户信息（返回简化结构，供外部模块使用）
func (s *ServiceImpl) GetCOSAccountInfo(ctx context.Context, accountID string) (*COSAccountInfo, error) {
	account, err := s.accountDAO.GetCloudAccount(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("获取云账户信息失败: %w", err)
	}

	return &COSAccountInfo{
		Provider:  account.Provider,
		SecretID:  account.SecretID,
		SecretKey: account.SecretKey,
		AppID:     account.AppID,
		COSRegion: account.COSRegion,
		COSBucket: account.COSBucket,
	}, nil
}

// ========== 内部服务访问 ==========

// GetProviderByAccount 获取云厂商客户端（供外部使用）
func (s *ServiceImpl) GetProviderByAccount(cloudAccountID string) provider.Client {
	s.init()
	return s.providerFactory.GetCloudProviderByAccount(cloudAccountID)
}
