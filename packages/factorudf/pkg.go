package factorudf

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

// 包内文件名与大小限制。整包不超过 2 MiB：管理调用经网关走 JSON，二进制按 base64 放大约 4/3，
// 2 MiB 的包编码后约 2.7 MiB，加上 JSON 包装仍在 FactorMgr 现有的 4 MiB 消息上限之内。
const (
	PkgModule   = "module.wasm"
	PkgManifest = "manifest.json"
	PkgReport   = "report.json"
	PkgFixtures = "fixtures/"

	MaxPackageBytes = 2 << 20
	maxPkgFiles     = 64
)

// SourceRef 是私有仓库与提交号：系统里的每个版本都能对应到私有仓库的一次提交。
type SourceRef struct {
	Repo   string `json:"repo"`
	Commit string `json:"commit"`
}

// PackageManifest 是包内的 manifest.json：语义字段（必须与 wasm 自定义段一致）加说明、示例、来源与工具链。
type PackageManifest struct {
	Manifest
	Doc       string    `json:"doc,omitempty"`
	Example   string    `json:"example,omitempty"`
	Source    SourceRef `json:"source"`
	Toolchain string    `json:"toolchain,omitempty"`
}

// Package 是解开的 .udfpkg。
type Package struct {
	Wasm      []byte
	Manifest  PackageManifest
	Fixtures  FixtureSet
	Report    *Report
	ReportRaw []byte
}

// WritePackage 把包写成 tar：module.wasm、manifest.json、fixtures/、report.json，顺序固定、时间戳置零，
// 同样的内容总是产出同样的字节。写出前检查整包大小。
func WritePackage(p *Package) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	add := func(name string, data []byte) error {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		_, err := tw.Write(data)
		return err
	}
	manifest, err := json.Marshal(p.Manifest)
	if err != nil {
		return nil, err
	}
	report := p.ReportRaw
	if report == nil && p.Report != nil {
		if report, err = json.Marshal(p.Report); err != nil {
			return nil, err
		}
	}
	if err := add(PkgModule, p.Wasm); err != nil {
		return nil, err
	}
	if err := add(PkgManifest, manifest); err != nil {
		return nil, err
	}
	for _, name := range p.Fixtures.names() {
		if err := add(PkgFixtures+name, p.Fixtures[name]); err != nil {
			return nil, err
		}
	}
	if err := add(PkgReport, report); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if buf.Len() > MaxPackageBytes {
		return nil, fmt.Errorf("包 %d 字节，超过上限 %d", buf.Len(), MaxPackageBytes)
	}
	return buf.Bytes(), nil
}

// ReadPackage 读取并校验 .udfpkg：整包大小、文件名白名单（不允许路径穿越与重复）、各部分大小、manifest 与 report 可解析。
// 模块本身的合格性由 Inspect / Runtime.Load 检查，这里只管包的结构。
func ReadPackage(raw []byte, l Limits) (*Package, error) {
	l = l.WithDefaults()
	if len(raw) > MaxPackageBytes {
		return nil, fmt.Errorf("包 %d 字节，超过上限 %d", len(raw), MaxPackageBytes)
	}
	p := &Package{Fixtures: FixtureSet{}}
	seen := map[string]bool{}
	tr := tar.NewReader(bytes.NewReader(raw))
	var manifestRaw []byte
	for n := 0; ; n++ {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("包不是合法的 tar：%w", err)
		}
		if n >= maxPkgFiles {
			return nil, fmt.Errorf("包里的文件超过 %d 个", maxPkgFiles)
		}
		if hdr.Typeflag != tar.TypeReg {
			return nil, fmt.Errorf("包里只允许普通文件：%q", hdr.Name)
		}
		name := hdr.Name
		if name != path.Clean(name) || strings.HasPrefix(name, "/") || strings.Contains(name, "..") {
			return nil, fmt.Errorf("包里的文件名 %q 不合法", name)
		}
		if seen[name] {
			return nil, fmt.Errorf("包里的文件 %q 重复", name)
		}
		seen[name] = true
		limit := int64(MaxFixturesBytes)
		switch {
		case name == PkgModule:
			limit = int64(l.MaxModuleBytes)
		case name == PkgManifest || name == PkgReport:
			limit = 256 << 10
		case strings.HasPrefix(name, PkgFixtures) && !strings.Contains(name[len(PkgFixtures):], "/") && len(name) > len(PkgFixtures):
		default:
			return nil, fmt.Errorf("包里有不认识的文件 %q", name)
		}
		if hdr.Size > limit {
			return nil, fmt.Errorf("包里的文件 %q 有 %d 字节，超过上限 %d", name, hdr.Size, limit)
		}
		data, err := io.ReadAll(io.LimitReader(tr, limit+1))
		if err != nil {
			return nil, fmt.Errorf("读取 %q：%w", name, err)
		}
		switch {
		case name == PkgModule:
			p.Wasm = data
		case name == PkgManifest:
			manifestRaw = data
		case name == PkgReport:
			p.ReportRaw = data
		default:
			p.Fixtures[strings.TrimPrefix(name, PkgFixtures)] = data
		}
	}
	if p.Wasm == nil || manifestRaw == nil || p.ReportRaw == nil {
		return nil, errors.New("包里缺少 module.wasm、manifest.json 或 report.json")
	}
	if p.Fixtures.TotalBytes() > MaxFixturesBytes {
		return nil, fmt.Errorf("fixtures 共 %d 字节，超过上限 %d", p.Fixtures.TotalBytes(), MaxFixturesBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(manifestRaw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p.Manifest); err != nil {
		return nil, fmt.Errorf("manifest.json 不合法：%w", err)
	}
	var report Report
	if err := json.Unmarshal(p.ReportRaw, &report); err != nil {
		return nil, fmt.Errorf("report.json 不合法：%w", err)
	}
	p.Report = &report
	for name, data := range p.Fixtures {
		norm, err := NormalizeFixtures(data)
		if err != nil {
			return nil, fmt.Errorf("fixtures/%s：%w", name, err)
		}
		if !bytes.Equal(norm, data) {
			return nil, fmt.Errorf("fixtures/%s 不是规范化的内容", name)
		}
	}
	return p, nil
}

// VerifyPackage 检查包的各部分彼此一致：wasm 通过模块检查，manifest 的语义字段与自定义段一致，
// 报告里的哈希与 wasm 一致。返回模块信息。
func VerifyPackage(p *Package, l Limits) (*ModuleInfo, error) {
	info, err := Inspect(p.Wasm, l)
	if err != nil {
		return nil, err
	}
	if !info.Manifest.Equal(&p.Manifest.Manifest) {
		return nil, checkFail("manifest.json 的语义字段与 wasm 自定义段不一致")
	}
	if p.Report == nil || p.Report.Hash != info.Hash {
		return nil, checkFail("报告里的哈希与 module.wasm 不一致")
	}
	return info, nil
}
