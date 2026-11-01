package bundle

import (
	"os"
	"path/filepath"
	"testing"
)

func writeManifest(t *testing.T, content string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ManifestName), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestLoadNotBundle(t *testing.T) {
	if _, ok, err := Load(t.TempDir()); ok || err != nil {
		t.Fatalf("普通目录不应是产物包: ok=%v err=%v", ok, err)
	}
}

func TestLoadValid(t *testing.T) {
	root := writeManifest(t, `{"version":1,"release":"v0.2.0","commit":"0123456789abcdef0123456789abcdef01234567","binary_directory":"bin/linux-amd64","files":["go.work","packages/servicecatalog/catalog.yaml"]}`)
	manifest, ok, err := Load(root)
	if err != nil || !ok {
		t.Fatalf("应读出清单: ok=%v err=%v", ok, err)
	}
	if got, want := manifest.BinaryPath(root), filepath.Join(root, "bin", "linux-amd64"); got != want {
		t.Fatalf("二进制目录 %s，期望 %s", got, want)
	}
}

func TestLoadRejectsBadManifest(t *testing.T) {
	for name, content := range map[string]string{
		"短提交":    `{"version":1,"release":"v0.2.0","commit":"abc","binary_directory":"bin","files":["a"]}`,
		"越界路径":   `{"version":1,"release":"v0.2.0","commit":"0123456789abcdef0123456789abcdef01234567","binary_directory":"bin","files":["../a"]}`,
		"没有文件":   `{"version":1,"release":"v0.2.0","commit":"0123456789abcdef0123456789abcdef01234567","binary_directory":"bin","files":[]}`,
		"不是JSON": `{`,
	} {
		if _, ok, err := Load(writeManifest(t, content)); !ok || err == nil {
			t.Fatalf("%s：应拒绝，ok=%v err=%v", name, ok, err)
		}
	}
}

func TestCheckCLI(t *testing.T) {
	manifest := Manifest{Release: "v1.2.3", CatalogSHA256: "abc"}
	if err := manifest.CheckCLI("v1.2.3", "abc"); err != nil {
		t.Fatal(err)
	}
	if err := manifest.CheckCLI("dev", "abc"); err != nil {
		t.Fatalf("开发版本不比版本号：%v", err)
	}
	if err := manifest.CheckCLI("v1.2.4", "abc"); err == nil {
		t.Fatal("版本不同要拒绝")
	}
	manifest.CatalogSHA256 = "other"
	if err := manifest.CheckCLI("dev", "abc"); err == nil {
		t.Fatal("服务目录不同要拒绝")
	}
}
