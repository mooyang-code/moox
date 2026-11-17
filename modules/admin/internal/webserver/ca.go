package webserver

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"embed"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"text/template"

	"github.com/mooyang-code/moox/modules/admin/internal/pki"
)

const (
	caCertPath        = "/ca/root.crt"
	caFingerprintPath = "/ca/root.sha256"
	caBundlePath      = "/ca/moox-root-ca.zip"

	// caFileName 是下载与压缩包里根证书的文件名，前端 web/src/components/root-ca/root-ca.ts 用同一个名字。
	caFileName = "moox-ca.crt"
)

// cadoc 是压缩包里的 Agent 说明与安装脚本。install.sh 必须和 scripts/deploy/install-moox-ca.sh 完全一致（测试保证）。
//
//go:embed cadoc/AGENT-INSTALL.md.tmpl cadoc/install.sh cadoc/install.ps1
var caBundleFiles embed.FS

// bundleHostPattern 限制写进说明文档的主机名，避免请求头里的异常内容混进文档。
var bundleHostPattern = regexp.MustCompile(`^[A-Za-z0-9._:\[\]-]{1,255}$`)

// caDownload 公开 MooX CA 根证书及其 SHA-256 指纹，换了电脑的用户在登录前就能下载、安装信任。
// 根证书本身是公开信息；启动时从 CA 目录读出并校验过（恰好一张 CA 证书），之后只输出内存里这一份。
type caDownload struct {
	pem         []byte
	cert        *x509.Certificate
	fingerprint string
}

func newCADownload(pkiDir string) (*caDownload, error) {
	store, err := pki.Open(pkiDir)
	if err != nil {
		return nil, fmt.Errorf("打开 MooX CA 目录失败：%w", err)
	}
	defer store.Close()
	raw, cert, err := store.CACertificate()
	if err != nil {
		return nil, fmt.Errorf("读取 MooX CA 失败：%w", err)
	}
	if !singleCertificate(raw) {
		return nil, errors.New("MooX CA 根证书文件必须恰好是一张证书")
	}
	return &caDownload{pem: raw, cert: cert, fingerprint: fingerprint(cert)}, nil
}

func (c *caDownload) handles(path string) bool {
	return path == caCertPath || path == caFingerprintPath || path == caBundlePath
}

func (c *caDownload) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body []byte
	var contentType, disposition string
	switch r.URL.Path {
	case caCertPath:
		body, contentType, disposition = c.pem, "application/x-x509-ca-cert", `attachment; filename="`+caFileName+`"`
	case caFingerprintPath:
		body, contentType = []byte(c.fingerprint+"\n"), "text/plain; charset=utf-8"
	case caBundlePath:
		archive, err := buildCABundle(c.pem, r.Host)
		if err != nil {
			http.Error(w, "root certificate unavailable", http.StatusNotFound)
			return
		}
		body, contentType, disposition = archive, "application/zip", `attachment; filename="moox-root-ca.zip"`
	default:
		http.NotFound(w, r)
		return
	}
	header := w.Header()
	header.Set("Content-Type", contentType)
	header.Set("Content-Length", strconv.Itoa(len(body)))
	header.Set("Cache-Control", "no-store")
	if disposition != "" {
		header.Set("Content-Disposition", disposition)
	}
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

// fingerprint 是证书的 SHA-256 指纹：冒号分隔的大写十六进制（前端 normalizeFingerprint 只认这种格式）。
func fingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(parts, ":")
}

// singleCertificate 只接受恰好一个 CERTIFICATE PEM 块，拒绝含私钥或其他内容的文件。
func singleCertificate(raw []byte) bool {
	raw = bytes.TrimSpace(raw)
	if !bytes.HasPrefix(raw, []byte("-----BEGIN CERTIFICATE-----")) {
		return false
	}
	block, rest := pem.Decode(raw)
	return block != nil && block.Type == "CERTIFICATE" && len(bytes.TrimSpace(rest)) == 0
}

// buildCABundle 把根证书、指纹、给 Agent 阅读的安装说明和各系统的安装脚本打成一个 zip，方便用户把整个包交给 Agent 自动安装。
// 根证书必须是单张 CA 证书；指纹由证书本身计算。
func buildCABundle(rootPEM []byte, host string) ([]byte, error) {
	block, _ := pem.Decode(rootPEM)
	if block == nil {
		return nil, errors.New("root certificate is not PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !cert.IsCA {
		return nil, errors.New("root certificate is not a CA")
	}
	sum := fingerprint(cert)
	if !bundleHostPattern.MatchString(host) {
		host = "<管理台地址>"
	}
	guide, err := renderAgentGuide(host, sum)
	if err != nil {
		return nil, err
	}
	installSh, err := caBundleFiles.ReadFile("cadoc/install.sh")
	if err != nil {
		return nil, err
	}
	installPs1, err := caBundleFiles.ReadFile("cadoc/install.ps1")
	if err != nil {
		return nil, err
	}
	files := []struct {
		name string
		mode os.FileMode
		body []byte
	}{
		{"AGENT-INSTALL.md", 0o644, guide},
		{caFileName, 0o644, rootPEM},
		{"root.sha256", 0o644, []byte(sum + "\n")},
		{"install.sh", 0o755, installSh},
		{"install.ps1", 0o644, installPs1},
	}
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	for _, file := range files {
		// 固定修改时间，使同一张证书每次生成的压缩包内容一致。
		header := &zip.FileHeader{Name: file.name, Method: zip.Deflate, Modified: cert.NotBefore.UTC()}
		header.SetMode(file.mode)
		entry, err := writer.CreateHeader(header)
		if err != nil {
			return nil, err
		}
		if _, err := entry.Write(file.body); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func renderAgentGuide(host, sum string) ([]byte, error) {
	source, err := caBundleFiles.ReadFile("cadoc/AGENT-INSTALL.md.tmpl")
	if err != nil {
		return nil, err
	}
	tmpl, err := template.New("agent").Option("missingkey=error").Parse(string(source))
	if err != nil {
		return nil, fmt.Errorf("parse agent guide: %w", err)
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, map[string]string{"Host": host, "Fingerprint": sum}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
