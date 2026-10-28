package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/factorwire"
	"github.com/mooyang-code/moox/packages/factorexpr"
	"github.com/mooyang-code/moox/packages/factorudf"
	"github.com/stretchr/testify/require"
)

// putExample 把测试 wasm 作为一个版本存进库（未启用），返回版本。
func putExample(t *testing.T, s *Store, name string) UdfVersion {
	t.Helper()
	wasm, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "packages", "factorudf", "testdata", name+".wasm"))
	require.NoError(t, err)
	info, err := factorudf.Inspect(wasm, factorudf.Limits{})
	require.NoError(t, err)
	manifest, err := json.Marshal(factorudf.PackageManifest{Manifest: *info.Manifest, Doc: "示例 " + name, Example: "udf." + info.Manifest.Name + "(close, 5)"})
	require.NoError(t, err)
	v := UdfVersion{Hash: info.Hash, Name: info.Manifest.Name, Manifest: string(manifest), SourceRef: "repo@abc", SDKVersion: info.Manifest.SDK, Toolchain: "rust-1.99.1", Report: "{}", Wasm: wasm}
	created, err := s.PutUdfVersion(context.Background(), v)
	require.NoError(t, err)
	require.True(t, created)
	return v
}

func TestUdfVersionsAreImmutableAndRepeatedUploadsOnlyUpdateTheDescription(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	v := putExample(t, s, "ts_mean")

	again := v
	again.Manifest = `{"abi":1,"doc":"更新后的说明"}`
	again.SourceRef, again.Wasm = "other@zzz", []byte("different")
	created, err := s.PutUdfVersion(ctx, again)
	require.NoError(t, err)
	require.False(t, created)
	got, err := s.GetUdfVersion(ctx, v.Hash, true)
	require.NoError(t, err)
	require.Equal(t, again.Manifest, got.Manifest, "说明与示例可更新")
	require.Equal(t, "repo@abc", got.SourceRef, "来源不变")
	require.Equal(t, v.Wasm, got.Wasm, "wasm 字节不变")

	// 同一哈希不能挂到另一个名称上。
	other := v
	other.Name = "other_name"
	_, err = s.PutUdfVersion(ctx, other)
	require.ErrorIs(t, err, ErrConflict)

	name, versions, err := s.GetUdf(ctx, v.Name)
	require.NoError(t, err)
	require.Empty(t, name.ActiveHash, "上传后未启用")
	require.Len(t, versions, 1)
	require.Empty(t, versions[0].Wasm, "列表不带 wasm")
}

func TestActivateValidatesVersionAndReferences(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	v := putExample(t, s, "ts_mean")
	other := putExample(t, s, "ema_trunc")

	require.ErrorIs(t, s.SetActiveUdf(ctx, v.Name, other.Hash), ErrConflict, "版本必须属于这个名称")
	require.ErrorIs(t, s.SetActiveUdf(ctx, v.Name, "deadbeef"), ErrConflict)
	require.NoError(t, s.SetActiveUdf(ctx, v.Name, v.Hash))
	name, _, err := s.GetUdf(ctx, v.Name)
	require.NoError(t, err)
	require.Equal(t, v.Hash, name.ActiveHash)

	// 被因子定义引用后不能清空。
	spec := "output: udf.ts_mean_udf(close, 5)"
	require.NoError(t, s.CreateFactor(ctx, domain.FactorDef{FactorID: "f_mean", Name: "n", Spec: spec, SpecHash: domain.SpecHash(spec)}))
	refs, err := s.UdfReferences(ctx, v.Name)
	require.NoError(t, err)
	require.Equal(t, []string{"f_mean"}, refs)
	require.ErrorIs(t, s.SetActiveUdf(ctx, v.Name, ""), ErrConflict)
	other2, err := s.UdfReferences(ctx, "ema_trunc")
	require.NoError(t, err)
	require.Empty(t, other2)
}

func TestDefinitionsAreCompiledWithTheActiveVersion(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	v := putExample(t, s, "ts_mean")
	spec := "output: udf.ts_mean_udf(close, 5)"
	require.NoError(t, s.CreateFactor(ctx, domain.FactorDef{FactorID: "f_mean", Name: "n", Spec: spec, SpecHash: domain.SpecHash(spec)}))

	// 没有启用版本：编译失败，定义仍能读出（由调用方校验报错），派生字段为空。
	def, err := s.GetFactor(ctx, "f_mean")
	require.NoError(t, err)
	require.Nil(t, def.Compiled())
	failedHash := domain.DefinitionHash(def)
	require.NotEmpty(t, failedHash)

	require.NoError(t, s.SetActiveUdf(ctx, v.Name, v.Hash))
	def, err = s.GetFactor(ctx, "f_mean")
	require.NoError(t, err)
	require.NotNil(t, def.Compiled())
	require.Equal(t, []string{"close"}, def.InputColumns)
	require.Equal(t, 5, def.LookbackPeriods)
	require.Equal(t, v.Hash, def.UDFs["ts_mean_udf"].Hash)
	hash1 := domain.DefinitionHash(def)
	require.NotEqual(t, failedHash, hash1)

	// ListFactors 同样解析绑定。
	list, err := s.ListFactors(ctx)
	require.NoError(t, err)
	require.Equal(t, hash1, domain.DefinitionHash(list[0]))
}

func TestPinsOverrideTheActiveVersion(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	v1 := putExample(t, s, "ts_mean")
	// 构造同一个名称的第二个版本：同一份 wasm 无法产出不同哈希，这里直接改写版本表里的哈希来模拟。
	v2 := v1
	v2.Hash = "00" + v1.Hash[2:]
	_, err := s.PutUdfVersion(ctx, v2)
	require.NoError(t, err)
	require.NoError(t, s.SetActiveUdf(ctx, v1.Name, v1.Hash))
	spec := "output: udf.ts_mean_udf(close, 5)"
	require.NoError(t, s.CreateFactor(ctx, domain.FactorDef{FactorID: "f", Name: "n", Spec: spec, SpecHash: domain.SpecHash(spec)}))

	live, err := s.GetFactor(ctx, "f")
	require.NoError(t, err)
	require.Equal(t, v1.Hash, live.UDFs["ts_mean_udf"].Hash)

	pinned, err := s.GetFactor(WithUDFPins(ctx, map[string]string{"ts_mean_udf": v2.Hash}), "f")
	require.NoError(t, err)
	require.Equal(t, v2.Hash, pinned.UDFs["ts_mean_udf"].Hash, "固定绑定优先于启用版本")
	require.NotEqual(t, domain.DefinitionHash(live), domain.DefinitionHash(pinned), "绑定不同，definition_hash 不同")

	// 请求级覆盖优先于固定绑定。
	env, err := s.ResolveUDFs(WithUDFPins(ctx, map[string]string{"ts_mean_udf": v2.Hash}), []string{"ts_mean_udf"}, map[string]string{"ts_mean_udf": v1.Hash})
	require.NoError(t, err)
	require.Equal(t, v1.Hash, env["ts_mean_udf"].Hash)

	// 指向不存在的版本是错误，不是静默回退。
	_, err = s.ResolveUDFs(ctx, []string{"ts_mean_udf"}, map[string]string{"ts_mean_udf": "nope"})
	require.ErrorContains(t, err, "does not exist")
}

// 已编译的定义复制后只替换绑定，会重新编译（编译缓存键含绑定）。
func TestCompiledDefinitionIsRecompiledWhenOnlyTheBindingChanges(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	v := putExample(t, s, "ts_mean")
	require.NoError(t, s.SetActiveUdf(ctx, v.Name, v.Hash))
	spec := "output: udf.ts_mean_udf(close, 5)"
	require.NoError(t, s.CreateFactor(ctx, domain.FactorDef{FactorID: "f", Name: "n", Spec: spec, SpecHash: domain.SpecHash(spec)}))
	def, err := s.GetFactor(ctx, "f")
	require.NoError(t, err)
	require.NotNil(t, def.Compiled())

	copied := def
	udfs := map[string]factorexpr.UDF{}
	for k, u := range def.UDFs {
		u.Hash = "11" + u.Hash[2:]
		udfs[k] = u
	}
	copied.UDFs = udfs
	recompiled, err := domain.CompileDefinition(copied)
	require.NoError(t, err)
	require.Contains(t, recompiled.Compiled().CanonicalText(), "@11", "复制后换了绑定，必须按新绑定重新编译，不能命中旧的编译结果")
	require.NotEqual(t, domain.DefinitionHash(def), domain.DefinitionHash(copied))
}

// 定义文本里只在注释中出现的算子名没有启用版本：不会让定义编译失败，也不会进入绑定与下发的集合。
func TestCommentOnlyReferencesDoNotEnterBindings(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	v := putExample(t, s, "ts_mean")
	require.NoError(t, s.SetActiveUdf(ctx, v.Name, v.Hash))
	spec := "# 以前用 udf.not_activated(close, 3)\noutput: udf.ts_mean_udf(close, 5)"
	require.NoError(t, s.CreateFactor(ctx, domain.FactorDef{FactorID: "f_c", Name: "n", Spec: spec, SpecHash: domain.SpecHash(spec)}))
	def, err := s.GetFactor(ctx, "f_c")
	require.NoError(t, err)
	require.NotNil(t, def.Compiled())
	require.Equal(t, []string{"ts_mean_udf"}, mapKeys(def.UDFs))
	require.Len(t, factorwire.UDFsToPB(def.UDFs), 1)
}

func TestUDFNamesToleratesWhitespaceAroundTheDot(t *testing.T) {
	require.Equal(t, []string{"ts_mean_udf"}, domain.UDFNames("output: udf .ts_mean_udf(close, 5)"))
	require.Equal(t, []string{"ts_mean_udf"}, domain.UDFNames("output: udf. ts_mean_udf (close, 5)"))
	require.Empty(t, domain.UDFNames("output: close"))
}

func TestUDFsToPBSkipsPlaceholdersWithoutAVersion(t *testing.T) {
	out := factorwire.UDFsToPB(map[string]factorexpr.UDF{"a": {Name: "a", Hash: "ab"}, "b": {Name: "b"}})
	require.Len(t, out, 1)
	require.Equal(t, "a", out[0].GetName())
}

func mapKeys(m map[string]factorexpr.UDF) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// 引用查询不能依赖 "udf." 的精确写法：点前后带空白的调用同样算引用（激活前提检查靠它）。
func TestUdfReferencesFindCallsWrittenWithWhitespace(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	v := putExample(t, s, "ts_mean")
	require.NoError(t, s.SetActiveUdf(ctx, v.Name, v.Hash))
	spec := "output: udf .ts_mean_udf(close, 5)"
	require.NoError(t, s.CreateFactor(ctx, domain.FactorDef{FactorID: "f_ws", Name: "n", Spec: spec, SpecHash: domain.SpecHash(spec)}))
	refs, err := s.UdfReferences(ctx, v.Name)
	require.NoError(t, err)
	require.Equal(t, []string{"f_ws"}, refs)
}
