package factorudf

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func buildTestPackage(t *testing.T, rt *Runtime, name string) *Package {
	t.Helper()
	wasm := testWasm(t, name)
	m, err := rt.Load(context.Background(), wasm)
	require.NoError(t, err)
	fx := loadFixtures(t, name)
	rep, err := RunConformance(context.Background(), rt, m, fx, ConformanceOptions{})
	require.NoError(t, err)
	require.True(t, rep.Passed)
	return &Package{
		Wasm: wasm, Manifest: PackageManifest{Manifest: *m.Info.Manifest, Doc: "说明", Source: SourceRef{Repo: "git@example/udfs.git", Commit: "abc123"}},
		Fixtures: fx, Report: rep,
	}
}

func TestPackageRoundTripIsDeterministic(t *testing.T) {
	rt := newTestRuntime(t, Options{})
	pkg := buildTestPackage(t, rt, "ts_mean")
	raw, err := WritePackage(pkg)
	require.NoError(t, err)
	again, err := WritePackage(pkg)
	require.NoError(t, err)
	require.Equal(t, raw, again, "同样的内容产出同样的字节")

	got, err := ReadPackage(raw, Limits{})
	require.NoError(t, err)
	require.Equal(t, pkg.Wasm, got.Wasm)
	require.Equal(t, pkg.Report.Digest, got.Report.Digest)
	require.Equal(t, "abc123", got.Manifest.Source.Commit)
	info, err := VerifyPackage(got, Limits{})
	require.NoError(t, err)
	require.Equal(t, Hash(pkg.Wasm), info.Hash)
}

func TestPackageRejections(t *testing.T) {
	rt := newTestRuntime(t, Options{})
	pkg := buildTestPackage(t, rt, "ts_mean")
	raw, err := WritePackage(pkg)
	require.NoError(t, err)

	write := func(files map[string][]byte, order []string) []byte {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		for _, name := range order {
			require.NoError(t, tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(files[name])), Typeflag: tar.TypeReg}))
			_, err := tw.Write(files[name])
			require.NoError(t, err)
		}
		require.NoError(t, tw.Close())
		return buf.Bytes()
	}
	manifest, _ := json.Marshal(pkg.Manifest)
	report, _ := json.Marshal(pkg.Report)
	base := map[string][]byte{PkgModule: pkg.Wasm, PkgManifest: manifest, PkgReport: report}

	_, err = ReadPackage(write(base, []string{PkgModule, PkgManifest}), Limits{})
	require.ErrorContains(t, err, "缺少")

	withExtra := map[string][]byte{PkgModule: pkg.Wasm, PkgManifest: manifest, PkgReport: report, "evil.sh": []byte("x")}
	_, err = ReadPackage(write(withExtra, []string{PkgModule, PkgManifest, PkgReport, "evil.sh"}), Limits{})
	require.ErrorContains(t, err, "不认识")

	traversal := map[string][]byte{PkgModule: pkg.Wasm, PkgManifest: manifest, PkgReport: report, "fixtures/../x": []byte("{}")}
	_, err = ReadPackage(write(traversal, []string{PkgModule, PkgManifest, PkgReport, "fixtures/../x"}), Limits{})
	require.ErrorContains(t, err, "不合法")

	_, err = ReadPackage(write(base, []string{PkgModule, PkgModule, PkgManifest, PkgReport}), Limits{})
	require.ErrorContains(t, err, "重复")

	_, err = ReadPackage(bytes.Repeat([]byte("x"), MaxPackageBytes+1), Limits{})
	require.ErrorContains(t, err, "超过上限")

	_, err = ReadPackage(raw[:len(raw)/2], Limits{})
	require.Error(t, err)

	// manifest 的语义字段与自定义段不一致。
	other := pkg.Manifest
	other.Lookback = "5"
	otherRaw, _ := json.Marshal(other)
	tampered, err := ReadPackage(write(map[string][]byte{PkgModule: pkg.Wasm, PkgManifest: otherRaw, PkgReport: report}, []string{PkgModule, PkgManifest, PkgReport}), Limits{})
	require.NoError(t, err)
	_, err = VerifyPackage(tampered, Limits{})
	require.ErrorContains(t, err, "不一致")

	// 报告里的哈希与模块不一致。
	bad := *pkg.Report
	bad.Hash = "deadbeef"
	badRaw, _ := json.Marshal(bad)
	tampered, err = ReadPackage(write(map[string][]byte{PkgModule: pkg.Wasm, PkgManifest: manifest, PkgReport: badRaw}, []string{PkgModule, PkgManifest, PkgReport}), Limits{})
	require.NoError(t, err)
	_, err = VerifyPackage(tampered, Limits{})
	require.ErrorContains(t, err, "哈希")
}

func TestNormalizeFixturesRejectsBadInput(t *testing.T) {
	for _, raw := range []string{`{}`, `{"cases":[]}`, `{"cases":[{"name":"x"}]}`, `{"cases":[{"name":"x","inputs":[[1],[1,2]],"expected":[1]}]}`, `{"cases":[{"name":"x","inputs":[[1]],"expected":[1],"bogus":1}]}`, `not json`} {
		_, err := NormalizeFixtures([]byte(raw))
		require.Error(t, err, raw)
	}
	norm, err := NormalizeFixtures([]byte(` {"cases": [ {"name":"a","params":[3],"inputs":[[1,null,3]],"expected":[null]} ]} `))
	require.NoError(t, err)
	again, err := NormalizeFixtures(norm)
	require.NoError(t, err)
	require.Equal(t, norm, again, "规范化是幂等的")
}
