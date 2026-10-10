package secret

import (
	"context"

	"github.com/mooyang-code/moox/modules/admin/internal/service/secret/dao"
	"github.com/mooyang-code/moox/modules/admin/internal/service/secret/model"
)

// Service 秘钥管理服务接口
type Service interface {
	CreateSecret(ctx context.Context, secret *model.Secret) error
	UpdateSecret(ctx context.Context, secret *model.Secret) error
	DeleteSecret(ctx context.Context, secretID string) error
	GetSecret(ctx context.Context, secretID string) (*model.Secret, error)
	ListSecrets(ctx context.Context, offset, limit int, filters *dao.SecretFilters) ([]model.Secret, int64, error)
	ToggleSecretStatus(ctx context.Context, secretID, status string) error
}

// ServiceImpl 秘钥服务实现
type ServiceImpl struct {
	secretDAO *dao.SecretDAO
}

// NewService 创建秘钥服务
func NewService(secretDAO *dao.SecretDAO) *ServiceImpl {
	return &ServiceImpl{secretDAO: secretDAO}
}

func (s *ServiceImpl) CreateSecret(ctx context.Context, secret *model.Secret) error {
	return s.secretDAO.Create(ctx, secret)
}

func (s *ServiceImpl) UpdateSecret(ctx context.Context, secret *model.Secret) error {
	return s.secretDAO.Update(ctx, secret)
}

func (s *ServiceImpl) DeleteSecret(ctx context.Context, secretID string) error {
	return s.secretDAO.Delete(ctx, secretID)
}

func (s *ServiceImpl) GetSecret(ctx context.Context, secretID string) (*model.Secret, error) {
	return s.secretDAO.FindByID(ctx, secretID)
}

func (s *ServiceImpl) ListSecrets(ctx context.Context, offset, limit int, filters *dao.SecretFilters) ([]model.Secret, int64, error) {
	return s.secretDAO.List(ctx, offset, limit, filters)
}

func (s *ServiceImpl) ToggleSecretStatus(ctx context.Context, secretID, status string) error {
	return s.secretDAO.UpdateStatus(ctx, secretID, status)
}
