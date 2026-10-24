// Package rpc 提供 secret 对外的 trpc 普通 RPC 服务实现，
// 承载秘钥管理 API（CRUD + 启用/禁用），
// 由主机网关转发原生 tRPC；控制台通过生成的 handler 在 Admin 进程内调用。
package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
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
	"ssh":      true,
	"exchange": true,
	"database": true,
	"cloud":    true,
	"jwt":      true,
	"eventbus": true,
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

// GetSecretValue returns the plaintext value. Gateway route method allowlists protect machine access.
func (s *Service) GetSecretValue(ctx context.Context, req *pb.GetSecretValueReq) (*pb.GetSecretValueRsp, error) {
	if req.GetSecretId() == "" {
		return &pb.GetSecretValueRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "secret_id不能为空")}, nil
	}
	secretRecord, err := s.svc.GetSecret(ctx, req.GetSecretId())
	if err != nil {
		log.ErrorContextf(ctx, "[Secret] GetSecretValue failed: %v", err)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return &pb.GetSecretValueRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, "秘钥不存在")}, nil
		}
		return &pb.GetSecretValueRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "查询秘钥失败")}, nil
	}
	if secretRecord.Status != "" && secretRecord.Status != "active" {
		return &pb.GetSecretValueRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "秘钥未启用")}, nil
	}
	return &pb.GetSecretValueRsp{RetInfo: retOK(), Secret: secretModelToPlainPB(secretRecord)}, nil
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
	if userID, username, err := utils.GetUserInfoFromCtx(ctx); err == nil && username != "" {
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
		// 响应里敏感项（如 OKX passphrase）是脱敏占位，原样提交回来表示保持原值。
		merged, err := mergeExtraConfig(existing.ExtraConfig, extraConfig)
		if err != nil {
			return &pb.UpdateSecretRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
		}
		existing.ExtraConfig = merged
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
		ExtraConfig: redactExtraConfig(s.ExtraConfig),
		Status:      s.Status,
		Creator:     s.Creator,
		CreateTime:  formatTime(s.CreateTime),
		ModifyTime:  formatTime(s.ModifyTime),
	}
}

func secretModelToPlainPB(s *model.Secret) *pb.SecretMaterial {
	if s == nil {
		return nil
	}
	return &pb.SecretMaterial{
		SecretId: s.SecretID, Name: s.Name, Description: s.Description,
		Category: s.Category, Provider: s.Provider, SecretType: s.SecretType,
		KeyId: s.KeyID, SecretValue: s.SecretValue, ExtraConfig: s.ExtraConfig, Status: s.Status,
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

const maskedExtraValue = "******"

var sensitiveExtraKeys = []string{"passphrase", "password", "secret", "token", "private"}

func sensitiveExtraKey(key string) bool {
	key = strings.ToLower(key)
	for _, marker := range sensitiveExtraKeys {
		if strings.Contains(key, marker) {
			return true
		}
	}
	return false
}

// decodeExtraConfig 解析 extra_config（保留数字原样），顶层必须是 JSON 对象。
func decodeExtraConfig(raw string) (map[string]any, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, errors.New("extra_config 不是 JSON 对象")
	}
	return object, nil
}

// redactExtraConfig 把 extra_config 里敏感项（口令、密码、令牌等，含嵌套对象与数组里的）的值替换为占位，只读接口不回显它们；
// 交易模块等机器调用方通过受 ACL 保护的 GetSecretValue 读取明文。无法解析为 JSON 对象时整体隐藏。
func redactExtraConfig(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return raw
	}
	object, err := decodeExtraConfig(raw)
	if err != nil {
		return maskedExtraValue
	}
	redactValue(object)
	redacted, err := json.Marshal(object)
	if err != nil {
		return maskedExtraValue
	}
	return string(redacted)
}

func redactValue(value any) {
	switch node := value.(type) {
	case map[string]any:
		for key, child := range node {
			if sensitiveExtraKey(key) {
				node[key] = maskedExtraValue
				continue
			}
			redactValue(child)
		}
	case []any:
		for _, child := range node {
			redactValue(child)
		}
	}
}

var errUnresolvedMask = errors.New("extra_config 中的敏感项已脱敏且无法确定对应的原值，请重新填写这些凭据")

// mergeExtraConfig 用提交的 extra_config 替换原值；提交值里仍是脱敏占位的敏感项沿用原值，避免保存时把口令改成占位。
// 数组元素没有稳定标识：只有数组长度不变且元素的非敏感内容与原值逐项一致时才按位置沿用；删除、插入、重排后
// 无法确定对应关系，返回错误让用户重新填写，不能把别的元素的凭据存进来。
func mergeExtraConfig(existing, submitted string) (string, error) {
	next, err := decodeExtraConfig(submitted)
	if err != nil {
		return submitted, nil
	}
	previous, _ := decodeExtraConfig(existing)
	if err := mergeValue(next, previous); err != nil {
		return "", err
	}
	merged, err := json.Marshal(next)
	if err != nil {
		return submitted, nil
	}
	return string(merged), nil
}

func mergeValue(next, previous any) error {
	switch node := next.(type) {
	case map[string]any:
		old, _ := previous.(map[string]any)
		for key, child := range node {
			if sensitiveExtraKey(key) {
				if text, ok := child.(string); ok && text == maskedExtraValue {
					value, exists := old[key]
					if !exists {
						return errUnresolvedMask
					}
					node[key] = value
				}
				continue
			}
			if err := mergeValue(child, old[key]); err != nil {
				return err
			}
		}
	case []any:
		old, _ := previous.([]any)
		for index, child := range node {
			if index < len(old) && len(node) == len(old) && reflect.DeepEqual(stripSensitive(child), stripSensitive(old[index])) {
				if err := mergeValue(child, old[index]); err != nil {
					return err
				}
				continue
			}
			if containsMask(child) {
				return errUnresolvedMask
			}
		}
	}
	return nil
}

// stripSensitive 返回去掉敏感项（脱敏占位或明文）后的值，用来判断数组元素是否与原值是同一个元素。
func stripSensitive(value any) any {
	switch node := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(node))
		for key, child := range node {
			if !sensitiveExtraKey(key) {
				out[key] = stripSensitive(child)
			}
		}
		return out
	case []any:
		out := make([]any, len(node))
		for index, child := range node {
			out[index] = stripSensitive(child)
		}
		return out
	}
	return value
}

func containsMask(value any) bool {
	switch node := value.(type) {
	case map[string]any:
		for key, child := range node {
			if sensitiveExtraKey(key) {
				if text, ok := child.(string); ok && text == maskedExtraValue {
					return true
				}
				continue
			}
			if containsMask(child) {
				return true
			}
		}
	case []any:
		for _, child := range node {
			if containsMask(child) {
				return true
			}
		}
	}
	return false
}
