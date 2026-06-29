// Package rpc 提供 secret 对外的 trpc 普通 RPC 服务实现，
// 承载秘钥管理 API（CRUD + 启用/禁用），
// 由统一 HTTP 转发层（/api/admin/secret/{method}）调度。
package rpc

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/service/auth/utils"
	secret "github.com/mooyang-code/moox/modules/admin/internal/service/secret"
	"github.com/mooyang-code/moox/modules/admin/internal/service/secret/dao"
	"github.com/mooyang-code/moox/modules/admin/internal/service/secret/model"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"gorm.io/gorm"

	"trpc.group/trpc-go/trpc-go/log"
)

// 合法的秘钥分类
var validCategories = map[string]bool{
	"cloud":    true,
	"ssh":      true,
	"exchange": true,
	"database": true,
	"jwt":      true,
	"other":    true,
}

// 合法的秘钥类型
var validSecretTypes = map[string]bool{
	"api_key":     true,
	"password":    true,
	"token":       true,
	"certificate": true,
	"ssh_key":     true,
	"other":       true,
}

// Service 实现 pb.SecretMgrService，承载秘钥管理 API 的业务逻辑。
type Service struct {
	pb.UnimplementedSecretMgr
	svc secret.Service
}

// NewService 创建 SecretMgr RPC 实现。
func NewService(svc secret.Service) *Service {
	return &Service{svc: svc}
}

// ========== 秘钥 CRUD ==========

// ListSecrets 列出秘钥。
func (s *Service) ListSecrets(ctx context.Context, req *pb.ListSecretsReq) (*pb.ListSecretsRsp, error) {
	filters := &dao.SecretFilters{
		Keyword:  req.GetKeyword(),
		Category: req.GetCategory(),
		Provider: req.GetProvider(),
		Status:   req.GetStatus(),
	}
	secrets, total, err := s.svc.ListSecrets(ctx, int(req.GetOffset()), int(req.GetLimit()), filters)
	if err != nil {
		log.ErrorContextf(ctx, "[Secret] ListSecrets failed: %v", err)
		return &pb.ListSecretsRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "查询秘钥列表失败")}, nil
	}
	pbSecrets := make([]*pb.Secret, 0, len(secrets))
	for i := range secrets {
		pbSecrets = append(pbSecrets, secretModelToPB(&secrets[i]))
	}
	return &pb.ListSecretsRsp{
		RetInfo: retOK(),
		Secrets: pbSecrets,
		Total:   total,
	}, nil
}

// GetSecret 获取秘钥详情（脱敏）。
func (s *Service) GetSecret(ctx context.Context, req *pb.GetSecretReq) (*pb.GetSecretRsp, error) {
	if req.GetSecretId() == "" {
		return &pb.GetSecretRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "secret_id不能为空")}, nil
	}
	secretRecord, err := s.svc.GetSecret(ctx, req.GetSecretId())
	if err != nil {
		log.ErrorContextf(ctx, "[Secret] GetSecret failed: %v", err)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return &pb.GetSecretRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, "秘钥不存在")}, nil
		}
		return &pb.GetSecretRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "查询秘钥失败")}, nil
	}
	return &pb.GetSecretRsp{RetInfo: retOK(), Secret: secretModelToPB(secretRecord)}, nil
}

// CreateSecret 创建秘钥。
func (s *Service) CreateSecret(ctx context.Context, req *pb.CreateSecretReq) (*pb.CreateSecretRsp, error) {
	secretRecord := secretPBToModel(req.GetSecret())
	if secretRecord == nil {
		return &pb.CreateSecretRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "秘钥配置不能为空")}, nil
	}
	if secretRecord.Name == "" {
		return &pb.CreateSecretRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "秘钥名称不能为空")}, nil
	}
	if secretRecord.SecretValue == "" {
		return &pb.CreateSecretRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "秘钥值不能为空")}, nil
	}
	if isMaskedSecret(secretRecord.SecretValue) {
		return &pb.CreateSecretRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "秘钥值疑似脱敏后的值，请输入明文")}, nil
	}
	if !validCategories[secretRecord.Category] {
		return &pb.CreateSecretRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "秘钥分类无效")}, nil
	}
	if secretRecord.SecretType != "" && !validSecretTypes[secretRecord.SecretType] {
		return &pb.CreateSecretRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "秘钥类型无效")}, nil
	}
	// 确保默认值
	if secretRecord.SecretType == "" {
		secretRecord.SecretType = "api_key"
	}
	if secretRecord.Status == "" {
		secretRecord.Status = "active"
	}
	// 从 JWT 自动填充 creator，忽略客户端传入值
	if userID, username, _, err := utils.GetUserInfoFromCtx(ctx); err == nil && username != "" {
		secretRecord.Creator = username
	} else if userID != "" {
		secretRecord.Creator = userID
	}
	secretRecord.SecretID = dao.GenerateSecretID()
	if err := s.svc.CreateSecret(ctx, secretRecord); err != nil {
		log.ErrorContextf(ctx, "[Secret] CreateSecret failed: %v", err)
		if errors.Is(err, dao.ErrMaskedValue) {
			return &pb.CreateSecretRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "秘钥值疑似脱敏后的值，请输入明文")}, nil
		}
		return &pb.CreateSecretRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "创建秘钥失败")}, nil
	}
	return &pb.CreateSecretRsp{RetInfo: retOK(), SecretId: secretRecord.SecretID}, nil
}

// UpdateSecret 更新秘钥。
func (s *Service) UpdateSecret(ctx context.Context, req *pb.UpdateSecretReq) (*pb.UpdateSecretRsp, error) {
	if req.GetSecretId() == "" {
		return &pb.UpdateSecretRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "secret_id不能为空")}, nil
	}

	// 先查出现有记录
	existing, err := s.svc.GetSecret(ctx, req.GetSecretId())
	if err != nil {
		log.ErrorContextf(ctx, "[Secret] UpdateSecret GetSecret failed: %v", err)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return &pb.UpdateSecretRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, "秘钥不存在")}, nil
		}
		return &pb.UpdateSecretRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "查询秘钥失败")}, nil
	}

	// 逐字段更新：空值表示不修改
	if name := req.GetName(); name != "" {
		existing.Name = name
	}
	if description := req.GetDescription(); description != "" {
		existing.Description = description
	}
	if keyID := req.GetKeyId(); keyID != "" {
		existing.KeyID = keyID
	}
	if extraConfig := req.GetExtraConfig(); extraConfig != "" {
		existing.ExtraConfig = extraConfig
	}
	if category := req.GetCategory(); category != "" {
		if !validCategories[category] {
			return &pb.UpdateSecretRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "秘钥分类无效")}, nil
		}
		existing.Category = category
	}
	if provider := req.GetProvider(); provider != "" {
		existing.Provider = provider
	}
	if secretType := req.GetSecretType(); secretType != "" {
		if !validSecretTypes[secretType] {
			return &pb.UpdateSecretRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "秘钥类型无效")}, nil
		}
		existing.SecretType = secretType
	}

	// secret_value 为空表示不修改秘钥值
	if secretValue := req.GetSecretValue(); secretValue != "" {
		// 拒绝脱敏值，防止脱敏串被当明文重新加密入库
		if isMaskedSecret(secretValue) {
			return &pb.UpdateSecretRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "秘钥值疑似脱敏后的值，请重新输入明文")}, nil
		}
		existing.SecretValue = secretValue
	}

	if err := s.svc.UpdateSecret(ctx, existing); err != nil {
		log.ErrorContextf(ctx, "[Secret] UpdateSecret failed: %v", err)
		if errors.Is(err, dao.ErrSecretNotFound) {
			return &pb.UpdateSecretRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, "秘钥不存在")}, nil
		}
		if errors.Is(err, dao.ErrMaskedValue) {
			return &pb.UpdateSecretRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "秘钥值疑似脱敏后的值，请输入明文")}, nil
		}
		return &pb.UpdateSecretRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "更新秘钥失败")}, nil
	}
	return &pb.UpdateSecretRsp{RetInfo: retOK()}, nil
}

// DeleteSecret 删除秘钥（软删除）。
func (s *Service) DeleteSecret(ctx context.Context, req *pb.DeleteSecretReq) (*pb.DeleteSecretRsp, error) {
	if req.GetSecretId() == "" {
		return &pb.DeleteSecretRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "secret_id不能为空")}, nil
	}
	if err := s.svc.DeleteSecret(ctx, req.GetSecretId()); err != nil {
		log.ErrorContextf(ctx, "[Secret] DeleteSecret failed: %v", err)
		if errors.Is(err, dao.ErrSecretNotFound) {
			return &pb.DeleteSecretRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, "秘钥不存在或已被删除")}, nil
		}
		return &pb.DeleteSecretRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "删除秘钥失败")}, nil
	}
	return &pb.DeleteSecretRsp{RetInfo: retOK()}, nil
}

// ToggleSecretStatus 启用/禁用秘钥。
func (s *Service) ToggleSecretStatus(ctx context.Context, req *pb.ToggleSecretStatusReq) (*pb.ToggleSecretStatusRsp, error) {
	if req.GetSecretId() == "" {
		return &pb.ToggleSecretStatusRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "secret_id不能为空")}, nil
	}
	status := req.GetStatus()
	if status != "active" && status != "inactive" {
		return &pb.ToggleSecretStatusRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "状态值无效，仅支持 active/inactive")}, nil
	}
	if err := s.svc.ToggleSecretStatus(ctx, req.GetSecretId(), status); err != nil {
		log.ErrorContextf(ctx, "[Secret] ToggleSecretStatus failed: %v", err)
		if errors.Is(err, dao.ErrSecretNotFound) {
			return &pb.ToggleSecretStatusRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, "秘钥不存在或已被删除")}, nil
		}
		return &pb.ToggleSecretStatusRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "更新秘钥状态失败")}, nil
	}
	return &pb.ToggleSecretStatusRsp{RetInfo: retOK()}, nil
}

// ========== 辅助函数 ==========

func retOK() *pb.RetInfo {
	return &pb.RetInfo{Code: pb.ErrorCode_SUCCESS, Msg: "success"}
}

func retErr(code pb.ErrorCode, msg string) *pb.RetInfo {
	return &pb.RetInfo{Code: code, Msg: msg}
}

// maskChar 脱敏替换字符，使用 • (U+2022 BULLET) 而非 *，
// 避免真实秘钥中可能出现的 * 被误判为脱敏串。
const maskChar = "•"

// isMaskedSecret 判断值是否为脱敏串（含 • 字符）。
// 真实秘钥（API Key、密码、Token、证书、SSH Key）不会包含 •。
func isMaskedSecret(value string) bool {
	return strings.Contains(value, maskChar)
}

// secretModelToPB model.Secret → pb.Secret，secret_value 脱敏处理。
func secretModelToPB(s *model.Secret) *pb.Secret {
	if s == nil {
		return nil
	}
	return &pb.Secret{
		Id:          int32(s.ID),
		SecretId:    s.SecretID,
		Name:        s.Name,
		Description: s.Description,
		Category:    s.Category,
		Provider:    s.Provider,
		SecretType:  s.SecretType,
		KeyId:       s.KeyID,
		SecretValue: maskSecretValue(s.SecretValue),
		ExtraConfig: s.ExtraConfig,
		Status:      s.Status,
		LastUsedAt:  formatTimePtr(s.LastUsedAt),
		LastUsedBy:  s.LastUsedBy,
		Creator:     s.Creator,
		CreateTime:  formatTime(s.CreateTime),
		ModifyTime:  formatTime(s.ModifyTime),
	}
}

// secretPBToModel pb.Secret → model.Secret（创建时用，不含 ID/时间戳）。
func secretPBToModel(s *pb.Secret) *model.Secret {
	if s == nil {
		return nil
	}
	return &model.Secret{
		SecretID:    s.GetSecretId(),
		Name:        s.GetName(),
		Description: s.GetDescription(),
		Category:    s.GetCategory(),
		Provider:    s.GetProvider(),
		SecretType:  s.GetSecretType(),
		KeyID:       s.GetKeyId(),
		SecretValue: s.GetSecretValue(),
		ExtraConfig: s.GetExtraConfig(),
		Status:      s.GetStatus(),
		Creator:     s.GetCreator(),
	}
}

// maskSecretValue 对秘钥值进行脱敏处理。
// 使用 • (U+2022 BULLET) 作为遮盖字符，避免与真实秘钥中的 * 混淆。
// 规则：
//
//	≤4 字符:  全部遮盖
//	5~8 字符: 保留前2+后2，中间遮盖
//	9~16 字符: 保留前4+后4，中间遮盖
//	>16 字符:  保留前4+后4，中间遮盖
func maskSecretValue(value string) string {
	if value == "" {
		return ""
	}
	n := len(value)
	if n <= 4 {
		return strings.Repeat(maskChar, n)
	}
	prefix, suffix := 2, 2
	if n > 8 {
		prefix, suffix = 4, 4
	}
	if n <= prefix+suffix {
		return strings.Repeat(maskChar, n)
	}
	return value[:prefix] + strings.Repeat(maskChar, n-prefix-suffix) + value[n-suffix:]
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02 15:04:05")
}

func formatTimePtr(t *time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02 15:04:05")
}
