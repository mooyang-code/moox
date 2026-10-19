package engine

import (
	"archive/zip"
	"bytes"
	"crypto/x509"
	"embed"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"text/template"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

const (
	caDownloadCertPath        = "/ca/root.crt"
	caDownloadFingerprintPath = "/ca/root.sha256"
	caDownloadBundlePath      = "/ca/moox-root-ca.zip"

	caCertFileName = "moox-caddy-root.crt"
)

// cadoc 是压缩包里的 Agent 说明与安装脚本。install.sh 必须和 scripts/deploy/install-caddy-ca.sh 完全一致（测试保证）。
//
//go:embed cadoc/AGENT-INSTALL.md.tmpl cadoc/install.sh cadoc/install.ps1
var caBundleFiles embed.FS

// bundleHostPattern 限制写进说明文档的主机名，避免请求头里的异常内容混进文档。
var bundleHostPattern = regexp.MustCompile(`^[A-Za-z0-9._:\[\]-]{1,255}$`)

// caDownloadHandler 公开已发布的根证书及其 SHA-256 指纹，供换了主机的用户在登录前安装信任。
// 根证书本身是公开信息；这里只读发布目录里的两个固定文件，并确认证书文件恰好是一张证书，
// 因此不会泄露私钥或发布目录里的其他内容。
type caDownloadHandler struct {
	Dir string `json:"dir"`
}

func init() { caddy.RegisterModule(caDownloadHandler{}) }

func (caDownloadHandler) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "http.handlers.moox_console_ca", New: func() caddy.Module { return new(caDownloadHandler) }}
}

func (h *caDownloadHandler) Provision(caddy.Context) error {
	if h.Dir == "" {
		return errors.New("CA publish directory is required")
	}
	return nil
}

func (h *caDownloadHandler) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	var name, contentType, disposition string
	switch r.URL.Path {
	case caDownloadCertPath:
		name, contentType, disposition = "root.crt", "application/x-x509-ca-cert", `attachment; filename="moox-caddy-root.crt"`
	case caDownloadFingerprintPath:
		name, contentType = "root.sha256", "text/plain; charset=utf-8"
	case caDownloadBundlePath:
		return h.serveBundle(w, r)
	default:
		return next.ServeHTTP(w, r)
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(h.Dir, name))
	if err != nil || (name == "root.crt" && !singleCertificate(raw)) {
		http.Error(w, "root certificate unavailable", http.StatusNotFound)
		return nil
	}
	header := w.Header()
	header.Set("Content-Type", contentType)
	header.Set("Content-Length", strconv.Itoa(len(raw)))
	header.Set("Cache-Control", "no-store")
	if disposition != "" {
		header.Set("Content-Disposition", disposition)
	}
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return nil
	}
	_, err = w.Write(raw)
	return err
}

// singleCertificate 只接受恰好一个 CERTIFICATE PEM 块，拒绝含私钥或其他内容的文件。
func singleCertificate(raw []byte) bool {
	if !bytes.HasPrefix(raw, []byte("-----BEGIN CERTIFICATE-----")) {
		return false
	}
	block, rest := pem.Decode(raw)
	return block != nil && block.Type == "CERTIFICATE" && len(bytes.TrimSpace(rest)) == 0
}

// serveBundle 把根证书、指纹、给 Agent 阅读的安装说明和各系统的安装脚本打成一个 zip，方便用户把整个包交给 Agent 自动安装。
func (h *caDownloadHandler) serveBundle(w http.ResponseWriter, r *http.Request) error {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(h.Dir, "root.crt"))
	if err != nil || !singleCertificate(raw) {
		http.Error(w, "root certificate unavailable", http.StatusNotFound)
		return nil
	}
	archive, err := buildCABundle(raw, r.Host)
	if err != nil {
		http.Error(w, "root certificate unavailable", http.StatusNotFound)
		return nil
	}
	header := w.Header()
	header.Set("Content-Type", "application/zip")
	header.Set("Content-Length", strconv.Itoa(len(archive)))
	header.Set("Content-Disposition", `attachment; filename="moox-root-ca.zip"`)
	header.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return nil
	}
	_, err = w.Write(archive)
	return err
}

// buildCABundle 生成压缩包内容。根证书必须是单张 CA 证书；指纹由证书本身计算，不读取可能被改动的指纹文件。
func buildCABundle(rootPEM []byte, host string) ([]byte, error) {
	block, _ := pem.Decode(rootPEM)
	if block == nil {
		return nil, errors.New("root certificate is not PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !cert.IsCA {
		return nil, errors.New("root certificate is not a CA")
	}
	sum := strings.TrimSpace(fingerprint(cert))
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
		{caCertFileName, 0o644, rootPEM},
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
