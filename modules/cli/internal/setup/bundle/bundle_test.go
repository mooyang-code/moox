package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/packages/servicecatalog"
)

const (
	testCommit = "0123456789abcdef0123456789abcdef01234567"
	testDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

func writeManifest(t *testing.T, content string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ManifestName), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func validComponent() string {
	return `{"file":"moox-collector_v0.2.0_linux_amd64.tar.gz","sha256":"` + testDigest + `","size":12,"binaries":{"moox-collector":"` + testDigest + `"}}`
}

func TestLoadNotBundle(t *testing.T) {
	if _, ok, err := Load(t.TempDir()); ok || err != nil {
		t.Fatalf("普通目录不应是产物包: ok=%v err=%v", ok, err)
	}
}

func TestLoadValid(t *testing.T) {
	root := writeManifest(t, `{"version":2,"release":"v0.2.0","commit":"`+testCommit+`","components":{"collector":`+validComponent()+`},"files":["go.mod","packages/servicecatalog/catalog.yaml"]}`)
	manifest, ok, err := Load(root)
	if err != nil || !ok {
		t.Fatalf("应读出清单: ok=%v err=%v", ok, err)
	}
	artifact, found := manifest.Component("collector")
	if manifest.Release != "v0.2.0" || !found || artifact.Binaries["moox-collector"] != testDigest {
		t.Fatalf("清单内容不对：%+v", manifest)
	}
	if _, found := manifest.Component("trade"); found {
		t.Fatal("没有的组件不应读出")
	}
}

// 旧的 version 1 清单（按部署单元出包）一律拒绝，提示换成同一版本的 CLI 与产物包。
func TestLoadRejectsVersionOne(t *testing.T) {
	root := writeManifest(t, `{"version":1,"release":"v0.2.0","commit":"`+testCommit+`","units":{"host":{}},"files":["a"]}`)
	if _, ok, err := Load(root); !ok || err == nil || !strings.Contains(err.Error(), "bundle_version_mismatch") {
		t.Fatalf("version 1 应拒绝：ok=%v err=%v", ok, err)
	}
}

func TestLoadRejectsBadManifest(t *testing.T) {
	component := validComponent()
	for name, content := range map[string]string{
		"短提交":    `{"version":2,"release":"v0.2.0","commit":"abc","components":{"collector":` + component + `},"files":["a"]}`,
		"越界路径":   `{"version":2,"release":"v0.2.0","commit":"` + testCommit + `","components":{"collector":` + component + `},"files":["../a"]}`,
		"没有文件":   `{"version":2,"release":"v0.2.0","commit":"` + testCommit + `","components":{"collector":` + component + `},"files":[]}`,
		"没有组件":   `{"version":2,"release":"v0.2.0","commit":"` + testCommit + `","components":{},"files":["a"]}`,
		"包没有二进制": `{"version":2,"release":"v0.2.0","commit":"` + testCommit + `","components":{"collector":{"file":"x.tar.gz","sha256":"` + testDigest + `","size":1}},"files":["a"]}`,
		"包名带目录":  `{"version":2,"release":"v0.2.0","commit":"` + testCommit + `","components":{"collector":{"file":"../x.tar.gz","sha256":"` + testDigest + `","size":1,"binaries":{"moox-collector":"` + testDigest + `"}}},"files":["a"]}`,
		"摘要带前缀":  `{"version":2,"release":"v0.2.0","commit":"` + testCommit + `","components":{"collector":{"file":"x.tar.gz","sha256":"sha256:` + testDigest + `","size":1,"binaries":{"moox-collector":"` + testDigest + `"}}},"files":["a"]}`,
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
	if !strings.HasPrefix(CatalogSHA256(), "sha256:") || len(CatalogSHA256()) != len("sha256:")+64 {
		t.Fatalf("服务目录摘要格式不对：%s", CatalogSHA256())
	}
}

// packageFixture 写好服务目录里全部组件的假二进制。
func packageFixture(t *testing.T, catalog servicecatalog.Catalog) string {
	t.Helper()
	binaries := t.TempDir()
	for _, component := range catalog.Components {
		for _, name := range Binaries(component) {
			if err := os.WriteFile(filepath.Join(binaries, name), []byte("binary "+name), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	return binaries
}

type tarEntry struct {
	name    string
	mode    int64
	content string
}

func readPackage(t *testing.T, name string) []tarEntry {
	t.Helper()
	file, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	if !compressed.ModTime.IsZero() || compressed.Name != "" {
		t.Fatalf("gzip 头不应带时间或文件名：%v %q", compressed.ModTime, compressed.Name)
	}
	reader := tar.NewReader(compressed)
	var entries []tarEntry
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.ModTime.Unix() != 0 || header.Uid != 0 || header.Gid != 0 || header.Uname != "" || header.Gname != "" || header.Typeflag != tar.TypeReg {
			t.Fatalf("%s 的条目头不确定：%+v", header.Name, header)
		}
		raw, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, tarEntry{header.Name, header.Mode, string(raw)})
	}
	return entries
}

// 每个组件一个包，内容只有 bin/<二进制>，同一输入两次打出的包逐字节相同。
func TestPackageComponentsIsDeterministic(t *testing.T) {
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	binaries := packageFixture(t, catalog)
	first, second := t.TempDir(), t.TempDir()
	one, err := PackageComponents(catalog, binaries, first, "v9.9.9", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	two, err := PackageComponents(catalog, binaries, second, "v9.9.9", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if len(one) != len(catalog.Components) {
		t.Fatalf("应每个组件一个包：%d 个，目录里 %d 个组件", len(one), len(catalog.Components))
	}
	for _, component := range catalog.Components {
		artifact := one[component.ID]
		if artifact.File != "moox-"+component.ID+"_v9.9.9_linux_amd64.tar.gz" || artifact.Size <= 0 || !digestPattern.MatchString(artifact.SHA256) {
			t.Fatalf("%s 的包记录不对：%+v", component.ID, artifact)
		}
		if err := artifact.validate(true); err != nil {
			t.Fatalf("%s 的包记录不能通过清单校验：%v", component.ID, err)
		}
		left, err := os.ReadFile(filepath.Join(first, artifact.File))
		if err != nil {
			t.Fatal(err)
		}
		right, err := os.ReadFile(filepath.Join(second, two[component.ID].File))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(left, right) || artifact.SHA256 != two[component.ID].SHA256 {
			t.Fatalf("%s 两次打包结果不同", component.ID)
		}
		wantNames := []string{}
		for _, name := range Binaries(component) {
			wantNames = append(wantNames, "bin/"+name)
			if artifact.Binaries[name] == "" {
				t.Fatalf("%s 的清单缺 %s 的摘要", component.ID, name)
			}
		}
		slices.Sort(wantNames)
		entries := readPackage(t, filepath.Join(first, artifact.File))
		gotNames := []string{}
		for _, entry := range entries {
			gotNames = append(gotNames, entry.name)
			if entry.mode != int64(0o755) {
				t.Fatalf("%s 的 %s 权限是 %o", component.ID, entry.name, entry.mode)
			}
		}
		if !slices.Equal(gotNames, wantNames) {
			t.Fatalf("%s 的包内容是 %v，应为 %v", component.ID, gotNames, wantNames)
		}
	}
	collector := one["collector"]
	if collector.Binaries["moox-collector-cli"] == "" {
		t.Fatalf("collector 的包应带配套命令行：%+v", collector)
	}
}

func TestPackageComponentsNamesMissingBinary(t *testing.T) {
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	binaries := packageFixture(t, catalog)
	if err := os.Remove(filepath.Join(binaries, "moox-collector-cli")); err != nil {
		t.Fatal(err)
	}
	_, err = PackageComponents(catalog, binaries, t.TempDir(), "v9.9.9", "amd64")
	if err == nil || !strings.Contains(err.Error(), "组件 collector 缺 moox-collector-cli") {
		t.Fatalf("缺二进制应指明组件与文件：%v", err)
	}
	// 符号链接不算二进制：打包只收普通文件。
	if err := os.Symlink(filepath.Join(binaries, "moox-collector"), filepath.Join(binaries, "moox-collector-cli")); err != nil {
		t.Fatal(err)
	}
	if _, err := PackageComponents(catalog, binaries, t.TempDir(), "v9.9.9", "amd64"); err == nil {
		t.Fatal("符号链接应拒绝")
	}
}

func TestPackageComponentsRejectsBadInputs(t *testing.T) {
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	binaries := packageFixture(t, catalog)
	if _, err := PackageComponents(catalog, binaries, t.TempDir(), "v9/9", "amd64"); err == nil {
		t.Fatal("版本带斜杠应拒绝")
	}
	if _, err := PackageComponents(catalog, binaries, t.TempDir(), "v9.9.9", "386"); err == nil {
		t.Fatal("不支持的架构应拒绝")
	}
}
