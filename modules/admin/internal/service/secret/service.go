package secret

import (
	"context"
	"errors"

	"github.com/mooyang-code/moox/modules/admin/internal/service/secret/dao"
	"github.com/mooyang-code/moox/modules/admin/internal/service/secret/model"
)

// ErrSystemSpace 表示请求试图在系统空间里创建秘钥。
var ErrSystemSpace = errors.New("不能在系统空间 mooxsys 中创建秘钥")

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

// 系统空间的密钥（网关调用方签名密钥、外部接入调用方密钥）只由 keys 服务管理：这里读不到，也改不了。
// 否则任何能调秘钥接口的调用方都能读出、替换或停用网关签名密钥。
func (s *ServiceImpl) visible(ctx context.Context, secretID string) error {
	row, err := s.secretDAO.FindByID(ctx, secretID)
	if err != nil {
		return err
	}
	if row.SpaceID == dao.SystemSpaceID {
		return dao.ErrSecretNotFound
	}
	return nil
}

func (s *ServiceImpl) CreateSecret(ctx context.Context, secret *model.Secret) error {
	if secret.SpaceID == dao.SystemSpaceID {
		return ErrSystemSpace
	}
	return s.secretDAO.Create(ctx, secret)
}

func (s *ServiceImpl) UpdateSecret(ctx context.Context, secret *model.Secret) error {
	if err := s.visible(ctx, secret.SecretID); err != nil {
		return err
	}
	return s.secretDAO.Update(ctx, secret)
}

func (s *ServiceImpl) DeleteSecret(ctx context.Context, secretID string) error {
	if err := s.visible(ctx, secretID); err != nil {
		return err
	}
	return s.secretDAO.Delete(ctx, secretID)
}

func (s *ServiceImpl) GetSecret(ctx context.Context, secretID string) (*model.Secret, error) {
	row, err := s.secretDAO.FindByID(ctx, secretID)
	if err != nil {
		return nil, err
	}
	if row.SpaceID == dao.SystemSpaceID {
		return nil, dao.ErrSecretNotFound
	}
	return row, nil
}

func (s *ServiceImpl) ListSecrets(ctx context.Context, offset, limit int, filters *dao.SecretFilters) ([]model.Secret, int64, error) {
	scoped := dao.SecretFilters{}
	if filters != nil {
		scoped = *filters
	}
	scoped.ExcludeSpaceID = dao.SystemSpaceID
	return s.secretDAO.List(ctx, offset, limit, &scoped)
}

func (s *ServiceImpl) ToggleSecretStatus(ctx context.Context, secretID, status string) error {
	if err := s.visible(ctx, secretID); err != nil {
		return err
	}
	return s.secretDAO.UpdateStatus(ctx, secretID, status)
}
