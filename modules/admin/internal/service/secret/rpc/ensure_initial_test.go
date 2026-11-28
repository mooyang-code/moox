package rpc

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/admin/internal/service/secret/dao"
	"github.com/mooyang-code/moox/modules/admin/internal/service/secret/model"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/admin/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newTestSecretRPCService(t *testing.T) (*Service, *dao.SecretDAO, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	if sqlDB, err := db.DB(); err == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	require.NoError(t, db.Exec(schema.AdminSQL()).Error)
	secretDAO := dao.NewSecretDAO(db, "0123456789abcdef0123456789abcdef")
	return NewService(secretDAO), secretDAO, db
}

func TestEnsureInitialSecret_CreateUnchangedKeptAndRecreate(t *testing.T) {
	svc, secretDAO, _ := newTestSecretRPCService(t)
	ctx := context.Background()

	req := &pb.EnsureInitialSecretReq{
		Secret: &pb.Secret{
			SecretId:    model.TencentDefaultSecretID,
			Name:        "Tencent Cloud Default",
			Category:    "cloud",
			Provider:    "tencent",
			SecretType:  "api_key",
			KeyId:       "AKID-initial",
			SecretValue: "secret-initial",
		},
	}

	// 首次写入：created，并按固定 secret_id 落库。
	rsp, err := svc.EnsureInitialSecret(ctx, req)
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.Equal(t, "created", rsp.GetAction())
	assert.Equal(t, model.TencentDefaultSecretID, rsp.GetSecretId())

	stored, err := secretDAO.FindByID(ctx, model.TencentDefaultSecretID)
	require.NoError(t, err)
	assert.Equal(t, "AKID-initial", stored.KeyID)
	assert.Equal(t, "secret-initial", stored.SecretValue)
	assert.Equal(t, "setup", stored.Creator)
	assert.Equal(t, "active", stored.Status)

	// 重复写入相同内容：unchanged。
	rsp, err = svc.EnsureInitialSecret(ctx, req)
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.Equal(t, "unchanged", rsp.GetAction())

	// 传入不同密文：只建不改，以管理台为准保留，返回 kept，且库里原值未变。
	different := &pb.EnsureInitialSecretReq{
		Secret: &pb.Secret{
			SecretId:    model.TencentDefaultSecretID,
			Name:        "Tencent Cloud Default",
			Category:    "cloud",
			Provider:    "tencent",
			SecretType:  "api_key",
			KeyId:       "AKID-initial",
			SecretValue: "secret-different",
		},
	}
	rsp, err = svc.EnsureInitialSecret(ctx, different)
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.Equal(t, "kept", rsp.GetAction())

	stored, err = secretDAO.FindByID(ctx, model.TencentDefaultSecretID)
	require.NoError(t, err)
	assert.Equal(t, "secret-initial", stored.SecretValue, "已存在时不得覆盖管理台里的值")

	// 硬删除后重新调用：重新创建（不再有墓碑表）。
	require.NoError(t, secretDAO.Delete(ctx, model.TencentDefaultSecretID))
	rsp, err = svc.EnsureInitialSecret(ctx, req)
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.Equal(t, "created", rsp.GetAction())
}

func TestEnsureInitialSecret_Validation(t *testing.T) {
	svc, _, _ := newTestSecretRPCService(t)
	ctx := context.Background()

	cases := map[string]*pb.EnsureInitialSecretReq{
		"空请求":      {},
		"缺少ID":     {Secret: &pb.Secret{Name: "n", Category: "cloud", SecretValue: "v"}},
		"缺少名称":     {Secret: &pb.Secret{SecretId: "id", Category: "cloud", SecretValue: "v"}},
		"缺少密文":     {Secret: &pb.Secret{SecretId: "id", Name: "n", Category: "cloud"}},
		"脱敏密文":     {Secret: &pb.Secret{SecretId: "id", Name: "n", Category: "cloud", SecretValue: "ab••cd"}},
		"无效分类":     {Secret: &pb.Secret{SecretId: "id", Name: "n", Category: "unknown", SecretValue: "v"}},
		"无效类型":     {Secret: &pb.Secret{SecretId: "id", Name: "n", Category: "cloud", SecretType: "bad", SecretValue: "v"}},
		"无效状态":     {Secret: &pb.Secret{SecretId: "id", Name: "n", Category: "cloud", Status: "bad", SecretValue: "v"}},
		"系统SSH密钥":  {Secret: &pb.Secret{SecretId: model.SSHHostSecretID("control"), Name: "ssh", Category: "ssh", SecretValue: "v"}},
		"EventBus": {Secret: &pb.Secret{SecretId: "eb-1", Name: "eb", Category: model.EventBusCategory, Provider: model.EventBusProvider, SecretValue: "v"}},
	}
	for name, req := range cases {
		rsp, err := svc.EnsureInitialSecret(ctx, req)
		require.NoError(t, err, name)
		assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode(), name)
	}
}
