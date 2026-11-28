package rpc

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mooyang-code/moox/modules/admin/internal/service/auth"
	"github.com/mooyang-code/moox/modules/admin/internal/service/secret/dao"
	"github.com/mooyang-code/moox/modules/admin/internal/service/secret/model"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"gorm.io/gorm"

	"trpc.group/trpc-go/trpc-go/log"
)

// exportPageSize 是导出时分页读取秘钥的批量。
const exportPageSize = 200

// ExportSecrets 把全部秘钥（含明文）加密成迁移包，方便迁移到另一套环境。属于本环境的秘钥（系统空间里的网关密钥环、
// EventBus 内部凭据，以及配置备份，见 model.Secret.EnvironmentBound）不导出。
// 调用权限由服务目录控制（console 与 moox-cli）。
func (s *Service) ExportSecrets(ctx context.Context, req *pb.ExportSecretsReq) (*pb.ExportSecretsRsp, error) {
	actor := migrationActor(ctx)
	if utf8.RuneCountInString(req.GetPassphrase()) < minPassphraseRunes {
		return &pb.ExportSecretsRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, errWeakPassphrase.Error())}, nil
	}
	records, err := s.allSecrets(ctx)
	if err != nil {
		log.ErrorContextf(ctx, "[Secret] ExportSecrets list failed: %v", err)
		return &pb.ExportSecretsRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "读取秘钥失败")}, nil
	}
	items := make([]bundleSecret, 0, len(records))
	for _, record := range records {
		if record.EnvironmentBound() {
			continue
		}
		items = append(items, bundleSecret{
			SecretID: record.SecretID, Name: record.Name, Description: record.Description, Category: record.Category,
			Provider: record.Provider, SecretType: record.SecretType, KeyID: record.KeyID, SecretValue: record.SecretValue,
			ExtraConfig: record.ExtraConfig, Status: record.Status,
		})
	}
	bundle, err := sealBundle(items, req.GetPassphrase(), time.Now())
	if err != nil {
		log.ErrorContextf(ctx, "[Secret] ExportSecrets seal failed: %v", err)
		return &pb.ExportSecretsRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "生成迁移包失败")}, nil
	}
	log.InfoContextf(ctx, "[Secret] ExportSecrets actor=%s count=%d", actor, len(items))
	return &pb.ExportSecretsRsp{RetInfo: retOK(), Bundle: bundle, Count: int32(len(items))}, nil
}

// ImportSecrets 用导出口令解开迁移包并逐个导入：保留原 secret_id，引用它的配置（交易账户、云账户等）迁移后仍然有效。
// 同一 secret_id 已存在时按 overwrite 覆盖或跳过；单个秘钥失败不影响其余，结果里列出原因。
// 调用权限由服务目录控制（console 与 moox-cli）。
func (s *Service) ImportSecrets(ctx context.Context, req *pb.ImportSecretsReq) (*pb.ImportSecretsRsp, error) {
	actor := migrationActor(ctx)
	items, err := openBundle(req.GetBundle(), req.GetPassphrase())
	if err != nil {
		var known bool
		for _, expected := range []error{errBundleInvalid, errBundleDecrypt, errEmptyPassphrase} {
			known = known || errors.Is(err, expected)
		}
		if !known {
			log.ErrorContextf(ctx, "[Secret] ImportSecrets open failed: %v", err)
			return &pb.ImportSecretsRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "解开迁移包失败")}, nil
		}
		return &pb.ImportSecretsRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	rsp := &pb.ImportSecretsRsp{RetInfo: retOK()}
	for _, item := range items {
		label := strings.TrimSpace(item.Name)
		if label == "" {
			label = item.SecretID
		}
		record, reason := importRecord(item)
		if reason != "" {
			rsp.Failed = append(rsp.Failed, label+"："+reason)
			continue
		}
		existing, err := s.store.FindByID(ctx, record.SecretID)
		switch {
		case err == nil && !req.GetOverwrite():
			rsp.Skipped = append(rsp.Skipped, label)
		case err == nil:
			existing.Name, existing.Description, existing.Category, existing.Provider = record.Name, record.Description, record.Category, record.Provider
			existing.SecretType, existing.KeyID, existing.SecretValue = record.SecretType, record.KeyID, record.SecretValue
			existing.ExtraConfig, existing.Status = record.ExtraConfig, record.Status
			if err := s.store.Update(ctx, existing); err != nil {
				log.ErrorContextf(ctx, "[Secret] ImportSecrets update %s failed: %v", record.SecretID, err)
				rsp.Failed = append(rsp.Failed, label+"：覆盖失败")
				continue
			}
			rsp.Updated++
		case errors.Is(err, gorm.ErrRecordNotFound):
			record.Creator = actor
			if err := s.store.Create(ctx, record); err != nil {
				log.ErrorContextf(ctx, "[Secret] ImportSecrets create %s failed: %v", record.SecretID, err)
				rsp.Failed = append(rsp.Failed, label+"：创建失败")
				continue
			}
			rsp.Created++
		default:
			log.ErrorContextf(ctx, "[Secret] ImportSecrets lookup %s failed: %v", record.SecretID, err)
			rsp.Failed = append(rsp.Failed, label+"：查询失败")
		}
	}
	log.InfoContextf(ctx, "[Secret] ImportSecrets actor=%s total=%d created=%d updated=%d skipped=%d failed=%d overwrite=%t",
		actor, len(items), rsp.Created, rsp.Updated, len(rsp.Skipped), len(rsp.Failed), req.GetOverwrite())
	return rsp, nil
}

// migrationActor 返回导出或导入的操作者标识：管理台登录请求取用户名（无用户名时取用户 ID）；
// 无登录身份时取网关写入的调用方名，均为空则记为服务目录放行的非登录调用方 moox-cli。
func migrationActor(ctx context.Context) string {
	if userID, username, err := auth.GetUserInfoFromCtx(ctx); err == nil {
		if username != "" {
			return username
		}
		if userID != "" {
			return userID
		}
	}
	if caller := strings.TrimSpace(componentCaller(ctx)); caller != "" {
		return caller
	}
	return servicecatalog.OperatorMooxCLI
}

// allSecrets 分页读出全部（未删除、非系统）秘钥的明文，按 secret_id 去重。
func (s *Service) allSecrets(ctx context.Context) ([]model.Secret, error) {
	var out []model.Secret
	seen := make(map[string]struct{})
	for offset := 0; ; offset += exportPageSize {
		page, total, err := s.store.List(ctx, offset, exportPageSize, &dao.SecretFilters{})
		if err != nil {
			return nil, err
		}
		for _, record := range page {
			if _, ok := seen[record.SecretID]; ok {
				continue
			}
			seen[record.SecretID] = struct{}{}
			out = append(out, record)
		}
		if len(page) < exportPageSize || int64(offset+exportPageSize) >= total {
			return out, nil
		}
	}
}

// importRecord 校验迁移包里的一个秘钥并转成待写入的记录；不合法时返回原因。
func importRecord(item bundleSecret) (*model.Secret, string) {
	record := &model.Secret{
		SecretID: strings.TrimSpace(item.SecretID), Name: strings.TrimSpace(item.Name), Description: item.Description,
		Category: item.Category, Provider: item.Provider, SecretType: item.SecretType, KeyID: item.KeyID,
		SecretValue: item.SecretValue, ExtraConfig: item.ExtraConfig, Status: item.Status,
	}
	switch {
	case record.Name == "":
		return nil, "名称为空"
	case record.SecretValue == "":
		return nil, "秘钥值为空"
	case isMaskedSecret(record.SecretValue):
		return nil, "秘钥值是脱敏后的值"
	case !validCategories[record.Category]:
		return nil, "分类无效"
	case record.SecretType != "" && !validSecretTypes[record.SecretType]:
		return nil, "类型无效"
	case record.Status != "" && record.Status != "active" && record.Status != "inactive":
		return nil, "状态无效"
	}
	if record.SecretID == "" {
		record.SecretID = dao.GenerateSecretID()
	}
	if record.EnvironmentBound() {
		return nil, "系统秘钥不导入"
	}
	if record.SecretType == "" {
		record.SecretType = "api_key"
	}
	if record.Status == "" {
		record.Status = "active"
	}
	if strings.TrimSpace(record.ExtraConfig) == "" {
		record.ExtraConfig = "{}"
	}
	return record, ""
}
