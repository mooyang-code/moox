package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	authmodel "github.com/mooyang-code/moox/modules/admin/internal/service/auth/model"
	"github.com/mooyang-code/moox/modules/admin/internal/service/secret/dao"
	"github.com/mooyang-code/moox/modules/admin/internal/service/secret/model"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go"
)

// memSecrets 是内存里的秘钥服务：明文保存，模拟 DAO 解密之后的视图。
type memSecrets struct {
	byID      map[string]model.Secret
	createErr error
}

func newMemSecrets(records ...model.Secret) *memSecrets {
	m := &memSecrets{byID: make(map[string]model.Secret)}
	for _, record := range records {
		m.byID[record.SecretID] = record
	}
	return m
}

func (m *memSecrets) CreateSecret(_ context.Context, secret *model.Secret) error {
	if m.createErr != nil {
		return m.createErr
	}
	m.byID[secret.SecretID] = *secret
	return nil
}

func (m *memSecrets) UpdateSecret(_ context.Context, secret *model.Secret) error {
	m.byID[secret.SecretID] = *secret
	return nil
}

func (m *memSecrets) DeleteSecret(_ context.Context, secretID string) error {
	delete(m.byID, secretID)
	return nil
}

func (m *memSecrets) GetSecret(_ context.Context, secretID string) (*model.Secret, error) {
	record, ok := m.byID[secretID]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	return &record, nil
}

func (m *memSecrets) ListSecrets(_ context.Context, offset, limit int, _ *dao.SecretFilters) ([]model.Secret, int64, error) {
	ids := make([]string, 0, len(m.byID))
	for id := range m.byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var out []model.Secret
	for i := offset; i < len(ids) && i < offset+limit; i++ {
		out = append(out, m.byID[ids[i]])
	}
	return out, int64(len(ids)), nil
}

func (m *memSecrets) ToggleSecretStatus(_ context.Context, secretID, status string) error {
	record := m.byID[secretID]
	record.Status = status
	m.byID[secretID] = record
	return nil
}

func userCtx(role string) context.Context {
	ctx := trpc.BackgroundContext()
	trpc.SetMetaData(ctx, authmodel.CtxUserID, []byte("u-1"))
	trpc.SetMetaData(ctx, authmodel.CtxUsername, []byte("ops"))
	trpc.SetMetaData(ctx, authmodel.CtxUserRole, []byte(role))
	return ctx
}

const testPassphrase = "迁移口令-correct-horse"

// 导出全部秘钥（含明文、停用的也导出），不导出网关密钥环和配置备份；迁移包里只有密文，换口令解不开。
// 导入到另一套环境保留 secret_id 与状态，创建人记为导入的管理员。
func TestExportThenImportSecretsKeepsIDsAndValues(t *testing.T) {
	source := newMemSecrets(
		model.Secret{SecretID: "sec_okx", Name: "OKX 主账户", Category: "exchange", Provider: "okx", SecretType: "api_key", KeyID: "ak-1",
			SecretValue: "plain-okx-secret", ExtraConfig: `{"passphrase":"p@ss"}`, Status: "active", Creator: "alice"},
		model.Secret{SecretID: "sec_ssh", Name: "跳板机", Category: "ssh", SecretType: "ssh_key", SecretValue: "-----BEGIN KEY-----", Status: "inactive"},
		model.Secret{SecretID: model.ConfigBackupID, Name: "MooX 配置备份", Category: "other", SecretValue: "MOOX-CONFIG-BACKUP v1\n..."},
		model.Secret{SecretID: model.GatewayKeyringIDPrefix + "console", Name: "Gateway", Category: "other", SecretType: model.GatewayKeyringType, SecretValue: "k"},
		model.Secret{SpaceID: "mooxsys", SecretID: "eb-admin", Name: "EventBus internal-admin", Category: "eventbus", Provider: "moox_eventbus",
			SecretType: "token", KeyID: "eventbus_internal_admin", SecretValue: "source-env-token"},
	)
	exported, err := NewService(source).ExportSecrets(userCtx("2"), &pb.ExportSecretsReq{Passphrase: testPassphrase})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, exported.GetRetInfo().GetCode(), exported.GetRetInfo().GetMsg())
	require.EqualValues(t, 2, exported.GetCount())
	require.NotContains(t, exported.GetBundle(), "plain-okx-secret", "迁移包里没有明文")
	// 密文是随机 base64，短片段（如 "OKX"）会偶然出现；用含空格和中文的完整名称，base64 字母表里不可能有。
	require.NotContains(t, exported.GetBundle(), "OKX 主账户")
	// EventBus 内部凭据由部署工具在 mooxsys 空间生成、按 key_id 读取：带到另一套环境会和那里的同名令牌冲突，不导出。
	var outer map[string]any
	require.NoError(t, json.Unmarshal([]byte(exported.GetBundle()), &outer))
	require.Equal(t, bundleFormat, outer["format"])

	target := newMemSecrets(model.Secret{SecretID: "sec_other", Name: "目标环境已有", Category: "other", SecretValue: "x"})
	service := NewService(target)
	wrong, err := service.ImportSecrets(userCtx("2"), &pb.ImportSecretsReq{Bundle: exported.GetBundle(), Passphrase: "another passphrase"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, wrong.GetRetInfo().GetCode())
	require.Equal(t, errBundleDecrypt.Error(), wrong.GetRetInfo().GetMsg())
	require.Len(t, target.byID, 1, "口令错误什么都不写")

	imported, err := service.ImportSecrets(userCtx("3"), &pb.ImportSecretsReq{Bundle: exported.GetBundle(), Passphrase: testPassphrase})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, imported.GetRetInfo().GetCode())
	require.EqualValues(t, 2, imported.GetCreated())
	require.Empty(t, imported.GetSkipped())
	require.Empty(t, imported.GetFailed())
	okx := target.byID["sec_okx"]
	require.Equal(t, "plain-okx-secret", okx.SecretValue)
	require.Equal(t, `{"passphrase":"p@ss"}`, okx.ExtraConfig)
	require.Equal(t, "ak-1", okx.KeyID)
	require.Equal(t, "ops", okx.Creator)
	require.Equal(t, "inactive", target.byID["sec_ssh"].Status)
	require.NotContains(t, target.byID, model.ConfigBackupID)
}

// 同一 secret_id 已存在：默认跳过，overwrite 时用迁移包里的内容覆盖。
func TestImportSecretsSkipsOrOverwritesExisting(t *testing.T) {
	bundle, err := sealBundle([]bundleSecret{
		{SecretID: "sec_a", Name: "A", Category: "exchange", SecretValue: "new-a", Status: "active"},
		{SecretID: "sec_b", Name: "B", Category: "exchange", SecretValue: "new-b"},
	}, testPassphrase, time.Now())
	require.NoError(t, err)
	target := newMemSecrets(model.Secret{SecretID: "sec_a", Name: "A-old", Category: "exchange", SecretValue: "old-a", Creator: "alice", Status: "inactive"})
	service := NewService(target)

	rsp, err := service.ImportSecrets(userCtx("2"), &pb.ImportSecretsReq{Bundle: bundle, Passphrase: testPassphrase})
	require.NoError(t, err)
	require.EqualValues(t, 1, rsp.GetCreated())
	require.Equal(t, []string{"A"}, rsp.GetSkipped())
	require.Equal(t, "old-a", target.byID["sec_a"].SecretValue)
	require.Equal(t, "active", target.byID["sec_b"].Status, "没有状态时默认启用")
	require.Equal(t, "api_key", target.byID["sec_b"].SecretType)

	rsp, err = service.ImportSecrets(userCtx("2"), &pb.ImportSecretsReq{Bundle: bundle, Passphrase: testPassphrase, Overwrite: true})
	require.NoError(t, err)
	require.EqualValues(t, 2, rsp.GetUpdated())
	updated := target.byID["sec_a"]
	require.Equal(t, "new-a", updated.SecretValue)
	require.Equal(t, "A", updated.Name)
	require.Equal(t, "active", updated.Status)
	require.Equal(t, "alice", updated.Creator, "覆盖不改创建人")
}

// 单个秘钥不合法或写入失败不影响其余，结果里列出原因；系统秘钥不导入。
func TestImportSecretsReportsInvalidItems(t *testing.T) {
	bundle, err := sealBundle([]bundleSecret{
		{SecretID: "sec_ok", Name: "OK", Category: "exchange", SecretValue: "v"},
		{SecretID: "sec_masked", Name: "Masked", Category: "exchange", SecretValue: "ab" + maskChar + "cd"},
		{SecretID: "sec_cat", Name: "BadCategory", Category: "nope", SecretValue: "v"},
		{SecretID: model.ConfigBackupID, Name: "Backup", Category: "other", SecretValue: "v"},
		{Name: "NoID", Category: "other", SecretValue: "v"},
		{SecretID: "eb-admin", Name: "EventBus", Category: "eventbus", Provider: "moox_eventbus", KeyID: "eventbus_internal_admin", SecretValue: "t"},
	}, testPassphrase, time.Now())
	require.NoError(t, err)
	target := newMemSecrets()
	rsp, err := NewService(target).ImportSecrets(userCtx("2"), &pb.ImportSecretsReq{Bundle: bundle, Passphrase: testPassphrase})
	require.NoError(t, err)
	require.EqualValues(t, 2, rsp.GetCreated())
	require.Equal(t, []string{"Masked：秘钥值是脱敏后的值", "BadCategory：分类无效", "Backup：系统秘钥不导入", "EventBus：系统秘钥不导入"}, rsp.GetFailed())
	require.Len(t, target.byID, 2)
	require.Contains(t, target.byID, "sec_ok")
	for id, record := range target.byID {
		if record.Name == "NoID" {
			require.NotEmpty(t, strings.TrimSpace(id), "没有 secret_id 时生成新的")
		}
	}

	target.createErr = errors.New("unique constraint")
	rsp, err = NewService(target).ImportSecrets(userCtx("2"), &pb.ImportSecretsReq{Bundle: bundle, Passphrase: testPassphrase})
	require.NoError(t, err)
	require.Contains(t, rsp.GetFailed(), "NoID：创建失败")
}

// 只有全局管理员可以导出、导入；导出口令太短拒绝。
func TestSecretMigrationRequiresGlobalAdminAndStrongPassphrase(t *testing.T) {
	service := NewService(newMemSecrets(model.Secret{SecretID: "sec_a", Name: "A", Category: "other", SecretValue: "v"}))
	for _, ctx := range []context.Context{userCtx("1"), trpc.BackgroundContext()} {
		exported, err := service.ExportSecrets(ctx, &pb.ExportSecretsReq{Passphrase: testPassphrase})
		require.NoError(t, err)
		require.Equal(t, pb.ErrorCode_NO_PERMISSION, exported.GetRetInfo().GetCode())
		require.Empty(t, exported.GetBundle())
		imported, err := service.ImportSecrets(ctx, &pb.ImportSecretsReq{Bundle: "{}", Passphrase: testPassphrase})
		require.NoError(t, err)
		require.Equal(t, pb.ErrorCode_NO_PERMISSION, imported.GetRetInfo().GetCode())
	}
	short, err := service.ExportSecrets(userCtx("2"), &pb.ExportSecretsReq{Passphrase: "short-口令"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, short.GetRetInfo().GetCode())
	require.Empty(t, short.GetBundle())
}

// 迁移包外层元数据参与认证：改动数量、KDF 参数或密文都解不开；格式不对、迭代次数离谱的直接拒绝。
func TestOpenBundleRejectsTamperedOrForeignFiles(t *testing.T) {
	raw, err := sealBundle([]bundleSecret{{SecretID: "s", Name: "n", Category: "other", SecretValue: "v"}}, testPassphrase, time.Now())
	require.NoError(t, err)
	secrets, err := openBundle(raw, testPassphrase)
	require.NoError(t, err)
	require.Equal(t, "v", secrets[0].SecretValue)

	tamper := func(change func(*secretBundle)) string {
		var bundle secretBundle
		require.NoError(t, json.Unmarshal([]byte(raw), &bundle))
		change(&bundle)
		out, err := json.Marshal(bundle)
		require.NoError(t, err)
		return string(out)
	}
	_, err = openBundle(tamper(func(b *secretBundle) { b.Count = 9 }), testPassphrase)
	require.ErrorIs(t, err, errBundleDecrypt)
	_, err = openBundle(tamper(func(b *secretBundle) { b.KDF.Iterations = minBundleIterations }), testPassphrase)
	require.ErrorIs(t, err, errBundleDecrypt)
	_, err = openBundle(tamper(func(b *secretBundle) { b.KDF.Iterations = maxBundleIterations + 1 }), testPassphrase)
	require.ErrorIs(t, err, errBundleInvalid)
	_, err = openBundle(tamper(func(b *secretBundle) { b.Format = "other" }), testPassphrase)
	require.ErrorIs(t, err, errBundleInvalid)
	_, err = openBundle("not json", testPassphrase)
	require.ErrorIs(t, err, errBundleInvalid)
	_, err = openBundle(raw, "")
	require.ErrorIs(t, err, errEmptyPassphrase)
	_, err = sealBundle(nil, "short", time.Now())
	require.ErrorIs(t, err, errWeakPassphrase)
}
