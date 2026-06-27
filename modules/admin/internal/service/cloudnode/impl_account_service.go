package cloudnode

import (
	"context"
	"fmt"
	"strconv"

	apperrors "github.com/mooyang-code/moox/modules/admin/internal/errors"
	"github.com/mooyang-code/moox/modules/admin/internal/service/cloudnode/provider"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
)

// ========== 云账户管理 ==========

// CreateAccount 创建云账户
func (s *ServiceImpl) CreateAccount(ctx context.Context, account *pb.CloudAccount) error {
	return s.accountDAO.CreateCloudAccount(ctx, cloudAccountPBToModel(account))
}

// UpdateAccount 更新云账户
func (s *ServiceImpl) UpdateAccount(ctx context.Context, account *pb.CloudAccount) error {
	return s.accountDAO.UpdateCloudAccount(ctx, cloudAccountPBToModel(account))
}

// DeleteAccount 删除云账户
func (s *ServiceImpl) DeleteAccount(ctx context.Context, accountID string) error {
	nodeRefs, err := s.nodeDAO.CountByCloudAccountID(ctx, accountID)
	if err != nil {
		return fmt.Errorf("检查云账户节点引用失败: %w", err)
	}
	packageRefs, err := s.packageDAO.CountByCloudAccountID(ctx, accountID)
	if err != nil {
		return fmt.Errorf("检查云账户代码包引用失败: %w", err)
	}
	if nodeRefs > 0 || packageRefs > 0 {
		return apperrors.Conflict(
			fmt.Sprintf("云账户 %s 正在被 %d 个节点、%d 个代码包引用，请先解绑或迁移后再删除", accountID, nodeRefs, packageRefs),
			nil,
		).
			WithDetail("account_id", accountID).
			WithDetail("node_refs", strconv.FormatInt(nodeRefs, 10)).
			WithDetail("package_refs", strconv.FormatInt(packageRefs, 10))
	}
	return s.accountDAO.DeleteCloudAccount(ctx, accountID)
}

// ========== 云账户查询 ==========

// GetAccount 获取云账户详情
func (s *ServiceImpl) GetAccount(ctx context.Context, accountID string) (*pb.CloudAccount, error) {
	account, err := s.accountDAO.GetCloudAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	return cloudAccountModelToPB(account), nil
}

// ListAccounts 获取所有云账户列表
func (s *ServiceImpl) ListAccounts(ctx context.Context) ([]*pb.CloudAccount, error) {
	accounts, err := s.accountDAO.GetCloudAccountList(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]*pb.CloudAccount, len(accounts))
	for i, account := range accounts {
		result[i] = cloudAccountModelToPB(account)
	}
	return result, nil
}

// ListAccountsByProvider 根据云厂商获取账户列表
func (s *ServiceImpl) ListAccountsByProvider(ctx context.Context, providerName string) ([]*pb.CloudAccount, error) {
	accounts, err := s.accountDAO.GetCloudAccountsByProvider(ctx, providerName)
	if err != nil {
		return nil, err
	}
	result := make([]*pb.CloudAccount, len(accounts))
	for i, account := range accounts {
		result[i] = cloudAccountModelToPB(account)
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
func (s *ServiceImpl) GetCOSAccountInfo(ctx context.Context, accountID string) (*pb.COSAccountInfo, error) {
	account, err := s.accountDAO.GetCloudAccount(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("获取云账户信息失败: %w", err)
	}

	return &pb.COSAccountInfo{
		Provider:  account.Provider,
		SecretId:  account.SecretID,
		SecretKey: account.SecretKey,
		AppId:     account.AppID,
		CosRegion: account.COSRegion,
		CosBucket: account.COSBucket,
	}, nil
}

// ========== 内部服务访问 ==========

// GetProviderByAccount 获取云厂商客户端（供外部使用）
func (s *ServiceImpl) GetProviderByAccount(cloudAccountID string) provider.Client {
	s.init()
	return s.providerFactory.GetCloudProviderByAccount(cloudAccountID)
}
