package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/service/auth"
	"github.com/mooyang-code/moox/modules/admin/internal/service/secret/dao"
	"github.com/mooyang-code/moox/modules/admin/internal/service/secret/model"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/servicecatalog"
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

func (m *memSecrets) Create(_ context.Context, secret *model.Secret) error {
	if m.createErr != nil {
		return m.createErr
	}
	m.byID[secret.SecretID] = *secret
	return nil
}

func (m *memSecrets) Update(_ context.Context, secret *model.Secret) error {
	m.byID[secret.SecretID] = *secret
	return nil
}

func (m *memSecrets) Delete(_ context.Context, secretID string) error {
	delete(m.byID, secretID)
	return nil
}

func (m *memSecrets) FindByID(_ context.Context, secretID string) (*model.Secret, error) {
	record, ok := m.byID[secretID]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	return &record, nil
}

func (m *memSecrets) List(_ context.Context, offset, limit int, _ *dao.SecretFilters) ([]model.Secret, int64, error) {
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

func (m *memSecrets) UpdateStatus(_ context.Context, secretID, status string) error {
	record := m.byID[secretID]
	record.Status = status
	m.byID[secretID] = record
	return nil
}

func userCtx() context.Context {
	ctx := trpc.BackgroundContext()
	trpc.SetMetaData(ctx, auth.CtxUserID, []byte("u-1"))
	trpc.SetMetaData(ctx, auth.CtxUsername, []byte("ops"))
	return ctx
}

const testPassphrase = "迁移口令-correct-horse"

// 导出全部秘钥（含明文、停用的也导出），不导出主机 SSH 凭据与 EventBus 内部凭据；迁移包里只有密文，换口令解不开。
// 导入到另一套环境保留 secret_id 与状态，创建人记为导入的管理员。
func TestExportThenImportSecretsKeepsIDsAndValues(t *testing.T) {
	source := newMemSecrets(
		model.Secret{SecretID: "sec_okx", Name: "OKX 主账户", Category: "exchange", Provider: "okx", SecretType: "api_key", KeyID: "ak-1",
			SecretValue: "plain-okx-secret", ExtraConfig: `{"passphrase":"p@ss"}`, Status: "active", Creator: "alice"},
		model.Secret{SecretID: "sec_ssh", Name: "跳板机", Category: "ssh", SecretType: "ssh_key", SecretValue: "-----BEGIN KEY-----", Status: "inactive"},
		model.Secret{SecretID: model.SSHHostSecretID("storage"), Name: "SSH", Category: model.SSHCategory, SecretType: "password", SecretValue: "k"},
		model.Secret{SecretID: "eb-admin", Name: "EventBus internal-admin", Category: "eventbus", Provider: "moox_eventbus",
			SecretType: "token", KeyID: "eventbus_internal_admin", SecretValue: "source-env-token"},
	)
	exported, err := NewService(source).ExportSecrets(userCtx(), &pb.ExportSecretsReq{Passphrase: testPassphrase})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, exported.GetRetInfo().GetCode(), exported.GetRetInfo().GetMsg())
	require.EqualValues(t, 2, exported.GetCount())
	require.NotContains(t, exported.GetBundle(), "plain-okx-secret", "迁移包里没有明文")
	// 密文是随机 base64，短片段（如 "OKX"）会偶然出现；用含空格和中文的完整名称，base64 字母表里不可能有。
	require.NotContains(t, exported.GetBundle(), "OKX 主账户")
	// EventBus 内部凭据由部署工具生成、按 key_id 读取：带到另一套环境会和那里的同名令牌冲突，不导出。
	var outer map[string]any
	require.NoError(t, json.Unmarshal([]byte(exported.GetBundle()), &outer))
	require.Equal(t, bundleFormat, outer["format"])

	target := newMemSecrets(model.Secret{SecretID: "sec_other", Name: "目标环境已有", Category: "other", SecretValue: "x"})
	service := NewService(target)
	wrong, err := service.ImportSecrets(userCtx(), &pb.ImportSecretsReq{Bundle: exported.GetBundle(), Passphrase: "another passphrase"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, wrong.GetRetInfo().GetCode())
	require.Equal(t, errBundleDecrypt.Error(), wrong.GetRetInfo().GetMsg())
	require.Len(t, target.byID, 1, "口令错误什么都不写")

	imported, err := service.ImportSecrets(userCtx(), &pb.ImportSecretsReq{Bundle: exported.GetBundle(), Passphrase: testPassphrase})
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
	require.NotContains(t, target.byID, "eb-admin")
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

	rsp, err := service.ImportSecrets(userCtx(), &pb.ImportSecretsReq{Bundle: bundle, Passphrase: testPassphrase})
	require.NoError(t, err)
	require.EqualValues(t, 1, rsp.GetCreated())
	require.Equal(t, []string{"A"}, rsp.GetSkipped())
	require.Equal(t, "old-a", target.byID["sec_a"].SecretValue)
	require.Equal(t, "active", target.byID["sec_b"].Status, "没有状态时默认启用")
	require.Equal(t, "api_key", target.byID["sec_b"].SecretType)

	rsp, err = service.ImportSecrets(userCtx(), &pb.ImportSecretsReq{Bundle: bundle, Passphrase: testPassphrase, Overwrite: true})
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
		{Name: "NoID", Category: "other", SecretValue: "v"},
		{SecretID: "eb-admin", Name: "EventBus", Category: "eventbus", Provider: "moox_eventbus", KeyID: "eventbus_internal_admin", SecretValue: "t"},
	}, testPassphrase, time.Now())
	require.NoError(t, err)
	target := newMemSecrets()
	rsp, err := NewService(target).ImportSecrets(userCtx(), &pb.ImportSecretsReq{Bundle: bundle, Passphrase: testPassphrase})
	require.NoError(t, err)
	require.EqualValues(t, 2, rsp.GetCreated())
	require.Equal(t, []string{"Masked：秘钥值是脱敏后的值", "BadCategory：分类无效", "EventBus：系统秘钥不导入"}, rsp.GetFailed())
	require.Len(t, target.byID, 2)
	require.Contains(t, target.byID, "sec_ok")
	for id, record := range target.byID {
		if record.Name == "NoID" {
			require.NotEmpty(t, strings.TrimSpace(id), "没有 secret_id 时生成新的")
		}
	}

	target.createErr = errors.New("unique constraint")
	rsp, err = NewService(target).ImportSecrets(userCtx(), &pb.ImportSecretsReq{Bundle: bundle, Passphrase: testPassphrase})
	require.NoError(t, err)
	require.Contains(t, rsp.GetFailed(), "NoID：创建失败")
}

// 非登录调用方（经服务目录放行的 moox-cli）允许导出与导入，操作者记为 moox-cli；导出口令太短拒绝。
func TestSecretMigrationAllowsNonLoginCallerAndRejectsWeakPassphrase(t *testing.T) {
	source := newMemSecrets(model.Secret{SecretID: "sec_a", Name: "A", Category: "other", SecretValue: "v"})
	exported, err := NewService(source).ExportSecrets(trpc.BackgroundContext(), &pb.ExportSecretsReq{Passphrase: testPassphrase})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, exported.GetRetInfo().GetCode(), exported.GetRetInfo().GetMsg())
	require.EqualValues(t, 1, exported.GetCount())
	require.NotEmpty(t, exported.GetBundle())

	target := newMemSecrets()
	imported, err := NewService(target).ImportSecrets(trpc.BackgroundContext(), &pb.ImportSecretsReq{Bundle: exported.GetBundle(), Passphrase: testPassphrase})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, imported.GetRetInfo().GetCode(), imported.GetRetInfo().GetMsg())
	require.EqualValues(t, 1, imported.GetCreated())
	require.Equal(t, servicecatalog.OperatorMooxCLI, target.byID["sec_a"].Creator)

	short, err := NewService(source).ExportSecrets(userCtx(), &pb.ExportSecretsReq{Passphrase: "short-口令"})
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

// 不带分页参数时默认返回第一页（size=50），且返回标准 PageResult。
func TestListSecretsDefaultsToFirstPageWithoutLimit(t *testing.T) {
	service := NewService(newMemSecrets(
		model.Secret{SecretID: "a"}, model.Secret{SecretID: "b"},
		model.Secret{SecretID: "c"}, model.Secret{SecretID: "d"},
		model.Secret{SecretID: "e"},
	))
	rsp, err := service.ListSecrets(context.Background(), &pb.ListSecretsReq{})
	require.NoError(t, err)
	require.Len(t, rsp.GetSecrets(), 5)
	require.EqualValues(t, 1, rsp.GetPageResult().GetPage())
	require.EqualValues(t, defaultListSecretsPageSize, rsp.GetPageResult().GetSize())
	require.EqualValues(t, 5, rsp.GetPageResult().GetTotal())
	require.False(t, rsp.GetPageResult().GetHasMore())
	require.Equal(t, commonpb.TotalState_EXACT, rsp.GetPageResult().GetTotalState())

	page1, err := service.ListSecrets(context.Background(), &pb.ListSecretsReq{Page: &pb.Page{Page: 1, Size: 2}})
	require.NoError(t, err)
	require.Len(t, page1.GetSecrets(), 2)
	require.EqualValues(t, 1, page1.GetPageResult().GetPage())
	require.EqualValues(t, 2, page1.GetPageResult().GetSize())
	require.EqualValues(t, 5, page1.GetPageResult().GetTotal())
	require.True(t, page1.GetPageResult().GetHasMore())
	require.Equal(t, commonpb.TotalState_EXACT, page1.GetPageResult().GetTotalState())

	page3, err := service.ListSecrets(context.Background(), &pb.ListSecretsReq{Page: &pb.Page{Page: 3, Size: 2}})
	require.NoError(t, err)
	require.Len(t, page3.GetSecrets(), 1)
	require.EqualValues(t, 3, page3.GetPageResult().GetPage())
	require.EqualValues(t, 2, page3.GetPageResult().GetSize())
	require.EqualValues(t, 5, page3.GetPageResult().GetTotal())
	require.False(t, page3.GetPageResult().GetHasMore())
	require.Equal(t, commonpb.TotalState_EXACT, page3.GetPageResult().GetTotalState())

	clamped, err := service.ListSecrets(context.Background(), &pb.ListSecretsReq{Page: &pb.Page{Page: 1, Size: 999}})
	require.NoError(t, err)
	require.EqualValues(t, maxListSecretsPageSize, clamped.GetPageResult().GetSize())

	// 整除末页（page*size == total）：has_more 必须为 false
	exact, err := service.ListSecrets(context.Background(), &pb.ListSecretsReq{Page: &pb.Page{Page: 1, Size: 5}})
	require.NoError(t, err)
	require.Len(t, exact.GetSecrets(), 5)
	require.False(t, exact.GetPageResult().GetHasMore())

	// 超页边界：返回空列表且 has_more 为 false
	overflow, err := service.ListSecrets(context.Background(), &pb.ListSecretsReq{Page: &pb.Page{Page: 4, Size: 2}})
	require.NoError(t, err)
	require.Empty(t, overflow.GetSecrets())
	require.EqualValues(t, 5, overflow.GetPageResult().GetTotal())
	require.False(t, overflow.GetPageResult().GetHasMore())

	// 空表边界：total=0、has_more=false
	emptyService := NewService(newMemSecrets())
	emptyRsp, err := emptyService.ListSecrets(context.Background(), &pb.ListSecretsReq{Page: &pb.Page{Page: 1, Size: 2}})
	require.NoError(t, err)
	require.Empty(t, emptyRsp.GetSecrets())
	require.EqualValues(t, 0, emptyRsp.GetPageResult().GetTotal())
	require.False(t, emptyRsp.GetPageResult().GetHasMore())
	require.Equal(t, commonpb.TotalState_EXACT, emptyRsp.GetPageResult().GetTotalState())
}
